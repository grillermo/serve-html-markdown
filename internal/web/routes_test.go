package web

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"serve-html-markdown/internal/config"
)

// The one test that builds the server on a real pool, so the integration path
// (New with a testdb pool) stays exercised. It must not call t.Parallel.
func TestServerWithDatabase(t *testing.T) {
	srv := newTestServer(t)
	if srv.pool == nil {
		t.Fatal("pool not set")
	}
	if rec := do(srv, "HEAD", "/health", nil); rec.Code != 200 {
		t.Fatalf("HEAD /health: got %d", rec.Code)
	}
}

func TestHealthAndFavicon(t *testing.T) {
	srv := newTestServerNoDB(t)
	cases := []struct {
		method, path string
		want         int
	}{
		{"HEAD", "/health", 200},
		{"GET", "/favicon.ico", 204},
		{"GET", "/robots.txt", 200},
		{"GET", "/icon.svg", 200},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.want {
			t.Errorf("%s %s: got %d want %d", c.method, c.path, rec.Code, c.want)
		}
	}
	if rec := do(srv, "HEAD", "/health", nil); rec.Body.Len() != 0 {
		t.Errorf("HEAD /health: %d body bytes", rec.Body.Len())
	}
}

func TestPublicFiles(t *testing.T) {
	srv := newTestServerNoDB(t)
	cases := []struct{ path, ctype string }{
		{"/robots.txt", "text/plain"},
		{"/icon.svg", "image/svg+xml"},
		{"/icon.png", "image/png"},
		{"/400.html", "text/html"},
		{"/404.html", "text/html"},
		{"/406-unsupported-browser.html", "text/html"},
		{"/422.html", "text/html"},
		{"/500.html", "text/html"},
	}
	for _, c := range cases {
		rec := do(srv, "GET", c.path, nil)
		if rec.Code != 200 {
			t.Errorf("GET %s: got %d", c.path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, c.ctype) {
			t.Errorf("GET %s: Content-Type %q, want prefix %q", c.path, got, c.ctype)
		}
		if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000" {
			t.Errorf("GET %s: Cache-Control %q", c.path, got)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("GET %s: empty body", c.path)
		}
	}
	head := do(srv, "HEAD", "/robots.txt", nil)
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Errorf("HEAD /robots.txt: %d, %d body bytes", head.Code, head.Body.Len())
	}
	if got := head.Header().Get("Cache-Control"); got != "public, max-age=31536000" {
		t.Errorf("HEAD /robots.txt: Cache-Control %q", got)
	}
}

func TestPublicFileUnlistedNameIs404(t *testing.T) {
	srv := newTestServerNoDB(t)
	rec := httptest.NewRecorder()
	srv.publicFile("nope.txt").ServeHTTP(rec, httptest.NewRequest("GET", "/nope.txt", nil))
	if rec.Code != 404 {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}

func TestMissingEmbeddedPublicFilePanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected a panic")
		}
		if msg, _ := r.(string); !strings.Contains(msg, "robots.txt") || !strings.Contains(msg, "missing") {
			t.Fatalf("unclear panic message: %v", r)
		}
	}()
	servePublic(fstest.MapFS{}, "robots.txt")
}

func TestHSTSOnEveryResponse(t *testing.T) {
	srv := newTestServerNoDB(t)
	for _, p := range [][2]string{
		{"HEAD", "/health"}, {"GET", "/favicon.ico"}, {"GET", "/robots.txt"},
		{"GET", "/assets/x"}, {"GET", "/index"}, {"GET", "/expansions/abc"},
		{"GET", "/nothing/here/at/all"},
	} {
		rec := do(srv, p[0], p[1], nil)
		want := "max-age=63072000; includeSubDomains"
		if got := rec.Header().Get("Strict-Transport-Security"); got != want {
			t.Errorf("%s %s: HSTS %q", p[0], p[1], got)
		}
	}
}

func TestNonDigitIDsAre404(t *testing.T) {
	srv := newTestServerNoDB(t)
	for _, p := range []string{"/expansions/abc", "/summarize-video/abc", "/twitter-video/abc", "/expansions/12a", "/summarize-video/-1"} {
		if rec := do(srv, "GET", p, nil); rec.Code != 404 {
			t.Errorf("GET %s: got %d, want 404", p, rec.Code)
		}
	}
	// Digit ids reach the (not yet ported) handlers.
	for _, p := range []string{"/expansions/12", "/summarize-video/12", "/twitter-video/12"} {
		if rec := do(srv, "GET", p, nil); rec.Code != 501 {
			t.Errorf("GET %s: got %d, want 501", p, rec.Code)
		}
	}
}

// numericID only checks characters, so an id larger than int64 passes it. The
// real {id} handlers (Tasks 8.3, 9.7) must treat a parse error as 404.
func TestHugeIDsPassTheDigitCheck(t *testing.T) {
	srv := newTestServerNoDB(t)
	for _, p := range []string{"/expansions/99999999999999999999", "/summarize-video/99999999999999999999"} {
		if rec := do(srv, "GET", p, nil); rec.Code != 501 {
			t.Errorf("GET %s: got %d, want 501 (reaches the handler)", p, rec.Code)
		}
	}
}

// TRIPWIRE: requireUser is a pass-through stub, so every guarded route answers
// 501 here. Task 3.2 replaces requireUser and must update this test.
func TestRoutePrecedence(t *testing.T) {
	srv := newTestServerNoDB(t)
	// Every stubbed route answers 501 with the JSON detail until its task lands.
	for _, p := range []string{"/", "/last", "/index", "/anything.md", "/users/sign_in"} {
		rec := do(srv, "GET", p, nil)
		if rec.Code != 501 {
			t.Errorf("GET %s: got %d, want 501", p, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Errorf("GET %s: Content-Type %q", p, got)
		}
		if strings.TrimSpace(rec.Body.String()) != `{"detail":"Not implemented."}` {
			t.Errorf("GET %s: body %q", p, rec.Body.String())
		}
	}
	for _, c := range [][2]string{
		{"POST", "/users/sign_in"}, {"DELETE", "/users/sign_out"},
		{"POST", "/file/new"}, {"POST", "/file/upload"},
		{"POST", "/summarize-video"}, {"POST", "/twitter-video"},
		{"GET", "/youtube/reauth"}, {"GET", "/youtube/callback"}, {"GET", "/youtube/debug"},
		{"POST", "/expansions"}, {"PATCH", "/scroll_position"}, {"POST", "/scroll_position"},
	} {
		if rec := do(srv, c[0], c[1], nil); rec.Code != 501 {
			t.Errorf("%s %s: got %d, want 501", c[0], c[1], rec.Code)
		}
	}
	// Static names beat the {file_name} wildcard; /assets/* is its own 404.
	if rec := do(srv, "GET", "/assets/x", nil); rec.Code != 404 {
		t.Errorf("GET /assets/x: got %d, want 404", rec.Code)
	}
}

// Go's CrossOriginProtection only rejects browser-originated cross-origin
// unsafe requests. Non-browser clients (bearer-token API callers such as iOS
// Shortcuts) send neither Sec-Fetch-Site nor Origin and are let through.
func TestCrossOriginProtection(t *testing.T) {
	srv := newTestServerNoDB(t)
	rec := do(srv, "POST", "/scroll_position", map[string]string{"Sec-Fetch-Site": "cross-site"})
	if rec.Code != 403 {
		t.Errorf("cross-site POST: got %d, want 403", rec.Code)
	}
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Error("403 from cross-origin protection lacks HSTS")
	}
	if rec := do(srv, "POST", "/scroll_position", nil); rec.Code != 501 {
		t.Errorf("POST without Sec-Fetch-Site/Origin: got %d, want 501 (allowed through)", rec.Code)
	}
	if rec := do(srv, "POST", "/file/new", map[string]string{"Authorization": "Bearer x"}); rec.Code != 501 {
		t.Errorf("bearer POST without Origin: got %d, want 501", rec.Code)
	}
	if rec := do(srv, "POST", "/scroll_position", map[string]string{"Sec-Fetch-Site": "same-origin"}); rec.Code != 501 {
		t.Errorf("same-origin POST: got %d, want 501", rec.Code)
	}
	if rec := do(srv, "GET", "/index", map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != 501 {
		t.Errorf("cross-site GET (safe method): got %d, want 501", rec.Code)
	}
}

func TestTrustedOriginFromHost(t *testing.T) {
	srv := New(config.Config{FilesDir: t.TempDir(), Host: "files.example.test"}, nil)
	trusted := do(srv, "POST", "/scroll_position", map[string]string{"Origin": "https://files.example.test"})
	if trusted.Code != 501 {
		t.Errorf("Origin https://<Host>: got %d, want 501 (allowed)", trusted.Code)
	}
	other := do(srv, "POST", "/scroll_position", map[string]string{"Origin": "https://evil.example.test"})
	if other.Code != 403 {
		t.Errorf("other Origin: got %d, want 403", other.Code)
	}
	// With HOST=localhost no origin is trusted beyond same-origin.
	local := newTestServerNoDB(t)
	if rec := do(local, "POST", "/scroll_position", map[string]string{"Origin": "https://files.example.test"}); rec.Code != 403 {
		t.Errorf("localhost config, foreign Origin: got %d, want 403", rec.Code)
	}
}

func TestInvalidHostPanicsAtStartup(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic for a malformed HOST")
		}
	}()
	New(config.Config{Host: "bad host/with path"}, nil)
}

// Not parallel: it swaps the slog default.
func TestLogsRejectedCrossOriginRequests(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	srv := newTestServerNoDB(t)
	if rec := do(srv, "POST", "/scroll_position", map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != 403 {
		t.Fatalf("got %d, want 403", rec.Code)
	}
	line := buf.String()
	for _, want := range []string{"method=POST", "path=/scroll_position", "status=403"} {
		if !strings.Contains(line, want) {
			t.Errorf("log %q lacks %q", line, want)
		}
	}
}

func TestOptionsAppliedBeforeRoutes(t *testing.T) {
	if got := newTestServerNoDB(t).Clock; got == nil {
		t.Fatal("Clock has no default")
	}
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	srv := newTestServerNoDB(t, WithClock(func() time.Time { return fixed }))
	if got := srv.Clock(); !got.Equal(fixed) {
		t.Errorf("Clock() = %v, want %v", got, fixed)
	}
}
