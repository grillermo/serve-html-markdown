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
	// Digit ids reach the guard (expansions) or the (not yet ported) public handlers.
	if rec := do(srv, "GET", "/expansions/12", nil); rec.Code != 302 {
		t.Errorf("GET /expansions/12: got %d, want 302 (guard)", rec.Code)
	}
	for _, p := range []string{"/summarize-video/12", "/twitter-video/12"} {
		if rec := do(srv, "GET", p, nil); rec.Code != 501 {
			t.Errorf("GET %s: got %d, want 501", p, rec.Code)
		}
	}
}

// numericID only checks characters, so an id larger than int64 passes it. The
// real {id} handlers (Tasks 8.3, 9.7) must treat a parse error as 404.
func TestHugeIDsPassTheDigitCheck(t *testing.T) {
	srv := newTestServerNoDB(t)
	if rec := do(srv, "GET", "/expansions/99999999999999999999", nil); rec.Code != 302 {
		t.Errorf("GET /expansions/<huge>: got %d, want 302 (reaches the guard)", rec.Code)
	}
	if rec := do(srv, "GET", "/summarize-video/99999999999999999999", nil); rec.Code != 501 {
		t.Errorf("GET /summarize-video/<huge>: got %d, want 501 (reaches the handler)", rec.Code)
	}
}

// Guarded routes send an anonymous visitor to the sign-in page and let a
// signed-in one reach the handler (still a 501 stub until its task lands).
func TestRoutePrecedence(t *testing.T) {
	srv := newTestServerNoDB(t)
	for _, p := range []string{"/", "/last", "/index", "/anything.md", "/youtube/reauth", "/expansions/1"} {
		rec := do(srv, "GET", p, nil)
		if rec.Code != 302 || !strings.HasPrefix(rec.Header().Get("Location"), "/users/sign_in") {
			t.Errorf("GET %s without a cookie: got %d %q, want 302 to sign-in", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := do(srv, "GET", "/users/sign_in", nil); rec.Code != 200 {
		t.Errorf("GET /users/sign_in: got %d", rec.Code)
	}
	if rec := do(srv, "DELETE", "/users/sign_out", nil); rec.Code != 303 {
		t.Errorf("DELETE /users/sign_out: got %d", rec.Code)
	}
	// Bearer routes are not behind the session guard: without a token they
	// answer 401 JSON, not a redirect to sign-in.
	for _, c := range [][2]string{{"POST", "/file/new"}, {"POST", "/file/upload"}} {
		rec := do(srv, c[0], c[1], nil)
		if rec.Code != 401 || strings.TrimSpace(rec.Body.String()) != `{"detail":"Unauthorized"}` {
			t.Errorf("%s %s: got %d %q, want 401 Unauthorized", c[0], c[1], rec.Code, rec.Body.String())
		}
	}
	// Public or bearer routes of later tasks are still stubs.
	for _, c := range [][2]string{{"POST", "/summarize-video"}, {"POST", "/twitter-video"}} {
		if rec := do(srv, c[0], c[1], nil); rec.Code != 501 {
			t.Errorf("%s %s: got %d, want 501", c[0], c[1], rec.Code)
		}
	}
	// Static names beat the {file_name} wildcard; /assets/* is its own 404.
	if rec := do(srv, "GET", "/assets/x", nil); rec.Code != 404 {
		t.Errorf("GET /assets/x: got %d, want 404", rec.Code)
	}
}

// With a valid session every guarded route reaches its handler: the ported
// ones answer for real, the others are 501 JSON stubs. Not parallel: it uses
// the shared test database.
func TestRoutePrecedenceSignedIn(t *testing.T) {
	env := newAuthEnv(t)
	h := map[string]string{"Cookie": env.cookieFor(env.user)}
	for _, p := range []string{"/youtube/reauth", "/youtube/callback", "/youtube/debug"} {
		rec := do(env.srv, "GET", p, h)
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
	// Expansions are ported: an empty POST is a 400 and an unknown id a 404, both JSON.
	if rec := do(env.srv, "POST", "/expansions", h); rec.Code != 400 {
		t.Errorf("POST /expansions: got %d, want 400", rec.Code)
	}
	if rec := do(env.srv, "GET", "/expansions/1", h); rec.Code != 404 {
		t.Errorf("GET /expansions/1: got %d, want 404", rec.Code)
	}
	// Ported handlers: no files yet, so / and /last are 404 JSON, /index lists
	// nothing, a file name is looked up, and the scroll position wants fields.
	for p, want := range map[string]int{"/": 404, "/last": 404, "/index": 200, "/anything.md": 404, "/anything.txt": 404} {
		if rec := do(env.srv, "GET", p, h); rec.Code != want {
			t.Errorf("GET %s: got %d, want %d", p, rec.Code, want)
		}
	}
	for _, m := range []string{"PATCH", "POST"} {
		if rec := do(env.srv, m, "/scroll_position", h); rec.Code != 400 {
			t.Errorf("%s /scroll_position: got %d, want 400", m, rec.Code)
		}
	}
	// Exact paths still win over /{file_name}: signing in is not a file.
	if rec := do(env.srv, "GET", "/users/sign_in", h); rec.Code != 302 {
		t.Errorf("GET /users/sign_in signed in: got %d, want 302", rec.Code)
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
	if rec := do(srv, "POST", "/scroll_position", nil); rec.Code != 302 {
		t.Errorf("POST without Sec-Fetch-Site/Origin: got %d, want 302 (allowed through to the guard)", rec.Code)
	}
	// A bearer request without Origin reaches the handler (wrong token: 401,
	// not 403); a cross-site browser request does not.
	if rec := do(srv, "POST", "/file/new", map[string]string{"Authorization": "Bearer x"}); rec.Code != 401 {
		t.Errorf("bearer POST without Origin: got %d, want 401 (reaches the handler)", rec.Code)
	}
	if rec := do(srv, "POST", "/file/new", map[string]string{"Authorization": "Bearer x", "Sec-Fetch-Site": "cross-site"}); rec.Code != 403 {
		t.Errorf("cross-site bearer POST: got %d, want 403", rec.Code)
	}
	if rec := do(srv, "POST", "/scroll_position", map[string]string{"Sec-Fetch-Site": "same-origin"}); rec.Code != 302 {
		t.Errorf("same-origin POST: got %d, want 302", rec.Code)
	}
	if rec := do(srv, "GET", "/index", map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != 302 {
		t.Errorf("cross-site GET (safe method): got %d, want 302", rec.Code)
	}
}

func TestTrustedOriginFromHost(t *testing.T) {
	srv := New(config.Config{FilesDir: t.TempDir(), Host: "files.example.test", SecretKeyBase: "x"}, nil)
	trusted := do(srv, "POST", "/scroll_position", map[string]string{"Origin": "https://files.example.test"})
	if trusted.Code != 302 {
		t.Errorf("Origin https://<Host>: got %d, want 302 (allowed)", trusted.Code)
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
	New(config.Config{Host: "bad host/with path", SecretKeyBase: "x"}, nil)
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
