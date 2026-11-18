package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// A job deadline (the Runner's per-kind Timeout) is not a shutdown: the
// expansion fails instead of waiting forever, and the handler returns the
// error so the Runner reports the timeout.
func TestGenerateExpansionDeadlineFailsTheExpansion(t *testing.T) {
	env := newExpansionEnv(t)
	x := env.create(t, "", "beta")
	dctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	p := &fakeProcessor{fn: func(ctx context.Context, _ *store.Expansion) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}}
	err := NewGenerateExpansion(ExpansionDeps{DB: env.pool, Processor: p})(dctx, Args{ID: x.ID})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline error", err)
	}
	if got := env.reload(t, x.ID); got.Status != "failed" || *got.ErrorDetail != "Expansion failed." {
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
