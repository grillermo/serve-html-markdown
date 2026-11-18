package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/expansion"
	"serve-html-markdown/internal/jobs"
	"serve-html-markdown/internal/llm"
	"serve-html-markdown/internal/store"
)

type enqueued struct {
	kind string
	args jobs.Args
	wait time.Duration
}

type fakeEnqueuer struct {
	mu   sync.Mutex
	got  []enqueued
	err  error
	onGo func() // runs inside Enqueue (to check ordering)
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, kind string, args jobs.Args, wait time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onGo != nil {
		f.onGo()
	}
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, enqueued{kind, args, wait})
	return nil
}

type expEnv struct {
	*authEnv
	enq  *fakeEnqueuer
	h    map[string]string
	dir  string
	pool interface{} // unused; keeps the struct explicit
}

func newExpEnv(t *testing.T) *expEnv {
	t.Helper()
	enq := &fakeEnqueuer{}
	env := &authEnv{now: new(time.Time)}
	*env.now = fixedNow
	cfg := testConfig(t)
	env.srv = New(cfg, testdb.New(t), WithClock(func() time.Time { return *env.now }), WithEnqueuer(enq))
	env.user = env.addUser(t, viewerEmail, viewerPassword)
	return &expEnv{authEnv: env, enq: enq, h: map[string]string{"Cookie": env.cookieFor(env.user), "Content-Type": "application/json"}, dir: cfg.FilesDir}
}

func (e *expEnv) post(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/expansions", strings.NewReader(body))
	for k, v := range e.h {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

func (e *expEnv) get(path string) *httptest.ResponseRecorder {
	return do(e.srv, "GET", path, map[string]string{"Cookie": e.h["Cookie"], "Accept": "application/json"})
}

func (e *expEnv) load(t *testing.T, id int64) *store.Expansion {
	t.Helper()
	x, err := store.FindExpansion(context.Background(), e.srv.pool, id)
	if err != nil || x == nil {
		t.Fatalf("expansion %d: %v", id, err)
	}
	return x
}

const validBody = `{"file_name":"notes.md","selected_text":"beta","occurrence":0,"question":"why?"}`

func bodyWith(t *testing.T, over map[string]any) string {
	t.Helper()
	m := map[string]any{"file_name": "notes.md", "selected_text": "beta", "occurrence": 0, "question": "why?"}
	for k, v := range over {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return m
}

func idOf(t *testing.T, rec *httptest.ResponseRecorder) int64 {
	t.Helper()
	return int64(decode(t, rec)["id"].(float64))
}

// Ruby: "creates a pending job and enqueues it without running the expander".
func TestCreateExpansionEnqueuesAPendingJob(t *testing.T) {
	e := newExpEnv(t)
	rec := e.post(bodyWith(t, map[string]any{"use_openai": true, "authenticity_token": ""}))
	if rec.Code != 202 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	id := idOf(t, rec)
	if want := `{"id":` + itoa(id) + `,"status":"pending"}`; rec.Body.String() != want {
		t.Errorf("body %q, want %q", rec.Body.String(), want)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type %q", got)
	}
	x := e.load(t, id)
	if x.FileName != "notes.md" || x.SelectedText != "beta" || x.Occurrence != 0 || x.Question != "why?" ||
		!x.UseOpenAI || x.Status != "pending" || x.Mode != "create_new" || x.UserID != e.user.ID {
		t.Errorf("row %+v", x)
	}
	if len(e.enq.got) != 1 || e.enq.got[0] != (enqueued{jobs.KindGenerateExpansion, jobs.Args{ID: id}, 0}) {
		t.Errorf("enqueued %+v", e.enq.got)
	}
}

func TestCreateExpansionWithTheRealEnqueuerWritesAGoJob(t *testing.T) {
	cfg := testConfig(t)
	env := &authEnv{now: new(time.Time)}
	*env.now = fixedNow
	env.srv = New(cfg, testdb.New(t), WithClock(func() time.Time { return *env.now }))
	env.user = env.addUser(t, viewerEmail, viewerPassword)
	e := &expEnv{authEnv: env, h: map[string]string{"Cookie": env.cookieFor(env.user), "Content-Type": "application/json"}}
	rec := e.post(validBody)
	if rec.Code != 202 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	id := idOf(t, rec)
	var kind, args string
	var runDelay float64
	err := env.srv.pool.QueryRow(context.Background(),
		`SELECT kind, args::text, extract(epoch FROM run_at - now()) FROM go_jobs`).Scan(&kind, &args, &runDelay)
	if err != nil {
		t.Fatal(err)
	}
	var a jobs.Args
	_ = json.Unmarshal([]byte(args), &a)
	if kind != "generate_expansion" || a.ID != id || runDelay > 1 {
		t.Errorf("go_jobs row: kind %q args %s delay %v", kind, args, runDelay)
	}
}

// Ruby: "stamps client_clicked ... and request_received unconditionally".
func TestCreateExpansionStampsTimings(t *testing.T) {
	e := newExpEnv(t)
	clicked := int64(1_700_000_000_000)
	rec := e.post(bodyWith(t, map[string]any{"client_clicked_at": clicked}))
	x := e.load(t, idOf(t, rec))
	if x.Timings["client_clicked"] != clicked || x.Timings["request_received"] == 0 || x.Timings["job_enqueued"] == 0 {
		t.Errorf("timings %v", x.Timings)
	}
	if x.Timings["request_received"] > x.Timings["job_enqueued"] {
		t.Errorf("request_received after job_enqueued: %v", x.Timings)
	}
	// As a string, like a form post.
	rec = e.post(bodyWith(t, map[string]any{"client_clicked_at": "1700000000123"}))
	if got := e.load(t, idOf(t, rec)).Timings["client_clicked"]; got != 1_700_000_000_123 {
		t.Errorf("string client_clicked_at stamped %d", got)
	}
}

// Ruby: "omits client_clicked when client_clicked_at is not sent"; garbage is ignored too.
func TestCreateExpansionOmitsClientClickedWhenAbsentOrGarbage(t *testing.T) {
	e := newExpEnv(t)
	for _, v := range []any{nil, "", "garbage", "-5", false} {
		rec := e.post(bodyWith(t, map[string]any{"client_clicked_at": v}))
		x := e.load(t, idOf(t, rec))
		if _, ok := x.Timings["client_clicked"]; ok {
			t.Errorf("client_clicked_at %v: stamped %v", v, x.Timings)
		}
		if x.Timings["request_received"] == 0 {
			t.Errorf("request_received missing: %v", x.Timings)
		}
	}
}

func TestCreateExpansionJobEnqueuedIsStampedAfterEnqueue(t *testing.T) {
	e := newExpEnv(t)
	e.enq.onGo = func() {
		var ids []int64
		rows, _ := e.srv.pool.Query(context.Background(), `SELECT id FROM expansions`)
		for rows.Next() {
			var id int64
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		if len(ids) != 1 || e.load(t, ids[0]).Timings["job_enqueued"] != 0 {
			t.Errorf("job_enqueued must be stamped after the enqueue")
		}
	}
	e.post(validBody)
}

// Ruby: "returns 400 when selected_text or question is missing", "...file_name is blank or missing".
func TestCreateExpansionMissingFields(t *testing.T) {
	e := newExpEnv(t)
	for name, over := range map[string]map[string]any{
		"blank selection":     {"selected_text": ""},
		"whitespace question": {"question": "  \n"},
		"missing question":    {"question": nil},
		"blank file":          {"file_name": ""},
		"missing file":        {"file_name": nil},
		"null selection":      {"selected_text": nil},
	} {
		rec := e.post(bodyWith(t, over))
		if rec.Code != 400 || strings.TrimSpace(rec.Body.String()) != `{"detail":"Missing file_name, selected_text, or question."}` {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := e.post(``); rec.Code != 400 {
		t.Errorf("empty body: %d", rec.Code)
	}
	if len(e.enq.got) != 0 || countRows(t, e, "expansions") != 0 {
		t.Error("nothing may be created or enqueued")
	}
}

func countRows(t *testing.T, e *expEnv, table string) int {
	t.Helper()
	var n int
	if err := e.srv.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Ruby: "returns 400 for an unknown mode".
func TestCreateExpansionUnknownMode(t *testing.T) {
	e := newExpEnv(t)
	rec := e.post(bodyWith(t, map[string]any{"mode": "nonsense"}))
	if rec.Code != 400 || strings.TrimSpace(rec.Body.String()) != `{"detail":"Unknown mode."}` {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if countRows(t, e, "expansions") != 0 {
		t.Error("no row for an unknown mode")
	}
}

// Ruby: "stores the requested mode and fallback anchor" and "remembers the submitted mode on the user".
func TestCreateExpansionStoresModeAndFallbackAndRemembersMode(t *testing.T) {
	e := newExpEnv(t)
	rec := e.post(bodyWith(t, map[string]any{"mode": "edit_in_place", "fallback_anchor": "section-two"}))
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	x := e.load(t, idOf(t, rec))
	if x.Mode != "edit_in_place" || x.FallbackAnchor == nil || *x.FallbackAnchor != "section-two" {
		t.Errorf("row %+v", x)
	}
	u, err := store.FindUserByID(context.Background(), e.srv.pool, e.user.ID)
	if err != nil || u.ExpansionMode != "edit_in_place" {
		t.Errorf("user %+v %v", u, err)
	}
	// A blank mode means create_new (Ruby's presence ||), and is remembered too.
	rec = e.post(bodyWith(t, map[string]any{"mode": "  ", "fallback_anchor": ""}))
	x = e.load(t, idOf(t, rec))
	if x.Mode != "create_new" || x.FallbackAnchor != nil {
		t.Errorf("row %+v", x)
	}
	if u, _ := store.FindUserByID(context.Background(), e.srv.pool, e.user.ID); u.ExpansionMode != "create_new" {
		t.Errorf("user mode %q", u.ExpansionMode)
	}
}

func TestCreateExpansionOccurrence(t *testing.T) {
	e := newExpEnv(t)
	for _, c := range []struct {
		in   any
		want int
	}{
		{-3, 0}, {"-3", 0}, {2, 2}, {"2", 2}, {"2abc", 2}, {2.7, 2}, {"abc", 0}, {nil, 0}, {"", 0},
		{"99999999999999999999", 2147483647}, {5000000000, 2147483647}, {"1_000", 1000},
	} {
		rec := e.post(bodyWith(t, map[string]any{"occurrence": c.in}))
		if rec.Code != 202 {
			t.Errorf("occurrence %v: %d %s", c.in, rec.Code, rec.Body)
			continue
		}
		if got := e.load(t, idOf(t, rec)).Occurrence; got != c.want {
			t.Errorf("occurrence %v stored as %d, want %d", c.in, got, c.want)
		}
	}
}

func TestCreateExpansionUseOpenAI(t *testing.T) {
	e := newExpEnv(t)
	for _, c := range []struct {
		in   any
		want bool
	}{
		{true, true}, {"true", true}, {"1", true}, {1, true}, {"on", true}, {"yes", true},
		{false, false}, {"false", false}, {"0", false}, {0, false}, {"off", false}, {"f", false}, {"", false}, {nil, false},
	} {
		rec := e.post(bodyWith(t, map[string]any{"use_openai": c.in}))
		if got := e.load(t, idOf(t, rec)).UseOpenAI; got != c.want {
			t.Errorf("use_openai %#v stored as %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCreateExpansionAcceptsFormAndQueryParams(t *testing.T) {
	e := newExpEnv(t)
	req := httptest.NewRequest("POST", "/expansions?question=why%3F",
		strings.NewReader("file_name=notes.md&selected_text=beta&occurrence=1&use_openai=1&mode=edit_in_place"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", e.h["Cookie"])
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	x := e.load(t, idOf(t, rec))
	if x.Question != "why?" || x.Occurrence != 1 || !x.UseOpenAI || x.Mode != "edit_in_place" {
		t.Errorf("row %+v", x)
	}
}

func TestCreateExpansionLimitsAndBadText(t *testing.T) {
	e := newExpEnv(t)
	for name, c := range map[string]struct {
		over   map[string]any
		status int
		detail string
	}{
		"selection too long": {map[string]any{"selected_text": strings.Repeat("a", expansion.MaxSelectedTextBytes+1)}, 400, "Selected text is too long."},
		"question too long":  {map[string]any{"question": strings.Repeat("a", MaxQuestionBytes+1)}, 400, "Question is too long."},
		"file name too long": {map[string]any{"file_name": strings.Repeat("a", MaxExpansionFileNameBytes+1)}, 400, "File name is too long."},
		"NUL in file name":   {map[string]any{"file_name": "a\u0000.md"}, 400, msgInvalidExpansionText},
		"NUL in selection":   {map[string]any{"selected_text": "a\u0000b"}, 400, msgInvalidExpansionText},
		"NUL in anchor":      {map[string]any{"fallback_anchor": "a\u0000b"}, 400, msgInvalidExpansionText},
	} {
		rec := e.post(bodyWith(t, c.over))
		if rec.Code != c.status || decode(t, rec)["detail"] != c.detail {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// Invalid UTF-8 inside a form value.
	req := httptest.NewRequest("POST", "/expansions", strings.NewReader("file_name=%ff.md&selected_text=beta&question=why"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", e.h["Cookie"])
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("invalid utf-8: %d %s", rec.Code, rec.Body)
	}
	// A selection exactly at the cap is accepted.
	if rec := e.post(bodyWith(t, map[string]any{"selected_text": strings.Repeat("a", expansion.MaxSelectedTextBytes)})); rec.Code != 202 {
		t.Errorf("selection at the cap: %d", rec.Code)
	}
	if rec := e.post(strings.Repeat("x", MaxExpansionBodyBytes+1)); rec.Code != 413 {
		t.Errorf("huge body: %d", rec.Code)
	}
	if rec := e.post(`{"file_name":`); rec.Code != 400 {
		t.Errorf("malformed json: %d", rec.Code)
	}
}

func TestCreateExpansionEnqueueFailureFailsTheExpansion(t *testing.T) {
	e := newExpEnv(t)
	e.enq.err = errors.New("db down: secret-host")
	rec := e.post(validBody)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret-host") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var x *store.Expansion
	rows, _ := e.srv.pool.Query(context.Background(), `SELECT id FROM expansions`)
	var id int64
	for rows.Next() {
		_ = rows.Scan(&id)
	}
	rows.Close()
	x = e.load(t, id)
	if x.Status != "failed" || x.ErrorDetail == nil || *x.ErrorDetail != "Expansion failed." {
		t.Errorf("row %+v", x)
	}
}

// Ruby: "requires authentication".
func TestExpansionsRequireAuthentication(t *testing.T) {
	e := newExpEnv(t)
	delete(e.h, "Cookie")
	rec := e.post(validBody)
	if rec.Code != 302 && rec.Code != 401 {
		t.Errorf("anonymous POST: %d", rec.Code)
	}
	e.h["Accept"] = "application/json"
	if rec := e.post(validBody); rec.Code != 401 {
		t.Errorf("anonymous JSON POST: %d", rec.Code)
	}
	if len(e.enq.got) != 0 || countRows(t, e, "expansions") != 0 {
		t.Error("nothing may be created")
	}
}

// Ruby: "returns the current user's status and only terminal fields".
func TestShowExpansionStatusPayloads(t *testing.T) {
	e := newExpEnv(t)
	ctx := context.Background()
	x, err := store.CreateExpansion(ctx, e.srv.pool, store.NewExpansion{UserID: e.user.ID, FileName: "notes.md", SelectedText: "beta", Question: "why?"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/expansions/" + itoa(x.ID)
	check := func(want string) {
		t.Helper()
		rec := e.get(path)
		if rec.Code != 200 || rec.Body.String() != want {
			t.Errorf("%d %q, want %q", rec.Code, rec.Body, want)
		}
	}
	check(`{"id":` + itoa(x.ID) + `,"status":"pending"}`)
	if _, err := store.ClaimExpansion(ctx, e.srv.pool, x.ID); err != nil {
		t.Fatal(err)
	}
	check(`{"id":` + itoa(x.ID) + `,"status":"processing"}`)
	if err := store.CompleteExpansion(ctx, e.srv.pool, x.ID, "/notes--expand-1.html"); err != nil {
		t.Fatal(err)
	}
	check(`{"id":` + itoa(x.ID) + `,"status":"completed","url":"/notes--expand-1.html"}`)
	if err := store.FailExpansion(ctx, e.srv.pool, x.ID, `Selection <not> "found"`); err != nil {
		t.Fatal(err)
	}
	check(`{"id":` + itoa(x.ID) + `,"status":"failed","detail":"Selection <not> \"found\""}`)
}

// Ruby: "does not reveal another user's job or missing jobs".
func TestShowExpansionIsNotFoundForOthersMissingAndHugeIDs(t *testing.T) {
	e := newExpEnv(t)
	other := e.addUser(t, "other@example.com", "another-password")
	x, err := store.CreateExpansion(context.Background(), e.srv.pool, store.NewExpansion{UserID: other.ID, FileName: "notes.md", SelectedText: "beta", Question: "why?"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{itoa(x.ID), "999999", "99999999999999999999", "0"} {
		rec := e.get("/expansions/" + id)
		if rec.Code != 404 || strings.TrimSpace(rec.Body.String()) != `{"detail":"Expansion not found."}` {
			t.Errorf("id %s: %d %s", id, rec.Code, rec.Body)
		}
	}
}

func TestShowExpansionHasAnETag(t *testing.T) {
	e := newExpEnv(t)
	x, _ := store.CreateExpansion(context.Background(), e.srv.pool, store.NewExpansion{UserID: e.user.ID, FileName: "notes.md", SelectedText: "beta", Question: "why?"})
	rec := e.get("/expansions/" + itoa(x.ID))
	etag := rec.Header().Get("ETag")
	if etag == "" || rec.Header().Get("Cache-Control") != defaultCacheControl {
		t.Fatalf("headers %v", rec.Header())
	}
	rec = do(e.srv, "GET", "/expansions/"+itoa(x.ID), map[string]string{"Cookie": e.h["Cookie"], "If-None-Match": etag})
	if rec.Code != http.StatusNotModified {
		t.Errorf("revalidation: %d", rec.Code)
	}
}

type fakeExpander struct{ rewriteOut string }

func (f fakeExpander) Expand(context.Context, llm.Params, llm.Recorder) (string, error) {
	return "<!DOCTYPE html><html><body>answer</body></html>", nil
}
func (f fakeExpander) Rewrite(context.Context, llm.Params, llm.Recorder) (string, error) {
	return f.rewriteOut, nil
}

// End to end: POST, run the job handler directly, poll the status, check the files.
func TestExpansionEndToEnd(t *testing.T) {
	for _, c := range []struct {
		name, mode, wantURL string
		check               func(t *testing.T, dir string)
	}{
		{"create_new", "create_new", "/notes--expand-1.html", func(t *testing.T, dir string) {
			if got := readFile(t, dir, "notes.md"); got != "Alpha [beta](/notes--expand-1.html) gamma." {
				t.Errorf("source %q", got)
			}
			if !strings.Contains(readFile(t, dir, "notes--expand-1.html"), "answer") {
				t.Error("page not written")
			}
		}},
		{"edit_in_place", "edit_in_place", "/notes--v2.md#expansion-anchor", func(t *testing.T, dir string) {
			if got := readFile(t, dir, "notes.md"); got != "Alpha beta gamma." {
				t.Errorf("source touched: %q", got)
			}
			if got := readFile(t, dir, "notes--v2.md"); !strings.Contains(got, `<a id="expansion-anchor"></a>beta, at length`) {
				t.Errorf("version %q", got)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newExpEnv(t)
			if err := os.WriteFile(filepath.Join(e.dir, "notes.md"), []byte("Alpha beta gamma."), 0o644); err != nil {
				t.Fatal(err)
			}
			rec := e.post(bodyWith(t, map[string]any{"mode": c.mode, "client_clicked_at": 1_700_000_000_000}))
			if rec.Code != 202 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			id := idOf(t, rec)
			if len(e.enq.got) != 1 {
				t.Fatalf("enqueued %+v", e.enq.got)
			}
			if got := decode(t, e.get("/expansions/"+itoa(id)))["status"]; got != "pending" {
				t.Fatalf("before the job: %v", got)
			}

			handler := jobs.NewGenerateExpansion(jobs.ExpansionDeps{
				DB: e.srv.pool,
				Processor: &expansion.Processor{
					FilesDir: e.dir, DB: e.srv.pool,
					Expander: fakeExpander{rewriteOut: "Alpha ⟦EXPANSION_ANCHOR⟧beta, at length, gamma. Plus much more text here."},
				},
			})
			if err := handler(context.Background(), e.enq.got[0].args); err != nil {
				t.Fatal(err)
			}
			rec = e.get("/expansions/" + itoa(id))
			want := `{"id":` + itoa(id) + `,"status":"completed","url":"` + c.wantURL + `"}`
			if rec.Body.String() != want {
				t.Fatalf("after the job: %s, want %s", rec.Body, want)
			}
			c.check(t, e.dir)
			x := e.load(t, id)
			for _, stage := range []string{"client_clicked", "request_received", "job_enqueued", "job_started", "completed"} {
				if x.Timings[stage] == 0 {
					t.Errorf("stage %s missing: %v", stage, x.Timings)
				}
			}
		})
	}
}

// Ruby: "accepts a syntactically valid missing file and fails the job in the background".
func TestExpansionOfAMissingFileFailsInTheBackground(t *testing.T) {
	e := newExpEnv(t)
	rec := e.post(bodyWith(t, map[string]any{"file_name": "missing.md"}))
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	id := idOf(t, rec)
	handler := jobs.NewGenerateExpansion(jobs.ExpansionDeps{
		DB: e.srv.pool, Processor: &expansion.Processor{FilesDir: e.dir, DB: e.srv.pool, Expander: fakeExpander{}},
	})
	if err := handler(context.Background(), jobs.Args{ID: id}); err != nil {
		t.Fatal(err)
	}
	got := decode(t, e.get("/expansions/"+itoa(id)))
	if got["status"] != "failed" || got["detail"] != "File not found: missing.md" {
		t.Errorf("got %v", got)
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
