package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	webfs "serve-html-markdown/web"
)

func embeddedAsset(t *testing.T, name string) []byte {
	t.Helper()
	data, err := fs.ReadFile(webfs.FS, "assets/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func digest8(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:8]
}

func TestAssetPathDigestsContent(t *testing.T) {
	srv := newTestServerNoDB(t)
	cases := map[string]string{
		"expand.js":       "/assets/expand-%s.js",
		"application.css": "/assets/application-%s.css",
		"markdown.css":    "/assets/markdown-%s.css",
	}
	for logical, pattern := range cases {
		want := strings.Replace(pattern, "%s", digest8(embeddedAsset(t, logical)), 1)
		if got := srv.AssetPath(logical); got != want {
			t.Errorf("AssetPath(%q) = %q, want %q", logical, got, want)
		}
	}
}

func TestAssetPathUnknownNameFallsBackToPlainPath(t *testing.T) {
	// A typo in a template must not 500 production; the link simply 404s.
	srv := newTestServerNoDB(t)
	if got := srv.AssetPath("nope.css"); got != "/assets/nope.css" {
		t.Errorf("got %q", got)
	}
}

func TestServesDigestedAsset(t *testing.T) {
	srv := newTestServerNoDB(t)
	p := srv.AssetPath("expand.js")
	rec := do(srv, "GET", p, nil)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	h := rec.Header()
	if got := h.Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Errorf("Content-Type %q", got)
	}
	if got := h.Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control %q", got)
	}
	if got := h.Get("ETag"); got == "" || !strings.HasPrefix(got, `"`) {
		t.Errorf("ETag %q, want a strong etag", got)
	}
	if got := h.Get("Content-Length"); got != "" && got != strconv.Itoa(len(embeddedAsset(t, "expand.js"))) {
		t.Errorf("Content-Length %q", got)
	}
	if rec.Body.String() != string(embeddedAsset(t, "expand.js")) {
		t.Error("body differs from the embedded file")
	}
}

func TestServesCSSWithCharset(t *testing.T) {
	srv := newTestServerNoDB(t)
	rec := do(srv, "GET", srv.AssetPath("markdown.css"), nil)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Errorf("Content-Type %q", got)
	}
}

func TestAssetWrongDigestOrShapeIs404(t *testing.T) {
	srv := newTestServerNoDB(t)
	good := srv.AssetPath("expand.js")
	for _, p := range []string{
		"/assets/expand-deadbeef.js",
		"/assets/expand.js", // the plain name is not served
		"/assets/expand-" + digest8(embeddedAsset(t, "expand.js")) + ".css",
		"/assets/EXPAND" + strings.TrimPrefix(good, "/assets/expand"),
		"/assets/..%2Fassets%2F" + strings.TrimPrefix(good, "/assets/"),
		"/assets/%2e%2e/templates/layout.html",
		"/assets/..%5Cexpand.js",
		"/assets/sub/" + strings.TrimPrefix(good, "/assets/"),
		"/assets/",
		"/assets/" + strings.TrimPrefix(good, "/assets/") + "/",
	} {
		if rec := do(srv, "GET", p, nil); rec.Code != 404 {
			t.Errorf("GET %s: got %d, want 404", p, rec.Code)
		}
	}
}

func TestAssetETagAndConditionalGet(t *testing.T) {
	srv := newTestServerNoDB(t)
	p := srv.AssetPath("markdown.css")
	etag := do(srv, "GET", p, nil).Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	rec := do(srv, "GET", p, map[string]string{"If-None-Match": etag})
	if rec.Code != 304 {
		t.Errorf("matching If-None-Match: got %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Error("304 has a body")
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("304 Cache-Control %q", got)
	}
	if rec := do(srv, "GET", p, map[string]string{"If-None-Match": `"other"`}); rec.Code != 200 {
		t.Errorf("non-matching If-None-Match: got %d", rec.Code)
	}
}

func TestAssetHead(t *testing.T) {
	srv := newTestServerNoDB(t)
	rec := do(srv, "HEAD", srv.AssetPath("expand.js"), nil)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Error("HEAD has a body")
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(embeddedAsset(t, "expand.js"))) {
		t.Errorf("Content-Length %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Errorf("Content-Type %q", got)
	}
}

func TestAssetsRejectUnsafeMethods(t *testing.T) {
	srv := newTestServerNoDB(t)
	if rec := do(srv, "POST", srv.AssetPath("expand.js"), nil); rec.Code != http.StatusMethodNotAllowed && rec.Code != 404 {
		t.Errorf("POST: got %d", rec.Code)
	}
}

func TestAssetServedWithoutSessionCookie(t *testing.T) {
	// Assets are public, like Propshaft's: no sign-in redirect.
	srv := newTestServerNoDB(t)
	req := httptest.NewRequest("GET", srv.AssetPath("application.css"), nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("got %d", rec.Code)
	}
}

// The stylesheets are served byte for byte. If one ever references another
// asset with url() or @import, Propshaft would rewrite the reference to the
// digested path and newAssetTable must do the same: this test is the reminder.
func TestEmbeddedStylesheetsHaveNoAssetReferences(t *testing.T) {
	re := regexp.MustCompile(`(?i)url\(|@import`)
	for _, name := range []string{"application.css", "markdown.css"} {
		if re.Match(embeddedAsset(t, name)) {
			t.Errorf("%s references another asset; teach newAssetTable to rewrite it", name)
		}
	}
}

func mustPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Fatalf("no panic, want one containing %q", want)
		}
		if msg, _ := r.(string); !strings.Contains(msg, want) {
			t.Fatalf("panic %v, want it to contain %q", r, want)
		}
	}()
	f()
}

func TestNewAssetTableSkipsHiddenFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"assets/app.css":      {Data: []byte("a{}")},
		"assets/.DS_Store":    {Data: []byte("junk")},
		"assets/_partial.css": {Data: []byte("b{}")},
		"assets/.hidden/x":    {Data: []byte("c")}, // a hidden directory is skipped, not an error
	}
	tbl := newAssetTable(fsys)
	if len(tbl.public) != 1 || len(tbl.byName) != 1 {
		t.Fatalf("public %v, byName has %d", tbl.public, len(tbl.byName))
	}
	if _, ok := tbl.public["app.css"]; !ok {
		t.Errorf("app.css missing: %v", tbl.public)
	}
}

func TestNewAssetTableDigestsAndNames(t *testing.T) {
	data := []byte("body{color:red}")
	tbl := newAssetTable(fstest.MapFS{"assets/site.min.css": {Data: data}})
	// path.Ext splits at the last dot: the digest goes before ".css".
	want := "site.min-" + digest8(data) + ".css"
	if got := tbl.public["site.min.css"]; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNewAssetTableFailurePaths(t *testing.T) {
	t.Run("no assets directory", func(t *testing.T) {
		mustPanic(t, "unreadable", func() { newAssetTable(fstest.MapFS{"other/x.css": {}}) })
	})
	t.Run("empty directory", func(t *testing.T) {
		mustPanic(t, "no files", func() { newAssetTable(fstest.MapFS{"assets": {Mode: fs.ModeDir}}) })
	})
	t.Run("only hidden files", func(t *testing.T) {
		mustPanic(t, "no files", func() { newAssetTable(fstest.MapFS{"assets/.keep": {}}) })
	})
	t.Run("subdirectory", func(t *testing.T) {
		mustPanic(t, "subdirectories are not supported", func() {
			newAssetTable(fstest.MapFS{"assets/a.css": {Data: []byte("a")}, "assets/sub/b.css": {Data: []byte("b")}})
		})
	})
	t.Run("unreadable file", func(t *testing.T) {
		fsys := failingOpenFS{FS: fstest.MapFS{"assets/a.css": {Data: []byte("a")}}, bad: "assets/a.css"}
		mustPanic(t, "read asset", func() { newAssetTable(fsys) })
	})
}

// failingOpenFS fails to open one file, as a permission error would.
type failingOpenFS struct {
	fs.FS
	bad string
}

func (f failingOpenFS) Open(name string) (fs.File, error) {
	if name == f.bad {
		return nil, errors.New("boom")
	}
	return f.FS.Open(name)
}
