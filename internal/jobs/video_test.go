package jobs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/llm"
	"serve-html-markdown/internal/store"
	"serve-html-markdown/internal/video"
)

func TestHasPendingJob(t *testing.T) {
	pool := testdb.New(t)
	has := func(kind string, id int64) bool {
		t.Helper()
		ok, err := HasPendingJob(ctx, pool, kind, id)
		must(t, err)
		return ok
	}
	if has(KindTwitterVideoCaptions, 7) {
		t.Fatal("empty queue")
	}
	// Runnable later, running (locked) and finished rows.
	must(t, Enqueue(ctx, pool, KindTwitterVideoCaptions, Args{ID: 7}, time.Hour))
	if !has(KindTwitterVideoCaptions, 7) || has(KindTwitterVideoCaptions, 8) || has(KindTwitterVideoIngest, 7) {
		t.Fatal("must match kind and id exactly")
	}
	insertAt(t, pool, KindTwitterVideoIngest, "now()", "now()", "NULL") // args id 7, locked: running
	if !has(KindTwitterVideoIngest, 7) {
		t.Fatal("a running job is pending")
	}
	if _, err := pool.Exec(ctx, `UPDATE go_jobs SET finished_at = now()`); err != nil {
		t.Fatal(err)
	}
	if has(KindTwitterVideoCaptions, 7) || has(KindTwitterVideoIngest, 7) {
		t.Fatal("finished jobs are not pending")
	}
}

func TestVideoHandlersAndTimeouts(t *testing.T) {
	e := newVideoEnv(t)
	h := VideoHandlers(e.deps)
	var kinds []string
	for k, fn := range h {
		kinds = append(kinds, k)
		if fn == nil {
			t.Errorf("%s: nil handler", k)
		}
	}
	sort.Strings(kinds)
	if want := []string{KindTwitterVideoCaptions, KindTwitterVideoIngest}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	to := VideoTimeouts()
	if to[KindTwitterVideoIngest] != RecommendedIngestTimeout || to[KindTwitterVideoCaptions] != RecommendedCaptionsTimeout ||
		RecommendedIngestTimeout != 2*time.Hour || RecommendedCaptionsTimeout != 15*time.Minute || len(to) != 2 {
		t.Fatalf("timeouts = %v", to)
	}
	// The sweeper must not fail a row the ingest timeout still allows to run.
	if DefaultVideoSweepAfter <= RecommendedIngestTimeout {
		t.Fatalf("sweep window %v <= ingest timeout %v", DefaultVideoSweepAfter, RecommendedIngestTimeout)
	}
	// Handlers drive the same code the constructors do.
	v := e.newTweet()
	must(t, h[KindTwitterVideoIngest](ctx, Args{ID: v.ID}))
	if e.reload(v.ID).Status != "awaiting_captions" {
		t.Fatal("ingest handler did not run")
	}
}

func TestVideoSweeperFailsStuckRowsOnly(t *testing.T) {
	e := newVideoEnv(t)
	sweep := VideoSweepers(e.deps)
	if len(sweep) != 2 {
		t.Fatalf("%d sweepers", len(sweep))
	}
	stuck := e.create(store.NewTwitterVideo{SourceURL: tweetURL, Status: "uploading"})
	e.ageRow(stuck.ID, 4*time.Hour)
	fresh := e.create(store.NewTwitterVideo{SourceURL: tweetURL, Status: "uploading"})
	waiting := e.awaiting(tweetURL)
	e.ageRow(waiting.ID, 4*time.Hour)
	for _, s := range sweep {
		must(t, s(ctx))
	}
	if got := e.reload(stuck.ID); got.Status != "failed" || *got.ErrorDetail != "Processing failed." {
		t.Fatalf("stuck: %+v", got)
	}
	if got := e.reload(fresh.ID); got.Status != "uploading" {
		t.Fatalf("fresh: %+v", got)
	}
	if got := e.reload(waiting.ID); got.Status != "awaiting_captions" {
		t.Fatalf("awaiting_captions must not be failed by age: %+v", got)
	}
}

// RequeueStaleCaptionChecks heals a video whose caption check was lost (an
// enqueue that failed, a job row deleted): overdue by more than the grace and
// nothing pending for it.
func TestRequeueStaleCaptionChecks(t *testing.T) {
	e := newVideoEnv(t)
	grace := e.deps.withDefaults().RequeueGrace
	now := time.Now().UTC()
	mk := func(uploadedAgo time.Duration, attempts int) *store.TwitterVideo {
		v := e.awaiting(tweetURL)
		e.update(v.ID, store.TwitterVideoChanges{UploadCompletedAt: new(now.Add(-uploadedAgo)), CaptionAttempts: &attempts})
		return v
	}
	lost := mk(3*time.Hour, 2)    // check due at 20 min: 2h40m overdue
	waiting := mk(3*time.Hour, 2) // same, but its check is queued
	must(t, Enqueue(ctx, e.pool, KindTwitterVideoCaptions, Args{ID: waiting.ID}, 0))
	early := mk(10*time.Minute, 0)     // due at 5 min: only 5 minutes late, inside the grace
	future := mk(time.Minute, 0)       // due in 4 minutes
	longWait := mk(10*time.Hour, 7)    // next check at 24h: not due yet
	exhausted := mk(5*24*time.Hour, 9) // schedule used up: a run will fail it properly
	noTime := e.create(store.NewTwitterVideo{SourceURL: tweetURL, Status: "awaiting_captions"})
	e.ageRow(noTime.ID, 3*time.Hour)
	if grace < 30*time.Minute {
		t.Fatalf("grace %v is too short", grace)
	}

	must(t, RequeueStaleCaptionChecks(e.deps)(ctx))

	count := func(v *store.TwitterVideo) int { return len(e.queued(KindTwitterVideoCaptions, v.ID)) }
	for name, c := range map[string]struct {
		v    *store.TwitterVideo
		want int
	}{"lost": {lost, 1}, "waiting": {waiting, 1}, "early": {early, 0}, "future": {future, 0},
		"longWait": {longWait, 0}, "exhausted": {exhausted, 1}, "noTime": {noTime, 1}} {
		if got := count(c.v); got != c.want {
			t.Errorf("%s: %d jobs queued, want %d", name, got, c.want)
		}
	}
	if q := e.queued(KindTwitterVideoCaptions, lost.ID); len(q) == 1 && q[0].Delay > 5*time.Second {
		t.Errorf("the lost check should run now, delay %v", q[0].Delay)
	}
	// Running it again adds nothing: the requeued checks are pending now.
	must(t, RequeueStaleCaptionChecks(e.deps)(ctx))
	if count(lost) != 1 || count(exhausted) != 1 {
		t.Fatal("the sweeper queued a duplicate")
	}
}

var rubyStringJoin = regexp.MustCompile(`"\s*\\\n\s*"`)

// Ruby message text is the specification: every literal fragment of every
// message the two jobs send or store must appear in the Ruby source they were
// ported from. The messages are rendered with markers where a value goes in, and
// the text between the markers is looked up in the source.
func TestMessagesMatchTheRubySource(t *testing.T) {
	var src strings.Builder
	for _, f := range []string{"twitter_video_ingest_job.rb", "twitter_video_captions_job.rb"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "app", "jobs", f))
		if err != nil {
			t.Skipf("Ruby source not present (%v)", err)
		}
		src.Write(b)
	}
	// Ruby breaks long strings with `" \` + newline + `"`; join them back.
	ruby := rubyStringJoin.ReplaceAllString(src.String(), "")

	const mark = "\u2063" // invisible separator: where a value was interpolated
	cases := map[string]string{
		"downloaded":      msgDownloaded(424242, mark),
		"uploaded":        msgUploaded(424242, mark, mark),
		"ingest failed":   msgIngestFailed(424242, mark, mark),
		"reauth":          msgReauthRequired(424242, mark),
		"captions failed": msgCaptionsFailed(424242, mark, mark),
		"timed out":       msgCaptionsTimedOut(424242, mark),
		"not ready":       msgCaptionsNotReady(424242, 99),
		"summarized":      msgSummarized(424242, mark),
		"published":       msgPublished(424242, mark),
		"never available": msgCaptionsNeverAvailable,
		"studio link":     strings.ReplaceAll(studioEditURL, "%s", mark),
		"fallback title":  fallbackTitle,
		"link":            publishedLink(mark, mark),
	}
	for name, text := range cases {
		// The studio link is Ruby's STUDIO_EDIT_URL constant, compared as "studio link".
		if name != "studio link" {
			text = strings.ReplaceAll(text, strings.ReplaceAll(studioEditURL, "%s", mark), mark)
		}
		text = strings.ReplaceAll(text, "424242", mark)
		text = strings.ReplaceAll(text, "99", mark)
		checked := 0
		for _, frag := range strings.Split(text, mark) {
			if frag == "" {
				continue
			}
			checked++
			// Real newlines are written \n inside Ruby strings.
			if !strings.Contains(ruby, strings.ReplaceAll(frag, "\n", `\n`)) {
				t.Errorf("%s: %q is not in the Ruby source", name, frag)
			}
		}
		if checked == 0 {
			t.Errorf("%s: nothing was compared", name)
		}
	}
}

func TestRubyErrorClassNames(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{&video.YtdlpError{Message: "x"}, "YtdlpClient::Error"},
		{&video.YoutubeError{Message: "x"}, "YoutubeUploader::Error"},
		{&video.YoutubeError{Message: "x", Expired: true}, "YoutubeUploader::AuthorizationExpired"},
		{&video.EgoCookiesError{Message: "x"}, "EgoCookies::Error"},
		{&video.RulinkyError{Message: "x"}, "RulinkyClient::Error"},
		{&video.RulinkyError{Message: "x", Config: true}, "RulinkyClient::ConfigurationError"},
		{llm.ErrOpenAI("x"), "OpenaiSummarizer::Error"},
		{llm.ErrConfiguration, "OpenaiSummarizer::ConfigurationError"},
		{fmt.Errorf("wrapped: %w", &video.YtdlpError{Message: "x"}), "YtdlpClient::Error"},
		{errors.New("x"), "StandardError"},
	} {
		if got := rubyErrorClass(c.err); got != c.want {
			t.Errorf("%T: %q, want %q", c.err, got, c.want)
		}
	}
}

func TestNewLinkPublisherReportsMissingConfigurationAtTheCall(t *testing.T) {
	_, err := NewLinkPublisher("", "").CreateLink(ctx, "https://example.com/x.html", "note")
	if !errors.Is(err, video.ErrRulinkyConfiguration) {
		t.Fatalf("got %v", err)
	}
	if NewLinkPublisher("https://rulinky.example", "tok") == nil {
		t.Fatal("a configured publisher is expected")
	}
}
