package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/llm"
	"serve-html-markdown/internal/store"
	"serve-html-markdown/internal/video"
)

// fakeSlack is Ruby's FakeSlack.
type fakeSlack struct {
	mu                  sync.Mutex
	successes, failures []string
}

func (f *fakeSlack) Success(_ context.Context, t string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.successes = append(f.successes, t)
}

func (f *fakeSlack) Failure(_ context.Context, t string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, t)
}

func (f *fakeSlack) lastSuccess() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.successes) == 0 {
		return ""
	}
	return f.successes[len(f.successes)-1]
}

func (f *fakeSlack) lastFailure() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.failures) == 0 {
		return ""
	}
	return f.failures[len(f.failures)-1]
}

func (f *fakeSlack) counts() (successes, failures int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.successes), len(f.failures)
}

// fakeYtdlp is Ruby's FakeYtdlp for both jobs.
type fakeYtdlp struct {
	mu          sync.Mutex
	downloadFn  func(ctx context.Context, url, dir string) (video.Downloaded, error)
	subsFn      func(ctx context.Context, url, dir, cookies string) (string, error)
	downloads   int
	subsCalls   int
	urls        []string
	lastCookies string
}

func (f *fakeYtdlp) Download(ctx context.Context, url, dir string) (video.Downloaded, error) {
	f.mu.Lock()
	f.downloads++
	f.urls = append(f.urls, url)
	fn := f.downloadFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, url, dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return video.Downloaded{}, err
	}
	path := filepath.Join(dir, "x.mp4")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return video.Downloaded{}, err
	}
	return video.Downloaded{Path: path, Title: "Tweet Title"}, nil
}

func (f *fakeYtdlp) FetchAutoSubs(ctx context.Context, url, dir, cookies string) (string, error) {
	f.mu.Lock()
	f.subsCalls++
	f.lastCookies = cookies
	fn := f.subsFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, url, dir, cookies)
	}
	return writeVtt(dir, "WEBVTT\nhello")
}

func writeVtt(dir, content string) (string, error) {
	if content == "" {
		return "", nil
	}
	path := filepath.Join(dir, "v.en.vtt")
	return path, os.WriteFile(path, []byte(content), 0o600)
}

func (f *fakeYtdlp) downloadCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.downloads }
func (f *fakeYtdlp) cookies() string    { f.mu.Lock(); defer f.mu.Unlock(); return f.lastCookies }

type fakeUploader struct {
	mu     sync.Mutex
	id     string
	fn     func(ctx context.Context, path, title string) (string, error)
	calls  int
	paths  []string
	titles []string
	descs  []string
}

func (f *fakeUploader) Upload(ctx context.Context, path, title, desc string) (string, error) {
	f.mu.Lock()
	f.calls++
	f.paths = append(f.paths, path)
	f.titles = append(f.titles, title)
	f.descs = append(f.descs, desc)
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, path, title)
	}
	return f.id, nil
}

func (f *fakeUploader) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

type fakeCookies struct {
	mu    sync.Mutex
	err   error
	paths []string
	modes []os.FileMode
}

func (f *fakeCookies) Export(_ context.Context, path string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, path)
	if info, err := os.Stat(filepath.Dir(path)); err == nil {
		f.modes = append(f.modes, info.Mode().Perm())
	}
	if f.err != nil {
		return "", f.err
	}
	return path, os.WriteFile(path, []byte("# Netscape HTTP Cookie File\n"), 0o600)
}

type fakeSummarizer struct {
	mu    sync.Mutex
	fn    func(ctx context.Context, captions string) (llm.Summary, error)
	calls int
	input string
}

func (f *fakeSummarizer) Summarize(ctx context.Context, captions string) (llm.Summary, error) {
	f.mu.Lock()
	f.calls++
	f.input = captions
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, captions)
	}
	return llm.Summary{Title: "The Idea", SummaryHTML: "<p>x</p>"}, nil
}

func (f *fakeSummarizer) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

type fakeRulinky struct {
	mu           sync.Mutex
	id           string
	fn           func(ctx context.Context, link, note string) (string, error)
	links, notes []string
	calls        int
}

func (f *fakeRulinky) CreateLink(ctx context.Context, link, note string) (string, error) {
	f.mu.Lock()
	f.calls++
	f.links = append(f.links, link)
	f.notes = append(f.notes, note)
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, link, note)
	}
	return f.id, nil
}

type fakeWriter struct {
	mu    sync.Mutex
	name  string
	fn    func(ctx context.Context, title, html string) (string, error)
	calls int
	title string
	html  string
}

func (f *fakeWriter) Write(ctx context.Context, title, html string) (string, error) {
	f.mu.Lock()
	f.calls++
	f.title, f.html = title, html
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, title, html)
	}
	return f.name, nil
}

// failingEnqueuer fails the first n calls, then delegates.
type failingEnqueuer struct {
	next  Enqueuer
	mu    sync.Mutex
	fails int
}

func (f *failingEnqueuer) Enqueue(ctx context.Context, kind string, args Args, wait time.Duration) error {
	f.mu.Lock()
	if f.fails > 0 {
		f.fails--
		f.mu.Unlock()
		return errors.New("enqueue: database unavailable")
	}
	f.mu.Unlock()
	return f.next.Enqueue(ctx, kind, args, wait)
}

type videoEnv struct {
	t         *testing.T
	pool      *pgxpool.Pool
	videosDir string
	slack     *fakeSlack
	ytdlp     *fakeYtdlp
	uploader  *fakeUploader
	cookies   *fakeCookies
	summ      *fakeSummarizer
	rulinky   *fakeRulinky
	writer    *fakeWriter
	deps      VideoDeps
}

// newVideoEnv wires every collaborator to a fake (Ruby's setup blocks), with
// the real queue underneath so enqueues can be read back from go_jobs.
func newVideoEnv(t *testing.T) *videoEnv {
	t.Helper()
	pool := testdb.New(t)
	e := &videoEnv{
		t: t, pool: pool, videosDir: filepath.Join(t.TempDir(), "videos"),
		slack: &fakeSlack{}, ytdlp: &fakeYtdlp{}, uploader: &fakeUploader{id: "YT9"},
		cookies: &fakeCookies{}, summ: &fakeSummarizer{}, rulinky: &fakeRulinky{id: "link-1"},
		writer: &fakeWriter{name: "the-idea.html"},
	}
	e.deps = VideoDeps{
		DB: pool, Enqueuer: DBEnqueuer{DB: pool}, Notifier: e.slack,
		Ytdlp: e.ytdlp, Cookies: e.cookies,
		NewUploader: func(context.Context) (Uploader, error) { return e.uploader, nil },
		Summarizer:  e.summ, Rulinky: e.rulinky, Writer: e.writer,
		VideosDir: e.videosDir, Host: "example.com", YoutubeHost: "serve.test:8009",
	}
	return e
}

func (e *videoEnv) ingest(id int64) error { return NewTwitterVideoIngest(e.deps)(ctx, Args{ID: id}) }
func (e *videoEnv) captions(id int64) error {
	return NewTwitterVideoCaptions(e.deps)(ctx, Args{ID: id})
}

func (e *videoEnv) create(n store.NewTwitterVideo) *store.TwitterVideo {
	e.t.Helper()
	v, err := store.CreateTwitterVideo(ctx, e.pool, n)
	must(e.t, err)
	return v
}

func (e *videoEnv) reload(id int64) *store.TwitterVideo {
	e.t.Helper()
	v, err := store.FindTwitterVideo(ctx, e.pool, id)
	must(e.t, err)
	if v == nil {
		e.t.Fatalf("video %d vanished", id)
	}
	return v
}

func (e *videoEnv) update(id int64, c store.TwitterVideoChanges) {
	e.t.Helper()
	must(e.t, store.UpdateTwitterVideo(ctx, e.pool, id, c))
}

// ageRow makes the row look untouched for d.
func (e *videoEnv) ageRow(id int64, d time.Duration) {
	e.t.Helper()
	_, err := e.pool.Exec(ctx, `UPDATE twitter_videos SET updated_at = updated_at - $2::float8 * interval '1 second' WHERE id = $1`, id, d.Seconds())
	must(e.t, err)
}

type queuedJob struct {
	ID       int64
	Delay    time.Duration // from now to run_at
	Finished bool
}

// queued lists the go_jobs rows of kind for the record id.
func (e *videoEnv) queued(kind string, id int64) []queuedJob {
	e.t.Helper()
	rows, err := e.pool.Query(ctx, `SELECT id, extract(epoch FROM run_at - now()), finished_at IS NOT NULL
		FROM go_jobs WHERE kind = $1 AND (args->>'id')::bigint = $2 ORDER BY id`, kind, id)
	must(e.t, err)
	defer rows.Close()
	var out []queuedJob
	for rows.Next() {
		var j queuedJob
		var secs float64
		must(e.t, rows.Scan(&j.ID, &secs, &j.Finished))
		j.Delay = time.Duration(secs * float64(time.Second))
		out = append(out, j)
	}
	must(e.t, rows.Err())
	return out
}

// near reports whether d is within a few seconds of want.
func near(d, want time.Duration) bool {
	diff := d - want
	if diff < 0 {
		diff = -diff
	}
	return diff < 10*time.Second
}

// awaiting makes a video that is waiting for its first caption check, as the
// Ruby captions test's setup does.
func (e *videoEnv) awaiting(source string) *store.TwitterVideo {
	e.t.Helper()
	at := time.Now().UTC()
	v := e.create(store.NewTwitterVideo{SourceURL: source, Status: "awaiting_captions", UploadCompletedAt: &at})
	e.update(v.ID, store.TwitterVideoChanges{YoutubeID: new("YT9")})
	return e.reload(v.ID)
}

func (e *videoEnv) newTweet() *store.TwitterVideo {
	return e.create(store.NewTwitterVideo{SourceURL: "https://x.com/foo/status/1", Status: "downloading"})
}

func contains(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

// updateFresh changes a video that is still unclaimed (a queued ingest's row) and
// keeps it unclaimed: any write marks a row "held" until the lease runs out
// (see store.ClaimTwitterVideoForIngest), which tests of other behaviour do not
// want.
func (e *videoEnv) updateFresh(id int64, c store.TwitterVideoChanges) {
	e.t.Helper()
	e.update(id, c)
	_, err := store.ReleaseTwitterVideoClaim(ctx, e.pool, id)
	must(e.t, err)
}
