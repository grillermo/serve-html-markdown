package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"serve-html-markdown/internal/db/testdb"
)

var ctx = context.Background()

const stale = 30 * time.Minute

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// eventually polls cond until it holds or a bounded wait runs out.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// insertAt puts a row in go_jobs with timestamps relative to the DB clock
// (SQL interval expressions, e.g. "now() - interval '31 minutes'"), so no test
// depends on how long it takes to run.
func insertAt(t *testing.T, pool *pgxpool.Pool, kind, runAt, lockedAt, finishedAt string) int64 {
	t.Helper()
	var id int64
	must(t, pool.QueryRow(ctx, fmt.Sprintf(
		`INSERT INTO go_jobs (kind, args, run_at, locked_at, finished_at) VALUES ($1, '{"id": 7}', %s, %s, %s) RETURNING id`,
		runAt, lockedAt, finishedAt), kind).Scan(&id))
	return id
}

type row struct {
	Attempts   int
	Locked     bool
	Finished   bool
	LastError  *string
	RunAtDelay float64 // seconds from now
}

func load(t *testing.T, pool *pgxpool.Pool, id int64) row {
	t.Helper()
	var r row
	must(t, pool.QueryRow(ctx, `SELECT attempts, locked_at IS NOT NULL, finished_at IS NOT NULL, last_error,
		extract(epoch FROM run_at - now()) FROM go_jobs WHERE id = $1`, id).
		Scan(&r.Attempts, &r.Locked, &r.Finished, &r.LastError, &r.RunAtDelay))
	return r
}

func TestEnqueueThenClaimReturnsJobOnce(t *testing.T) {
	pool := testdb.New(t)
	must(t, Enqueue(ctx, pool, KindGenerateExpansion, Args{ID: 42}, 0))

	j, err := claim(ctx, pool, stale)
	must(t, err)
	if j == nil || j.Kind != KindGenerateExpansion || j.Args.ID != 42 || j.ID == 0 {
		t.Fatalf("got %+v", j)
	}
	if r := load(t, pool, j.ID); r.Attempts != 1 || !r.Locked || r.Finished {
		t.Fatalf("claimed row: %+v", r)
	}
	again, err := claim(ctx, pool, stale)
	must(t, err)
	if again != nil {
		t.Fatalf("second claim should find nothing, got %+v", again)
	}
}

func TestConcurrentClaimsOfOneJobReturnItOnce(t *testing.T) {
	pool := testdb.New(t)
	must(t, Enqueue(ctx, pool, "k", Args{ID: 1}, 0))
	var got atomic.Int32
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := claim(ctx, pool, stale)
			if err != nil {
				t.Error(err)
			}
			if j != nil {
				got.Add(1)
			}
		}()
	}
	wg.Wait()
	if got.Load() != 1 {
		t.Fatalf("want exactly 1 claim, got %d", got.Load())
	}
}

func TestTwentyConcurrentClaimsOnFiveJobsClaimFiveDistinct(t *testing.T) {
	pool := testdb.New(t)
	for i := 1; i <= 5; i++ {
		must(t, Enqueue(ctx, pool, "k", Args{ID: int64(i)}, 0))
	}
	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := claim(ctx, pool, stale)
			if err != nil {
				t.Error(err)
				return
			}
			if j != nil {
				mu.Lock()
				seen[j.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != 5 {
		t.Fatalf("want 5 distinct jobs, got %v", seen)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("job %d claimed %d times", id, n)
		}
	}
}

func TestClaimOrdersByRunAtThenID(t *testing.T) {
	pool := testdb.New(t)
	late := insertAt(t, pool, "k", "now() - interval '1 minute'", "NULL", "NULL")
	early := insertAt(t, pool, "k", "now() - interval '1 hour'", "NULL", "NULL")
	for _, want := range []int64{early, late} {
		j, err := claim(ctx, pool, stale)
		must(t, err)
		if j == nil || j.ID != want {
			t.Fatalf("want job %d, got %+v", want, j)
		}
	}
}

func TestClaimSkipsFutureFinishedAndFreshlyLockedJobs(t *testing.T) {
	pool := testdb.New(t)
	insertAt(t, pool, "future", "now() + interval '1 minute'", "NULL", "NULL")
	insertAt(t, pool, "done", "now()", "NULL", "now()")
	insertAt(t, pool, "busy", "now()", "now() - interval '29 minutes'", "NULL")
	// Stale but finished must stay finished.
	insertAt(t, pool, "done-stale", "now()", "now() - interval '2 hours'", "now() - interval '1 hour'")
	j, err := claim(ctx, pool, stale)
	must(t, err)
	if j != nil {
		t.Fatalf("nothing should be claimable, got %+v", j)
	}
}

func TestStaleLockIsClaimedAgain(t *testing.T) {
	pool := testdb.New(t)
	id := insertAt(t, pool, "k", "now() - interval '40 minutes'", "now() - interval '31 minutes'", "NULL")
	j, err := claim(ctx, pool, stale)
	must(t, err)
	if j == nil || j.ID != id {
		t.Fatalf("crashed job should be recovered, got %+v", j)
	}
	if r := load(t, pool, id); r.Attempts != 1 {
		t.Fatalf("attempts: %+v", r)
	}
}

func TestEnqueueDelayIsRelativeToDBClock(t *testing.T) {
	pool := testdb.New(t)
	cases := []struct {
		wait time.Duration
		secs float64
	}{
		{0, 0},
		{-time.Hour, 0}, // negative clamps to now
		{500 * time.Microsecond, 0},
		{time.Minute, 60},
		{2880 * time.Minute, 2880 * 60}, // longest captions checkpoint: 2 days
		{1500 * time.Millisecond, 1.5},
	}
	for _, c := range cases {
		must(t, Enqueue(ctx, pool, "k", Args{ID: 1}, c.wait))
		var delay float64
		must(t, pool.QueryRow(ctx, `SELECT extract(epoch FROM run_at - now()) FROM go_jobs ORDER BY id DESC LIMIT 1`).Scan(&delay))
		if delay > c.secs+0.01 || delay < c.secs-5 {
			t.Errorf("wait %v: run_at is %.3fs from now, want about %.3fs", c.wait, delay, c.secs)
		}
	}
	// Anything scheduled in the future is not claimable yet.
	pool2 := testdb.New(t)
	must(t, Enqueue(ctx, pool2, "k", Args{ID: 1}, time.Minute))
	j, err := claim(ctx, pool2, stale)
	must(t, err)
	if j != nil {
		t.Fatalf("job due in a minute was claimed: %+v", j)
	}
}

func TestEnqueueStoresArgsJSONAndRejectsBadInput(t *testing.T) {
	pool := testdb.New(t)
	must(t, Enqueue(ctx, pool, "k", Args{ID: 9}, 0))
	var raw string
	must(t, pool.QueryRow(ctx, `SELECT args::text FROM go_jobs`).Scan(&raw))
	if raw != `{"id": 9}` {
		t.Fatalf("args: %s", raw)
	}
	for _, bad := range []Args{{ID: 0}, {ID: -3}} {
		if err := Enqueue(ctx, pool, "k", bad, 0); !errors.Is(err, ErrInvalidJob) {
			t.Errorf("Enqueue(%+v) = %v, want ErrInvalidJob", bad, err)
		}
	}
	if err := Enqueue(ctx, pool, "  ", Args{ID: 1}, 0); !errors.Is(err, ErrInvalidJob) {
		t.Errorf("blank kind: %v", err)
	}
	var n int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM go_jobs`).Scan(&n))
	if n != 1 {
		t.Fatalf("rejected jobs must not be stored, have %d rows", n)
	}
}

func TestDBEnqueuerSatisfiesEnqueuer(t *testing.T) {
	pool := testdb.New(t)
	var e Enqueuer = DBEnqueuer{DB: pool}
	must(t, e.Enqueue(ctx, KindTwitterVideoCaptions, Args{ID: 5}, time.Minute))
	var kind string
	must(t, pool.QueryRow(ctx, `SELECT kind FROM go_jobs`).Scan(&kind))
	if kind != "twitter_video_captions" {
		t.Fatal(kind)
	}
}

func TestCleanupDeletesFinishedJobsOlderThan24Hours(t *testing.T) {
	pool := testdb.New(t)
	old := insertAt(t, pool, "k", "now()", "now()", "now() - interval '25 hours'")
	recent := insertAt(t, pool, "k", "now()", "now()", "now() - interval '23 hours'")
	pending := insertAt(t, pool, "k", "now() - interval '3 days'", "NULL", "NULL")
	n, err := Cleanup(ctx, pool)
	must(t, err)
	if n != 1 {
		t.Fatalf("deleted %d, want 1", n)
	}
	for id, want := range map[int64]bool{old: false, recent: true, pending: true} {
		var exists bool
		must(t, pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM go_jobs WHERE id = $1)`, id).Scan(&exists))
		if exists != want {
			t.Errorf("job %d exists=%v, want %v", id, exists, want)
		}
	}
}

// logBuf is a goroutine-safe log sink.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// startRunner runs r until the returned stop func is called; stop waits for Run.
func startRunner(t *testing.T, r *Runner) (stop func()) {
	t.Helper()
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { r.Run(runCtx); close(done) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("Run did not return after cancel")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func finished(t *testing.T, pool *pgxpool.Pool, id int64) func() bool {
	return func() bool { return load(t, pool, id).Finished }
}

func TestWorkerMarksFinishedAndNeverRerunsAJob(t *testing.T) {
	pool := testdb.New(t)
	must(t, Enqueue(ctx, pool, "ok", Args{ID: 11}, 0))
	var calls atomic.Int32
	got := make(chan Args, 4)
	r := &Runner{DB: pool, Workers: 2, Poll: 5 * time.Millisecond, Handlers: map[string]Handler{
		"ok": func(_ context.Context, a Args) error { calls.Add(1); got <- a; return nil },
	}}
	stop := startRunner(t, r)
	if a := <-got; a.ID != 11 {
		t.Fatalf("args: %+v", a)
	}
	var id int64
	must(t, pool.QueryRow(ctx, `SELECT id FROM go_jobs`).Scan(&id))
	eventually(t, "job finished", finished(t, pool, id))
	stop()
	if row := load(t, pool, id); row.LastError != nil || row.Attempts != 1 {
		t.Fatalf("row: %+v", row)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler ran %d times", calls.Load())
	}
}

func TestWorkerRecordsHandlerErrorAndDoesNotRetry(t *testing.T) {
	pool := testdb.New(t)
	must(t, Enqueue(ctx, pool, "bad", Args{ID: 1}, 0))
	var calls atomic.Int32
	r := &Runner{DB: pool, Workers: 1, Poll: 5 * time.Millisecond, Handlers: map[string]Handler{
		"bad": func(context.Context, Args) error { calls.Add(1); return errors.New("boom \x00 \xff") },
	}}
	stop := startRunner(t, r)
	var id int64
	eventually(t, "job inserted", func() bool { return pool.QueryRow(ctx, `SELECT id FROM go_jobs`).Scan(&id) == nil })
	eventually(t, "job finished", finished(t, pool, id))
	stop()
	row := load(t, pool, id)
	if row.LastError == nil || !strings.HasPrefix(*row.LastError, "boom") {
		t.Fatalf("last_error: %v", row.LastError)
	}
	if calls.Load() != 1 {
		t.Fatalf("errors must not be retried, ran %d times", calls.Load())
	}
}

func TestLongErrorsAreTruncated(t *testing.T) {
	pool := testdb.New(t)
	must(t, Enqueue(ctx, pool, "bad", Args{ID: 1}, 0))
	r := &Runner{DB: pool, Workers: 1, Poll: 5 * time.Millisecond, Handlers: map[string]Handler{
		"bad": func(context.Context, Args) error { return errors.New(strings.Repeat("é", 10000)) },
	}}
	stop := startRunner(t, r)
	var id int64
	eventually(t, "job inserted", func() bool { return pool.QueryRow(ctx, `SELECT id FROM go_jobs`).Scan(&id) == nil })
	eventually(t, "job finished", finished(t, pool, id))
	stop()
	row := load(t, pool, id)
	if row.LastError == nil || len(*row.LastError) > maxErrorBytes || len(*row.LastError) < maxErrorBytes-4 {
		t.Fatalf("last_error length %d", len(*row.LastError))
	}
}

func TestPanickingHandlerIsRecoveredLoggedAndMarkedFinished(t *testing.T) {
	pool := testdb.New(t)
	must(t, Enqueue(ctx, pool, "panics", Args{ID: 1}, 0))
	must(t, Enqueue(ctx, pool, "ok", Args{ID: 2}, 0))
	logs := &logBuf{}
	ran := make(chan struct{})
	r := &Runner{DB: pool, Workers: 1, Poll: 5 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(logs, nil)),
		Handlers: map[string]Handler{
			"panics": func(context.Context, Args) error { panic("kaboom") },
			"ok":     func(context.Context, Args) error { close(ran); return nil },
		}}
	stop := startRunner(t, r)
	<-ran // the single worker survived the panic and took the next job
	var id int64
	must(t, pool.QueryRow(ctx, `SELECT id FROM go_jobs WHERE kind = 'panics'`).Scan(&id))
	eventually(t, "panicking job finished", finished(t, pool, id))
	stop()
	row := load(t, pool, id)
	if row.LastError == nil || *row.LastError != "panic: kaboom" {
		t.Fatalf("last_error: %v", row.LastError)
	}
	if out := logs.String(); !strings.Contains(out, "kaboom") || !strings.Contains(out, "stack=") {
		t.Fatalf("panic and stack should be logged, got: %s", out)
	}
}

func TestUnknownKindIsFinishedWithErrorAndNeverLoops(t *testing.T) {
	pool := testdb.New(t)
	must(t, Enqueue(ctx, pool, "mystery", Args{ID: 1}, 0))
	r := &Runner{DB: pool, Workers: 1, Poll: 5 * time.Millisecond, Handlers: map[string]Handler{}}
	stop := startRunner(t, r)
	var id int64
	eventually(t, "job inserted", func() bool { return pool.QueryRow(ctx, `SELECT id FROM go_jobs`).Scan(&id) == nil })
	eventually(t, "job finished", finished(t, pool, id))
	stop()
	row := load(t, pool, id)
	if row.LastError == nil || *row.LastError != "unknown job kind" || row.Attempts != 1 {
		t.Fatalf("row: %+v err=%v", row, row.LastError)
	}
}

func TestInvalidStoredArgsFinishWithError(t *testing.T) {
	pool := testdb.New(t)
	var id int64
	must(t, pool.QueryRow(ctx, `INSERT INTO go_jobs (kind, args) VALUES ('ok', '{"id": "nope"}') RETURNING id`).Scan(&id))
	r := &Runner{DB: pool, Workers: 1, Poll: 5 * time.Millisecond, Handlers: map[string]Handler{
		"ok": func(context.Context, Args) error { t.Error("handler must not run"); return nil },
	}}
	stop := startRunner(t, r)
	eventually(t, "job finished", finished(t, pool, id))
	stop()
	if row := load(t, pool, id); row.LastError == nil || !strings.HasPrefix(*row.LastError, "invalid args") {
		t.Fatalf("last_error: %v", row.LastError)
	}
}

func TestManyWorkersRunEveryJobExactlyOnce(t *testing.T) {
	pool := testdb.New(t)
	const jobs = 60
	for i := 1; i <= jobs; i++ {
		must(t, Enqueue(ctx, pool, "k", Args{ID: int64(i)}, 0))
	}
	var mu sync.Mutex
	runs := map[int64]int{}
	r := &Runner{DB: pool, Workers: 6, Poll: 5 * time.Millisecond, Handlers: map[string]Handler{
		"k": func(_ context.Context, a Args) error {
			mu.Lock()
			runs[a.ID]++
			mu.Unlock()
			return nil
		},
	}}
	stop := startRunner(t, r)
	eventually(t, "all jobs finished", func() bool {
		var n int
		must(t, pool.QueryRow(ctx, `SELECT count(*) FROM go_jobs WHERE finished_at IS NULL`).Scan(&n))
		return n == 0
	})
	stop()
	mu.Lock()
	defer mu.Unlock()
	if len(runs) != jobs {
		t.Fatalf("ran %d distinct jobs, want %d", len(runs), jobs)
	}
	for id, n := range runs {
		if n != 1 {
			t.Errorf("job arg %d ran %d times", id, n)
		}
	}
}

func TestShutdownWaitsForInFlightHandlerAndMarksItFinished(t *testing.T) {
	pool := testdb.New(t)
	must(t, Enqueue(ctx, pool, "slow", Args{ID: 1}, 0))
	started, release := make(chan struct{}), make(chan struct{})
	var handlerCtxErr atomic.Value
	r := &Runner{DB: pool, Workers: 1, Poll: 5 * time.Millisecond, Handlers: map[string]Handler{
		"slow": func(hctx context.Context, _ Args) error {
			close(started)
			<-release
			handlerCtxErr.Store(fmt.Sprint(hctx.Err()))
			return nil
		},
	}}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { r.Run(runCtx); close(done) }()
	<-started
	cancel()
	select {
	case <-done:
		t.Fatal("Run returned while a handler was still running")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the handler finished")
	}
	var id int64
	must(t, pool.QueryRow(ctx, `SELECT id FROM go_jobs`).Scan(&id))
	if row := load(t, pool, id); !row.Finished || row.LastError != nil {
		t.Fatalf("row after shutdown: %+v", row)
	}
	if v := handlerCtxErr.Load(); v != "<nil>" {
		t.Fatalf("handler context was cancelled by shutdown within the grace period: %v", v)
	}
}

func TestShutdownGraceExpiryCancelsHandlerAndReleasesTheJob(t *testing.T) {
	pool := testdb.New(t)
	must(t, Enqueue(ctx, pool, "slow", Args{ID: 1}, 0))
	started := make(chan struct{})
	r := &Runner{DB: pool, Workers: 1, Poll: 5 * time.Millisecond, ShutdownGrace: 50 * time.Millisecond,
		Handlers: map[string]Handler{
			"slow": func(hctx context.Context, _ Args) error {
				close(started)
				<-hctx.Done()
				return hctx.Err()
			},
		}}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { r.Run(runCtx); close(done) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the grace period")
	}
	var id int64
	must(t, pool.QueryRow(ctx, `SELECT id FROM go_jobs`).Scan(&id))
	// Interrupted by shutdown: not finished, lock released so the next process runs it.
	if row := load(t, pool, id); row.Finished || row.Locked {
		t.Fatalf("interrupted job should be released: %+v", row)
	}
	j, err := claim(ctx, pool, stale)
	must(t, err)
	if j == nil || j.ID != id {
		t.Fatalf("released job should be claimable at once, got %+v", j)
	}
}

func TestRunWithNoJobsReturnsPromptlyOnCancel(t *testing.T) {
	pool := testdb.New(t)
	r := &Runner{DB: pool, Handlers: map[string]Handler{}}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { r.Run(runCtx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run with no jobs did not return promptly (default 1s poll must be interruptible)")
	}
}

func TestHeartbeatKeepsALongRunningJobFromBeingReclaimed(t *testing.T) {
	pool := testdb.New(t)
	must(t, Enqueue(ctx, pool, "long", Args{ID: 1}, 0))
	started, release := make(chan struct{}), make(chan struct{})
	r := &Runner{DB: pool, Workers: 1, Poll: 5 * time.Millisecond, HeartbeatEvery: 20 * time.Millisecond,
		Handlers: map[string]Handler{
			"long": func(context.Context, Args) error { close(started); <-release; return nil },
		}}
	stop := startRunner(t, r)
	<-started
	var id int64
	must(t, pool.QueryRow(ctx, `SELECT id FROM go_jobs`).Scan(&id))

	// Pretend the handler has been running for 31 minutes: the lock looks stale.
	_, err := pool.Exec(ctx, `UPDATE go_jobs SET locked_at = now() - interval '31 minutes' WHERE id = $1`, id)
	must(t, err)
	// The heartbeat refreshes it, so another worker cannot take the job over.
	eventually(t, "heartbeat refreshing locked_at", func() bool {
		var fresh bool
		must(t, pool.QueryRow(ctx, `SELECT locked_at > now() - interval '1 minute' FROM go_jobs WHERE id = $1`, id).Scan(&fresh))
		return fresh
	})
	j, err := claim(ctx, pool, stale)
	must(t, err)
	if j != nil {
		t.Fatalf("running job was re-claimed: %+v", j)
	}
	close(release)
	eventually(t, "job finished", finished(t, pool, id))
	stop()

	// Once the handler returned the heartbeat stops: it cannot touch the finished row.
	var lockedBefore time.Time
	must(t, pool.QueryRow(ctx, `SELECT locked_at FROM go_jobs WHERE id = $1`, id).Scan(&lockedBefore))
	time.Sleep(60 * time.Millisecond)
	var lockedAfter time.Time
	must(t, pool.QueryRow(ctx, `SELECT locked_at FROM go_jobs WHERE id = $1`, id).Scan(&lockedAfter))
	if !lockedBefore.Equal(lockedAfter) {
		t.Fatal("heartbeat kept running after the job finished")
	}
}

func TestRunDefaultsWorkersAndKeepsGoingWhenTheDatabaseFails(t *testing.T) {
	// A closed pool makes every claim fail; Run must neither panic nor spin
	// past cancellation.
	pool := testdb.New(t)
	bad, err := pgxpool.New(ctx, pool.Config().ConnString())
	must(t, err)
	bad.Close()
	r := &Runner{DB: bad, Poll: 5 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(&logBuf{}, nil)),
		Handlers: map[string]Handler{}}
	stop := startRunner(t, r)
	time.Sleep(30 * time.Millisecond)
	stop()
}
