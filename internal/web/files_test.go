package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/store"
)

// Ports of the show / index / last parts of test/controllers/files_controller_test.rb.
// Test names say which Ruby test they port ("Ruby: ...").

// writeFile puts a file in the server's FilesDir.
func (e *authEnv) writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.srv.cfg.FilesDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// addRow inserts a served_files row with explicit timestamps.
func (e *authEnv) addRow(t *testing.T, name string, created, updated time.Time) {
	t.Helper()
	_, err := e.srv.pool.Exec(context.Background(),
		`INSERT INTO served_files (name, created_at, updated_at) VALUES ($1, $2, $3)`, name, created, updated)
	if err != nil {
		t.Fatal(err)
	}
}

// record is ServedFile.record: a row stamped by the store.
func (e *authEnv) record(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := store.RecordServedFile(context.Background(), e.srv.pool, n); err != nil {
			t.Fatal(err)
		}
	}
}

// get is a signed-in GET.
func (e *authEnv) get(path string, hdr ...string) *httptest.ResponseRecorder {
	h := map[string]string{"Cookie": e.cookieFor(e.user)}
	for i := 0; i+1 < len(hdr); i += 2 {
		h[hdr[i]] = hdr[i+1]
	}
	return do(e.srv, "GET", path, h)
}

func (e *authEnv) expandPath() string { return e.srv.AssetPath("expand.js") }

func wantDetail(t *testing.T, label string, rec *httptest.ResponseRecorder, status int, detail string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("%s: got %d %q, want %d", label, rec.Code, rec.Body.String(), status)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("%s: Content-Type %q", label, got)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body) != 1 || body["detail"] != detail {
		t.Errorf("%s: body %q, want {detail: %q}", label, rec.Body.String(), detail)
	}
}

var fileVersionsRE = regexp.MustCompile(`window\.__fileVersions = (\[.*?\]);`)

func fileVersions(t *testing.T, body string) []FileVersionEntry {
	t.Helper()
	m := fileVersionsRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no window.__fileVersions in %q", body)
	}
	var v []FileVersionEntry
	if err := json.Unmarshal([]byte(m[1]), &v); err != nil {
		t.Fatalf("fileVersions %q: %v", m[1], err)
	}
	return v
}

// Ruby: "serves HTML files without a Rails layout" + "injects the expand script ... into served HTML".
func TestShowHTMLInjectsBootstrapBeforeBody(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "page.html", "<html><body><main>Raw</main></body></html>")

	rec := env.get("/page.html")
	if rec.Code != 200 {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type %q", got)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "<html><body><main>Raw</main><script>") {
		t.Errorf("bootstrap not right after the content: %q", body)
	}
	want := `<script src="` + env.expandPath() + `" defer></script></body></html>`
	if !strings.HasSuffix(body, want) {
		t.Errorf("body %q does not end with %q", body, want)
	}
	if !regexp.MustCompile(`^/assets/expand-[0-9a-f]{8,}\.js$`).MatchString(env.expandPath()) {
		t.Errorf("expand.js is not digested: %q", env.expandPath())
	}
	if strings.Contains(body, "csrf-token") {
		t.Error("csrf meta tag should be gone")
	}
}

// Ruby: content.sub(%r{</body>}i) { "#{snippet}</body>" }: any case, the first
// match only, and the tag comes out lower-case.
func TestShowHTMLInjectsBeforeFirstBodyAnyCase(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "page.html", "<p>a</p></BODY>mid</body>end")

	body := env.get("/page.html").Body.String()
	if strings.Count(body, "<script src=") != 1 {
		t.Fatalf("bootstrap inserted %d times: %q", strings.Count(body, "<script src="), body)
	}
	i := strings.Index(body, "<script>")
	j := strings.Index(body, "</body>mid</body>end")
	if !strings.HasPrefix(body, "<p>a</p><script>") || j < i {
		t.Errorf("bootstrap not before the first </body>: %q", body)
	}
}

// Ruby: "appends the expand script when HTML has no body tag".
func TestShowHTMLAppendsWithoutBody(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "page.html", "<main>Raw HTML</main>")

	body := env.get("/page.html").Body.String()
	if !strings.HasPrefix(body, "<main>Raw HTML</main><script>") || !strings.HasSuffix(body, `defer></script>`) {
		t.Errorf("body %q", body)
	}
}

func TestInjectBootstrap(t *testing.T) {
	const snip = "<S/>"
	for _, tc := range []struct{ name, in, want string }{
		{"empty", "", "<S/>"},
		{"lower", "a</body>b", "a<S/></body>b"},
		{"upper", "a</BODY>b", "a<S/></body>b"},
		{"mixed", "a</BoDy>b", "a<S/></body>b"},
		{"first only", "</body></body>", "<S/></body></body>"},
		{"near miss then match", "</bod></bodyx></body>", "</bod></bodyx><S/></body>"},
		{"truncated at end", "x</body", "x</body<S/>"},
		{"multibyte", "héllo ✓ 日本</body>é", "héllo ✓ 日本<S/></body>é"},
		{"invalid utf-8 passes through", "\xff\xfe<p>\x80</p></body>\xc3", "\xff\xfe<p>\x80</p><S/></body>\xc3"},
		{"lt storm", strings.Repeat("<", 1000) + "</body>", strings.Repeat("<", 1000) + "<S/></body>"},
		{"unicode lookalike is not body", "</bödy></body>", "</bödy><S/></body>"},
	} {
		if got := string(injectBootstrap([]byte(tc.in), snip)); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Invalid UTF-8 in an .html file: Ruby would raise on the regexp; we pass the bytes through.
func TestShowHTMLPassesInvalidUTF8Through(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "bin.html", "\xff\xfe<p>x</p>")
	rec := env.get("/bin.html")
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "\xff\xfe<p>x</p><script>") {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}
}

// Ruby: "renders Markdown with the dark layout and Commonmarker options".
func TestShowMarkdownRendersInLayout(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "notes.md", "# \"Notes\"\n\nVisit https://example.com.\n\n<mark>Trusted HTML</mark>\n")

	rec := env.get("/notes.md")
	if rec.Code != 200 {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type %q", got)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<title>notes.md</title>",
		`href="/assets/markdown-`,
		`<h1><a href="#notes" aria-hidden="true" class="anchor" id="notes"></a>“Notes”</h1>`,
		`<a href="https://example.com">https://example.com</a>`,
		"<mark>Trusted HTML</mark>",
		`<script src="` + env.expandPath() + `" defer></script>`,
		"window.__fileVersions = [];",
		`window.__expansionMode = "create_new";`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %q", want, body)
		}
	}
	if strings.Contains(body, "window.__scrollAnchor") {
		t.Error("scroll anchor set without a saved position")
	}
}

// The title is the canonical file name, escaped.
func TestShowMarkdownTitleIsEscaped(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "a<b>&c.md", "x")
	if body := env.get("/a%3Cb%3E%26c.md").Body.String(); !strings.Contains(body, "<title>a&lt;b&gt;&amp;c.md</title>") {
		t.Errorf("title not escaped: %q", body)
	}
}

// Ruby: any extension that is not .html renders as Markdown, including
// .markdown and upper-case ones.
func TestShowMarkdownExtensions(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "a.markdown", "# A")
	env.writeFile(t, "B.MD", "# B")
	env.writeFile(t, "C.HTML", "<b>c</b>")
	env.writeFile(t, "dot.v2.md", "# D")
	for path, want := range map[string]string{
		"/a.markdown": `<title>a.markdown</title>`,
		"/B.MD":       `<title>B.MD</title>`,
		"/dot.v2.md":  `<title>dot.v2.md</title>`,
		"/C.HTML":     `<b>c</b><script>`,
	} {
		rec := env.get(path)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s: %d %q, want %q", path, rec.Code, rec.Body.String(), want)
		}
	}
}

// Invalid UTF-8 in Markdown: Ruby fails with a 500; we log a warning and render
// the text with U+FFFD in place of the bad bytes.
func TestShowMarkdownInvalidUTF8IsRepaired(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "bad.md", "# Caf\xe9\n\nok \xff\n")
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)

	rec := env.get("/bad.md")
	if rec.Code != 200 {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "Caf�") || !strings.Contains(body, "ok �") {
		t.Errorf("body %q", body)
	}
	if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "bad.md") {
		t.Errorf("no warning logged: %q", out)
	}
}

// Ruby: "embeds a saved scroll anchor for markdown and HTML files" and "omits ... when no position was saved".
func TestShowEmbedsSavedScrollAnchor(t *testing.T) {
	env := newAuthEnv(t)
	ctx := context.Background()
	if err := store.UpsertScrollPosition(ctx, env.srv.pool, env.user.ID, "notes.md", "saved-heading"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertScrollPosition(ctx, env.srv.pool, env.user.ID, "page.html", "saved-section"); err != nil {
		t.Fatal(err)
	}
	env.writeFile(t, "notes.md", "# Notes")
	env.writeFile(t, "page.html", `<main id="saved-section">Raw HTML</main>`)
	env.writeFile(t, "other.md", "# Other")

	if body := env.get("/notes.md").Body.String(); !strings.Contains(body, `window.__scrollAnchor = "saved-heading";`) {
		t.Errorf("markdown: %q", body)
	}
	if body := env.get("/page.html").Body.String(); !strings.Contains(body, `window.__scrollAnchor = "saved-section";`) {
		t.Errorf("html: %q", body)
	}
	if body := env.get("/other.md").Body.String(); strings.Contains(body, "__scrollAnchor") {
		t.Errorf("anchor leaked to another file: %q", body)
	}
}

// Anchors are per (user, file name).
func TestShowScrollAnchorIsPerUser(t *testing.T) {
	env := newAuthEnv(t)
	other := env.addUser(t, "other@example.com", "pw")
	if err := store.UpsertScrollPosition(context.Background(), env.srv.pool, other.ID, "notes.md", "theirs"); err != nil {
		t.Fatal(err)
	}
	env.writeFile(t, "notes.md", "# Notes")
	if body := env.get("/notes.md").Body.String(); strings.Contains(body, "__scrollAnchor") {
		t.Errorf("another user's anchor shown: %q", body)
	}
}

// The scroll row is keyed by the name on disk, not by the case the URL used
// (only observable on a case-insensitive file system such as macOS's).
func TestShowUsesCanonicalNameForScrollPosition(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "Canon.md", "# Canon")
	if _, err := os.Stat(filepath.Join(env.srv.cfg.FilesDir, "canon.md")); err != nil {
		t.Skip("file system is case-sensitive")
	}
	if err := store.UpsertScrollPosition(context.Background(), env.srv.pool, env.user.ID, "Canon.md", "here"); err != nil {
		t.Fatal(err)
	}
	body := env.get("/canon.md").Body.String()
	if !strings.Contains(body, `window.__scrollAnchor = "here";`) || !strings.Contains(body, "<title>Canon.md</title>") {
		t.Errorf("body %q", body)
	}
}

// Ruby: "injects version data into a markdown page with siblings".
func TestShowMarkdownFileVersions(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "foo.md", "# Foo")
	env.writeFile(t, "foo--v2.md", "# Foo, expanded")
	env.record(t, "foo.md", "foo--v2.md")

	rec := env.get("/foo.md")
	want := []FileVersionEntry{
		{Name: "foo.md", URL: "/foo.md", Version: 1, Current: true},
		{Name: "foo--v2.md", URL: "/foo--v2.md", Version: 2},
	}
	if got := fileVersions(t, rec.Body.String()); !equalEntries(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	want[0].Current, want[1].Current = false, true
	if got := fileVersions(t, env.get("/foo--v2.md").Body.String()); !equalEntries(got, want) {
		t.Errorf("v2: got %+v, want %+v", got, want)
	}
}

func equalEntries(a, b []FileVersionEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Ruby: "injects version data into an html page with siblings".
func TestShowHTMLFileVersions(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "page.html", "<html><body>one</body></html>")
	env.writeFile(t, "page--v2.html", "<html><body>two</body></html>")
	env.record(t, "page.html", "page--v2.html")

	body := env.get("/page--v2.html").Body.String()
	if !strings.Contains(body, `"version":2,"current":true`) || !strings.Contains(body, `"name":"page.html"`) {
		t.Errorf("body %q", body)
	}
}

// Ruby: "injects an empty version list for a file with no siblings", and a
// family of one member (the file next to a different-extension file) is also [].
func TestShowFileVersionsEmptyWithoutSiblings(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "solo.md", "# Solo")
	env.writeFile(t, "solo--v2.html", "<p>other extension</p>")
	env.record(t, "solo.md", "solo--v2.html")

	body := env.get("/solo.md").Body.String()
	if !strings.Contains(body, "window.__fileVersions = [];") {
		t.Errorf("body %q", body)
	}
	body = env.get("/solo--v2.html").Body.String()
	if !strings.Contains(body, "window.__fileVersions = [];") {
		t.Errorf("html body %q", body)
	}
}

// Ruby: "reflects the user's remembered expansion mode".
func TestShowReflectsExpansionMode(t *testing.T) {
	env := newAuthEnv(t)
	if err := store.SetExpansionMode(context.Background(), env.srv.pool, env.user.ID, "edit_in_place"); err != nil {
		t.Fatal(err)
	}
	env.writeFile(t, "notes.md", "# Notes")
	env.writeFile(t, "page.html", "<p>x</p>")
	for _, p := range []string{"/notes.md", "/page.html"} {
		if body := env.get(p).Body.String(); !strings.Contains(body, `window.__expansionMode = "edit_in_place";`) {
			t.Errorf("%s: %q", p, body)
		}
	}
}

// Ruby: "rejects unsupported and missing files".
func TestShowErrors(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "private.txt", "not served")
	wantDetail(t, "unsupported", env.get("/private.txt"), 404, "Only .html, .md, and .markdown files are supported.")
	wantDetail(t, "missing", env.get("/missing.md"), 404, "File not found: missing.md")
	wantDetail(t, "dot only", env.get("/.md"), 404, "Only .html, .md, and .markdown files are supported.")
	wantDetail(t, "trailing dot", env.get("/notes.md."), 404, "Only .html, .md, and .markdown files are supported.")
	wantDetail(t, "NUL", env.get("/a%00.md"), 400, "Invalid file path.")
	if err := os.Mkdir(filepath.Join(env.srv.cfg.FilesDir, "dir.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantDetail(t, "directory", env.get("/dir.md"), 404, "File not found: dir.md")
	// Errors are not cacheable (Rack::ETag's "no-cache" for responses without an ETag).
	if got := env.get("/missing.md").Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control %q", got)
	}
}

// Ruby: "does not serve encoded path traversal attempts" and "does not follow
// symlinks outside the files directory".
func TestShowCannotEscapeFilesDir(t *testing.T) {
	env := newAuthEnv(t)
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "secret.md")
	if err := os.WriteFile(secret, []byte("TOPSECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(env.srv.cfg.FilesDir, "linked.md")); err != nil {
		t.Fatal(err)
	}
	// A secret next to the files directory, reachable by "../secret.md".
	sibling := filepath.Join(filepath.Dir(env.srv.cfg.FilesDir), "secret-sibling.md")
	if err := os.WriteFile(sibling, []byte("TOPSECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(sibling)

	for _, p := range []string{
		"/%2E%2E%2Fsecret-sibling.md",
		"/..%2Fsecret-sibling.md",
		"/%2e%2e%2f%2e%2e%2fetc%2fpasswd.md",
		"/..%5Csecret-sibling.md",
		"/" + URLEncode(secret),
		"/%2Fetc%2Fpasswd.md",
		"/..%2F..%2F..%2F..%2F..%2Fetc%2Fhosts.md",
		"/linked.md",
	} {
		rec := env.get(p)
		if rec.Code != 400 && rec.Code != 404 {
			t.Errorf("GET %s: got %d, want 400 or 404", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "TOPSECRET") {
			t.Errorf("GET %s leaked the secret", p)
		}
	}
	wantDetail(t, "symlink", env.get("/linked.md"), 400, "Invalid file path.")
	wantDetail(t, "encoded traversal", env.get("/%2E%2E%2Fsecret-sibling.md"), 400, "Invalid file path.")
}

func url_(s string) string { return URLEncode(s) }

// Ruby: "serves files to older browser user agents".
func TestShowServesOldBrowsers(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "page.html", "<main>Available</main>")
	rec := env.get("/page.html", "User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/80.0.3987.149 Safari/537.36")
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "<main>Available</main>") {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}
}

// Ruby: "redirects unauthenticated viewers to sign in".
func TestShowRequiresSignIn(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "notes.md", "# Notes")
	rec := do(env.srv, "GET", "/notes.md", nil)
	if rec.Code != 302 || !strings.HasPrefix(rec.Header().Get("Location"), "/users/sign_in") {
		t.Errorf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// Rails routing ignores a trailing slash.
func TestShowTrailingSlash(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "notes.md", "# Notes")
	env.addRow(t, "notes.md", fixedNow, fixedNow)
	if rec := env.get("/notes.md/"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>notes.md</title>") {
		t.Errorf("/notes.md/: %d %q", rec.Code, rec.Body.String())
	}
	if rec := env.get("/last/"); rec.Code != 302 || rec.Header().Get("Location") != "/notes.md" {
		t.Errorf("/last/: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := env.get("/index/"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `href="/notes.md"`) {
		t.Errorf("/index/: %d", rec.Code)
	}
}

// --- size caps ---

func TestShowRefusesLargeMarkdown(t *testing.T) {
	env := newAuthEnv(t)
	if MaxRenderBytes != 5<<20 {
		t.Fatalf("MaxRenderBytes = %d, want 5 MiB", MaxRenderBytes)
	}
	env.writeFile(t, "big.md", strings.Repeat("a", MaxRenderBytes+1))
	wantDetail(t, "big.md", env.get("/big.md"), 413, "File is too large to display.")

	env.writeFile(t, "exact.md", strings.Repeat("a", MaxRenderBytes))
	if rec := env.get("/exact.md"); rec.Code != 200 {
		t.Errorf("a file of exactly MaxRenderBytes: got %d", rec.Code)
	}
}

func TestShowRefusesLargeHTML(t *testing.T) {
	env := newAuthEnv(t)
	if MaxHTMLBytes <= MaxRenderBytes {
		t.Fatalf("MaxHTMLBytes %d must exceed MaxRenderBytes %d", MaxHTMLBytes, MaxRenderBytes)
	}
	env.srv.maxHTMLBytes = 1000
	env.writeFile(t, "big.html", strings.Repeat("x", 1001))
	wantDetail(t, "big.html", env.get("/big.html"), 413, "File is too large to display.")
	env.writeFile(t, "ok.html", strings.Repeat("x", 1000))
	if rec := env.get("/ok.html"); rec.Code != 200 {
		t.Errorf("html at the cap: got %d", rec.Code)
	}
	// HTML is not rendered, so it may exceed the Markdown cap.
	env.srv.maxRenderBytes = 10
	if rec := env.get("/ok.html"); rec.Code != 200 {
		t.Errorf("html above the markdown cap: got %d", rec.Code)
	}
}

// --- render concurrency ---

// spyWriter records whether anything was written.
type spyWriter struct {
	h       http.Header
	written bool
}

func (w *spyWriter) Header() http.Header         { return w.h }
func (w *spyWriter) Write(b []byte) (int, error) { w.written = true; return len(b), nil }
func (w *spyWriter) WriteHeader(int)             { w.written = true }

// With one slot, a second render waits for the first; a waiting request whose
// context is cancelled returns without writing anything.
func TestMarkdownRenderWaitsForSlotAndHonoursCancellation(t *testing.T) {
	env := newAuthEnv(t)
	env.srv = New(env.srv.cfg, env.srv.pool, WithClock(env.srv.Clock), WithRenderConcurrency(1))
	env.writeFile(t, "a.md", "# A")

	acquired := make(chan struct{}, 4)
	release := make(chan struct{})
	waiting := make(chan struct{}, 4)
	var running, maxRunning atomic.Int32
	env.srv.renderAcquired = func() {
		n := running.Add(1)
		for m := maxRunning.Load(); n > m && !maxRunning.CompareAndSwap(m, n); m = maxRunning.Load() {
		}
		acquired <- struct{}{}
		<-release
		running.Add(-1)
	}
	env.srv.renderWaiting = func() { waiting <- struct{}{} }

	var wg sync.WaitGroup
	first := httptest.NewRecorder()
	wg.Add(1)
	go func() { defer wg.Done(); env.srv.ServeHTTP(first, signedReq(env, "/a.md", context.Background())) }()
	<-acquired // the first render holds the only slot

	// Second request: cancelled while it waits.
	ctx, cancel := context.WithCancel(context.Background())
	spy := &spyWriter{h: http.Header{}}
	done := make(chan struct{})
	go func() { env.srv.ServeHTTP(spy, signedReq(env, "/a.md", ctx)); close(done) }()
	<-waiting
	cancel()
	<-done
	if spy.written {
		t.Error("a cancelled request wrote a response")
	}

	// Third request: waits, then completes once the slot is free.
	third := httptest.NewRecorder()
	wg.Add(1)
	go func() { defer wg.Done(); env.srv.ServeHTTP(third, signedReq(env, "/a.md", context.Background())) }()
	<-waiting
	close(release) // free the first; the third takes the slot and finishes
	wg.Wait()

	if first.Code != 200 || third.Code != 200 || !strings.Contains(third.Body.String(), "<title>a.md</title>") {
		t.Errorf("first %d, third %d", first.Code, third.Code)
	}
	if got := maxRunning.Load(); got != 1 {
		t.Errorf("max concurrent renders = %d, want 1", got)
	}
}

func signedReq(env *authEnv, path string, ctx context.Context) *http.Request {
	req := httptest.NewRequest("GET", path, nil).WithContext(ctx)
	req.Header.Set("Cookie", env.cookieFor(env.user))
	return req
}

func TestRenderConcurrencyOption(t *testing.T) {
	srv := newTestServerNoDB(t)
	if cap(srv.renderSlots) != DefaultRenderConcurrency {
		t.Errorf("default slots %d, want %d", cap(srv.renderSlots), DefaultRenderConcurrency)
	}
	srv = newTestServerNoDB(t, WithRenderConcurrency(2), WithRenderConcurrency(0))
	if cap(srv.renderSlots) != 2 {
		t.Errorf("slots %d, want 2", cap(srv.renderSlots))
	}
}

// --- index ---

// Ruby: "lists served files with a link and updated_at from the database" and
// "orders the index listing by most recently updated first".
func TestIndexListsFilesNewestUpdateFirst(t *testing.T) {
	env := newAuthEnv(t)
	env.addRow(t, "older.md", fixedNow.Add(-72*time.Hour), fixedNow.Add(-48*time.Hour))
	env.addRow(t, "newer.html", fixedNow.Add(-96*time.Hour), fixedNow.Add(-time.Hour))
	env.addRow(t, "my notes & more.md", fixedNow.Add(-96*time.Hour), fixedNow.Add(-24*time.Hour))

	rec := env.get("/index")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	order := []string{
		`<a href="/newer.html">newer.html</a>`,
		`<a href="/my%20notes%20%26%20more.md">my notes &amp; more.md</a>`,
		`<a href="/older.md">older.md</a>`,
	}
	last := -1
	for _, want := range order {
		i := strings.Index(body, want)
		if i < 0 || i < last {
			t.Fatalf("%q missing or out of order in %q", want, body)
		}
		last = i
	}
	if !strings.Contains(body, "Nov 17, 2026 5:00 AM") { // 11:00 UTC is 05:00 in Mexico City
		t.Errorf("updated_at not shown in Mexico City time: %q", body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "max-age=0, private, must-revalidate" {
		t.Errorf("Cache-Control %q", got)
	}
}

func TestIndexEmpty(t *testing.T) {
	env := newAuthEnv(t)
	rec := env.get("/index")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<th>Name</th>") {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}
}

// --- last ---

// Ruby: "redirects to the last added file from the served_files table".
func TestLastRedirectsToNewestFile(t *testing.T) {
	env := newAuthEnv(t)
	env.addRow(t, "older.html", fixedNow.Add(-48*time.Hour), fixedNow)
	env.addRow(t, "newer one#1.markdown", fixedNow.Add(-24*time.Hour), fixedNow.Add(-48*time.Hour))
	for _, p := range []string{"/last", "/"} {
		rec := env.get(p)
		if rec.Code != 302 || rec.Header().Get("Location") != "/newer%20one%231.markdown" {
			t.Errorf("GET %s: got %d %q", p, rec.Code, rec.Header().Get("Location"))
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s: Cache-Control %q", p, got)
		}
	}
}

// Ruby: "returns JSON not found when the served_files table is empty".
func TestLastWithoutFiles(t *testing.T) {
	env := newAuthEnv(t)
	for _, p := range []string{"/", "/last"} {
		wantDetail(t, p, env.get(p), 404, "No files found.")
	}
}

// Ruby: "redirects unauthenticated root requests to sign in".
func TestLastRequiresSignIn(t *testing.T) {
	env := newAuthEnv(t)
	rec := do(env.srv, "GET", "/", nil)
	if rec.Code != 302 || !strings.HasPrefix(rec.Header().Get("Location"), "/users/sign_in") {
		t.Errorf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// --- HEAD, ETag, conditional GET ---

func TestHeadHasHeadersButNoBody(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "notes.md", "# Notes")
	env.writeFile(t, "page.html", "<p>x</p>")
	for _, p := range []string{"/notes.md", "/page.html", "/index"} {
		get := env.get(p)
		rec := do(env.srv, "HEAD", p, map[string]string{"Cookie": env.cookieFor(env.user)})
		if rec.Code != 200 || rec.Body.Len() != 0 {
			t.Errorf("HEAD %s: %d with %d body bytes", p, rec.Code, rec.Body.Len())
		}
		if rec.Header().Get("Content-Length") != get.Header().Get("Content-Length") || rec.Header().Get("Content-Length") == "" ||
			rec.Header().Get("ETag") != get.Header().Get("ETag") || rec.Header().Get("Content-Type") != get.Header().Get("Content-Type") {
			t.Errorf("HEAD %s headers %v differ from GET %v", p, rec.Header(), get.Header())
		}
	}
}

// Rack::ETag adds a weak ETag to every 200 response and Rack::ConditionalGet
// answers a matching If-None-Match with 304.
func TestConditionalGet(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "notes.md", "# Notes")
	env.writeFile(t, "page.html", "<p>x</p>")
	for _, p := range []string{"/notes.md", "/page.html", "/index"} {
		first := env.get(p)
		etag := first.Header().Get("ETag")
		if !regexp.MustCompile(`^W/"[0-9a-f]{32}"$`).MatchString(etag) {
			t.Fatalf("GET %s: ETag %q", p, etag)
		}
		if got := first.Header().Get("Cache-Control"); got != "max-age=0, private, must-revalidate" {
			t.Errorf("GET %s: Cache-Control %q", p, got)
		}
		if again := env.get(p); again.Header().Get("ETag") != etag {
			t.Errorf("GET %s: ETag changed between identical requests", p)
		}
		for _, inm := range []string{etag, `"nope", ` + etag, "*"} {
			rec := env.get(p, "If-None-Match", inm)
			if rec.Code != 304 || rec.Body.Len() != 0 || rec.Header().Get("ETag") != etag ||
				rec.Header().Get("Content-Type") != "" || rec.Header().Get("Cache-Control") == "" {
				t.Errorf("GET %s If-None-Match %q: %d %v %q", p, inm, rec.Code, rec.Header(), rec.Body.String())
			}
		}
		if rec := env.get(p, "If-None-Match", `W/"other"`); rec.Code != 200 {
			t.Errorf("GET %s with another ETag: %d", p, rec.Code)
		}
	}
	// A changed document changes the ETag.
	before := env.get("/notes.md").Header().Get("ETag")
	env.writeFile(t, "notes.md", "# Changed")
	if env.get("/notes.md").Header().Get("ETag") == before {
		t.Error("ETag did not change with the content")
	}
}

// --- database failures ---

// A failing lookup fails the request instead of dropping the anchor or versions.
func TestShowFailsOnDatabaseError(t *testing.T) {
	env := newAuthEnv(t)
	env.writeFile(t, "notes.md", "# Notes")
	env.srv.pool.Close()
	for _, p := range []string{"/notes.md", "/last", "/index"} {
		req := httptest.NewRequest("GET", p, nil)
		req = req.WithContext(auth.WithUser(req.Context(), env.user))
		req.SetPathValue("file_name", strings.TrimPrefix(p, "/"))
		rec := httptest.NewRecorder()
		switch p {
		case "/notes.md":
			env.srv.showFile(rec, req)
		case "/last":
			env.srv.lastFile(rec, req)
		default:
			env.srv.indexFiles(rec, req)
		}
		wantDetail(t, p, rec, 500, "Internal server error.")
	}
}

// Anything that is not a files error is a 500 with the generic detail, and
// the response does not carry the real error.
func TestFileErrorHidesInternalDetails(t *testing.T) {
	env := newAuthEnv(t)
	rec := httptest.NewRecorder()
	env.srv.fileError(rec, httptest.NewRequest("GET", "/x.md", nil), io.ErrUnexpectedEOF)
	wantDetail(t, "internal", rec, 500, "Internal server error.")
}
