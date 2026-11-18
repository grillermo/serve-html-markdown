package store_test

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"serve-html-markdown/internal/db"
	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/store"
)

func newExpansionUser(t *testing.T, pool *pgxpool.Pool, email string) *store.User {
	t.Helper()
	u, err := store.UpsertUser(ctx, pool, email, "hash")
	must(t, err)
	return u
}

func newExpansion(t *testing.T, pool *pgxpool.Pool, userID int64) *store.Expansion {
	t.Helper()
	e, err := store.CreateExpansion(ctx, pool, store.NewExpansion{
		UserID: userID, FileName: "notes.md", SelectedText: "beta", Question: "Why?",
	})
	must(t, err)
	return e
}

func TestCreateExpansionDefaults(t *testing.T) {
	pool := testdb.New(t)
	clock(t, t0)
	u := newExpansionUser(t, pool, "e@x.com")
	e := newExpansion(t, pool, u.ID)
	if e.ID == 0 || e.UserID != u.ID || e.FileName != "notes.md" || e.SelectedText != "beta" || e.Question != "Why?" {
		t.Fatalf("got %+v", e)
	}
	if e.Status != "pending" || e.Mode != "create_new" || e.Occurrence != 0 || e.UseOpenAI {
		t.Fatalf("defaults: %+v", e)
	}
	if e.URL != nil || e.ErrorDetail != nil || e.FallbackAnchor != nil || e.ProviderUsed != nil || e.HTMLBytes != nil {
		t.Fatalf("nullable columns should start NULL: %+v", e)
	}
	if len(e.Timings) != 0 {
		t.Fatalf("timings = %v, want empty", e.Timings)
	}
	if !e.CreatedAt.Equal(t0) || !e.UpdatedAt.Equal(t0) {
		t.Fatalf("timestamps %v %v", e.CreatedAt, e.UpdatedAt)
	}
	if e.EditInPlace() {
		t.Fatal("create_new is not edit in place")
	}
}

func TestCreateExpansionStoresEveryColumn(t *testing.T) {
	pool := testdb.New(t)
	u := newExpansionUser(t, pool, "e@x.com")
	e, err := store.CreateExpansion(ctx, pool, store.NewExpansion{
		UserID: u.ID, FileName: "n.md", SelectedText: "s", Question: "q",
		Occurrence: 3, Mode: "edit_in_place", FallbackAnchor: "sec-2", UseOpenAI: true,
	})
	must(t, err)
	got, err := store.FindExpansion(ctx, pool, e.ID)
	must(t, err)
	if got.Occurrence != 3 || got.Mode != "edit_in_place" || !got.UseOpenAI || !got.EditInPlace() ||
		got.FallbackAnchor == nil || *got.FallbackAnchor != "sec-2" {
		t.Fatalf("got %+v", got)
	}
	// A blank fallback anchor is stored as NULL (the controller's .presence).
	e2, err := store.CreateExpansion(ctx, pool, store.NewExpansion{
		UserID: u.ID, FileName: "n.md", SelectedText: "s", Question: "q", FallbackAnchor: "",
	})
	must(t, err)
	if e2.FallbackAnchor != nil {
		t.Fatalf("fallback anchor = %q, want NULL", *e2.FallbackAnchor)
	}
}

func TestCreateExpansionValidates(t *testing.T) {
	pool := testdb.New(t)
	u := newExpansionUser(t, pool, "e@x.com")
	ok := store.NewExpansion{UserID: u.ID, FileName: "n.md", SelectedText: "s", Question: "q"}
	cases := []struct {
		name   string
		mutate func(*store.NewExpansion)
		field  string
		msg    string
	}{
		{"blank file name", func(n *store.NewExpansion) { n.FileName = "" }, "file_name", "can't be blank"},
		{"whitespace file name", func(n *store.NewExpansion) { n.FileName = " \n\t" }, "file_name", "can't be blank"},
		{"blank selected text", func(n *store.NewExpansion) { n.SelectedText = "" }, "selected_text", "can't be blank"},
		{"blank question", func(n *store.NewExpansion) { n.Question = "  " }, "question", "can't be blank"},
		{"negative occurrence", func(n *store.NewExpansion) { n.Occurrence = -1 }, "occurrence", "must be greater than or equal to 0"},
		{"huge occurrence", func(n *store.NewExpansion) { n.Occurrence = 1 << 40 }, "occurrence", "must be less than or equal to 2147483647"},
		{"unknown mode", func(n *store.NewExpansion) { n.Mode = "nonsense" }, "mode", "is not included in the list"},
		{"NUL in question", func(n *store.NewExpansion) { n.Question = "a\x00b" }, "question", "is invalid"},
		{"bad UTF-8 in selected text", func(n *store.NewExpansion) { n.SelectedText = "a\xffb" }, "selected_text", "is invalid"},
	}
	for _, c := range cases {
		in := ok
		c.mutate(&in)
		e, err := store.CreateExpansion(ctx, pool, in)
		var invalid *store.InvalidExpansionError
		if e != nil || !errors.Is(err, store.ErrInvalidExpansion) || !errors.As(err, &invalid) {
			t.Errorf("%s: got %v, %v", c.name, e, err)
			continue
		}
		if invalid.Field != c.field || invalid.Message != c.msg {
			t.Errorf("%s: field %q message %q, want %q %q", c.name, invalid.Field, invalid.Message, c.field, c.msg)
		}
	}
	// An unknown mode is also the users-table sentinel.
	in := ok
	in.Mode = "nonsense"
	if _, err := store.CreateExpansion(ctx, pool, in); !errors.Is(err, store.ErrInvalidExpansionMode) {
		t.Errorf("err = %v, want ErrInvalidExpansionMode", err)
	}
	var n int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM expansions`).Scan(&n))
	if n != 0 {
		t.Fatalf("%d invalid expansions were stored", n)
	}
	// Blank-looking text with the first field wrong reports file_name first
	// (Rails lists errors in attribute order; the first one wins here).
	_, err := store.CreateExpansion(ctx, pool, store.NewExpansion{UserID: u.ID})
	var invalid *store.InvalidExpansionError
	if !errors.As(err, &invalid) || invalid.Field != "file_name" {
		t.Fatalf("all blank: err = %v", err)
	}
	if got := invalid.Error(); got != "store: invalid expansion: file_name can't be blank" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestCreateExpansionUnknownUser(t *testing.T) {
	pool := testdb.New(t)
	_, err := store.CreateExpansion(ctx, pool, store.NewExpansion{UserID: 999, FileName: "n.md", SelectedText: "s", Question: "q"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestExpansionStatusesAndModes(t *testing.T) {
	if got := store.ExpansionStatuses; len(got) != 4 || got[0] != "pending" || got[1] != "processing" || got[2] != "completed" || got[3] != "failed" {
		t.Fatalf("ExpansionStatuses = %v", got)
	}
}

func TestClaimOnlyOnce(t *testing.T) {
	pool := testdb.New(t)
	advance := clock(t, t0)
	u := newExpansionUser(t, pool, "e@x.com")
	e := newExpansion(t, pool, u.ID)
	advance(time.Minute)
	won, err := store.ClaimExpansion(ctx, pool, e.ID)
	must(t, err)
	if !won {
		t.Fatal("first claim should win")
	}
	got, err := store.FindExpansion(ctx, pool, e.ID)
	must(t, err)
	if got.Status != "processing" || !got.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("got %+v", got)
	}
	won, err = store.ClaimExpansion(ctx, pool, e.ID)
	must(t, err)
	if won {
		t.Fatal("second claim should lose")
	}
	won, err = store.ClaimExpansion(ctx, pool, 12345)
	must(t, err)
	if won {
		t.Fatal("claim of a missing row should lose")
	}
}

func TestClaimIsExclusiveAcrossConnections(t *testing.T) {
	simple := testdb.New(t)
	pool, err := db.Open(ctx, simple.Config().ConnConfig.ConnString(), 10, false)
	must(t, err)
	t.Cleanup(pool.Close)
	u := newExpansionUser(t, simple, "e@x.com")
	e := newExpansion(t, simple, u.ID)

	var wg sync.WaitGroup
	results := make(chan bool, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := store.ClaimExpansion(ctx, pool, e.ID)
			if err != nil {
				t.Error(err)
			}
			results <- won
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for won := range results {
		if won {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d claims won, want exactly 1", wins)
	}
}

func TestCompleteAndFail(t *testing.T) {
	pool := testdb.New(t)
	advance := clock(t, t0)
	u := newExpansionUser(t, pool, "e@x.com")
	e := newExpansion(t, pool, u.ID)
	_, err := store.ClaimExpansion(ctx, pool, e.ID)
	must(t, err)
	// A previous failure's detail must be cleared by a later completion.
	_, err = pool.Exec(ctx, `UPDATE expansions SET error_detail = 'old' WHERE id = $1`, e.ID)
	must(t, err)

	advance(time.Minute)
	must(t, store.CompleteExpansion(ctx, pool, e.ID, "/notes--expand-1.html"))
	got, err := store.FindExpansion(ctx, pool, e.ID)
	must(t, err)
	if got.Status != "completed" || got.URL == nil || *got.URL != "/notes--expand-1.html" || got.ErrorDetail != nil {
		t.Fatalf("completed: %+v", got)
	}
	if got.Timings["completed"] != t0.Add(time.Minute).UnixMilli() {
		t.Fatalf("completed stamp = %d, timings %v", got.Timings["completed"], got.Timings)
	}
	if !got.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("updated_at = %v", got.UpdatedAt)
	}

	failed := newExpansion(t, pool, u.ID)
	_, err = store.ClaimExpansion(ctx, pool, failed.ID)
	must(t, err)
	_, err = pool.Exec(ctx, `UPDATE expansions SET url = '/stale.html' WHERE id = $1`, failed.ID)
	must(t, err)
	advance(time.Minute)
	must(t, store.FailExpansion(ctx, pool, failed.ID, "Generation failed."))
	got, err = store.FindExpansion(ctx, pool, failed.ID)
	must(t, err)
	if got.Status != "failed" || got.URL != nil || got.ErrorDetail == nil || *got.ErrorDetail != "Generation failed." {
		t.Fatalf("failed: %+v", got)
	}
	if _, ok := got.Timings["completed"]; ok {
		t.Fatalf("a failure must not stamp completed: %v", got.Timings)
	}

	if err := store.CompleteExpansion(ctx, pool, 999, "/x"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Complete missing: %v", err)
	}
	if err := store.FailExpansion(ctx, pool, 999, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Fail missing: %v", err)
	}
}

func TestStampStartsFromEmptyTimings(t *testing.T) {
	pool := testdb.New(t)
	clock(t, t0)
	u := newExpansionUser(t, pool, "e@x.com")
	e := newExpansion(t, pool, u.ID)
	if len(e.Timings) != 0 {
		t.Fatalf("timings = %v", e.Timings)
	}
	store.StampExpansion(ctx, pool, e.ID, "job_started", store.NowMS())
	got, err := store.FindExpansion(ctx, pool, e.ID)
	must(t, err)
	if want := t0.UnixMilli(); got.Timings["job_started"] != want {
		t.Fatalf("timings = %v, want job_started=%d", got.Timings, want)
	}
	if store.NowMS() != t0.UnixMilli() {
		t.Fatalf("NowMS = %d, want the package clock %d", store.NowMS(), t0.UnixMilli())
	}
}

func TestStampTakesAnExplicitEpoch(t *testing.T) {
	pool := testdb.New(t)
	u := newExpansionUser(t, pool, "e@x.com")
	e := newExpansion(t, pool, u.ID)
	store.StampExpansion(ctx, pool, e.ID, "client_clicked", 1_700_000_000_000)
	got, err := store.FindExpansion(ctx, pool, e.ID)
	must(t, err)
	if got.Timings["client_clicked"] != 1_700_000_000_000 {
		t.Fatalf("timings = %v", got.Timings)
	}
}

func TestStampRestampsAStageAndKeepsOthers(t *testing.T) {
	pool := testdb.New(t)
	u := newExpansionUser(t, pool, "e@x.com")
	e := newExpansion(t, pool, u.ID)
	store.StampExpansion(ctx, pool, e.ID, "a", 1)
	store.StampExpansion(ctx, pool, e.ID, "b", 2)
	store.StampExpansion(ctx, pool, e.ID, "a", 3)
	got, err := store.FindExpansion(ctx, pool, e.ID)
	must(t, err)
	if len(got.Timings) != 2 || got.Timings["a"] != 3 || got.Timings["b"] != 2 {
		t.Fatalf("timings = %v", got.Timings)
	}
}

// Rails wrote the column with Ruby's JSON coder; whatever it left behind, a
// stamp must keep working and keep the existing keys.
func TestStampReadsRubyWrittenTimings(t *testing.T) {
	pool := testdb.New(t)
	u := newExpansionUser(t, pool, "e@x.com")
	e := newExpansion(t, pool, u.ID)
	for _, stored := range []string{`{"request_received":1}`, ``, `null`, `[]`} {
		_, err := pool.Exec(ctx, `UPDATE expansions SET timings = $2 WHERE id = $1`, e.ID, stored)
		must(t, err)
		got, err := store.FindExpansion(ctx, pool, e.ID)
		must(t, err)
		if len(got.Timings) > 1 {
			t.Fatalf("stored %q read as %v", stored, got.Timings)
		}
		store.StampExpansion(ctx, pool, e.ID, "job_started", 2)
		got, err = store.FindExpansion(ctx, pool, e.ID)
		must(t, err)
		if got.Timings["job_started"] != 2 {
			t.Fatalf("stored %q: timings = %v", stored, got.Timings)
		}
		if stored == `{"request_received":1}` && got.Timings["request_received"] != 1 {
			t.Fatalf("lost existing key: %v", got.Timings)
		}
	}
}

func TestConcurrentStampsAllLand(t *testing.T) {
	simple := testdb.New(t)
	// Ten connections, so the ten stamps really overlap in the database.
	pool, err := db.Open(ctx, simple.Config().ConnConfig.ConnString(), 10, false)
	must(t, err)
	t.Cleanup(pool.Close)
	u := newExpansionUser(t, simple, "e@x.com")
	e := newExpansion(t, simple, u.ID)
	store.StampExpansion(ctx, pool, e.ID, "request_received", 1)

	stages := []string{"s0", "s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8", "s9"}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, stage := range stages {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			store.StampExpansion(ctx, pool, e.ID, stage, int64(100+i))
		}()
	}
	close(start)
	wg.Wait()

	got, err := store.FindExpansion(ctx, simple, e.ID)
	must(t, err)
	if len(got.Timings) != 11 || got.Timings["request_received"] != 1 {
		t.Fatalf("timings = %v", got.Timings)
	}
	for i, stage := range stages {
		if got.Timings[stage] != int64(100+i) {
			t.Errorf("stage %s = %d, want %d", stage, got.Timings[stage], 100+i)
		}
	}
}

func TestStampIgnoresFailures(t *testing.T) {
	pool := testdb.New(t)
	// A missing row and a stage Postgres cannot store: logged, never returned
	// or panicking (the Ruby rescue).
	store.StampExpansion(ctx, pool, 999, "job_started", 1)
	u := newExpansionUser(t, pool, "e@x.com")
	e := newExpansion(t, pool, u.ID)
	store.StampExpansion(ctx, pool, e.ID, "bad\x00stage", 1)
	got, err := store.FindExpansion(ctx, pool, e.ID)
	must(t, err)
	if len(got.Timings) != 0 {
		t.Fatalf("timings = %v", got.Timings)
	}
}

func TestRecordResponse(t *testing.T) {
	pool := testdb.New(t)
	advance := clock(t, t0)
	u := newExpansionUser(t, pool, "e@x.com")
	e := newExpansion(t, pool, u.ID)
	advance(time.Hour)
	must(t, store.RecordExpansionResponse(ctx, pool, e.ID, "claude", 1234))
	got, err := store.FindExpansion(ctx, pool, e.ID)
	must(t, err)
	if got.ProviderUsed == nil || *got.ProviderUsed != "claude" || got.HTMLBytes == nil || *got.HTMLBytes != 1234 {
		t.Fatalf("got %+v", got)
	}
	// update_columns in the Ruby app: updated_at is left alone.
	if !got.UpdatedAt.Equal(t0) {
		t.Fatalf("updated_at = %v, want %v", got.UpdatedAt, t0)
	}
	if err := store.RecordExpansionResponse(ctx, pool, 999, "claude", 1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing row: %v", err)
	}
}

func TestFindUserExpansionIsScopedToTheUser(t *testing.T) {
	pool := testdb.New(t)
	owner := newExpansionUser(t, pool, "owner@x.com")
	other := newExpansionUser(t, pool, "other@x.com")
	e := newExpansion(t, pool, owner.ID)

	got, err := store.FindUserExpansion(ctx, pool, owner.ID, e.ID)
	must(t, err)
	if got == nil || got.ID != e.ID {
		t.Fatalf("owner got %+v", got)
	}
	got, err = store.FindUserExpansion(ctx, pool, other.ID, e.ID)
	must(t, err)
	if got != nil {
		t.Fatalf("other user got %+v", got)
	}
	got, err = store.FindExpansion(ctx, pool, 999)
	must(t, err)
	if got != nil {
		t.Fatalf("missing id got %+v", got)
	}
}

func TestCompleteKeepsExistingTimings(t *testing.T) {
	pool := testdb.New(t)
	u := newExpansionUser(t, pool, "e@x.com")
	e := newExpansion(t, pool, u.ID)
	store.StampExpansion(ctx, pool, e.ID, "job_started", 7)
	must(t, store.CompleteExpansion(ctx, pool, e.ID, "/n--expand-1.html"))
	got, err := store.FindExpansion(ctx, pool, e.ID)
	must(t, err)
	if len(got.Timings) != 2 || got.Timings["job_started"] != 7 || got.Timings["completed"] == 0 {
		t.Fatalf("timings = %v", got.Timings)
	}
}

// Rails' stamp! rescued, so a broken timings column never lost the result.
func TestCompleteSavesResultWhenTimingsIsNotJSON(t *testing.T) {
	pool := testdb.New(t)
	u := newExpansionUser(t, pool, "e@x.com")
	e := newExpansion(t, pool, u.ID)
	_, err := pool.Exec(ctx, `UPDATE expansions SET timings = 'garbage', error_detail = 'old' WHERE id = $1`, e.ID)
	must(t, err)
	must(t, store.CompleteExpansion(ctx, pool, e.ID, "/n--expand-1.html"))
	got, err := store.FindExpansion(ctx, pool, e.ID)
	must(t, err)
	if got.Status != "completed" || got.URL == nil || *got.URL != "/n--expand-1.html" || got.ErrorDetail != nil {
		t.Fatalf("got %+v", got)
	}
	var raw string
	must(t, pool.QueryRow(ctx, `SELECT timings FROM expansions WHERE id = $1`, e.ID).Scan(&raw))
	if raw != "garbage" {
		t.Fatalf("timings = %q, want it left alone", raw)
	}
}

// FailExpansion is the job's last resort, so odd text is sanitised, not rejected.
func TestFailSanitisesDetail(t *testing.T) {
	pool := testdb.New(t)
	u := newExpansionUser(t, pool, "e@x.com")
	for detail, want := range map[string]string{
		"a\x00b":        "ab",
		"bad\xffbytes":  "bad\uFFFDbytes",
		"\x00\xfe\x00z": "\uFFFDz",
		"plain é":       "plain é",
	} {
		e := newExpansion(t, pool, u.ID)
		must(t, store.FailExpansion(ctx, pool, e.ID, detail))
		got, err := store.FindExpansion(ctx, pool, e.ID)
		must(t, err)
		if got.Status != "failed" || got.ErrorDetail == nil || *got.ErrorDetail != want {
			t.Errorf("detail %q: got %+v, want %q", detail, got, want)
		}
	}
}

// Timings written by anything but this package may hold odd values: keep the
// numbers (floats truncated), skip the rest.
func TestTimingsAreReadKeyByKey(t *testing.T) {
	pool := testdb.New(t)
	u := newExpansionUser(t, pool, "e@x.com")
	e := newExpansion(t, pool, u.ID)
	_, err := pool.Exec(ctx, `UPDATE expansions SET timings = $2 WHERE id = $1`, e.ID,
		`{"int":5,"float":1700000000123.9,"neg":-3,"str":"7","null":null,"obj":{"a":1},"arr":[1],"bool":true,"huge":1e30}`)
	must(t, err)
	got, err := store.FindExpansion(ctx, pool, e.ID)
	must(t, err)
	want := map[string]int64{"int": 5, "float": 1700000000123, "neg": -3}
	if !reflect.DeepEqual(got.Timings, want) {
		t.Fatalf("timings = %v, want %v", got.Timings, want)
	}
}

func TestReleaseExpansionPutsProcessingBackToPending(t *testing.T) {
	pool := testdb.New(t)
	clock(t, t0)
	u := newExpansionUser(t, pool, "e@x.com")
	e := newExpansion(t, pool, u.ID)

	released, err := store.ReleaseExpansion(ctx, pool, e.ID)
	must(t, err)
	if released {
		t.Fatal("a pending expansion is not released")
	}
	_, err = store.ClaimExpansion(ctx, pool, e.ID)
	must(t, err)
	released, err = store.ReleaseExpansion(ctx, pool, e.ID)
	must(t, err)
	if !released {
		t.Fatal("a processing expansion should be released")
	}
	won, err := store.ClaimExpansion(ctx, pool, e.ID)
	must(t, err)
	if !won {
		t.Fatal("a released expansion can be claimed again")
	}
	must(t, store.CompleteExpansion(ctx, pool, e.ID, "/x.html"))
	if released, _ = store.ReleaseExpansion(ctx, pool, e.ID); released {
		t.Fatal("a completed expansion must stay completed")
	}
}

func TestReclaimExpansionOnlyTakesStaleProcessingRows(t *testing.T) {
	pool := testdb.New(t)
	advance := clock(t, t0)
	u := newExpansionUser(t, pool, "e@x.com")
	e := newExpansion(t, pool, u.ID)
	stale := 15 * time.Minute

	reclaimed, err := store.ReclaimExpansion(ctx, pool, e.ID, stale)
	must(t, err)
	if reclaimed {
		t.Fatal("pending rows are not reclaimed")
	}
	_, err = store.ClaimExpansion(ctx, pool, e.ID)
	must(t, err)
	advance(stale - time.Second)
	if reclaimed, err = store.ReclaimExpansion(ctx, pool, e.ID, stale); err != nil || reclaimed {
		t.Fatalf("a recent processing row is live: reclaimed=%v err=%v", reclaimed, err)
	}
	advance(2 * time.Second)
	if reclaimed, err = store.ReclaimExpansion(ctx, pool, e.ID, stale); err != nil || !reclaimed {
		t.Fatalf("a stale processing row should be reclaimed: reclaimed=%v err=%v", reclaimed, err)
	}
	got, err := store.FindExpansion(ctx, pool, e.ID)
	must(t, err)
	if got.Status != "pending" {
		t.Fatalf("status = %q, want pending", got.Status)
	}
	must(t, store.FailExpansion(ctx, pool, e.ID, "nope"))
	advance(time.Hour)
	if reclaimed, _ = store.ReclaimExpansion(ctx, pool, e.ID, stale); reclaimed {
		t.Fatal("a failed row is never reclaimed")
	}
}

func TestTerminalWritesNeverOverwriteAFinishedExpansion(t *testing.T) {
	pool := testdb.New(t)
	clock(t, t0)
	u := newExpansionUser(t, pool, "e@x.com")

	done := newExpansion(t, pool, u.ID)
	must(t, store.CompleteExpansion(ctx, pool, done.ID, "/done.html"))
	if err := store.FailExpansion(ctx, pool, done.ID, "late"); !errors.Is(err, store.ErrExpansionFinished) {
		t.Fatalf("late Fail: %v", err)
	}
	if err := store.CompleteExpansion(ctx, pool, done.ID, "/other.html"); !errors.Is(err, store.ErrExpansionFinished) {
		t.Fatalf("second Complete: %v", err)
	}
	if err := store.SetExpansionURL(ctx, pool, done.ID, "/other.html"); !errors.Is(err, store.ErrExpansionFinished) {
		t.Fatalf("late SetExpansionURL: %v", err)
	}
	got, err := store.FindExpansion(ctx, pool, done.ID)
	must(t, err)
	if got.Status != "completed" || *got.URL != "/done.html" || got.ErrorDetail != nil {
		t.Fatalf("completed row was overwritten: %+v", got)
	}

	bad := newExpansion(t, pool, u.ID)
	must(t, store.FailExpansion(ctx, pool, bad.ID, "Generation failed."))
	if err := store.CompleteExpansion(ctx, pool, bad.ID, "/late.html"); !errors.Is(err, store.ErrExpansionFinished) {
		t.Fatalf("late Complete: %v", err)
	}
	got, err = store.FindExpansion(ctx, pool, bad.ID)
	must(t, err)
	if got.Status != "failed" || got.URL != nil || *got.ErrorDetail != "Generation failed." || got.Timings["completed"] != 0 {
		t.Fatalf("failed row was overwritten: %+v", got)
	}
	if err := store.SetExpansionURL(ctx, pool, 999, "/x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetExpansionURL missing: %v", err)
	}
}

func TestSetExpansionURLKeepsTheStatus(t *testing.T) {
	pool := testdb.New(t)
	clock(t, t0)
	u := newExpansionUser(t, pool, "e@x.com")
	e := newExpansion(t, pool, u.ID)
	_, err := store.ClaimExpansion(ctx, pool, e.ID)
	must(t, err)
	must(t, store.SetExpansionURL(ctx, pool, e.ID, "/n--v2.md#expansion-anchor"))
	got, err := store.FindExpansion(ctx, pool, e.ID)
	must(t, err)
	if got.Status != "processing" || got.URL == nil || *got.URL != "/n--v2.md#expansion-anchor" {
		t.Fatalf("got %+v", got)
	}
}

func TestFailStaleExpansions(t *testing.T) {
	pool := testdb.New(t)
	advance := clock(t, t0)
	u := newExpansionUser(t, pool, "e@x.com")
	stale := 40 * time.Minute

	pending := newExpansion(t, pool, u.ID)
	processing := newExpansion(t, pool, u.ID)
	_, err := store.ClaimExpansion(ctx, pool, processing.ID)
	must(t, err)
	withURL := newExpansion(t, pool, u.ID)
	must(t, store.SetExpansionURL(ctx, pool, withURL.ID, "/kept.html"))
	finished := newExpansion(t, pool, u.ID)
	must(t, store.CompleteExpansion(ctx, pool, finished.ID, "/done.html"))

	advance(stale - time.Second)
	n, err := store.FailStaleExpansions(ctx, pool, stale)
	must(t, err)
	if n != 0 {
		t.Fatalf("nothing is stale yet, swept %d", n)
	}
	recent := newExpansion(t, pool, u.ID) // created later, so younger
	advance(2 * time.Second)
	n, err = store.FailStaleExpansions(ctx, pool, stale)
	must(t, err)
	if n != 3 {
		t.Fatalf("swept %d, want 3", n)
	}
	for _, id := range []int64{pending.ID, processing.ID} {
		got, err := store.FindExpansion(ctx, pool, id)
		must(t, err)
		if got.Status != "failed" || got.ErrorDetail == nil || *got.ErrorDetail != "Expansion failed." {
			t.Errorf("stale row %d: %+v", id, got)
		}
	}
	got, err := store.FindExpansion(ctx, pool, withURL.ID)
	must(t, err)
	if got.Status != "completed" || *got.URL != "/kept.html" || got.ErrorDetail != nil {
		t.Errorf("a row with a url completes with it: %+v", got)
	}
	for id, want := range map[int64]string{finished.ID: "completed", recent.ID: "pending"} {
		got, err := store.FindExpansion(ctx, pool, id)
		must(t, err)
		if got.Status != want {
			t.Errorf("row %d: status %q, want %q", id, got.Status, want)
		}
	}
	if n, err = store.FailStaleExpansions(ctx, pool, stale); err != nil || n != 0 {
		t.Errorf("second sweep: %d %v", n, err)
	}
}
