package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/store"
)

// Ports of test/controllers/scroll_positions_controller_test.rb, plus the
// bodies the browser really sends (sendBeacon, forms) and Rails' param merging.

// send is a signed-in request with a body.
func (e *authEnv) send(method, path, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Cookie", e.cookieFor(e.user))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

func (e *authEnv) anchor(t *testing.T, fileName string) string {
	t.Helper()
	a, err := store.FindScrollPosition(context.Background(), e.srv.pool, e.user.ID, fileName)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (e *authEnv) countPositions(t *testing.T, fileName string) int {
	t.Helper()
	var n int
	err := e.srv.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM scroll_positions WHERE user_id = $1 AND file_name = $2`, e.user.ID, fileName).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Ruby: "saves and updates a position for a file".
func TestScrollPositionSavesAndUpdates(t *testing.T) {
	env := newAuthEnv(t)
	rec := env.send("PATCH", "/scroll_position", "application/json", `{"file_name":"notes.md","anchor":"first-heading"}`)
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("PATCH: %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control %q", got)
	}
	if got := env.anchor(t, "notes.md"); got != "first-heading" {
		t.Errorf("anchor %q", got)
	}
	rec = env.send("POST", "/scroll_position", "application/json", `{"file_name":"notes.md","anchor":"second-heading"}`)
	if rec.Code != 204 {
		t.Fatalf("POST: %d", rec.Code)
	}
	if got, n := env.anchor(t, "notes.md"), env.countPositions(t, "notes.md"); got != "second-heading" || n != 1 {
		t.Errorf("anchor %q in %d rows", got, n)
	}
}

// expand.js: sendBeacon posts a Blob typed application/json (or a client may send
// text/plain), always with an empty authenticity_token that must be ignored.
func TestScrollPositionAcceptsBeaconBodies(t *testing.T) {
	for _, ct := range []string{"application/json", "application/json; charset=UTF-8", "text/plain;charset=UTF-8", "text/plain", ""} {
		env := newAuthEnv(t)
		body := `{"file_name":"notes.md","anchor":"intro","authenticity_token":""}`
		if rec := env.send("POST", "/scroll_position", ct, body); rec.Code != 204 {
			t.Errorf("Content-Type %q: got %d %q", ct, rec.Code, rec.Body.String())
		}
		if got := env.anchor(t, "notes.md"); got != "intro" {
			t.Errorf("Content-Type %q: anchor %q", ct, got)
		}
	}
	// Leading white space before the "{" still sniffs as JSON.
	env := newAuthEnv(t)
	if rec := env.send("POST", "/scroll_position", "text/plain", " \n"+`{"file_name":"a.md","anchor":"x","extra":[1,{"y":null}]}`); rec.Code != 204 {
		t.Errorf("padded body: %d", rec.Code)
	}
}

func TestScrollPositionAcceptsFormAndQueryParams(t *testing.T) {
	env := newAuthEnv(t)
	form := url.Values{"file_name": {"form.md"}, "anchor": {"from-form"}, "authenticity_token": {"x"}}.Encode()
	if rec := env.send("PATCH", "/scroll_position", "application/x-www-form-urlencoded", form); rec.Code != 204 {
		t.Fatalf("form: %d %q", rec.Code, rec.Body.String())
	}
	if got := env.anchor(t, "form.md"); got != "from-form" {
		t.Errorf("form anchor %q", got)
	}

	// The query string counts too, and wins over the body, like Rails' params.
	rec := env.send("POST", "/scroll_position?file_name=q.md&anchor=from-query", "application/json", `{"file_name":"body.md","anchor":"from-body"}`)
	if rec.Code != 204 || env.anchor(t, "q.md") != "from-query" || env.anchor(t, "body.md") != "" {
		t.Errorf("query: %d, q.md %q, body.md %q", rec.Code, env.anchor(t, "q.md"), env.anchor(t, "body.md"))
	}
	// Params can be split between query and body.
	rec = env.send("POST", "/scroll_position?anchor=split", "application/json", `{"file_name":"split.md"}`)
	if rec.Code != 204 || env.anchor(t, "split.md") != "split" {
		t.Errorf("split: %d %q", rec.Code, env.anchor(t, "split.md"))
	}
	// Repeated keys: the last wins.
	rec = env.send("POST", "/scroll_position", "application/x-www-form-urlencoded", "file_name=r.md&anchor=one&anchor=two")
	if rec.Code != 204 || env.anchor(t, "r.md") != "two" {
		t.Errorf("repeated: %d %q", rec.Code, env.anchor(t, "r.md"))
	}
	// Multipart.
	mp := "--B\r\nContent-Disposition: form-data; name=\"file_name\"\r\n\r\nmp.md\r\n--B\r\nContent-Disposition: form-data; name=\"anchor\"\r\n\r\nmpa\r\n--B--\r\n"
	if rec := env.send("POST", "/scroll_position", "multipart/form-data; boundary=B", mp); rec.Code != 204 || env.anchor(t, "mp.md") != "mpa" {
		t.Errorf("multipart: %d %q", rec.Code, env.anchor(t, "mp.md"))
	}
}

// Ruby: "returns 400 for missing file name or anchor".
func TestScrollPositionMissingFields(t *testing.T) {
	env := newAuthEnv(t)
	for _, tc := range []struct{ body, detail string }{
		{`{"anchor":"heading"}`, "Missing file_name."},
		{`{"file_name":"notes.md"}`, "Missing anchor."},
		{`{"file_name":"  ","anchor":"heading"}`, "Missing file_name."},
		{`{"file_name":"notes.md","anchor":" \t"}`, "Missing anchor."},
		{`{"file_name":null,"anchor":"a"}`, "Missing file_name."},
		{`{"file_name":"notes.md","anchor":null}`, "Missing anchor."},
		{`{"file_name":"","anchor":""}`, "Missing file_name."},
		{`{}`, "Missing file_name."},
		{`[1,2]`, "Missing file_name."},
		{``, "Missing file_name."},
	} {
		wantDetail(t, tc.body, env.send("PATCH", "/scroll_position", "application/json", tc.body), 400, tc.detail)
	}
	wantDetail(t, "malformed", env.send("PATCH", "/scroll_position", "application/json", `{"file_name":`), 400, "Invalid request body.")
	wantDetail(t, "sniffed malformed", env.send("PATCH", "/scroll_position", "text/plain", `{nope`), 400, "Invalid request body.")
	if n := env.countPositions(t, "notes.md"); n != 0 {
		t.Errorf("%d rows stored", n)
	}
}

// Ruby: "returns 422 for an unsafe anchor".
func TestScrollPositionRejectsUnsafeAnchor(t *testing.T) {
	env := newAuthEnv(t)
	for _, anchor := range []string{"</script>", "a b", "a\n", `"quoted"`, "ünï", "a#b", "[1]"} {
		body := `{"file_name":"notes.md","anchor":` + jsonString(anchor) + `}`
		wantDetail(t, anchor, env.send("PATCH", "/scroll_position", "application/json", body), 422, "Anchor is invalid")
	}
	// Non-string JSON values are read like Ruby's to_s: an array is no anchor, a number is.
	wantDetail(t, "array", env.send("PATCH", "/scroll_position", "application/json", `{"file_name":"notes.md","anchor":["a"]}`), 422, "Anchor is invalid")
	if n := env.countPositions(t, "notes.md"); n != 0 {
		t.Errorf("%d rows stored", n)
	}
	if rec := env.send("PATCH", "/scroll_position", "application/json", `{"file_name":"notes.md","anchor":5}`); rec.Code != 204 || env.anchor(t, "notes.md") != "5" {
		t.Errorf("numeric anchor: %d %q", rec.Code, env.anchor(t, "notes.md"))
	}
}

func jsonString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

// Ruby validates only the shape: the file need not exist, and a name Postgres
// cannot store is rejected (Rails would answer 500).
func TestScrollPositionFileNameIsNotChecked(t *testing.T) {
	env := newAuthEnv(t)
	if rec := env.send("PATCH", "/scroll_position", "application/json", `{"file_name":"nowhere.md","anchor":"a"}`); rec.Code != 204 {
		t.Errorf("unknown file: %d", rec.Code)
	}
	if rec := env.send("PATCH", "/scroll_position", "application/json", `{"file_name":"my notes ✓.md","anchor":"a"}`); rec.Code != 204 || env.anchor(t, "my notes ✓.md") != "a" {
		t.Errorf("unicode name: %d", rec.Code)
	}
	wantDetail(t, "NUL", env.send("PATCH", "/scroll_position", "application/json", `{"file_name":"a\u0000b","anchor":"a"}`), 422, "File name is invalid")
}

// Ruby: "requires authentication".
func TestScrollPositionRequiresSignIn(t *testing.T) {
	env := newAuthEnv(t)
	req := httptest.NewRequest("PATCH", "/scroll_position", strings.NewReader(`{"file_name":"notes.md","anchor":"heading"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("got %d", rec.Code)
	}
	if env.countPositions(t, "notes.md") != 0 {
		t.Error("position stored by an anonymous request")
	}
}

func TestScrollPositionBodyLimit(t *testing.T) {
	env := newAuthEnv(t)
	body := `{"file_name":"notes.md","anchor":"` + strings.Repeat("a", maxScrollBodyBytes) + `"}`
	wantDetail(t, "oversized", env.send("PATCH", "/scroll_position", "application/json", body), 413, "Request body is too large.")
	if n := env.countPositions(t, "notes.md"); n != 0 {
		t.Errorf("%d rows stored", n)
	}
}

func TestScrollPositionFailsOnDatabaseError(t *testing.T) {
	env := newAuthEnv(t)
	env.srv.pool.Close()
	req := httptest.NewRequest("PATCH", "/scroll_position", strings.NewReader(`{"file_name":"notes.md","anchor":"a"}`))
	req = req.WithContext(auth.WithUser(req.Context(), env.user))
	rec := httptest.NewRecorder()
	env.srv.updateScrollPosition(rec, req)
	wantDetail(t, "db down", rec, http.StatusInternalServerError, "Internal server error.")
}

// A user who is gone (deleted after the session check) is not a validation error.
func TestScrollPositionForUnknownUser(t *testing.T) {
	env := newAuthEnv(t)
	req := httptest.NewRequest("PATCH", "/scroll_position", strings.NewReader(`{"file_name":"notes.md","anchor":"a"}`))
	req = req.WithContext(auth.WithUser(req.Context(), &store.User{ID: 999999}))
	rec := httptest.NewRecorder()
	env.srv.updateScrollPosition(rec, req)
	wantDetail(t, "unknown user", rec, 500, "Internal server error.")
}
