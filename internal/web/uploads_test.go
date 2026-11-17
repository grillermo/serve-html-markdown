package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/llm"
	"serve-html-markdown/internal/store"
)

// Ports of the /file/new and /file/upload cases of
// test/controllers/files_controller_test.rb ("Ruby: ..." names the test).

const bearer = "Bearer upload-token"

// fakeFormatter records what it was asked to format.
type fakeFormatter struct {
	fn    func(ctx context.Context, content string) (string, error)
	mu    sync.Mutex
	calls []string
}

func (f *fakeFormatter) Format(ctx context.Context, content string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, content)
	f.mu.Unlock()
	return f.fn(ctx, content)
}

func returning(out string) *fakeFormatter {
	return &fakeFormatter{fn: func(context.Context, string) (string, error) { return out, nil }}
}

func failing(err error) *fakeFormatter {
	return &fakeFormatter{fn: func(context.Context, string) (string, error) { return "", err }}
}

type uploadEnv struct {
	srv *Server
	dir string
	f   *fakeFormatter
}

// newUploadEnv is a server on the test database with the API token
// "upload-token", the host "reader.example" and a fake formatter.
func newUploadEnv(t *testing.T, f *fakeFormatter, opts ...Option) *uploadEnv {
	t.Helper()
	cfg := testConfig(t)
	cfg.APIToken, cfg.Host = "upload-token", "reader.example"
	if f != nil {
		opts = append(opts, WithFormatter(f))
	}
	return &uploadEnv{srv: New(cfg, testdb.New(t), opts...), dir: cfg.FilesDir, f: f}
}

func (e *uploadEnv) children(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	return names
}

func (e *uploadEnv) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *uploadEnv) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (e *uploadEnv) served(t *testing.T, name string) bool {
	t.Helper()
	var n int
	err := e.srv.pool.QueryRow(context.Background(), `SELECT count(*) FROM served_files WHERE name = $1`, name).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func (e *uploadEnv) servedCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.srv.pool.QueryRow(context.Background(), `SELECT count(*) FROM served_files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func formReq(path string, v url.Values, auth string) *http.Request {
	req := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return req
}

func (e *uploadEnv) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

type part struct {
	field, filename, contentType string
	data                         []byte
}

func multipartReq(t *testing.T, path, auth string, fields map[string]string, files ...part) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="`+p.field+`"; filename="`+p.filename+`"`)
		ct := p.contentType
		if ct == "" {
			ct = "text/markdown"
		}
		h.Set("Content-Type", ct)
		w, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(p.data)
	}
	_ = mw.Close()
	req := httptest.NewRequest("POST", path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return req
}

func filePart(name, content string) part {
	return part{field: "file", filename: name, data: []byte(content)}
}

func wantURL(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["url"] != want {
		t.Errorf("body = %v, want url %q", got, want)
	}
}

// ---- POST /file/new ----

// Ruby: "creates files with a bearer token and no session".
func TestCreateWithBearerAndNoSession(t *testing.T) {
	e := newUploadEnv(t, returning("formatted"))
	rec := e.serve(formReq("/file/new", url.Values{"content": {"# Hi"}, "filename": {"hi.md"}}, bearer))
	wantURL(t, rec, "https://reader.example/hi.md")
	if got := e.read(t, "hi.md"); got != "formatted" {
		t.Errorf("file = %q", got)
	}
}

// Ruby: "rejects uploads without a configured bearer token".
func TestCreateRejectsWhenNoTokenIsConfigured(t *testing.T) {
	cfg := testConfig(t)
	cfg.APIToken = ""
	f := returning("x")
	srv := New(cfg, testdb.New(t), WithFormatter(f))
	for _, auth := range []string{"", "Bearer ", "Bearer", "bearer "} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, formReq("/file/new", url.Values{"content": {"hello"}, "filename": {"note"}}, auth))
		wantDetail(t, "auth "+auth, rec, 401, "Unauthorized")
	}
	if len(f.calls) != 0 {
		t.Error("the formatter ran without authorization")
	}
}

// Ruby: "rejects uploads with the wrong bearer token".
func TestCreateRejectsTheWrongToken(t *testing.T) {
	e := newUploadEnv(t, returning("x"))
	for _, auth := range []string{"Bearer wrong-token", "Bearer upload-token ", "upload-token", "Basic upload-token", ""} {
		rec := e.serve(formReq("/file/new", url.Values{"content": {"hello"}, "filename": {"note"}}, auth))
		wantDetail(t, "auth "+auth, rec, 401, "Unauthorized")
	}
	if got := e.children(t); len(got) != 0 {
		t.Errorf("files created: %v", got)
	}
	if len(e.f.calls) != 0 {
		t.Error("the formatter ran without authorization")
	}
}

// The session cookie is not an alternative to the bearer token on these
// endpoints (Ruby: skip_before_action :authenticate_user!, authenticated? checks
// the header only).
func TestCreateIgnoresTheSessionCookie(t *testing.T) {
	env := newAuthEnv(t)
	env.srv.cfg.APIToken = "upload-token"
	req := formReq("/file/new", url.Values{"content": {"x"}, "filename": {"a"}}, "")
	req.Header.Set("Cookie", env.cookieFor(env.user))
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	wantDetail(t, "cookie only", rec, 401, "Unauthorized")
}

// Ruby: "formats and saves an authenticated upload with a unique safe filename".
func TestCreateFormatsAndSavesWithAUniqueSafeName(t *testing.T) {
	e := newUploadEnv(t, returning("formatted markdown"))
	e.write(t, "note.md", "existing")
	rec := e.serve(formReq("/file/new", url.Values{"content": {"source text"}, "filename": {"../nested/note.txt"}}, bearer))
	wantURL(t, rec, "https://reader.example/note-1.md")
	if len(e.f.calls) != 1 || e.f.calls[0] != "source text" {
		t.Errorf("formatter got %q", e.f.calls)
	}
	if got := e.read(t, "note-1.md"); got != "formatted markdown" {
		t.Errorf("note-1.md = %q", got)
	}
	if got := e.read(t, "note.md"); got != "existing" {
		t.Errorf("note.md was changed: %q", got)
	}
	if len(e.children(t)) != 2 {
		t.Errorf("children = %v", e.children(t))
	}
}

// Ruby: "records the uploaded file in served_files so /last finds it immediately".
func TestCreateRecordsTheServedFile(t *testing.T) {
	e := newUploadEnv(t, returning("formatted markdown"))
	rec := e.serve(formReq("/file/new", url.Values{"content": {"source text"}, "filename": {"fresh-note.md"}}, bearer))
	wantURL(t, rec, "https://reader.example/fresh-note.md")
	if !e.served(t, "fresh-note.md") || e.servedCount(t) != 1 {
		t.Errorf("served_files rows: fresh-note.md=%v count=%d", e.served(t, "fresh-note.md"), e.servedCount(t))
	}
	newest, err := store.NewestServedFile(context.Background(), e.srv.pool)
	if err != nil || newest == nil || newest.Name != "fresh-note.md" {
		t.Errorf("newest = %+v, %v", newest, err)
	}
}

// Ruby: "returns a generic bad gateway response when OpenAI fails".
func TestCreateOpenAIFailureIsAGenericBadGateway(t *testing.T) {
	e := newUploadEnv(t, failing(llm.ErrOpenAI("sensitive upstream detail")))
	rec := e.serve(formReq("/file/new", url.Values{"content": {"source text"}, "filename": {"note"}}, bearer))
	wantDetail(t, "openai failure", rec, 502, "OpenAI formatting failed.")
	if strings.Contains(rec.Body.String(), "sensitive") {
		t.Error("the upstream message leaked")
	}
	if got := e.children(t); len(got) != 0 {
		t.Errorf("files created: %v", got)
	}
	if e.servedCount(t) != 0 {
		t.Error("a served_files row was recorded")
	}
}

// Ruby: ConfigurationError < OpenaiFormatter::Error, so a missing key is the
// same 502 with the same text.
func TestCreateMissingAPIKeyIsABadGateway(t *testing.T) {
	e := newUploadEnv(t, failing(llm.ErrConfiguration))
	rec := e.serve(formReq("/file/new", url.Values{"content": {"x"}, "filename": {"note"}}, bearer))
	wantDetail(t, "configuration", rec, 502, "OpenAI formatting failed.")
	if got := e.children(t); len(got) != 0 {
		t.Errorf("files created: %v", got)
	}
	// And the real default formatter, built from a config without keys.
	cfg := testConfig(t)
	cfg.APIToken = "upload-token"
	srv := New(cfg, testdb.New(t))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, formReq("/file/new", url.Values{"content": {"x"}, "filename": {"note"}}, bearer))
	wantDetail(t, "default formatter", rec, 502, "OpenAI formatting failed.")
}

func TestCreateUnexpectedFormatterErrorIsAnInternalError(t *testing.T) {
	e := newUploadEnv(t, failing(errors.New("boom secret")))
	rec := e.serve(formReq("/file/new", url.Values{"content": {"x"}, "filename": {"note"}}, bearer))
	wantDetail(t, "unexpected", rec, 500, "Internal server error.")
	if strings.Contains(rec.Body.String(), "secret") {
		t.Error("the error leaked")
	}
	if got := e.children(t); len(got) != 0 {
		t.Errorf("files created: %v", got)
	}
}

// The default formatter talks to OpenAI through the keys of the config.
func TestDefaultFormatterUsesTheConfiguredKey(t *testing.T) {
	cfg := testConfig(t)
	cfg.OpenAIKey, cfg.ExpansionLLMKey = "", "expansion-key"
	if got := newFormatter(cfg).Client.APIKey; got != "expansion-key" {
		t.Errorf("key = %q", got)
	}
	cfg.OpenAIKey = "openai-key"
	if got := newFormatter(cfg).Client.APIKey; got != "openai-key" {
		t.Errorf("key = %q", got)
	}
}

// Ruby: "rejects an empty upload filename" (and a missing one: nil.to_s).
func TestCreateRejectsAnEmptyOrMissingFilename(t *testing.T) {
	e := newUploadEnv(t, returning("x"))
	for _, v := range []url.Values{{"content": {"source"}, "filename": {""}}, {"content": {"source"}}, {"content": {"source"}, "filename": {".."}}} {
		rec := e.serve(formReq("/file/new", v, bearer))
		wantDetail(t, "filename", rec, 400, "Invalid filename.")
	}
	if len(e.f.calls) != 0 {
		t.Error("the formatter ran for an invalid filename")
	}
}

// Ruby: "rejects a created file whose name uses the reserved version suffix".
func TestCreateRejectsTheReservedVersionSuffix(t *testing.T) {
	e := newUploadEnv(t, returning("x"))
	rec := e.serve(formReq("/file/new", url.Values{"content": {"# Hi"}, "filename": {"report--v2.md"}}, bearer))
	wantDetail(t, "reserved", rec, 400, "Filenames may not use the reserved --v<number> suffix.")
	if got := e.children(t); len(got) != 0 {
		t.Errorf("files created: %v", got)
	}
	if len(e.f.calls) != 0 {
		t.Error("the formatter ran for an invalid filename")
	}
}

// Ruby: "allows filenames that merely resemble the reserved suffix".
func TestCreateAllowsNamesResemblingTheReservedSuffix(t *testing.T) {
	e := newUploadEnv(t, returning("x"))
	rec := e.serve(formReq("/file/new", url.Values{"content": {"# Hi"}, "filename": {"report-v2.md"}}, bearer))
	wantURL(t, rec, "https://reader.example/report-v2.md")
}

func TestCreateMissingContent(t *testing.T) {
	e := newUploadEnv(t, returning("x"))
	for name, req := range map[string]*http.Request{
		"no params": formReq("/file/new", url.Values{"filename": {"a.md"}}, bearer),
		"json null": jsonReq("/file/new", `{"content":null,"filename":"a.md"}`, bearer),
		"json none": jsonReq("/file/new", `{"filename":"a.md"}`, bearer),
		"no body":   httptest.NewRequest("POST", "/file/new", nil),
	} {
		req.Header.Set("Authorization", bearer)
		wantDetail(t, name, e.serve(req), 400, "Missing content.")
	}
	if len(e.f.calls) != 0 {
		t.Error("the formatter ran without content")
	}
}

func jsonReq(path, body, auth string) *http.Request {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return req
}

func TestCreateAcceptsJSONQueryAndEmptyContent(t *testing.T) {
	e := newUploadEnv(t, returning("formatted"))
	wantURL(t, e.serve(jsonReq("/file/new", `{"content":"a","filename":"j.md"}`, bearer)), "https://reader.example/j.md")
	// The query string wins over the body, as in Rails.
	wantURL(t, e.serve(jsonReq("/file/new?filename=q", `{"content":"b","filename":"ignored"}`, bearer)), "https://reader.example/q.md")
	// Empty content is content (Ruby only rejects nil).
	wantURL(t, e.serve(formReq("/file/new", url.Values{"content": {""}, "filename": {"empty"}}, bearer)), "https://reader.example/empty.md")
	if got := strings.Join(e.f.calls, "|"); got != "a|b|" {
		t.Errorf("formatter calls = %q", got)
	}
}

func TestCreateMalformedJSONIsBadRequest(t *testing.T) {
	e := newUploadEnv(t, returning("x"))
	wantDetail(t, "bad json", e.serve(jsonReq("/file/new", `{"content":`, bearer)), 400, "Invalid request body.")
}

func TestCreateBodyIsCapped(t *testing.T) {
	e := newUploadEnv(t, returning("x"))
	e.srv.maxCreateBytes = 1 << 10
	big := url.Values{"content": {strings.Repeat("a", 2<<10)}, "filename": {"big"}}
	wantDetail(t, "too big", e.serve(formReq("/file/new", big, bearer)), 413, "Request body is too large.")
	if len(e.f.calls) != 0 || len(e.children(t)) != 0 {
		t.Error("a too-large body was processed")
	}
	small := url.Values{"content": {strings.Repeat("a", 100)}, "filename": {"small"}}
	wantURL(t, e.serve(formReq("/file/new", small, bearer)), "https://reader.example/small.md")
	if e.srv.maxCreateBytes != 1<<10 || New(testConfig(t), nil).maxCreateBytes != 16<<20 {
		t.Error("default create cap is not 16 MiB")
	}
}

// The formatter is called with the request's context, so a client that goes away
// stops the OpenAI call, and nothing is created.
func TestCreatePassesTheRequestContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeFormatter{fn: func(ctx context.Context, _ string) (string, error) {
		cancel()
		<-ctx.Done()
		return "", llm.ErrOpenAI("OpenAI formatting failed.")
	}}
	e := newUploadEnv(t, f)
	req := formReq("/file/new", url.Values{"content": {"x"}, "filename": {"a"}}, bearer).WithContext(ctx)
	rec := e.serve(req)
	if rec.Code != 502 {
		t.Errorf("status = %d", rec.Code)
	}
	if len(e.children(t)) != 0 {
		t.Error("a file was created")
	}
}

// Concurrent creates of one name never share a file (Ruby had a race here).
func TestCreateConcurrentSameNameGetsDistinctFiles(t *testing.T) {
	e := newUploadEnv(t, returning("body"))
	const n = 8
	urls := make(chan string, n)
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			rec := e.serve(formReq("/file/new", url.Values{"content": {"x"}, "filename": {"same"}}, bearer))
			var got map[string]string
			_ = json.Unmarshal(rec.Body.Bytes(), &got)
			urls <- got["url"]
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	close(urls)
	seen := map[string]bool{}
	for u := range urls {
		if u == "" || seen[u] {
			t.Errorf("url %q empty or repeated", u)
		}
		seen[u] = true
	}
	if len(e.children(t)) != n || e.servedCount(t) != n {
		t.Errorf("files = %v, rows = %d", e.children(t), e.servedCount(t))
	}
}

// ---- POST /file/upload ----

// Ruby: "rejects an unauthenticated file upload".
func TestUploadRequiresTheBearerToken(t *testing.T) {
	e := newUploadEnv(t, returning("x"))
	for _, auth := range []string{"", "Bearer nope"} {
		rec := e.serve(multipartReq(t, "/file/upload", auth, nil, filePart("note.md", "# Hi")))
		wantDetail(t, "auth "+auth, rec, 401, "Unauthorized")
	}
	if got := e.children(t); len(got) != 0 {
		t.Errorf("files created: %v", got)
	}
}

// Ruby: "stores an uploaded markdown file verbatim without formatting".
func TestUploadStoresVerbatimWithoutFormatting(t *testing.T) {
	e := newUploadEnv(t, &fakeFormatter{fn: func(context.Context, string) (string, error) {
		t.Error("formatter must not be called")
		return "", nil
	}})
	e.write(t, "note.md", "existing")
	req := multipartReq(t, "/file/upload", bearer, map[string]string{"filename": "../nested/note.markdown"}, filePart("note.md", "# Hi\n"))
	wantURL(t, e.serve(req), "https://reader.example/note-1.md")
	if got := e.read(t, "note-1.md"); got != "# Hi\n" {
		t.Errorf("note-1.md = %q", got)
	}
	if !e.served(t, "note-1.md") {
		t.Error("no served_files row for note-1.md")
	}
	if e.servedCount(t) != 1 {
		t.Errorf("rows = %d, want only the new file", e.servedCount(t))
	}
}

// Ruby: "keeps the html extension for uploaded html files" plus the extension map.
func TestUploadExtensionMap(t *testing.T) {
	cases := []struct{ upload, filename, want string }{
		{"page.html", "", "page.html"},
		{"page.htm", "", "page.html"},
		{"PAGE.HTM", "", "PAGE.html"},
		{"note.md", "", "note.md"},
		{"note.markdown", "", "note.md"},
		{"x.md", "renamed.htm", "renamed.html"},
		{"x.md", "  ", "x.md"},
		{"ignored.txt", "dir/ok.markdown", "ok.md"},
		{"x.md", `..\evil.md`, "evil.md"},
		{`..\evil.md`, "", "evil.md"},
	}
	for _, c := range cases {
		e := newUploadEnv(t, returning("x"))
		fields := map[string]string{}
		if c.filename != "" {
			fields["filename"] = c.filename
		}
		rec := e.serve(multipartReq(t, "/file/upload", bearer, fields, filePart(c.upload, "<main>Hi</main>")))
		wantURL(t, rec, "https://reader.example/"+c.want)
		if got := e.read(t, c.want); got != "<main>Hi</main>" {
			t.Errorf("%+v: content %q", c, got)
		}
		if !e.served(t, c.want) {
			t.Errorf("%+v: no served_files row", c)
		}
	}
}

// Ruby: "rejects uploaded files with an unsupported extension".
func TestUploadRejectsUnsupportedExtensions(t *testing.T) {
	for _, name := range []string{"note.txt", "note", "note.md.txt", ".md", "note.md."} {
		e := newUploadEnv(t, returning("x"))
		rec := e.serve(multipartReq(t, "/file/upload", bearer, nil, filePart(name, "plain")))
		wantDetail(t, name, rec, 400, "Only .html, .md, and .markdown files are supported.")
		if got := e.children(t); len(got) != 0 {
			t.Errorf("%s: files created: %v", name, got)
		}
	}
}

// Ruby: "rejects an upload without a file".
func TestUploadRejectsAMissingFile(t *testing.T) {
	e := newUploadEnv(t, returning("x"))
	for name, req := range map[string]*http.Request{
		"filename only": multipartReq(t, "/file/upload", bearer, map[string]string{"filename": "note.md"}),
		"form field":    multipartReq(t, "/file/upload", bearer, map[string]string{"file": "# text, not a file part"}),
		"urlencoded":    formReq("/file/upload", url.Values{"filename": {"note.md"}}, bearer),
		"json":          jsonReq("/file/upload", `{"file":"x"}`, bearer),
		"no body":       httptest.NewRequest("POST", "/file/upload", nil),
	} {
		req.Header.Set("Authorization", bearer)
		wantDetail(t, name, e.serve(req), 400, "Missing file.")
	}
	if got := e.children(t); len(got) != 0 {
		t.Errorf("files created: %v", got)
	}
}

// Ruby: "rejects uploaded files that are not valid UTF-8 text".
func TestUploadRejectsInvalidUTF8(t *testing.T) {
	e := newUploadEnv(t, returning("x"))
	rec := e.serve(multipartReq(t, "/file/upload", bearer, nil, part{field: "file", filename: "note.md", data: []byte("\xff\xfe binary")}))
	wantDetail(t, "utf-8", rec, 400, "File must be UTF-8 text.")
	if got := e.children(t); len(got) != 0 {
		t.Errorf("files created: %v", got)
	}
	if e.servedCount(t) != 0 {
		t.Error("a row was recorded")
	}
}

// Ruby's utf8_contents only checks valid_encoding?, so NUL bytes and an empty
// file are stored as they are.
func TestUploadAllowsNULBytesAndEmptyFiles(t *testing.T) {
	e := newUploadEnv(t, returning("x"))
	wantURL(t, e.serve(multipartReq(t, "/file/upload", bearer, nil, part{field: "file", filename: "nul.md", data: []byte("a\x00b é")})), "https://reader.example/nul.md")
	if got := e.read(t, "nul.md"); got != "a\x00b é" {
		t.Errorf("nul.md = %q", got)
	}
	wantURL(t, e.serve(multipartReq(t, "/file/upload", bearer, nil, filePart("empty.md", ""))), "https://reader.example/empty.md")
}

// Ruby: "rejects an uploaded file whose name uses the reserved version suffix".
func TestUploadRejectsTheReservedVersionSuffix(t *testing.T) {
	e := newUploadEnv(t, returning("x"))
	rec := e.serve(multipartReq(t, "/file/upload", bearer, nil, filePart("report--v10.md", "# Hi")))
	wantDetail(t, "reserved", rec, 400, "Filenames may not use the reserved --v<number> suffix.")
	if got := e.children(t); len(got) != 0 {
		t.Errorf("files created: %v", got)
	}
}

// Ruby checks the extension, then the name, then the contents.
func TestUploadErrorPrecedence(t *testing.T) {
	e := newUploadEnv(t, returning("x"))
	bad := []byte("\xff")
	rec := e.serve(multipartReq(t, "/file/upload", bearer, nil, part{field: "file", filename: "a--v2.md", data: bad}))
	wantDetail(t, "name before contents", rec, 400, "Filenames may not use the reserved --v<number> suffix.")
	rec = e.serve(multipartReq(t, "/file/upload", bearer, nil, part{field: "file", filename: "a--v2.txt", data: bad}))
	wantDetail(t, "extension before name", rec, 400, "Only .html, .md, and .markdown files are supported.")
}

// The query string wins over the multipart body, like Rails params.
func TestUploadFilenameFromTheQuery(t *testing.T) {
	e := newUploadEnv(t, returning("x"))
	req := multipartReq(t, "/file/upload?filename=q.md", bearer, map[string]string{"filename": "body.md"}, filePart("orig.md", "# Hi"))
	wantURL(t, e.serve(req), "https://reader.example/q.md")
}

func TestUploadBodyIsCapped(t *testing.T) {
	e := newUploadEnv(t, returning("x"))
	e.srv.maxUploadBytes = 4 << 10
	rec := e.serve(multipartReq(t, "/file/upload", bearer, nil, filePart("big.md", strings.Repeat("a", 8<<10))))
	wantDetail(t, "too big", rec, 413, "Request body is too large.")
	if got := e.children(t); len(got) != 0 {
		t.Errorf("files created: %v", got)
	}
	wantURL(t, e.serve(multipartReq(t, "/file/upload", bearer, nil, filePart("ok.md", strings.Repeat("a", 1<<10)))), "https://reader.example/ok.md")
	if New(testConfig(t), nil).maxUploadBytes != 64<<20 {
		t.Error("default upload cap is not 64 MiB")
	}
}

// Ruby: "serves an uploaded markdown file ..." / "an uploaded file becomes the
// newest file for /last": the created file is served and is the newest one.
func TestUploadedFileIsServedAndIsTheNewest(t *testing.T) {
	env := newAuthEnv(t)
	env.srv.cfg.APIToken = "upload-token"
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, multipartReq(t, "/file/upload", bearer, nil, filePart("newest.md", "# Newest\n")))
	if rec.Code != 200 {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	if got := env.get("/newest.md"); got.Code != 200 || !strings.Contains(got.Body.String(), "Newest") {
		t.Errorf("GET /newest.md = %d", got.Code)
	}
	last := env.get("/last")
	if last.Code != 302 || last.Header().Get("Location") != "/newest.md" {
		t.Errorf("GET /last = %d %q", last.Code, last.Header().Get("Location"))
	}
}

// ---- CSRF ----

// Ruby: skip_forgery_protection only: [:create, :upload]. Non-browser clients send
// neither Origin nor Sec-Fetch-Site and are let through; a cross-site browser
// request is refused before the handler.
func TestCreateAndUploadCrossOriginProtection(t *testing.T) {
	e := newUploadEnv(t, returning("formatted"))
	rec := e.serve(formReq("/file/new", url.Values{"content": {"a"}, "filename": {"a"}}, bearer))
	wantURL(t, rec, "https://reader.example/a.md")

	cross := formReq("/file/new", url.Values{"content": {"a"}, "filename": {"b"}}, bearer)
	cross.Header.Set("Sec-Fetch-Site", "cross-site")
	if rec := e.serve(cross); rec.Code != 403 {
		t.Errorf("cross-site /file/new = %d, want 403", rec.Code)
	}
	crossUp := multipartReq(t, "/file/upload", bearer, nil, filePart("b.md", "x"))
	crossUp.Header.Set("Sec-Fetch-Site", "cross-site")
	if rec := e.serve(crossUp); rec.Code != 403 {
		t.Errorf("cross-site /file/upload = %d, want 403", rec.Code)
	}
	if got := e.children(t); len(got) != 1 {
		t.Errorf("files = %v, want only a.md", got)
	}
}
