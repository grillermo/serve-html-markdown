package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/expansion"
	"serve-html-markdown/internal/files"
	"serve-html-markdown/internal/linker"
	"serve-html-markdown/internal/llm"
	"serve-html-markdown/internal/store"
)

type fakeProcessor struct {
	fn    func(ctx context.Context, e *store.Expansion) (string, error)
	calls atomic.Int32
}

func (f *fakeProcessor) Process(ctx context.Context, e *store.Expansion) (string, error) {
	f.calls.Add(1)
	return f.fn(ctx, e)
}

func returningURL(url string) *fakeProcessor {
	return &fakeProcessor{fn: func(context.Context, *store.Expansion) (string, error) { return url, nil }}
}

func failingWith(err error) *fakeProcessor {
	return &fakeProcessor{fn: func(context.Context, *store.Expansion) (string, error) { return "", err }}
}

type expansionEnv struct {
	pool *pgxpool.Pool
	user *store.User
}

func newExpansionEnv(t *testing.T) *expansionEnv {
	t.Helper()
	pool := testdb.New(t)
	u, err := store.UpsertUser(ctx, pool, "job@example.com", "hash")
	must(t, err)
	return &expansionEnv{pool: pool, user: u}
}

func (e *expansionEnv) create(t *testing.T, mode, selected string) *store.Expansion {
	t.Helper()
	x, err := store.CreateExpansion(ctx, e.pool, store.NewExpansion{
		UserID: e.user.ID, FileName: "notes.md", SelectedText: selected, Question: "why?", Mode: mode,
	})
	must(t, err)
	return x
}

func (e *expansionEnv) reload(t *testing.T, id int64) *store.Expansion {
	t.Helper()
	x, err := store.FindExpansion(ctx, e.pool, id)
	must(t, err)
	if x == nil {
		t.Fatalf("expansion %d vanished", id)
	}
	return x
}

func (e *expansionEnv) run(t *testing.T, p ExpansionProcessor, id int64) error {
	t.Helper()
	return NewGenerateExpansion(ExpansionDeps{DB: e.pool, Processor: p})(ctx, Args{ID: id})
}

// Ruby: "completes a pending expansion once".
func TestGenerateExpansionCompletesOnce(t *testing.T) {
	env := newExpansionEnv(t)
	x := env.create(t, "", "beta")
	p := returningURL("/notes--expand-1.html")
	must(t, env.run(t, p, x.ID))
	must(t, env.run(t, p, x.ID))
	got := env.reload(t, x.ID)
	if got.Status != "completed" || got.URL == nil || *got.URL != "/notes--expand-1.html" {
		t.Fatalf("got %+v", got)
	}
	if n := p.calls.Load(); n != 1 {
		t.Fatalf("processor ran %d times, want 1 (second run must be a no-op)", n)
	}
}

// Ruby: "stamps job_started after claiming the expansion".
func TestGenerateExpansionStampsJobStarted(t *testing.T) {
	env := newExpansionEnv(t)
	x := env.create(t, "", "beta")
	var startedBeforeProcess bool
	p := &fakeProcessor{fn: func(ctx context.Context, e *store.Expansion) (string, error) {
		got, _ := store.FindExpansion(ctx, env.pool, e.ID)
		startedBeforeProcess = got.Status == "processing" && got.Timings["job_started"] > 0
		return "/x.html", nil
	}}
	must(t, env.run(t, p, x.ID))
	if !startedBeforeProcess {
		t.Fatal("job_started must be stamped (and the row claimed) before Process runs")
	}
	if env.reload(t, x.ID).Timings["completed"] == 0 {
		t.Fatal("completed should be stamped")
	}
}

func TestGenerateExpansionMissingOrFinishedIsANoOp(t *testing.T) {
	env := newExpansionEnv(t)
	p := returningURL("/x.html")
	must(t, env.run(t, p, 424242))
	x := env.create(t, "", "beta")
	must(t, store.FailExpansion(ctx, env.pool, x.ID, "earlier"))
	must(t, env.run(t, p, x.ID))
	if p.calls.Load() != 0 {
		t.Fatal("processor must not run")
	}
	if got := env.reload(t, x.ID); got.Status != "failed" || *got.ErrorDetail != "earlier" {
		t.Fatalf("got %+v", got)
	}
}

// Ruby: "stores safe details for known and unexpected failures" and
// "reports a truncated rewrite to the reader", plus the rest of the rescue list.
func TestGenerateExpansionFailureDetails(t *testing.T) {
	wrapped := fmt.Errorf("while linking: %w", linker.ErrNotFound)
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"generation failed hides the cause", fmt.Errorf("token leaked: %w", llm.ErrExpand("token leaked")), "Generation failed."},
		{"linker not found", linker.ErrNotFound, "Selection not found in source — select a plainer run of text."},
		{"wrapped linker error", wrapped, "Selection not found in source — select a plainer run of text."},
		{"linker unsafe match", &linker.Error{Kind: linker.KindUnsafeMatch, Message: "Selection spans blocks."}, "Selection spans blocks."},
		{"bad request", files.BadRequest("Bad request."), "Bad request."},
		{"missing file", files.Missing("File not found."), "File not found."},
		{"unsupported file", files.ErrUnsupported, "Only .html, .md, and .markdown files are supported."},
		{"truncated rewrite", expansion.ErrTruncatedRewrite, "Rewrite looked truncated."},
		{"selection too large", expansion.ErrSelectionTooLarge, "Selected text is too long."},
		{"source too large", expansion.ErrSourceTooLarge, "File is too large to expand."},
		{"document too large", expansion.ErrDocumentTooLarge, "Document is too large to expand."},
		{"source not utf8 is generic", expansion.ErrSourceNotUTF8, "Expansion failed."},
		{"unexpected error is generic", errors.New("pq: password=hunter2"), "Expansion failed."},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newExpansionEnv(t)
			x := env.create(t, "", "beta")
			must(t, env.run(t, failingWith(c.err), x.ID))
			got := env.reload(t, x.ID)
			if got.Status != "failed" || got.ErrorDetail == nil || *got.ErrorDetail != c.want || got.URL != nil {
				t.Fatalf("got status %q detail %v, want failed %q", got.Status, got.ErrorDetail, c.want)
			}
		})
	}
}

func TestGenerateExpansionPanicIsAGenericFailureAndLogsNoDocument(t *testing.T) {
	env := newExpansionEnv(t)
	x := env.create(t, "", "SECRET-SELECTION")
	var logs bytes.Buffer
	h := NewGenerateExpansion(ExpansionDeps{
		DB: env.pool, Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		Processor: &fakeProcessor{fn: func(context.Context, *store.Expansion) (string, error) { panic("boom") }},
	})
	must(t, h(ctx, Args{ID: x.ID}))
	got := env.reload(t, x.ID)
	if got.Status != "failed" || *got.ErrorDetail != "Expansion failed." {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(logs.String(), "boom") || strings.Contains(logs.String(), "SECRET-SELECTION") {
		t.Fatalf("log should name the panic and no document text: %s", logs.String())
	}
}

// A shutdown cancel mid-generation must not fail the expansion: the handler
// gives it back (pending) and returns the error so the queue releases the job,
// and the re-run completes it.
func TestGenerateExpansionShutdownCancelThenRerun(t *testing.T) {
	env := newExpansionEnv(t)
	x := env.create(t, "", "beta")
	cctx, cancel := context.WithCancel(ctx)
	p := &fakeProcessor{fn: func(ctx context.Context, _ *store.Expansion) (string, error) {
		cancel()
		<-ctx.Done()
		return "", fmt.Errorf("llm: %w", llm.ErrExpand("signal: killed"))
	}}
	err := NewGenerateExpansion(ExpansionDeps{DB: env.pool, Processor: p})(cctx, Args{ID: x.ID})
	if err == nil {
		t.Fatal("a cancelled run must return its error so the queue releases the job")
	}
	got := env.reload(t, x.ID)
	if got.Status != "pending" || got.ErrorDetail != nil {
		t.Fatalf("after cancel: status %q detail %v, want pending", got.Status, got.ErrorDetail)
	}
	must(t, env.run(t, returningURL("/notes--expand-1.html"), x.ID))
	if got := env.reload(t, x.ID); got.Status != "completed" || *got.URL != "/notes--expand-1.html" {
		t.Fatalf("after re-run: %+v", got)
	}
}

// manualCtx is a context whose end the test triggers, with the error it picks
// (context.DeadlineExceeded without waiting for a real deadline).
type manualCtx struct {
	context.Context
	done chan struct{}
	mu   sync.Mutex
	err  error
}

func newManualCtx() *manualCtx { return &manualCtx{Context: ctx, done: make(chan struct{})} }

func (m *manualCtx) Done() <-chan struct{} { return m.done }
func (m *manualCtx) Err() error            { m.mu.Lock(); defer m.mu.Unlock(); return m.err }
func (m *manualCtx) end(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err == nil {
		m.err = err
		close(m.done)
	}
}

// A job deadline (the Runner's per-kind Timeout) is not a shutdown: the
// expansion fails instead of waiting forever, and the handler returns the
// error so the Runner records the timeout.
func TestGenerateExpansionDeadlineFailsTheExpansion(t *testing.T) {
	env := newExpansionEnv(t)
	x := env.create(t, "", "beta")
	dctx := newManualCtx()
	p := &fakeProcessor{fn: func(c context.Context, _ *store.Expansion) (string, error) {
		dctx.end(context.DeadlineExceeded) // the deadline passes while generating
		<-c.Done()
		return "", c.Err()
	}}
	err := NewGenerateExpansion(ExpansionDeps{DB: env.pool, Processor: p})(dctx, Args{ID: x.ID})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline error", err)
	}
	if got := env.reload(t, x.ID); got.Status != "failed" || *got.ErrorDetail != "Expansion failed." {
		t.Fatalf("got %+v", got)
	}
}

// A context that ended before the handler started claims nothing.
func TestGenerateExpansionWithAnEndedContextClaimsNothing(t *testing.T) {
	env := newExpansionEnv(t)
	x := env.create(t, "", "beta")
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	p := returningURL("/x.html")
	if err := NewGenerateExpansion(ExpansionDeps{DB: env.pool, Processor: p})(cctx, Args{ID: x.ID}); err == nil {
		t.Fatal("want the context error")
	}
	if got := env.reload(t, x.ID); got.Status != "pending" || p.calls.Load() != 0 {
		t.Fatalf("got %+v, calls %d", got, p.calls.Load())
	}
}

// cancelOnClaim ends the handler's context while the claim statement runs, the
// moment that used to leave a committed claim behind an error.
type cancelOnClaim struct {
	store.DB
	cancel context.CancelFunc
}

func (c cancelOnClaim) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "AND status = 'pending'") {
		c.cancel()
	}
	return c.DB.Exec(ctx, sql, args...)
}

func TestGenerateExpansionClaimOutcomeIsNeverInDoubt(t *testing.T) {
	env := newExpansionEnv(t)
	x := env.create(t, "", "beta")
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p := &fakeProcessor{fn: func(c context.Context, _ *store.Expansion) (string, error) {
		<-c.Done()
		return "", c.Err()
	}}
	err := NewGenerateExpansion(ExpansionDeps{DB: cancelOnClaim{env.pool, cancel}, Processor: p})(cctx, Args{ID: x.ID})
	if err == nil {
		t.Fatal("a run cancelled mid-claim must return its error")
	}
	// The claim committed although the context ended; the run owned it and
	// released it, so the re-run works.
	if got := env.reload(t, x.ID); got.Status != "pending" {
		t.Fatalf("status %q, want pending (not stuck processing)", got.Status)
	}
	must(t, env.run(t, returningURL("/x.html"), x.ID))
	if got := env.reload(t, x.ID); got.Status != "completed" {
		t.Fatalf("re-run: %+v", got)
	}
}

// A late writer never overwrites an outcome that another run recorded.
func TestGenerateExpansionDoesNotOverwriteAFinishedExpansion(t *testing.T) {
	env := newExpansionEnv(t)
	x := env.create(t, "", "beta")
	finishFirst := func(finish func(id int64) error) *fakeProcessor {
		return &fakeProcessor{fn: func(_ context.Context, e *store.Expansion) (string, error) {
			must(t, finish(e.ID))
			return "/late.html", nil
		}}
	}
	must(t, env.run(t, finishFirst(func(id int64) error { return store.FailExpansion(ctx, env.pool, id, "first") }), x.ID))
	if got := env.reload(t, x.ID); got.Status != "failed" || *got.ErrorDetail != "first" || got.URL != nil {
		t.Fatalf("a late Complete overwrote the failure: %+v", got)
	}

	y := env.create(t, "", "gamma")
	late := &fakeProcessor{fn: func(_ context.Context, e *store.Expansion) (string, error) {
		must(t, store.CompleteExpansion(ctx, env.pool, e.ID, "/first.html"))
		return "", errors.New("late failure")
	}}
	must(t, env.run(t, late, y.ID))
	if got := env.reload(t, y.ID); got.Status != "completed" || *got.URL != "/first.html" || got.ErrorDetail != nil {
		t.Fatalf("a late Fail overwrote the result: %+v", got)
	}
}

// A countingExpander stands in for the LLM and counts its calls.
type countingExpander struct{ calls atomic.Int32 }

func (c *countingExpander) Expand(context.Context, llm.Params, llm.Recorder) (string, error) {
	c.calls.Add(1)
	return "<!DOCTYPE html><html><body>answer</body></html>", nil
}

func (c *countingExpander) Rewrite(context.Context, llm.Params, llm.Recorder) (string, error) {
	c.calls.Add(1)
	return "Alpha \u27e6EXPANSION_ANCHOR\u27e7beta, at length, gamma. Plus much more text here.", nil
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	must(t, err)
	var names []string
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// A run that wrote its files and stored the url, then died, is completed by
// its re-run with the same url: no second file, no LLM call.
func TestGenerateExpansionRerunAfterPartialSuccess(t *testing.T) {
	for _, c := range []struct {
		mode, wantURL string
		wantFiles     []string
	}{
		{"create_new", "/notes--expand-1.html", []string{"notes--expand-1.html", "notes.md"}},
		{"edit_in_place", "/notes--v2.md#expansion-anchor", []string{"notes--v2.md", "notes.md"}},
	} {
		for _, state := range []string{"processing and stale", "released to pending"} {
			t.Run(c.mode+"/"+state, func(t *testing.T) {
				env := newExpansionEnv(t)
				dir := t.TempDir()
				must(t, os.WriteFile(filepath.Join(dir, "notes.md"), []byte("Alpha beta gamma."), 0o644))
				x := env.create(t, c.mode, "beta")
				exp := &countingExpander{}
				proc := &expansion.Processor{FilesDir: dir, DB: env.pool, Expander: exp}

				// First run: claimed, generated, files written, url stored... then it dies.
				_, err := store.ClaimExpansion(ctx, env.pool, x.ID)
				must(t, err)
				url, err := proc.Process(ctx, x)
				must(t, err)
				if url != c.wantURL || exp.calls.Load() != 1 {
					t.Fatalf("first run: url %q, calls %d", url, exp.calls.Load())
				}
				if got := env.reload(t, x.ID); got.Status != "processing" || got.URL == nil || *got.URL != url {
					t.Fatalf("the url must be stored without completing: %+v", got)
				}
				if state == "released to pending" {
					_, err = store.ReleaseExpansion(ctx, env.pool, x.ID)
				} else {
					_, err = env.pool.Exec(ctx, `UPDATE expansions SET updated_at = now() at time zone 'utc' - interval '16 minutes' WHERE id = $1`, x.ID)
				}
				must(t, err)

				// The re-run must not generate or write anything.
				untouched := &fakeProcessor{fn: func(context.Context, *store.Expansion) (string, error) {
					t.Error("the processor must not run again")
					return "", errors.New("regenerated")
				}}
				must(t, env.run(t, untouched, x.ID))
				got := env.reload(t, x.ID)
				if got.Status != "completed" || got.URL == nil || *got.URL != c.wantURL || got.ErrorDetail != nil {
					t.Fatalf("after the re-run: %+v", got)
				}
				if names := dirEntries(t, dir); !slices.Equal(names, c.wantFiles) {
					t.Fatalf("files %v, want %v", names, c.wantFiles)
				}
				if exp.calls.Load() != 1 {
					t.Fatalf("LLM called %d times", exp.calls.Load())
				}
			})
		}
	}
}

// The real cause of a failed write is logged (not shown to the reader).
func TestGenerateExpansionLogsTheCauseOfAFailedWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	env := newExpansionEnv(t)
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "notes.md"), []byte("Alpha beta gamma."), 0o644))
	must(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	x := env.create(t, "", "beta")
	var logs bytes.Buffer
	h := NewGenerateExpansion(ExpansionDeps{
		DB: env.pool, Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		Processor: &expansion.Processor{FilesDir: dir, DB: env.pool, Expander: &countingExpander{}},
	})
	must(t, h(ctx, Args{ID: x.ID}))
	if got := env.reload(t, x.ID); got.Status != "failed" || *got.ErrorDetail != "Could not save the expansion." {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(logs.String(), "cause=") || !strings.Contains(logs.String(), "permission denied") {
		t.Fatalf("the cause is not logged: %s", logs.String())
	}
}

// A sweeper finishes stuck expansions on a Runner's start and on every tick.
func TestRunnerRunsSweepers(t *testing.T) {
	env := newExpansionEnv(t)
	old := env.create(t, "", "beta")
	_, err := env.pool.Exec(ctx, `UPDATE expansions SET updated_at = now() at time zone 'utc' - interval '41 minutes' WHERE id = $1`, old.ID)
	must(t, err)
	var failing atomic.Int32
	r := &Runner{
		DB: env.pool, Workers: 1, Poll: 5 * time.Millisecond, CleanupEvery: 20 * time.Millisecond,
		Sweepers: []func(context.Context) error{
			func(context.Context) error { failing.Add(1); return errors.New("sweeper boom") },
			func(context.Context) error { panic("sweeper panic") },
			ExpansionSweeper(env.pool, 0),
		},
	}
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { r.Run(rctx); close(done) }()
	eventually(t, "the stale expansion to be failed at start", func() bool { return env.reload(t, old.ID).Status == "failed" })
	if got := env.reload(t, old.ID); *got.ErrorDetail != "Expansion failed." {
		t.Errorf("detail %v", got.ErrorDetail)
	}

	// One that goes stale later is caught by a tick, even though the other
	// sweepers keep failing and panicking.
	later := env.create(t, "", "gamma")
	_, err = env.pool.Exec(ctx, `UPDATE expansions SET updated_at = now() at time zone 'utc' - interval '41 minutes' WHERE id = $1`, later.ID)
	must(t, err)
	eventually(t, "the next tick to sweep", func() bool { return env.reload(t, later.ID).Status == "failed" })
	if failing.Load() < 2 {
		t.Errorf("sweepers ran %d times, want at least start + tick", failing.Load())
	}
	cancel()
	<-done
}

func TestExpansionSweepersWiring(t *testing.T) {
	env := newExpansionEnv(t)
	x := env.create(t, "", "beta")
	_, err := env.pool.Exec(ctx, `UPDATE expansions SET updated_at = now() at time zone 'utc' - interval '3 minutes' WHERE id = $1`, x.ID)
	must(t, err)
	sw := ExpansionSweepers(ExpansionDeps{DB: env.pool, SweepAfter: time.Minute})
	if len(sw) != 1 {
		t.Fatalf("sweepers %d", len(sw))
	}
	must(t, sw[0](ctx))
	if got := env.reload(t, x.ID); got.Status != "failed" {
		t.Fatalf("got %+v", got)
	}
}

// A run that died (kill -9) leaves a processing row; once it is stale the next
// run of the job takes it over. A recent processing row is left alone.
func TestGenerateExpansionReclaimsStaleProcessingRows(t *testing.T) {
	env := newExpansionEnv(t)
	x := env.create(t, "", "beta")
	_, err := store.ClaimExpansion(ctx, env.pool, x.ID)
	must(t, err)

	p := returningURL("/x.html")
	must(t, env.run(t, p, x.ID))
	if p.calls.Load() != 0 || env.reload(t, x.ID).Status != "processing" {
		t.Fatal("a recently touched processing row belongs to a live run")
	}

	_, err = env.pool.Exec(ctx, `UPDATE expansions SET updated_at = now() at time zone 'utc' - interval '16 minutes' WHERE id = $1`, x.ID)
	must(t, err)
	must(t, env.run(t, p, x.ID))
	if got := env.reload(t, x.ID); p.calls.Load() != 1 || got.Status != "completed" {
		t.Fatalf("stale row should be re-run: calls %d, %+v", p.calls.Load(), got)
	}
}

func TestExpansionHandlersIsKeyedByKind(t *testing.T) {
	env := newExpansionEnv(t)
	hs := ExpansionHandlers(ExpansionDeps{DB: env.pool, Processor: returningURL("/x.html")})
	if _, ok := hs[KindGenerateExpansion]; len(hs) != 1 || !ok {
		t.Fatalf("handlers %v", hs)
	}
	x := env.create(t, "", "beta")
	// Through the real queue path: enqueue, then let a Runner run it.
	must(t, Enqueue(ctx, env.pool, KindGenerateExpansion, Args{ID: x.ID}, 0))
	rctx, cancel := context.WithCancel(ctx)
	r := &Runner{DB: env.pool, Handlers: hs, Workers: 1, Poll: 5 * time.Millisecond}
	done := make(chan struct{})
	go func() { r.Run(rctx); close(done) }()
	eventually(t, "the expansion to complete", func() bool { return env.reload(t, x.ID).Status == "completed" })
	cancel()
	<-done
}
