package web

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/jobs"
	"serve-html-markdown/internal/store"
)

// Ports of test/controllers/video_summaries_controller_test.rb ("Ruby: ..."
// names the test), plus the retry contract with the jobs.

const videoBearer = "Bearer secret-token"

type videoEnv struct {
	srv  *Server
	enq  *fakeEnqueuer
	pool store.DB
}

// newVideoEnv is a server on the test database with the API token
// "secret-token" and a fake enqueuer. A pool-backed (real) enqueuer is used when
// real is set.
func newVideoEnv(t *testing.T, real bool) *videoEnv {
	t.Helper()
	cfg := testConfig(t)
	cfg.APIToken = "secret-token"
	pool := testdb.New(t)
	enq := &fakeEnqueuer{}
	var opts []Option
	if !real {
		opts = append(opts, WithEnqueuer(enq))
	}
	return &videoEnv{srv: New(cfg, pool, opts...), enq: enq, pool: pool}
}

func (e *videoEnv) postJSON(path, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", videoBearer)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

func (e *videoEnv) submit(u string) *httptest.ResponseRecorder {
	return e.postJSON("/summarize-video", `{"url":`+string(marshalJSON(u))+`}`)
}

func (e *videoEnv) create(t *testing.T, n store.NewTwitterVideo, c store.TwitterVideoChanges) *store.TwitterVideo {
	t.Helper()
	v, err := store.CreateTwitterVideo(context.Background(), e.pool, n)
	if err != nil {
		t.Fatal(err)
	}
	if c != (store.TwitterVideoChanges{}) {
		if err := store.UpdateTwitterVideo(context.Background(), e.pool, v.ID, c); err != nil {
			t.Fatal(err)
		}
		return e.load(t, v.ID)
	}
	return v
}

func (e *videoEnv) load(t *testing.T, id int64) *store.TwitterVideo {
	t.Helper()
	v, err := store.FindTwitterVideo(context.Background(), e.pool, id)
	if err != nil || v == nil {
		t.Fatalf("video %d: %v", id, err)
	}
	return v
}

func (e *videoEnv) count(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM twitter_videos`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// queued counts the jobs of kind in go_jobs (real enqueuer only).
func (e *videoEnv) queued(t *testing.T, kind string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM go_jobs WHERE kind = $1`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *videoEnv) wantEnqueued(t *testing.T, want ...enqueued) {
	t.Helper()
	e.enq.mu.Lock()
	defer e.enq.mu.Unlock()
	if len(e.enq.got) != len(want) {
		t.Fatalf("enqueued %+v, want %+v", e.enq.got, want)
	}
	for i := range want {
		if e.enq.got[i] != want[i] {
			t.Fatalf("enqueued %+v, want %+v", e.enq.got, want)
		}
	}
}

func ingest(id int64) enqueued { return enqueued{jobs.KindTwitterVideoIngest, jobs.Args{ID: id}, 0} }
func captions(id int64) enqueued {
	return enqueued{jobs.KindTwitterVideoCaptions, jobs.Args{ID: id}, 0}
}

func mustCode(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("got %d %s, want %d", rec.Code, rec.Body, want)
	}
}

// Ruby: "rejects missing bearer token".
func TestVideoSummaryRejectsMissingBearer(t *testing.T) {
	e := newVideoEnv(t, false)
	for name, header := range map[string]string{"none": "", "wrong": "Bearer nope", "no scheme": "secret-token"} {
		req := httptest.NewRequest("POST", "/summarize-video", strings.NewReader(`{"url":"https://x.com/a/status/1"}`))
		req.Header.Set("Content-Type", "application/json")
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		e.srv.ServeHTTP(rec, req)
		if rec.Code != 401 || rec.Body.String() != `{"detail":"Unauthorized"}` {
			t.Errorf("%s: got %d %s", name, rec.Code, rec.Body)
		}
	}
	if e.count(t) != 0 {
		t.Error("a row was created without authorization")
	}
	e.wantEnqueued(t)
}

// Authorization is checked before the body is read: an oversized body from a
// stranger is a 401, not a 413.
func TestVideoSummaryAuthorizesBeforeReadingTheBody(t *testing.T) {
	e := newVideoEnv(t, false)
	req := httptest.NewRequest("POST", "/summarize-video", strings.NewReader(strings.Repeat("x", MaxVideoBodyBytes+1)))
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	mustCode(t, rec, 401)
}

// Ruby: "creates a row and enqueues ingest job".
func TestVideoSummaryCreatesRowAndEnqueuesIngest(t *testing.T) {
	e := newVideoEnv(t, false)
	rec := e.submit("https://twitter.com/a/status/1")
	mustCode(t, rec, 202)
	id := idOf(t, rec)
	if want := `{"id":` + itoa(id) + `,"status":"downloading"}`; rec.Body.String() != want {
		t.Errorf("body %q, want %q", rec.Body, want)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type %q", got)
	}
	v := e.load(t, id)
	if v.SourceURL != "https://x.com/a/status/1" || v.Status != "downloading" || v.YoutubeID != nil {
		t.Errorf("row %+v", v)
	}
	e.wantEnqueued(t, ingest(id))
	// The row was only inserted: ingest's lease sees a fresh row nobody holds.
	if !v.UpdatedAt.Equal(v.CreatedAt) {
		t.Errorf("updated_at %v != created_at %v", v.UpdatedAt, v.CreatedAt)
	}
	if ok, err := store.ClaimTwitterVideoForIngest(context.Background(), e.pool, id, 10*time.Minute); err != nil || !ok {
		t.Errorf("the ingest job cannot claim the new row: %v %v", ok, err)
	}
}

// Ruby: "reuses an existing row for the same tweet and re-enqueues ingest".
func TestVideoSummaryReusesFailedRowWithoutWritingIt(t *testing.T) {
	e := newVideoEnv(t, false)
	existing := e.create(t, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/7?s=20", Status: "failed"}, store.TwitterVideoChanges{})
	rec := e.submit("https://twitter.com/a/status/7")
	mustCode(t, rec, 202)
	if idOf(t, rec) != existing.ID || e.count(t) != 1 {
		t.Fatalf("a new row was made: %s", rec.Body)
	}
	if got := decode(t, rec)["status"]; got != "failed" { // the row as it is, like Ruby
		t.Errorf("status %v", got)
	}
	e.wantEnqueued(t, ingest(existing.ID))
	if after := e.load(t, existing.ID); !after.UpdatedAt.Equal(existing.UpdatedAt) || after.Status != "failed" {
		t.Errorf("the row was written before its job ran: %+v -> %+v", existing, after)
	}
}

// Ruby: "sends a youtube link straight to the captions job".
func TestVideoSummarySendsYoutubeLinkToCaptions(t *testing.T) {
	e := newVideoEnv(t, false)
	rec := e.submit("https://youtu.be/KpILHX2D-AA?si=3zdm0UjTBIYF-k0c")
	mustCode(t, rec, 202)
	id := idOf(t, rec)
	v := e.load(t, id)
	if v.SourceURL != "https://www.youtube.com/watch?v=KpILHX2D-AA" || v.YoutubeID == nil || *v.YoutubeID != "KpILHX2D-AA" ||
		v.Status != "awaiting_captions" || v.UploadCompletedAt == nil || v.CaptionAttempts != 0 {
		t.Errorf("row %+v", v)
	}
	if got := decode(t, rec)["status"]; got != "awaiting_captions" {
		t.Errorf("status %v", got)
	}
	e.wantEnqueued(t, captions(id))
}

// Ruby: "retries a failed youtube row instead of adding another".
func TestVideoSummaryRetriesFailedYoutubeRow(t *testing.T) {
	e := newVideoEnv(t, false)
	existing := e.create(t, store.NewTwitterVideo{SourceURL: "https://www.youtube.com/watch?v=KpILHX2D-AA", Status: "failed", CaptionAttempts: 9},
		store.TwitterVideoChanges{ErrorDetail: new("boom")})
	mustCode(t, e.submit("https://www.youtube.com/watch?v=KpILHX2D-AA"), 202)
	if e.count(t) != 1 {
		t.Fatal("another row was added")
	}
	got := e.load(t, existing.ID)
	if got.Status != "awaiting_captions" || got.CaptionAttempts != 0 || got.ErrorDetail != nil {
		t.Errorf("%s %d %v", got.Status, got.CaptionAttempts, got.ErrorDetail)
	}
	e.wantEnqueued(t, captions(existing.ID))
}

// Ruby: "rejects an invalid url".
func TestVideoSummaryRejectsInvalidURL(t *testing.T) {
	e := newVideoEnv(t, false)
	for _, u := range []string{"https://youtube.com/@channel", "https://example.com/a", "not a link", "https://x.com/a"} {
		rec := e.submit(u)
		want := `{"detail":"Unsupported URL: send an x.com/twitter.com post or a YouTube video link"}`
		if rec.Code != 400 || rec.Body.String() != want {
			t.Errorf("%q: got %d %s", u, rec.Code, rec.Body)
		}
	}
	if e.count(t) != 0 {
		t.Error("a row was created")
	}
	e.wantEnqueued(t)
}

// Ruby: "returns status for an existing row", and both routes behave the same.
func TestVideoSummaryShowsStatusOnBothRoutes(t *testing.T) {
	e := newVideoEnv(t, false)
	v := e.create(t, store.NewTwitterVideo{SourceURL: "u", Status: "awaiting_captions"}, store.TwitterVideoChanges{YoutubeID: new("YT1")})
	done := e.create(t, store.NewTwitterVideo{SourceURL: "u2", Status: "done"}, store.TwitterVideoChanges{
		YoutubeID: new("YT2"), HTMLFilename: new("page.html"), ErrorDetail: new("old <b>")})
	for _, prefix := range []string{"/summarize-video/", "/twitter-video/"} {
		// No credentials: the status is public, like Rails.
		rec := do(e.srv, "GET", prefix+itoa(v.ID), nil)
		mustCode(t, rec, 200)
		if want := `{"id":` + itoa(v.ID) + `,"status":"awaiting_captions","error_detail":null,"html_filename":null,"youtube_id":"YT1"}`; rec.Body.String() != want {
			t.Errorf("%s: body %q, want %q", prefix, rec.Body, want)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Errorf("Content-Type %q", got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("Cache-Control %q", got)
		}
		rec = do(e.srv, "GET", prefix+itoa(done.ID), nil)
		if want := `{"id":` + itoa(done.ID) + `,"status":"done","error_detail":"old <b>","html_filename":"page.html","youtube_id":"YT2"}`; rec.Body.String() != want {
			t.Errorf("%s: body %q, want %q", prefix, rec.Body, want)
		}
	}
}

func TestVideoSummaryShowNotFound(t *testing.T) {
	e := newVideoEnv(t, false)
	for _, p := range []string{"/summarize-video/99999", "/twitter-video/99999", "/summarize-video/99999999999999999999", "/twitter-video/99999999999999999999"} {
		rec := do(e.srv, "GET", p, nil)
		if rec.Code != 404 || rec.Body.String() != `{"detail":"Not found"}` {
			t.Errorf("%s: got %d %s", p, rec.Code, rec.Body)
		}
	}
}

// Ruby: "accepts the plain watch link that was reported as unsupported".
func TestVideoSummaryAcceptsPlainWatchLink(t *testing.T) {
	e := newVideoEnv(t, false)
	rec := e.submit("https://www.youtube.com/watch?v=hWe-j78vNpc")
	mustCode(t, rec, 202)
	if v := e.load(t, idOf(t, rec)); v.YoutubeID == nil || *v.YoutubeID != "hWe-j78vNpc" {
		t.Errorf("%+v", v)
	}
	e.wantEnqueued(t, captions(idOf(t, rec)))
}

// Ruby: "pulls the link out of shared text"; the first link wins.
func TestVideoSummaryPullsTheFirstLinkOutOfSharedText(t *testing.T) {
	e := newVideoEnv(t, false)
	rec := e.submit("  Look at this https://x.com/someone/status/42?s=46 wow\n")
	mustCode(t, rec, 202)
	if got := e.load(t, idOf(t, rec)).SourceURL; got != "https://x.com/someone/status/42" {
		t.Errorf("source_url %q", got)
	}
	// Two links: the first one, even if the second is the supported one.
	rec = e.submit("see HTTP://example.com/a and https://x.com/b/status/5")
	if rec.Code != 400 {
		t.Errorf("an unsupported first link: got %d %s", rec.Code, rec.Body)
	}
	rec = e.submit("watch\thttps://youtu.be/hWe-j78vNpc\u000bhttps://x.com/b/status/5")
	mustCode(t, rec, 202)
	if v := e.load(t, idOf(t, rec)); v.YoutubeID == nil || *v.YoutubeID != "hWe-j78vNpc" {
		t.Errorf("%+v", v)
	}
	// A bare link (no text around it) still works, and so does one without a scheme.
	mustCode(t, e.submit("x.com/c/status/9"), 202)
}

// Ruby: "says the url is missing when it is blank".
func TestVideoSummaryMissingURL(t *testing.T) {
	e := newVideoEnv(t, false)
	for _, body := range []string{`{"url":""}`, `{"url":"  \n "}`, `{}`, `{"url":null}`, ``} {
		rec := e.postJSON("/summarize-video", body)
		if rec.Code != 400 || rec.Body.String() != `{"detail":"Missing url"}` {
			t.Errorf("%q: got %d %s", body, rec.Code, rec.Body)
		}
	}
}

// Ruby: "keeps the old twitter-video path working".
func TestVideoSummaryKeepsTheOldPath(t *testing.T) {
	e := newVideoEnv(t, false)
	rec := e.postJSON("/twitter-video", `{"url":"https://youtu.be/hWe-j78vNpc"}`)
	mustCode(t, rec, 202)
	mustCode(t, do(e.srv, "GET", "/twitter-video/"+itoa(idOf(t, rec)), nil), 200)
	e.wantEnqueued(t, captions(idOf(t, rec)))
}

// iOS Shortcuts send a form or the query string as well as JSON.
func TestVideoSummaryParamSources(t *testing.T) {
	e := newVideoEnv(t, false)
	link := "https://x.com/a/status/11"
	form := postForm(e.srv, "POST", "/summarize-video", url.Values{"url": {"look " + link}}, map[string]string{"Authorization": videoBearer})
	mustCode(t, form, 202)
	query := postForm(e.srv, "POST", "/summarize-video?url="+url.QueryEscape("https://x.com/a/status/12"), nil, map[string]string{"Authorization": videoBearer})
	mustCode(t, query, 202)
	if e.load(t, idOf(t, form)).SourceURL != link || e.load(t, idOf(t, query)).SourceURL != "https://x.com/a/status/12" {
		t.Error("wrong source urls")
	}
}

func TestVideoSummaryBadBodies(t *testing.T) {
	e := newVideoEnv(t, false)
	if rec := e.postJSON("/summarize-video", `{"url":`); rec.Code != 400 || rec.Body.String() != `{"detail":"Invalid request body."}` {
		t.Errorf("malformed JSON: %d %s", rec.Code, rec.Body)
	}
	big := `{"url":"` + strings.Repeat("a", MaxVideoBodyBytes) + `"}`
	if rec := e.postJSON("/summarize-video", big); rec.Code != 413 {
		t.Errorf("big body: %d", rec.Code)
	}
	if rec := e.postJSON("/summarize-video", `{"url":"https://x.com/a/sta\u0000tus/1"}`); rec.Code != 400 {
		t.Errorf("NUL: %d %s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest("POST", "/summarize-video?url=https://x.com/a/status/1%FF", nil)
	req.Header.Set("Authorization", videoBearer)
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	mustCode(t, rec, 400)
	if e.count(t) != 0 {
		t.Error("a row was created")
	}
}

// --- The retry contract with the jobs -------------------------------------

// A failed row without an id is retried by ingest; a second submission while
// that job is still queued adds nothing (real enqueuer, so HasPendingJob sees it).
func TestVideoSummaryDoesNotQueueDuplicateIngest(t *testing.T) {
	e := newVideoEnv(t, true)
	first := e.submit("https://x.com/a/status/1")
	mustCode(t, first, 202)
	id := idOf(t, first)
	mustCode(t, e.submit("https://x.com/a/status/1"), 202)
	mustCode(t, e.submit("https://twitter.com/a/status/1?s=20"), 202)
	if n := e.queued(t, jobs.KindTwitterVideoIngest); n != 1 {
		t.Fatalf("%d ingest jobs queued, want 1", n)
	}
	// The row was never written after its insert, so the job can still claim it.
	if v := e.load(t, id); !v.UpdatedAt.Equal(v.CreatedAt) {
		t.Errorf("updated_at moved: %+v", v)
	}
	if ok, err := store.ClaimTwitterVideoForIngest(context.Background(), e.pool, id, 10*time.Minute); err != nil || !ok {
		t.Errorf("claim: %v %v", ok, err)
	}
}

// With the real queue: a pending job of the other kind does not block, and a
// finished one does not count.
func TestVideoSummaryPendingGuardIsPerKindAndIgnoresFinishedJobs(t *testing.T) {
	e := newVideoEnv(t, true)
	v := e.create(t, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/3", Status: "failed"}, store.TwitterVideoChanges{})
	if err := jobs.Enqueue(context.Background(), e.pool, jobs.KindTwitterVideoCaptions, jobs.Args{ID: v.ID}, 0); err != nil {
		t.Fatal(err)
	}
	mustCode(t, e.submit("https://x.com/a/status/3"), 202)
	if e.queued(t, jobs.KindTwitterVideoIngest) != 1 {
		t.Fatal("the captions job blocked ingest")
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE go_jobs SET finished_at = now()`); err != nil {
		t.Fatal(err)
	}
	mustCode(t, e.submit("https://x.com/a/status/3"), 202)
	if n := e.queued(t, jobs.KindTwitterVideoIngest); n != 2 {
		t.Fatalf("%d ingest jobs, want 2 (the first one finished)", n)
	}
}

func TestVideoSummaryRetryByStatus(t *testing.T) {
	type tc struct {
		name    string
		status  string
		youtube *string
		want    func(id int64) []enqueued
		after   string // status of the row afterwards
		reset   bool   // the row was restarted (written)
	}
	cases := []tc{
		{"downloading, no id", "downloading", nil, func(id int64) []enqueued { return []enqueued{ingest(id)} }, "downloading", false},
		{"uploading, no id", "uploading", nil, func(id int64) []enqueued { return []enqueued{ingest(id)} }, "uploading", false},
		{"failed, no id", "failed", nil, func(id int64) []enqueued { return []enqueued{ingest(id)} }, "failed", false},
		{"failed, with id", "failed", new("KpILHX2D-AA"), func(id int64) []enqueued { return []enqueued{captions(id)} }, "awaiting_captions", true},
		{"awaiting_captions", "awaiting_captions", new("KpILHX2D-AA"), func(id int64) []enqueued { return []enqueued{captions(id)} }, "awaiting_captions", false},
		{"summarizing", "summarizing", new("KpILHX2D-AA"), func(int64) []enqueued { return nil }, "summarizing", false},
		{"publishing", "publishing", new("KpILHX2D-AA"), func(int64) []enqueued { return nil }, "publishing", false},
		{"done", "done", new("KpILHX2D-AA"), func(int64) []enqueued { return nil }, "done", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newVideoEnv(t, false)
			n := store.NewTwitterVideo{SourceURL: "https://x.com/a/status/20", Status: c.status}
			ch := store.TwitterVideoChanges{YoutubeID: c.youtube}
			if c.status == "failed" {
				ch.ErrorDetail = new("boom")
			}
			if c.youtube != nil {
				n.UploadCompletedAt = new(time.Now().Add(-time.Hour))
			}
			before := e.create(t, n, ch)
			rec := e.submit("https://x.com/a/status/20")
			mustCode(t, rec, 202)
			if idOf(t, rec) != before.ID {
				t.Fatalf("another row: %s", rec.Body)
			}
			if got := decode(t, rec)["status"]; got != c.after {
				t.Errorf("answered status %v, want %s", got, c.after)
			}
			e.wantEnqueued(t, c.want(before.ID)...)
			after := e.load(t, before.ID)
			if after.Status != c.after {
				t.Errorf("status %s, want %s", after.Status, c.after)
			}
			if !c.reset && !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Errorf("the row was written: %v -> %v", before.UpdatedAt, after.UpdatedAt)
			}
			if c.reset && (after.CaptionAttempts != 0 || after.ErrorDetail != nil || after.UploadCompletedAt == nil || !after.UploadCompletedAt.After(*before.UploadCompletedAt)) {
				t.Errorf("not reset: %+v", after)
			}
		})
	}
}

// A captions check already queued is not doubled, for a resubmitted
// awaiting_captions row, a restarted failed one and a YouTube link alike.
func TestVideoSummaryDoesNotQueueDuplicateCaptions(t *testing.T) {
	e := newVideoEnv(t, true)
	failed := e.create(t, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/30", Status: "failed"}, store.TwitterVideoChanges{YoutubeID: new("KpILHX2D-AA")})
	awaiting := e.create(t, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/31", Status: "awaiting_captions"}, store.TwitterVideoChanges{YoutubeID: new("KpILHX2D-AA")})
	yt := e.submit("https://youtu.be/hWe-j78vNpc")
	mustCode(t, yt, 202)
	ytID := idOf(t, yt)

	for i := 0; i < 3; i++ {
		mustCode(t, e.submit("https://x.com/a/status/30"), 202)
		mustCode(t, e.submit("https://x.com/a/status/31"), 202)
		mustCode(t, e.submit("https://www.youtube.com/watch?v=hWe-j78vNpc"), 202)
	}
	for _, id := range []int64{failed.ID, awaiting.ID, ytID} {
		var n int
		if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM go_jobs WHERE kind = $1 AND args @> $2::jsonb`,
			jobs.KindTwitterVideoCaptions, `{"id": `+itoa(id)+`}`).Scan(&n); err != nil || n != 1 {
			t.Errorf("video %d: %d captions jobs (%v), want 1", id, n, err)
		}
	}
	if n := e.queued(t, jobs.KindTwitterVideoIngest); n != 0 {
		t.Errorf("%d ingest jobs for rows that already have a YouTube id", n)
	}
}

// A failed enqueue is a generic 500 and marks nothing: a new row stays
// "downloading" (the sweeper fails it later), an existing one is untouched.
func TestVideoSummaryEnqueueFailure(t *testing.T) {
	e := newVideoEnv(t, false)
	e.enq.err = errors.New("db down: secret detail")
	rec := e.submit("https://x.com/a/status/40")
	if rec.Code != 500 || rec.Body.String() != `{"detail":"Internal server error."}` {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	var id int64
	var status string
	if err := e.pool.QueryRow(context.Background(), `SELECT id, status FROM twitter_videos`).Scan(&id, &status); err != nil || status != "downloading" {
		t.Fatalf("row %d %q: %v", id, status, err)
	}
	before := e.load(t, id)
	if rec := e.submit("https://x.com/a/status/40"); rec.Code != 500 {
		t.Fatalf("got %d", rec.Code)
	}
	if after := e.load(t, id); !after.UpdatedAt.Equal(before.UpdatedAt) || after.Status != "downloading" || after.ErrorDetail != nil {
		t.Errorf("%+v -> %+v", before, after)
	}
	if rec := e.submit("https://youtu.be/hWe-j78vNpc"); rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("youtube: %d %s", rec.Code, rec.Body)
	}
}

// The enqueue is not tied to the client's connection.
func TestVideoSummaryEnqueuesEvenIfTheClientLeft(t *testing.T) {
	e := newVideoEnv(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "/summarize-video", strings.NewReader(`{"url":"https://x.com/a/status/50"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", videoBearer)
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	mustCode(t, rec, 202)
	e.wantEnqueued(t, ingest(idOf(t, rec)))
	for _, err := range e.enq.ctxErrs {
		if err != nil {
			t.Errorf("enqueue context ended: %v", err)
		}
	}
}
