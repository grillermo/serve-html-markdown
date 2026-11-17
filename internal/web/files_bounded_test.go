package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"serve-html-markdown/internal/markdown"
)

// Tests of the bounds around rendering: the plain-text fallback, the render
// deadline, the limiters, panics and read failures.

// limitedEnv is an authEnv whose server has one render slot and one .html slot.
func limitedEnv(t *testing.T) *authEnv {
	t.Helper()
	env := newAuthEnv(t)
	env.srv = New(env.srv.cfg, env.srv.pool, WithClock(env.srv.Clock), WithRenderConcurrency(1), WithHTMLConcurrency(1))
	return env
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &logs
}

// A document the renderer refuses as too complex is shown as escaped plain text
// in the normal layout, with status 200.
func TestShowTooComplexMarkdownIsPlainText(t *testing.T) {
	env := newAuthEnv(t)
	logs := captureLogs(t)
	src := strings.Repeat(">", 200) + " <script>alert(1)</script> & \"q\" 'x'\nsecond line\n"
	env.writeFile(t, "deep.md", src)

	rec := env.get("/deep.md")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<title>deep.md</title>",
		"<pre style=",
		"&lt;script&gt;alert(1)&lt;/script&gt; &amp; &#34;q&#34; &#39;x&#39;\nsecond line\n</pre>",
		`<script src="` + env.expandPath() + `" defer></script>`,
		"window.__fileVersions = [];",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %q", want, body)
		}
	}
	if strings.Contains(body, "<script>alert") {
		t.Error("document text was not escaped")
	}
	if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "deep.md") {
		t.Errorf("no warning logged: %q", out)
	}
}

// deadlineSeam makes renderDeadline hand out one channel the test fires for the
// first request, and a channel that never fires for later ones.
func deadlineSeam(srv *Server) (fire func()) {
	first := make(chan time.Time)
	var calls atomic.Int32
	srv.renderDeadline = func() (<-chan time.Time, func()) {
		if calls.Add(1) == 1 {
			return first, func() {}
		}
		return nil, func() {}
	}
	return func() { close(first) }
}

// After the deadline the request is answered with the plain-text page, but the
// render slot stays taken until the render goroutine really returns.
func TestShowRenderDeadlineFallsBackAndKeepsSlot(t *testing.T) {
	env := limitedEnv(t)
	logs := captureLogs(t)
	env.writeFile(t, "slow.md", "# Slow & <b>steady</b>\n")
	fire := deadlineSeam(env.srv)

	started := make(chan struct{}, 4)
	release := make(chan struct{})
	env.srv.renderMarkdown = func(src []byte) ([]byte, error) {
		started <- struct{}{}
		<-release
		return markdown.Render(src)
	}
	waiting := make(chan struct{}, 4)
	var acquired atomic.Int32
	env.srv.renders.waiting = func() { waiting <- struct{}{} }
	env.srv.renders.acquired = func() { acquired.Add(1) }

	first := httptest.NewRecorder()
	firstDone := make(chan struct{})
	go func() { env.srv.ServeHTTP(first, signedReq(env, "/slow.md", context.Background())); close(firstDone) }()
	<-started
	fire()
	<-firstDone
	if first.Code != 200 || !strings.Contains(first.Body.String(), "<pre style=") ||
		!strings.Contains(first.Body.String(), "# Slow &amp; &lt;b&gt;steady&lt;/b&gt;") {
		t.Fatalf("fallback: %d %q", first.Code, first.Body.String())
	}
	if out := logs.String(); !strings.Contains(out, "timed out") {
		t.Errorf("timeout not logged: %q", out)
	}

	// The runaway render still holds the slot: the next request has to queue.
	second := httptest.NewRecorder()
	secondDone := make(chan struct{})
	go func() { env.srv.ServeHTTP(second, signedReq(env, "/slow.md", context.Background())); close(secondDone) }()
	<-waiting
	if got := acquired.Load(); got != 1 {
		t.Errorf("slot acquired %d times while the first render still runs, want 1", got)
	}
	close(release) // the first render returns, freeing the slot for the second
	<-secondDone
	if second.Code != 200 || !strings.Contains(second.Body.String(), "<h1>") {
		t.Errorf("second request: %d %q", second.Code, second.Body.String())
	}
}

// A client that leaves while its render runs gets no response, and the slot
// stays taken until the render returns.
func TestShowClientGoneDuringRender(t *testing.T) {
	env := limitedEnv(t)
	env.writeFile(t, "a.md", "# A")
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	env.srv.renderMarkdown = func(src []byte) ([]byte, error) {
		started <- struct{}{}
		<-release
		return markdown.Render(src)
	}
	ctx, cancel := context.WithCancel(context.Background())
	spy := &spyWriter{h: http.Header{}}
	done := make(chan struct{})
	go func() { env.srv.ServeHTTP(spy, signedReq(env, "/a.md", ctx)); close(done) }()
	<-started
	cancel()
	<-done
	if spy.written {
		t.Error("a response was written to a client that left")
	}
	if len(env.srv.renders.slots) != 1 {
		t.Errorf("slot released while the render still runs (held: %d)", len(env.srv.renders.slots))
	}
	close(release)
	// The goroutine frees the slot as it returns; take it to prove that.
	if !env.srv.renders.acquire(context.Background()) {
		t.Fatal("slot never freed")
	}
	env.srv.renders.release()
}

// A panic in the render is a logged 500 and frees its slot.
func TestShowRenderPanicReleasesSlot(t *testing.T) {
	env := limitedEnv(t)
	logs := captureLogs(t)
	env.writeFile(t, "boom.md", "# Boom")
	var calls atomic.Int32
	env.srv.renderMarkdown = func(src []byte) ([]byte, error) {
		if calls.Add(1) == 1 {
			panic("renderer bug")
		}
		return markdown.Render(src)
	}
	wantDetail(t, "panic", env.get("/boom.md"), 500, "Internal server error.")
	if out := logs.String(); !strings.Contains(out, "renderer bug") || !strings.Contains(out, "goroutine") {
		t.Errorf("panic and stack not logged: %q", out)
	}
	// With one slot, a leak would make this wait forever.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, signedReq(env, "/boom.md", ctx))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<h1>") {
		t.Errorf("after the panic: %d %q", rec.Code, rec.Body.String())
	}
}

// A render error that is not a fallback case is a 500 that hides its cause.
func TestShowRenderErrorIs500(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "x.md", "# X")
	env.srv.renderMarkdown = func([]byte) ([]byte, error) { return nil, errors.New("secret detail") }
	rec := env.get("/x.md")
	wantDetail(t, "render error", rec, 500, "Internal server error.")
	if strings.Contains(rec.Body.String(), "secret") {
		t.Error("the error leaked")
	}
}

// --- read failures ---

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk on fire") }
func (failingReader) Close() error             { return nil }

func TestShowReadErrorIs500(t *testing.T) {
	env := newAuthEnv(t)
	logs := captureLogs(t)
	env.srv.openFile = func(string, string) (io.ReadCloser, string, error) {
		return failingReader{}, "x.md", nil
	}
	for _, p := range []string{"/x.md", "/x.html"} {
		rec := env.get(p)
		wantDetail(t, p, rec, 500, "Internal server error.")
		if strings.Contains(rec.Body.String(), "fire") {
			t.Error("the read error leaked")
		}
	}
	if !strings.Contains(logs.String(), "disk on fire") {
		t.Errorf("read error not logged: %q", logs.String())
	}
}

// --- .html limiter ---

func TestShowHTMLWaitsForSlotAndHonoursCancellation(t *testing.T) {
	env := limitedEnv(t)
	env.writeFile(t, "p.html", "<p>x</p>")
	acquired := make(chan struct{}, 4)
	release := make(chan struct{})
	waiting := make(chan struct{}, 4)
	env.srv.htmlLimit.acquired = func() { acquired <- struct{}{}; <-release }
	env.srv.htmlLimit.waiting = func() { waiting <- struct{}{} }

	var wg sync.WaitGroup
	first := httptest.NewRecorder()
	wg.Add(1)
	go func() { defer wg.Done(); env.srv.ServeHTTP(first, signedReq(env, "/p.html", context.Background())) }()
	<-acquired

	ctx, cancel := context.WithCancel(context.Background())
	spy := &spyWriter{h: http.Header{}}
	done := make(chan struct{})
	go func() { env.srv.ServeHTTP(spy, signedReq(env, "/p.html", ctx)); close(done) }()
	<-waiting
	cancel()
	<-done
	if spy.written {
		t.Error("a cancelled request wrote a response")
	}

	third := httptest.NewRecorder()
	wg.Add(1)
	go func() { defer wg.Done(); env.srv.ServeHTTP(third, signedReq(env, "/p.html", context.Background())) }()
	<-waiting
	close(release)
	wg.Wait()
	if first.Code != 200 || third.Code != 200 || !strings.HasPrefix(third.Body.String(), "<p>x</p><script>") {
		t.Errorf("first %d, third %d %q", first.Code, third.Code, third.Body.String())
	}
}

func TestLimitDefaults(t *testing.T) {
	srv := newTestServerNoDB(t)
	if cap(srv.htmlLimit.slots) != DefaultHTMLConcurrency || DefaultHTMLConcurrency != 2 {
		t.Errorf("html slots %d", cap(srv.htmlLimit.slots))
	}
	srv = newTestServerNoDB(t, WithHTMLConcurrency(3), WithHTMLConcurrency(0))
	if cap(srv.htmlLimit.slots) != 3 {
		t.Errorf("html slots %d, want 3", cap(srv.htmlLimit.slots))
	}
	if MaxHTMLBytes != 20<<20 {
		t.Errorf("MaxHTMLBytes = %d, want 20 MiB", MaxHTMLBytes)
	}
}

// --- panic recovery ---

func TestRecoverPanicsAnswers500(t *testing.T) {
	srv := newTestServerNoDB(t)
	logs := captureLogs(t)
	h := srv.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("handler bug") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	wantDetail(t, "panic", rec, 500, "Internal server error.")
	if out := logs.String(); !strings.Contains(out, "handler bug") || !strings.Contains(out, "goroutine") {
		t.Errorf("panic and stack not logged: %q", out)
	}
}

// Through the whole server, with the security headers still on the 500.
func TestRecoverPanicsInTheRouteTable(t *testing.T) {
	env := newAuthEnv(t)
	captureLogs(t)
	env.srv.openFile = func(string, string) (io.ReadCloser, string, error) { panic("opener bug") }
	rec := env.get("/x.md")
	wantDetail(t, "panic", rec, 500, "Internal server error.")
	assertRailsHeaders(t, "500", rec)
}

func TestRecoverPanicsAfterTheResponseStartedAbortsTheConnection(t *testing.T) {
	srv := newTestServerNoDB(t)
	captureLogs(t)
	h := srv.recoverPanics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial"))
		panic("late bug")
	}))
	rec := httptest.NewRecorder()
	defer func() {
		if p := recover(); p != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", p)
		}
		if rec.Body.String() != "partial" {
			t.Errorf("body %q", rec.Body.String())
		}
	}()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
}

func TestRecoverPanicsPassesAbortHandlerThrough(t *testing.T) {
	srv := newTestServerNoDB(t)
	logs := captureLogs(t)
	h := srv.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	defer func() {
		if p := recover(); p != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", p)
		}
		if logs.Len() != 0 {
			t.Errorf("logged an intentional abort: %q", logs.String())
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
}

// --- buffered responses ---

// Write copies what it is given: a handler may reuse its buffer.
func TestBufferedResponseCopiesWrites(t *testing.T) {
	rec := httptest.NewRecorder()
	h := conditionalGet(func(w http.ResponseWriter, _ *http.Request) {
		buf := []byte("first")
		_, _ = w.Write(buf)
		copy(buf, "XXXXX")
		_, _ = w.Write(buf)
	})
	h(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Body.String() != "firstXXXXX" || rec.Header().Get("Content-Length") != "10" {
		t.Errorf("body %q, Content-Length %q", rec.Body.String(), rec.Header().Get("Content-Length"))
	}
}

// The documents that used to pin a render slot for minutes are now answered
// at once with the plain-text page, using the real renderer.
func TestShowQuadraticDocumentsAreAnsweredQuickly(t *testing.T) {
	env := newAuthEnv(t)
	captureLogs(t)
	env.writeFile(t, "gt.md", strings.Repeat(">", MaxRenderBytes))
	env.writeFile(t, "refs.md", strings.Repeat("[a]: b\n", MaxRenderBytes/7))
	for _, p := range []string{"/gt.md", "/refs.md"} {
		start := time.Now()
		rec := env.get(p)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<pre style=") {
			t.Errorf("GET %s: %d", p, rec.Code)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("GET %s took %v", p, d)
		}
	}
	if len(env.srv.renders.slots) != 0 {
		t.Error("a render slot is still held")
	}
}
