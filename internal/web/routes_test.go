package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthAndFavicon(t *testing.T) {
	srv := newTestServer(t)
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
}

func TestHealthHeadHasEmptyBody(t *testing.T) {
	rec := do(newTestServer(t), "HEAD", "/health", nil)
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("got %d with %d body bytes", rec.Code, rec.Body.Len())
	}
}

func TestPublicFiles(t *testing.T) {
	srv := newTestServer(t)
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

func TestPublicFileUnknownName(t *testing.T) {
	srv := newTestServer(t)
	if got := srv.publicFile("nope.txt"); got == nil {
		t.Fatal("nil handler")
	}
	rec := httptest.NewRecorder()
	srv.publicFile("nope.txt").ServeHTTP(rec, httptest.NewRequest("GET", "/nope.txt", nil))
	if rec.Code != 404 {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}

func TestHSTSOnEveryResponse(t *testing.T) {
	srv := newTestServer(t)
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
	srv := newTestServer(t)
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

func TestRoutePrecedence(t *testing.T) {
	srv := newTestServer(t)
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
	// Specific static names beat the {file_name} wildcard.
	if rec := do(srv, "GET", "/robots.txt", nil); rec.Code != 200 {
		t.Errorf("GET /robots.txt: got %d", rec.Code)
	}
	if rec := do(srv, "GET", "/assets/x", nil); rec.Code != 404 {
		t.Errorf("GET /assets/x: got %d, want 404", rec.Code)
	}
}

// Go's CrossOriginProtection only rejects browser-originated cross-origin
// unsafe requests. Non-browser clients (bearer-token API callers such as iOS
// Shortcuts) send neither Sec-Fetch-Site nor Origin and are let through; no
// bypass paths are configured.
func TestCrossOriginProtection(t *testing.T) {
	srv := newTestServer(t)
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
