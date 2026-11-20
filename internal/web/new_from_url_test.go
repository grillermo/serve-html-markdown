package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func newReq(rawQuery string) *http.Request {
	req := httptest.NewRequest("GET", "/new", nil)
	req.URL.RawQuery = rawQuery
	req.Header.Set("Authorization", bearer)
	return req
}

func TestSourceURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://x.test/a.html?b=1&c=2":                     "https://x.test/a.html?b=1&c=2",
		"url=https%3A%2F%2Fx.test%2Fa.html%3Fb%3D1%26c%3D2": "https://x.test/a.html?b=1&c=2",
		"https%3A%2F%2Fx.test%2Fa.html":                     "https://x.test/a.html",
		"  https://x.test/a.html ":                          "https://x.test/a.html",
	} {
		r := httptest.NewRequest("GET", "/new", nil)
		r.URL.RawQuery = in
		if got := sourceURL(r); got != want {
			t.Errorf("sourceURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewFromURL(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		if r.URL.Path == "/missing.html" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("<h1>hi</h1>"))
	}))
	defer upstream.Close()
	e := newUploadEnv(t, nil, WithFetchClient(upstream.Client()))

	rec := e.serve(newReq(upstream.URL + "/files/d4bc9e0f-6120-40ca-983f-84bdad51252b-lemon-io.html?x=1&y=2"))
	if rec.Code != 303 || rec.Header().Get("Location") != "/lemon-io.html" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if gotPath != "/files/d4bc9e0f-6120-40ca-983f-84bdad51252b-lemon-io.html?x=1&y=2" {
		t.Errorf("upstream saw %q", gotPath)
	}
	if got := e.read(t, "lemon-io.html"); got != "<h1>hi</h1>" || !e.served(t, "lemon-io.html") {
		t.Errorf("stored %q", got)
	}
	// Same URL again: a new file with a numeric suffix.
	rec = e.serve(newReq(url.QueryEscape(upstream.URL + "/lemon-io.html")))
	if rec.Code != 303 || rec.Header().Get("Location") != "/lemon-io-1.html" {
		t.Errorf("second: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec = e.serve(newReq(upstream.URL + "/missing.html")); rec.Code != 502 {
		t.Errorf("404 upstream = %d", rec.Code)
	}
	if rec = e.serve(newReq("not a url")); rec.Code != 400 {
		t.Errorf("bad url = %d", rec.Code)
	}
	if rec = e.serve(newReq(upstream.URL + "/x.exe")); rec.Code != 400 {
		t.Errorf("bad ext = %d", rec.Code)
	}
	req := newReq(upstream.URL + "/a.html")
	req.Header.Del("Authorization")
	if rec = e.serve(req); rec.Code == 303 {
		t.Error("created without auth")
	}
}

func TestNewFromURLBlocksPrivateAddresses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) }))
	defer upstream.Close()
	e := newUploadEnv(t, nil) // default client
	if rec := e.serve(newReq(upstream.URL + "/a.html")); rec.Code != 502 {
		t.Errorf("loopback fetch = %d, want 502", rec.Code)
	}
}
