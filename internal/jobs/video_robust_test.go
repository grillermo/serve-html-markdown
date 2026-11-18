package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"serve-html-markdown/internal/llm"
	"serve-html-markdown/internal/store"
	"serve-html-markdown/internal/video"
)

// gate lets a fake collaborator hold a run in the middle of its work.
type gate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGate() *gate { return &gate{entered: make(chan struct{}), release: make(chan struct{})} }

func (g *gate) hold() {
	g.once.Do(func() { close(g.entered) })
	<-g.release
}

func (g *gate) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no run reached the gate")
	}
}

func goIngest(e *videoEnv, id int64) chan error {
	done := make(chan error, 1)
	go func() { done <- e.ingest(id) }()
	return done
}

func waitErr(t *testing.T, ch chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("run did not finish")
		return nil
	}
}

// --- lease -------------------------------------------------------------------

// Two ingest runs for one row at the same moment: exactly one uploads.
func TestIngestTwoRunsAtOnceUploadOnce(t *testing.T) {
	e := newVideoEnv(t)
	g := newGate()
	e.uploader.fn = func(context.Context, string, string) (string, error) { g.hold(); return "YT9", nil }
	v := e.newTweet()

	a, b := goIngest(e, v.ID), goIngest(e, v.ID)
	g.waitEntered(t)
	// The run that did not get the lease is done while the other still uploads.
	var first chan error
	select {
	case err := <-a:
		must(t, err)
		first = b
	case err := <-b:
		must(t, err)
		first = a
	case <-time.After(10 * time.Second):
		t.Fatal("the run without the lease did not return")
	}
	close(g.release)
	must(t, waitErr(t, first))

	if n := e.uploader.callCount(); n != 1 {
		t.Fatalf("Upload called %d times", n)
	}
	if got := e.reload(v.ID); got.Status != "awaiting_captions" || *got.YoutubeID != "YT9" {
		t.Fatalf("got %+v", got)
	}
	if n := len(e.queued(KindTwitterVideoCaptions, v.ID)); n != 1 {
		t.Fatalf("%d caption checks queued", n)
	}
	if _, f := e.slack.counts(); f != 0 {
		t.Fatalf("failure notices: %v", e.slack.failures)
	}
}

// A resubmit while the first run is still downloading enqueues a second job.
func TestIngestResubmitWhileRunningDoesNothing(t *testing.T) {
	e := newVideoEnv(t)
	g := newGate()
	e.ytdlp.downloadFn = func(_ context.Context, _, dir string) (video.Downloaded, error) {
		g.hold()
		must(t, os.MkdirAll(dir, 0o700))
		p := filepath.Join(dir, "x.mp4")
		return video.Downloaded{Path: p, Title: "T"}, os.WriteFile(p, nil, 0o600)
	}
	v := e.newTweet()
	a := goIngest(e, v.ID)
	g.waitEntered(t)

	must(t, e.ingest(v.ID)) // the second job: returns at once, touching nothing
	if e.uploader.callCount() != 0 || e.ytdlp.downloadCount() != 1 {
		t.Fatalf("the second run worked: uploads %d downloads %d", e.uploader.callCount(), e.ytdlp.downloadCount())
	}
	close(g.release)
	must(t, waitErr(t, a))
	if e.uploader.callCount() != 1 || e.reload(v.ID).Status != "awaiting_captions" {
		t.Fatal("the first run did not finish")
	}
}

// The re-authorization retry arrives while another retry of the same failed row runs.
func TestIngestReauthRetryWhileAnotherRetryRuns(t *testing.T) {
	e := newVideoEnv(t)
	v := e.newTweet()
	must(t, store.FailTwitterVideo(ctx, e.pool, v.ID, "invalid_grant"))
	g := newGate()
	e.uploader.fn = func(context.Context, string, string) (string, error) { g.hold(); return "YT9", nil }
	a := goIngest(e, v.ID)
	g.waitEntered(t)
	if got := e.reload(v.ID); got.Status != "uploading" {
		t.Fatalf("a retry in flight shows %q", got.Status)
	}
	must(t, e.ingest(v.ID))
	close(g.release)
	must(t, waitErr(t, a))
	if n := e.uploader.callCount(); n != 1 {
		t.Fatalf("Upload called %d times", n)
	}
}

// While a run works, its heartbeat keeps the lease: a second run that starts
// well after the lease length still finds the video held.
func TestIngestHeartbeatKeepsTheLease(t *testing.T) {
	e := newVideoEnv(t)
	e.deps.IngestLease = 300 * time.Millisecond
	g := newGate()
	e.uploader.fn = func(context.Context, string, string) (string, error) { g.hold(); return "YT9", nil }
	v := e.newTweet()
	a := goIngest(e, v.ID)
	g.waitEntered(t)

	time.Sleep(900 * time.Millisecond) // three leases
	must(t, e.ingest(v.ID))
	if n := e.uploader.callCount(); n != 1 {
		t.Fatalf("the lease expired under a live run (Upload called %d times)", n)
	}
	close(g.release)
	must(t, waitErr(t, a))
}

// A run that died leaves its lease to expire; then the job's re-run takes over.
func TestIngestRerunAfterAnExpiredLeaseTakesOver(t *testing.T) {
	e := newVideoEnv(t)
	existing := e.putVideoFile("x.mp4")
	v := e.newTweet()
	e.update(v.ID, store.TwitterVideoChanges{Status: new("uploading"), VideoPath: &existing, YoutubeTitle: new("T")})

	must(t, e.ingest(v.ID)) // the dead run touched it moments ago: still held
	if e.uploader.callCount() != 0 {
		t.Fatal("took a lease that is still alive")
	}
	e.ageRow(v.ID, e.deps.withDefaults().IngestLease+time.Minute)
	must(t, e.ingest(v.ID))
	if got := e.reload(v.ID); got.Status != "awaiting_captions" || e.uploader.callCount() != 1 {
		t.Fatalf("got %+v uploads=%d", got, e.uploader.callCount())
	}
	if m := e.slack.lastFailure(); m != fmt.Sprintf("[twitter-video #%d] re-uploading after an interrupted run; check YouTube for a duplicate", v.ID) {
		t.Fatalf("no warning about the possible duplicate: %q", m)
	}
}

func TestIngestFreshRunPostsNoDuplicateWarning(t *testing.T) {
	e := newVideoEnv(t)
	v := e.newTweet()
	must(t, e.ingest(v.ID))
	if _, f := e.slack.counts(); f != 0 {
		t.Fatalf("unexpected failure notices %v", e.slack.failures)
	}
}

// --- panics ------------------------------------------------------------------

func TestIngestPanicsFailTheVideoInsteadOfStrandingIt(t *testing.T) {
	cases := map[string]func(e *videoEnv){
		"ytdlp": func(e *videoEnv) {
			e.ytdlp.downloadFn = func(context.Context, string, string) (video.Downloaded, error) { panic("ytdlp boom") }
		},
		"uploader factory": func(e *videoEnv) {
			e.deps.NewUploader = func(context.Context) (Uploader, error) { panic("factory boom") }
		},
		"uploader": func(e *videoEnv) {
			e.uploader.fn = func(context.Context, string, string) (string, error) { panic("upload boom") }
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			e := newVideoEnv(t)
			set(e)
			v := e.newTweet()
			must(t, e.ingest(v.ID))
			got := e.reload(v.ID)
			if got.Status != "failed" || got.ErrorDetail == nil || *got.ErrorDetail != "Processing failed." {
				t.Fatalf("got %+v", got)
			}
			if m := e.slack.lastFailure(); !contains(m, fmt.Sprintf("[twitter-video #%d] ingest failed: panic: panic: ", v.ID), "boom") {
				t.Fatalf("slack %q", m)
			}
			// The retry the failed row allows works.
			e.ytdlp.downloadFn, e.deps.NewUploader, e.uploader.fn = nil, func(context.Context) (Uploader, error) { return e.uploader, nil }, nil
			must(t, e.ingest(v.ID))
			if e.reload(v.ID).Status != "awaiting_captions" {
				t.Fatal("retry after a panic failed")
			}
		})
	}
}

// A panicking notifier is swallowed like a failed post; a panicking enqueuer is
// an enqueue error that leaves the uploaded video alone.
func TestIngestNotifierAndEnqueuerPanics(t *testing.T) {
	e := newVideoEnv(t)
	e.deps.Notifier = panicNotifier{}
	v := e.newTweet()
	must(t, e.ingest(v.ID))
	if got := e.reload(v.ID); got.Status != "awaiting_captions" || len(e.queued(KindTwitterVideoCaptions, v.ID)) != 1 {
		t.Fatalf("a notifier panic broke the run: %+v", got)
	}

	e2 := newVideoEnv(t)
	e2.deps.Enqueuer = panicEnqueuer{}
	v2 := e2.newTweet()
	if err := e2.ingest(v2.ID); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("want the enqueue panic as an error, got %v", err)
	}
	if got := e2.reload(v2.ID); got.Status != "awaiting_captions" || *got.YoutubeID != "YT9" {
		t.Fatalf("the upload must survive: %+v", got)
	}
	// ... and the re-run queues the check.
	e2.deps.Enqueuer = DBEnqueuer{DB: e2.pool}
	must(t, e2.ingest(v2.ID))
	if len(e2.queued(KindTwitterVideoCaptions, v2.ID)) != 1 || e2.uploader.callCount() != 1 {
		t.Fatal("re-run after an enqueue panic")
	}
}

type panicNotifier struct{}

func (panicNotifier) Success(context.Context, string) { panic("slack boom") }
func (panicNotifier) Failure(context.Context, string) { panic("slack boom") }

type panicEnqueuer struct{}

func (panicEnqueuer) Enqueue(context.Context, string, Args, time.Duration) error {
	panic("enqueue boom")
}

func TestCaptionsPanicsFailTheVideoInsteadOfStrandingIt(t *testing.T) {
	cases := map[string]func(e *videoEnv){
		"cookies": func(e *videoEnv) {
			e.deps.Cookies = cookieFunc(func(context.Context, string) (string, error) { panic("cookies boom") })
		},
		"ytdlp": func(e *videoEnv) {
			e.ytdlp.subsFn = func(context.Context, string, string, string) (string, error) { panic("ytdlp boom") }
		},
		"summarizer": func(e *videoEnv) {
			e.summ.fn = func(context.Context, string) (llm.Summary, error) { panic("summarizer boom") }
		},
		"writer": func(e *videoEnv) {
			e.writer.fn = func(context.Context, string, string) (string, error) { panic("writer boom") }
		},
		"rulinky": func(e *videoEnv) {
			e.rulinky.fn = func(context.Context, string, string) (string, error) { panic("rulinky boom") }
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			e := newVideoEnv(t)
			set(e)
			v := e.awaiting(tweetURL)
			must(t, e.captions(v.ID))
			got := e.reload(v.ID)
			if got.Status != "failed" || got.ErrorDetail == nil || *got.ErrorDetail != "Processing failed." {
				t.Fatalf("a panic left the video %+v", got)
			}
			if m := e.slack.lastFailure(); !contains(m, fmt.Sprintf("[twitter-video #%d] captions/publish failed: panic: panic: ", v.ID), "boom") {
				t.Fatalf("slack %q", m)
			}
		})
	}
}

func TestCaptionsEnqueuerPanicIsAnErrorAndKeepsTheRow(t *testing.T) {
	e := newVideoEnv(t)
	e.deps.Enqueuer = panicEnqueuer{}
	e.ytdlp.subsFn = noCaptions
	v := e.awaiting(tweetURL)
	if err := e.captions(v.ID); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("got %v", err)
	}
	if got := e.reload(v.ID); got.Status != "awaiting_captions" || got.CaptionAttempts != 1 {
		t.Fatalf("got %+v", got)
	}
}

// --- recording the upload ----------------------------------------------------

// flakyDB fails the first n statements that record an upload.
type flakyDB struct {
	store.DB
	mu    sync.Mutex
	fails int
	seen  int
}

func (f *flakyDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "youtube_id = $2") {
		f.mu.Lock()
		f.seen++
		fail := f.fails > 0
		if fail {
			f.fails--
		}
		f.mu.Unlock()
		if fail {
			return pgconn.CommandTag{}, errors.New("connection reset")
		}
	}
	return f.DB.Exec(ctx, sql, args...)
}

func TestIngestRetriesTheWriteThatRecordsTheUpload(t *testing.T) {
	old := recordRetryDelay
	recordRetryDelay = time.Millisecond
	defer func() { recordRetryDelay = old }()

	e := newVideoEnv(t)
	flaky := &flakyDB{DB: e.pool, fails: 2}
	e.deps.DB = flaky
	v := e.newTweet()
	must(t, e.ingest(v.ID))
	if got := e.reload(v.ID); got.Status != "awaiting_captions" || *got.YoutubeID != "YT9" || flaky.seen != 3 || e.uploader.callCount() != 1 {
		t.Fatalf("got %+v, write attempts %d", got, flaky.seen)
	}
}

func TestIngestReportsAnUploadItCouldNotRecord(t *testing.T) {
	old := recordRetryDelay
	recordRetryDelay = time.Millisecond
	defer func() { recordRetryDelay = old }()

	e := newVideoEnv(t)
	e.deps.DB = &flakyDB{DB: e.pool, fails: 10}
	v := e.newTweet()
	must(t, e.ingest(v.ID))
	if m := e.slack.lastFailure(); !contains(m, "uploaded as YouTube video YT9 but could not record it") {
		t.Fatalf("the YouTube id must reach Slack, got %q", m)
	}
	if e.uploader.callCount() != 1 {
		t.Fatal("uploaded twice")
	}
}

// --- logs --------------------------------------------------------------------

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func (e *videoEnv) captureLogs() *lockedBuffer {
	buf := &lockedBuffer{}
	e.deps.Logger = slog.New(slog.NewTextHandler(buf, nil))
	return buf
}

func TestFailureLogsTheWholeCauseChain(t *testing.T) {
	e := newVideoEnv(t)
	logs := e.captureLogs()
	e.ytdlp.downloadFn = func(context.Context, string, string) (video.Downloaded, error) {
		return video.Downloaded{}, fmt.Errorf("yt-dlp download: %w", &os.PathError{Op: "fork/exec", Path: "/opt/yt-dlp", Err: errors.New("no such file or directory")})
	}
	v := e.newTweet()
	must(t, e.ingest(v.ID))
	out := logs.String()
	for _, want := range []string{"*fmt.wrapError", "*fs.PathError", "*errors.errorString", "no such file or directory", "chain="} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "stack=") {
		t.Error("a plain error must not log a panic stack")
	}

	e2 := newVideoEnv(t)
	logs2 := e2.captureLogs()
	e2.summ.fn = func(context.Context, string) (llm.Summary, error) { panic("kaboom") }
	v2 := e2.awaiting(tweetURL)
	must(t, e2.captions(v2.ID))
	if out := logs2.String(); !contains(out, "kaboom", "stack=", "goroutine") {
		t.Errorf("a panic must log its stack:\n%s", out)
	}
}

func TestReuseSkippedByThePathCheckIsLogged(t *testing.T) {
	e := newVideoEnv(t)
	logs := e.captureLogs()
	outside := filepath.Join(t.TempDir(), "secret.mp4")
	must(t, os.WriteFile(outside, []byte("s"), 0o600))
	sib := e.create(store.NewTwitterVideo{SourceURL: "https://x.com/foo/status/1?z=1"})
	e.update(sib.ID, store.TwitterVideoChanges{VideoPath: &outside})
	v := e.newTweet()
	must(t, e.ingest(v.ID))
	if out := logs.String(); !contains(out, "not reusing an earlier download", "level=INFO") {
		t.Errorf("no log line for the skipped path:\n%s", out)
	}
}

// --- sweeper -----------------------------------------------------------------

func TestVideoSweeperAnnouncesEachFailureAndSkipsRowsWithAJob(t *testing.T) {
	e := newVideoEnv(t)
	sweep, err := VideoSweepers(e.deps)
	must(t, err)
	ingestStuck := e.create(store.NewTwitterVideo{SourceURL: tweetURL, Status: "uploading"})
	captionsStuck := e.create(store.NewTwitterVideo{SourceURL: tweetURL, Status: "publishing"})
	queued := e.create(store.NewTwitterVideo{SourceURL: tweetURL, Status: "downloading"})
	queuedCaptions := e.create(store.NewTwitterVideo{SourceURL: tweetURL, Status: "summarizing"})
	for _, v := range []*store.TwitterVideo{ingestStuck, captionsStuck, queued, queuedCaptions} {
		e.ageRow(v.ID, 4*time.Hour)
	}
	must(t, Enqueue(ctx, e.pool, KindTwitterVideoIngest, Args{ID: queued.ID}, 0))
	must(t, Enqueue(ctx, e.pool, KindTwitterVideoCaptions, Args{ID: queuedCaptions.ID}, 0))

	for _, s := range sweep {
		must(t, s(ctx))
	}
	for _, v := range []*store.TwitterVideo{queued, queuedCaptions} {
		if got := e.reload(v.ID); got.Status == "failed" {
			t.Errorf("#%d has a job and must be left to it", v.ID)
		}
	}
	for _, v := range []*store.TwitterVideo{ingestStuck, captionsStuck} {
		if got := e.reload(v.ID); got.Status != "failed" || *got.ErrorDetail != "Processing failed." {
			t.Errorf("#%d: %+v", v.ID, got)
		}
	}
	want := []string{
		fmt.Sprintf("[twitter-video #%d] ingest failed: Processing failed.", ingestStuck.ID),
		fmt.Sprintf("[twitter-video #%d] captions/publish failed: Processing failed.", captionsStuck.ID),
	}
	if fmt.Sprint(e.slack.failures) != fmt.Sprint(want) {
		t.Fatalf("slack %q want %q", e.slack.failures, want)
	}
	// A second pass announces nothing again.
	for _, s := range sweep {
		must(t, s(ctx))
	}
	if _, f := e.slack.counts(); f != 2 {
		t.Fatalf("%d notices after the second pass", f)
	}
}

// --- validation --------------------------------------------------------------

func TestVideoDepsValidate(t *testing.T) {
	e := newVideoEnv(t)
	if err := e.deps.Validate(); err != nil {
		t.Fatalf("complete deps: %v", err)
	}
	cases := map[string]func(d *VideoDeps){
		"DB":          func(d *VideoDeps) { d.DB = nil },
		"Ytdlp":       func(d *VideoDeps) { d.Ytdlp = nil },
		"Cookies":     func(d *VideoDeps) { d.Cookies = nil },
		"NewUploader": func(d *VideoDeps) { d.NewUploader = nil },
		"Summarizer":  func(d *VideoDeps) { d.Summarizer = nil },
		"Rulinky":     func(d *VideoDeps) { d.Rulinky = nil },
		"Writer":      func(d *VideoDeps) { d.Writer = nil },
		"VideosDir":   func(d *VideoDeps) { d.VideosDir = "" },
	}
	for field, mutate := range cases {
		d := e.deps
		mutate(&d)
		err := d.Validate()
		if err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("without %s: %v", field, err)
		}
		if _, err := VideoHandlers(d); err == nil {
			t.Errorf("VideoHandlers accepted deps without %s", field)
		}
		if _, err := VideoSweepers(d); err == nil {
			t.Errorf("VideoSweepers accepted deps without %s", field)
		}
	}
	d := e.deps
	d.VideosDir = "relative/videos"
	if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("relative VideosDir: %v", err)
	}
	// Notifier, Enqueuer and Now default.
	d = e.deps
	d.Notifier, d.Enqueuer, d.Now = nil, nil, nil
	if err := d.Validate(); err != nil {
		t.Errorf("optional fields: %v", err)
	}
	var multi = e.deps
	multi.Cookies, multi.Writer = nil, nil
	if err := multi.Validate(); err == nil || !contains(err.Error(), "Cookies", "Writer") {
		t.Errorf("all missing fields should be listed: %v", err)
	}
	// The constructors refuse what their own handler needs, at construction.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("NewTwitterVideoIngest accepted deps without an uploader")
			}
		}()
		d := e.deps
		d.NewUploader = nil
		NewTwitterVideoIngest(d)
	}()
	// Ingest does not need the captions collaborators.
	d = e.deps
	d.Cookies, d.Summarizer, d.Rulinky, d.Writer = nil, nil, nil, nil
	NewTwitterVideoIngest(d)
}

func TestVideoDepsDefaultsTieTheStaleWindowToTheCaptionsTimeout(t *testing.T) {
	d := VideoDeps{}.withDefaults()
	if d.StaleAfter != d.CaptionsTimeout+5*time.Minute || d.StaleAfter != DefaultVideoStaleAfter || d.StaleAfter >= DefaultStaleAfter {
		t.Fatalf("StaleAfter = %v", d.StaleAfter)
	}
	if d.IngestTimeout != RecommendedIngestTimeout || d.CaptionsTimeout != RecommendedCaptionsTimeout {
		t.Fatalf("timeouts %v %v", d.IngestTimeout, d.CaptionsTimeout)
	}
	if d.IngestLease >= RecommendedIngestTimeout {
		t.Fatalf("lease %v", d.IngestLease)
	}
}
