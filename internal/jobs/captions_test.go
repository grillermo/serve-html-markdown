package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"serve-html-markdown/internal/llm"
	"serve-html-markdown/internal/store"
	"serve-html-markdown/internal/video"
)

const tweetURL = "https://x.com/foo/status/1"

// Ruby: "on captions hit: summarizes, writes html, pushes rulinky, done", plus
// the status walk summarizing -> publishing -> done.
func TestCaptionsHitSummarizesWritesPublishesDone(t *testing.T) {
	e := newVideoEnv(t)
	v := e.awaiting(tweetURL)
	var seen []string
	statusNow := func() string { return e.reload(v.ID).Status }
	e.summ.fn = func(context.Context, string) (llm.Summary, error) {
		seen = append(seen, statusNow())
		return llm.Summary{Title: "The Idea", SummaryHTML: "<p>x</p>"}, nil
	}
	e.writer.fn = func(context.Context, string, string) (string, error) {
		seen = append(seen, statusNow())
		return "the-idea.html", nil
	}
	e.rulinky.fn = func(context.Context, string, string) (string, error) {
		seen = append(seen, statusNow())
		return "link-1", nil
	}

	must(t, e.captions(v.ID))

	got := e.reload(v.ID)
	if got.Status != "done" || got.HTMLFilename == nil || *got.HTMLFilename != "the-idea.html" ||
		got.RulinkyLinkID == nil || *got.RulinkyLinkID != "link-1" {
		t.Fatalf("got %+v", got)
	}
	if want := []string{"summarizing", "publishing", "publishing"}; fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("statuses seen by summarizer, writer, rulinky = %v, want %v", seen, want)
	}
	if e.summ.input != "WEBVTT\nhello" || e.writer.title != "The Idea" || e.writer.html != "<p>x</p>" {
		t.Fatalf("inputs: %q %q %q", e.summ.input, e.writer.title, e.writer.html)
	}
	if e.rulinky.links[0] != "https://example.com/the-idea.html" || e.rulinky.notes[0] != "The Idea" {
		t.Fatalf("rulinky got %v %v", e.rulinky.links, e.rulinky.notes)
	}
	wantMsgs := []string{
		fmt.Sprintf("[twitter-video #%d] summarized: The Idea", v.ID),
		fmt.Sprintf("[twitter-video #%d] published: https://example.com/the-idea.html", v.ID),
	}
	if fmt.Sprint(e.slack.successes) != fmt.Sprint(wantMsgs) {
		t.Fatalf("slack %q want %q", e.slack.successes, wantMsgs)
	}
	if len(e.queued(KindTwitterVideoCaptions, v.ID)) != 0 {
		t.Fatal("a finished video must not queue another check")
	}
}

func TestCaptionsRulinkyWithoutAnIDStoresNoLinkID(t *testing.T) {
	e := newVideoEnv(t)
	e.rulinky.id = ""
	v := e.awaiting(tweetURL)
	must(t, e.captions(v.ID))
	if got := e.reload(v.ID); got.Status != "done" || got.RulinkyLinkID != nil {
		t.Fatalf("got %+v", got)
	}
}

// Ruby: "on miss with checkpoints remaining: reschedules and bumps attempts".
func TestCaptionsMissReschedulesAndBumpsAttempts(t *testing.T) {
	e := newVideoEnv(t)
	e.ytdlp.subsFn = func(_ context.Context, _, _, _ string) (string, error) { return "", nil }
	v := e.awaiting(tweetURL)
	must(t, e.captions(v.ID))

	got := e.reload(v.ID)
	if got.CaptionAttempts != 1 || got.Status != "awaiting_captions" {
		t.Fatalf("got %+v", got)
	}
	// Upload just finished; attempt 1 waits for the second checkpoint (10 min).
	q := e.queued(KindTwitterVideoCaptions, v.ID)
	if len(q) != 1 || !near(q[0].Delay, 10*time.Minute) {
		t.Fatalf("queued %+v", q)
	}
	if want := fmt.Sprintf("[twitter-video #%d] captions not ready (attempt 1); retrying", v.ID); e.slack.lastSuccess() != want {
		t.Fatalf("slack %q", e.slack.lastSuccess())
	}
	if e.summ.callCount() != 0 {
		t.Fatal("summarized without captions")
	}
}

// The wait is NextCaptionWait: time left until upload + checkpoint, never negative.
func TestCaptionsRescheduleWaitCountsFromTheUploadTime(t *testing.T) {
	e := newVideoEnv(t)
	e.ytdlp.subsFn = func(_ context.Context, _, _, _ string) (string, error) { return "", nil }

	v := e.awaiting(tweetURL)
	e.update(v.ID, store.TwitterVideoChanges{UploadCompletedAt: new(time.Now().Add(-8 * time.Minute))})
	must(t, e.captions(v.ID)) // attempt 0 -> 1: the 10 minute mark is 2 minutes away
	if q := e.queued(KindTwitterVideoCaptions, v.ID); len(q) != 1 || !near(q[0].Delay, 2*time.Minute) {
		t.Fatalf("queued %+v", q)
	}

	late := e.awaiting(tweetURL)
	e.update(late.ID, store.TwitterVideoChanges{UploadCompletedAt: new(time.Now().Add(-3 * time.Hour))})
	must(t, e.captions(late.ID)) // the 10 minute mark has long passed: run now
	if q := e.queued(KindTwitterVideoCaptions, late.ID); len(q) != 1 || q[0].Delay > 5*time.Second {
		t.Fatalf("queued %+v", q)
	}
}

// Ruby: "on yt-dlp error: waits for the next checkpoint instead of failing".
func TestCaptionsYtdlpErrorCountsAsNotReady(t *testing.T) {
	e := newVideoEnv(t)
	e.ytdlp.subsFn = func(_ context.Context, _, _, _ string) (string, error) {
		return "", &video.YtdlpError{Message: "HTTP 429"}
	}
	v := e.awaiting(tweetURL)
	must(t, e.captions(v.ID))
	got := e.reload(v.ID)
	if got.Status != "awaiting_captions" || got.CaptionAttempts != 1 {
		t.Fatalf("got %+v", got)
	}
	if len(e.queued(KindTwitterVideoCaptions, v.ID)) != 1 {
		t.Fatal("next check not queued")
	}
	if _, f := e.slack.counts(); f != 0 {
		t.Fatalf("unexpected failure notices %v", e.slack.failures)
	}
}

// Ruby: "passes the exported ego lite cookies to yt-dlp" (and the temp dir is
// private and gone afterwards).
func TestCaptionsPassesExportedCookiesAndCleansUp(t *testing.T) {
	e := newVideoEnv(t)
	var dir string
	e.ytdlp.subsFn = func(_ context.Context, _, d, cookies string) (string, error) {
		dir = d
		if filepath.Dir(cookies) != d {
			t.Errorf("cookies %q not in the scratch dir %q", cookies, d)
		}
		return writeVtt(d, "WEBVTT\nhello")
	}
	v := e.awaiting(tweetURL)
	must(t, e.captions(v.ID))
	if filepath.Base(e.ytdlp.cookies()) != "cookies.txt" {
		t.Fatalf("cookies = %q", e.ytdlp.cookies())
	}
	if len(e.cookies.modes) != 1 || e.cookies.modes[0] != 0o700 {
		t.Fatalf("scratch dir modes %v", e.cookies.modes)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("scratch dir %q left behind (%v)", dir, err)
	}
}

// Ruby: "still fetches anonymously when the cookies can't be exported".
func TestCaptionsStillFetchesAnonymouslyWhenCookiesCannotBeExported(t *testing.T) {
	for _, err := range []error{&video.EgoCookiesError{Message: "no key"}, fmt.Errorf("wrapped: %w", &video.EgoCookiesError{Message: "x"})} {
		e := newVideoEnv(t)
		e.cookies.err = err
		v := e.awaiting(tweetURL)
		must(t, e.captions(v.ID))
		if e.ytdlp.cookies() != "" || e.ytdlp.subsCalls != 1 {
			t.Fatalf("cookies %q subsCalls %d", e.ytdlp.cookies(), e.ytdlp.subsCalls)
		}
		if got := e.reload(v.ID); got.Status != "done" {
			t.Fatalf("got %+v", got)
		}
	}
}

// A cancelled context is not "no cookies": it propagates and nothing changes.
func TestCaptionsCancelledDuringCookieExportReleasesWithoutChanges(t *testing.T) {
	e := newVideoEnv(t)
	cctx, cancel := context.WithCancel(ctx)
	e.cookies.err = context.Canceled
	cancel()
	v := e.awaiting(tweetURL)
	// ctx is already done, so the handler stops before touching anything.
	if err := NewTwitterVideoCaptions(e.deps)(cctx, Args{ID: v.ID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	got := e.reload(v.ID)
	if got.Status != "awaiting_captions" || got.CaptionAttempts != 0 || len(e.queued(KindTwitterVideoCaptions, v.ID)) != 0 {
		t.Fatalf("got %+v", got)
	}

	// Cancelled while the export runs (not already done at the start).
	e2 := newVideoEnv(t)
	cctx2, cancel2 := context.WithCancel(ctx)
	e2.deps.Cookies = cookieFunc(func(ctx context.Context, _ string) (string, error) {
		cancel2()
		return "", ctx.Err()
	})
	v2 := e2.awaiting(tweetURL)
	if err := NewTwitterVideoCaptions(e2.deps)(cctx2, Args{ID: v2.ID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if got := e2.reload(v2.ID); got.Status != "awaiting_captions" || got.CaptionAttempts != 0 || e2.ytdlp.subsCalls != 0 {
		t.Fatalf("got %+v", got)
	}
}

type cookieFunc func(ctx context.Context, path string) (string, error)

func (f cookieFunc) Export(ctx context.Context, path string) (string, error) { return f(ctx, path) }

// Ruby: "on miss at final checkpoint: fails".
func TestCaptionsMissAtFinalCheckpointFails(t *testing.T) {
	e := newVideoEnv(t)
	e.ytdlp.subsFn = func(_ context.Context, _, _, _ string) (string, error) { return "", nil }
	v := e.awaiting(tweetURL)
	e.update(v.ID, store.TwitterVideoChanges{CaptionAttempts: new(len(store.CaptionCheckpoints) - 1)})
	must(t, e.captions(v.ID))

	got := e.reload(v.ID)
	if got.Status != "failed" || got.ErrorDetail == nil || *got.ErrorDetail != "Auto-captions never became available." ||
		got.CaptionAttempts != len(store.CaptionCheckpoints) {
		t.Fatalf("got %+v", got)
	}
	want := fmt.Sprintf("[twitter-video #%d] captions timed out after 2880 minutes", v.ID)
	if e.slack.lastFailure() != want {
		t.Fatalf("slack %q want %q", e.slack.lastFailure(), want)
	}
	if len(e.queued(KindTwitterVideoCaptions, v.ID)) != 0 {
		t.Fatal("an exhausted video must not queue another check")
	}
}

// A YouTube link has the short schedule: its last checkpoint is 120 minutes.
func TestCaptionsYoutubeLinkExhaustsAtItsOwnLastCheckpoint(t *testing.T) {
	e := newVideoEnv(t)
	e.ytdlp.subsFn = func(_ context.Context, _, _, _ string) (string, error) { return "", nil }
	v := e.awaiting("https://www.youtube.com/watch?v=abcdefghijk")
	e.update(v.ID, store.TwitterVideoChanges{CaptionAttempts: new(len(store.YoutubeLinkCheckpoints) - 1)})
	must(t, e.captions(v.ID))
	if got := e.reload(v.ID); got.Status != "failed" {
		t.Fatalf("got %+v", got)
	}
	if want := fmt.Sprintf("[twitter-video #%d] captions timed out after 120 minutes", v.ID); e.slack.lastFailure() != want {
		t.Fatalf("slack %q", e.slack.lastFailure())
	}
}

func TestMinutesInspectIsActiveSupportDurationInspect(t *testing.T) {
	for n, want := range map[int]string{1: "1 minute", 5: "5 minutes", 120: "120 minutes", 2880: "2880 minutes"} {
		if got := minutesInspect(n); got != want {
			t.Errorf("minutesInspect(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestCaptionsSkipsMissingAndNotAwaitingVideos(t *testing.T) {
	e := newVideoEnv(t)
	must(t, e.captions(424242))
	for _, status := range []string{"downloading", "uploading", "done", "failed"} {
		v := e.create(store.NewTwitterVideo{SourceURL: tweetURL, Status: status})
		must(t, e.captions(v.ID))
		if got := e.reload(v.ID); got.Status != status || got.CaptionAttempts != 0 {
			t.Fatalf("%s: got %+v", status, got)
		}
	}
	if e.ytdlp.subsCalls != 0 {
		t.Fatal("fetched captions for rows that are not awaiting them")
	}
}

// --- errors in the publish chain --------------------------------------------

func TestCaptionsPublishFailuresFailTheVideoWithRubysTexts(t *testing.T) {
	cases := []struct {
		name       string
		set        func(e *videoEnv)
		detail     string
		slackClass string
	}{
		{"summarizer", func(e *videoEnv) {
			e.summ.fn = func(context.Context, string) (llm.Summary, error) {
				return llm.Summary{}, llm.ErrOpenAI("OpenAI summarization failed.")
			}
		}, "OpenAI summarization failed.", "OpenaiSummarizer::Error: OpenAI summarization failed."},
		{"summarizer unconfigured", func(e *videoEnv) {
			e.summ.fn = func(context.Context, string) (llm.Summary, error) { return llm.Summary{}, llm.ErrConfiguration }
		}, "OPENAI_API_KEY is not configured.", "OpenaiSummarizer::ConfigurationError: OPENAI_API_KEY is not configured."},
		{"rulinky", func(e *videoEnv) {
			e.rulinky.fn = func(context.Context, string, string) (string, error) {
				return "", &video.RulinkyError{Message: "rulinky link creation failed."}
			}
		}, "rulinky link creation failed.", "RulinkyClient::Error: rulinky link creation failed."},
		{"rulinky unconfigured", func(e *videoEnv) {
			e.rulinky.fn = func(context.Context, string, string) (string, error) {
				return "", &video.RulinkyError{Message: "RULINKY_HOST and RULINKY_API_TOKEN must be configured.", Config: true}
			}
		}, "RULINKY_HOST and RULINKY_API_TOKEN must be configured.", "RulinkyClient::ConfigurationError: RULINKY_HOST and RULINKY_API_TOKEN must be configured."},
		{"writer", func(e *videoEnv) {
			e.writer.fn = func(context.Context, string, string) (string, error) {
				return "", errors.New("open /srv/files/x.html: permission denied")
			}
		}, "Processing failed.", "*errors.errorString: open /srv/files/x.html: permission denied"},
		{"fetch start failure", func(e *videoEnv) {
			e.ytdlp.subsFn = func(context.Context, string, string, string) (string, error) {
				return "", errors.New("yt-dlp subs fetch: fork/exec /opt/yt-dlp: no such file or directory")
			}
		}, "Processing failed.", "*errors.errorString: yt-dlp subs fetch: fork/exec /opt/yt-dlp: no such file or directory"},
		{"refused url", func(e *videoEnv) {
			e.ytdlp.subsFn = func(context.Context, string, string, string) (string, error) { return "", video.ErrInvalidURL }
		}, "yt-dlp: url not allowed", "*errors.errorString: yt-dlp: url not allowed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newVideoEnv(t)
			c.set(e)
			v := e.awaiting(tweetURL)
			must(t, e.captions(v.ID))
			got := e.reload(v.ID)
			if got.Status != "failed" || got.ErrorDetail == nil || *got.ErrorDetail != c.detail {
				t.Fatalf("got %+v", got)
			}
			// Errors with no Ruby counterpart are named by the Go type of their root cause.
			want := fmt.Sprintf("[twitter-video #%d] captions/publish failed: %s", v.ID, c.slackClass)
			if e.slack.lastFailure() != want {
				t.Fatalf("slack %q want %q", e.slack.lastFailure(), want)
			}
		})
	}
}

// The page exists but only recording it in served_files failed: carry on with
// the name, as the writer documents (a second write would make a second page).
func TestCaptionsWriterThatOnlyFailedToRecordStillPublishes(t *testing.T) {
	e := newVideoEnv(t)
	e.writer.fn = func(context.Context, string, string) (string, error) {
		return "the-idea.html", errors.New("record failed")
	}
	v := e.awaiting(tweetURL)
	must(t, e.captions(v.ID))
	if got := e.reload(v.ID); got.Status != "done" || *got.HTMLFilename != "the-idea.html" || e.writer.calls != 1 {
		t.Fatalf("got %+v calls=%d", got, e.writer.calls)
	}
}

// Ruby would raise NoMethodError on a nil upload_completed_at at reschedule time.
func TestCaptionsMissWithoutUploadTimeFailsTheVideo(t *testing.T) {
	e := newVideoEnv(t)
	e.ytdlp.subsFn = func(context.Context, string, string, string) (string, error) { return "", nil }
	v := e.create(store.NewTwitterVideo{SourceURL: tweetURL, Status: "awaiting_captions"})
	must(t, e.captions(v.ID))
	got := e.reload(v.ID)
	if got.Status != "failed" || got.ErrorDetail == nil || *got.ErrorDetail != "Upload completion time is missing." {
		t.Fatalf("got %+v", got)
	}
	if len(e.queued(KindTwitterVideoCaptions, v.ID)) != 0 {
		t.Fatal("queued a check without a schedule")
	}
}

// --- idempotency ------------------------------------------------------------

// Shutdown while summarizing: the status goes back to awaiting_captions (not
// failed), the error is returned so the job is released, and the re-run finishes.
func TestCaptionsShutdownDuringPublishPutsTheVideoBack(t *testing.T) {
	e := newVideoEnv(t)
	cctx, cancel := context.WithCancel(ctx)
	e.summ.fn = func(ctx context.Context, _ string) (llm.Summary, error) {
		cancel()
		return llm.Summary{}, llm.ErrOpenAI("OpenAI summarization failed.") // wraps nothing: the handler looks at ctx
	}
	v := e.awaiting(tweetURL)
	err := NewTwitterVideoCaptions(e.deps)(cctx, Args{ID: v.ID})
	if err == nil {
		t.Fatal("a cancelled run must return an error so the Runner releases the job")
	}
	got := e.reload(v.ID)
	if got.Status != "awaiting_captions" || got.ErrorDetail != nil || got.CaptionAttempts != 0 {
		t.Fatalf("got %+v", got)
	}
	if _, f := e.slack.counts(); f != 0 {
		t.Fatalf("shutdown is not a failure: %v", e.slack.failures)
	}

	e.summ.fn = nil
	must(t, e.captions(v.ID))
	if got := e.reload(v.ID); got.Status != "done" {
		t.Fatalf("re-run: %+v", got)
	}
}

// A run that died leaves summarizing/publishing behind; the queue hands the job
// out again after its stale window and the handler takes the row over, but only
// once the row is old enough to have no live owner.
func TestCaptionsRerunTakesOverAStaleInFlightRow(t *testing.T) {
	for _, status := range []string{"summarizing", "publishing"} {
		t.Run(status, func(t *testing.T) {
			e := newVideoEnv(t)
			v := e.awaiting(tweetURL)
			e.update(v.ID, store.TwitterVideoChanges{Status: &status})

			must(t, e.captions(v.ID)) // fresh: someone may be working on it
			if got := e.reload(v.ID); got.Status != status || e.ytdlp.subsCalls != 0 {
				t.Fatalf("a fresh in-flight row was touched: %+v", got)
			}

			e.ageRow(v.ID, DefaultVideoStaleAfter+time.Minute)
			must(t, e.captions(v.ID))
			if got := e.reload(v.ID); got.Status != "done" {
				t.Fatalf("stale row not taken over: %+v", got)
			}
		})
	}
}

// Two jobs for the same check (a duplicate): only one moves the schedule on
// and queues the successor, so duplicates never multiply.
func TestCaptionsDuplicateJobsQueueOneSuccessor(t *testing.T) {
	e := newVideoEnv(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	e.ytdlp.subsFn = func(ctx context.Context, _, _, _ string) (string, error) {
		first := false
		once.Do(func() { first = true })
		if first { // job A reads the row, then stalls inside the fetch
			close(started)
			<-release
		}
		return "", nil
	}
	v := e.awaiting(tweetURL)

	done := make(chan error)
	go func() { done <- e.captions(v.ID) }()
	<-started
	must(t, e.captions(v.ID)) // job B runs to completion: attempt 0 -> 1, successor queued
	close(release)
	must(t, <-done) // job A resumes with a stale view of the attempt

	got := e.reload(v.ID)
	if got.CaptionAttempts != 1 {
		t.Fatalf("attempts = %d: a duplicate counted the same check twice", got.CaptionAttempts)
	}
	if n := len(e.queued(KindTwitterVideoCaptions, v.ID)); n != 1 {
		t.Fatalf("%d successors queued, want 1", n)
	}
	if s, _ := e.slack.counts(); s != 1 {
		t.Fatalf("%d 'not ready' notices, want 1: %v", s, e.slack.successes)
	}
}

// Two jobs both find the captions: one publishes, the other finds the row
// taken and does nothing.
func TestCaptionsDuplicateJobsPublishOnce(t *testing.T) {
	e := newVideoEnv(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	e.ytdlp.subsFn = func(ctx context.Context, _, dir, _ string) (string, error) {
		first := false
		once.Do(func() { first = true })
		if first {
			close(started)
			<-release
		}
		return writeVtt(dir, "WEBVTT\nhello")
	}
	v := e.awaiting(tweetURL)
	done := make(chan error)
	go func() { done <- e.captions(v.ID) }()
	<-started
	must(t, e.captions(v.ID))
	close(release)
	must(t, <-done)

	if e.summ.callCount() != 1 || e.writer.calls != 1 || e.rulinky.calls != 1 {
		t.Fatalf("summarized %d, wrote %d, pushed %d times", e.summ.callCount(), e.writer.calls, e.rulinky.calls)
	}
	if got := e.reload(v.ID); got.Status != "done" {
		t.Fatalf("got %+v", got)
	}
}

// The enqueue of the next check failed: the attempt is counted, the row stays
// awaiting_captions, and the job reports the error; the requeue sweeper heals it.
func TestCaptionsEnqueueFailureKeepsTheRowAwaiting(t *testing.T) {
	e := newVideoEnv(t)
	e.deps.Enqueuer = &failingEnqueuer{next: DBEnqueuer{DB: e.pool}, fails: 1}
	e.ytdlp.subsFn = func(context.Context, string, string, string) (string, error) { return "", nil }
	v := e.awaiting(tweetURL)
	if err := e.captions(v.ID); err == nil {
		t.Fatal("the enqueue error must surface")
	}
	if got := e.reload(v.ID); got.Status != "awaiting_captions" || got.CaptionAttempts != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestCaptionsDeadlineFailsTheVideo(t *testing.T) {
	e := newVideoEnv(t)
	e.deps.CaptionsTimeout = 50 * time.Millisecond // the handler enforces its own deadline
	e.summ.fn = func(ctx context.Context, _ string) (llm.Summary, error) {
		<-ctx.Done()
		return llm.Summary{}, fmt.Errorf("summarize: %w", ctx.Err())
	}
	v := e.awaiting(tweetURL)
	if err := e.captions(v.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if got := e.reload(v.ID); got.Status != "failed" || *got.ErrorDetail != "Processing failed." {
		t.Fatalf("got %+v", got)
	}
}
