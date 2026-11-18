package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"time"

	"serve-html-markdown/internal/store"
)

// Handler runs one job. It must be safe to run again after a partial run (see
// the package comment): a crash or a shutdown leaves its job to run again.
// The context is not cancelled when the Runner's context is; it is cancelled
// only when ShutdownGrace runs out, or (if the kind has an entry in
// Runner.Timeouts) when the job's deadline passes. A handler that ignores it
// and never returns blocks Run, and its heartbeat keeps the job locked.
type Handler func(ctx context.Context, args Args) error

// Defaults for the Runner's knobs.
const (
	DefaultWorkers        = 3 // config/queue.yml: threads 3
	DefaultPoll           = time.Second
	DefaultStaleAfter     = 30 * time.Minute
	DefaultHeartbeatEvery = 5 * time.Minute
	DefaultShutdownGrace  = 30 * time.Second
	DefaultCleanupEvery   = time.Hour
	DefaultMaxBackoff     = 30 * time.Second
	finishTimeout         = 10 * time.Second
	// outageLogEvery is the least time between "claim failed" log lines while
	// the database stays down.
	outageLogEvery = time.Minute
)

// Runner polls go_jobs and runs jobs. Zero-valued knobs take the defaults
// above; the time knobs exist mainly so tests can shrink them.
type Runner struct {
	DB       store.DB
	Handlers map[string]Handler // by job kind
	Workers  int                // goroutines claiming jobs
	Poll     time.Duration      // idle sleep between claims, plus up to 20% jitter

	// Timeouts bounds a handler's run time per job kind; a kind without an
	// entry is unlimited. A handler that fails once its own deadline passed is
	// an ordinary failed job (finished, last_error "job timed out after X").
	// Needed because the heartbeat keeps a hung job locked indefinitely.
	Timeouts map[string]time.Duration

	// Sweepers are optional housekeeping functions, run when the Runner starts
	// and on every cleanup tick (CleanupEvery). An error is logged, never fatal,
	// and does not stop the other sweepers.
	Sweepers []func(context.Context) error

	StaleAfter     time.Duration // lock age after which an unfinished job is reclaimed
	HeartbeatEvery time.Duration // how often a running job refreshes its lock; at most StaleAfter/3
	ShutdownGrace  time.Duration // how long in-flight handlers get after Run's context is cancelled
	CleanupEvery   time.Duration // how often finished jobs are purged
	MaxBackoff     time.Duration // longest wait between claims while the database is failing

	Logger *slog.Logger // default slog.Default()
}

func (r Runner) withDefaults() Runner {
	if r.Workers <= 0 {
		r.Workers = DefaultWorkers
	}
	if r.Poll <= 0 {
		r.Poll = DefaultPoll
	}
	if r.StaleAfter <= 0 {
		r.StaleAfter = DefaultStaleAfter
	}
	if r.HeartbeatEvery <= 0 {
		r.HeartbeatEvery = DefaultHeartbeatEvery
	}
	if r.ShutdownGrace <= 0 {
		r.ShutdownGrace = DefaultShutdownGrace
	}
	if r.CleanupEvery <= 0 {
		r.CleanupEvery = DefaultCleanupEvery
	}
	if r.MaxBackoff <= 0 {
		r.MaxBackoff = DefaultMaxBackoff
	}
	if r.Logger == nil {
		r.Logger = slog.Default()
	}
	// A heartbeat slower than a third of the stale window could miss a beat or
	// two and let a live job look crashed.
	if limit := max(r.StaleAfter/3, time.Millisecond); r.HeartbeatEvery > limit {
		r.Logger.Warn("jobs: heartbeat interval too long for the stale window, clamping",
			"heartbeat_every", r.HeartbeatEvery, "stale_after", r.StaleAfter, "clamped_to", limit)
		r.HeartbeatEvery = limit
	}
	return r
}

// Run starts the workers and the cleanup (once at start, then hourly), and
// blocks until ctx is cancelled and every in-flight job has finished (graceful
// shutdown on SIGTERM). After cancellation no new jobs are claimed; running
// handlers keep a live context for ShutdownGrace, then theirs is cancelled.
// A job whose handler fails once that happened is released (unfinished,
// unlocked), so the next process runs it again; a handler that returns nil
// completed, and is recorded as finished.
func (r *Runner) Run(ctx context.Context) {
	cfg := r.withDefaults()
	handlerCtx, hardStop := context.WithCancel(context.WithoutCancel(ctx))
	defer hardStop()

	done := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-ctx.Done():
			timer := time.NewTimer(cfg.ShutdownGrace)
			defer timer.Stop()
			select {
			case <-done:
			case <-timer.C:
				cfg.Logger.Warn("jobs: shutdown grace expired, cancelling running handlers", "grace", cfg.ShutdownGrace)
				hardStop()
			}
		}
	}()

	outage := &outageLog{log: cfg.Logger}
	var wg sync.WaitGroup
	for range cfg.Workers {
		wg.Go(func() { cfg.work(ctx, handlerCtx, outage) })
	}
	wg.Go(func() { cfg.cleanupLoop(ctx) })
	wg.Wait()
	close(done)
}

// outageLog throttles the log of a database outage shared by all workers: the
// first failure is logged, then at most one line per outageLogEvery, and one
// line when claims work again.
type outageLog struct {
	log *slog.Logger

	mu       sync.Mutex
	down     bool
	lastLog  time.Time
	failures int // since the last line
}

func (o *outageLog) failed(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.failures++
	if o.down && time.Since(o.lastLog) < outageLogEvery {
		return
	}
	o.down, o.lastLog = true, time.Now()
	o.log.Error("jobs: claim failed", "err", err, "failures_since_last_log", o.failures)
	o.failures = 0
}

func (o *outageLog) ok() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.down {
		return
	}
	o.down = false
	o.log.Info("jobs: claim recovered", "failures_since_last_log", o.failures)
	o.failures = 0
}

func (r Runner) work(ctx, handlerCtx context.Context, outage *outageLog) {
	backoff := r.Poll
	for ctx.Err() == nil {
		// Claiming must not be abandoned halfway: a cancelled request whose
		// UPDATE had already committed would leave the job locked for 30 minutes.
		claimCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
		j, err := claim(claimCtx, r.DB, r.StaleAfter)
		cancel()
		switch {
		case err != nil:
			outage.failed(err)
			r.sleep(ctx, backoff)
			backoff = min(backoff*2, max(r.MaxBackoff, r.Poll))
		case j == nil:
			outage.ok()
			backoff = r.Poll
			r.sleep(ctx, r.Poll)
		default:
			outage.ok()
			backoff = r.Poll
			r.execute(handlerCtx, j)
		}
	}
}

// sleep waits d (plus up to 20% jitter) or until ctx is cancelled.
func (r Runner) sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d + rand.N(d/5+1))
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (r Runner) cleanupLoop(ctx context.Context) {
	tick := time.NewTicker(r.CleanupEvery)
	defer tick.Stop()
	for {
		cctx, cancel := context.WithTimeout(ctx, finishTimeout)
		n, err := Cleanup(cctx, r.DB)
		cancel()
		switch {
		case err != nil && ctx.Err() == nil:
			r.Logger.Warn("jobs: cleanup failed", "err", err)
		case err == nil && n > 0:
			r.Logger.Info("jobs: cleaned up finished jobs", "deleted", n)
		}
		r.sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// sweep runs the Sweepers, each under its own timeout.
func (r Runner) sweep(ctx context.Context) {
	for _, sweeper := range r.Sweepers {
		if ctx.Err() != nil {
			return
		}
		sctx, cancel := context.WithTimeout(ctx, finishTimeout)
		err := runSweeper(sctx, sweeper)
		cancel()
		if err != nil && ctx.Err() == nil {
			r.Logger.Warn("jobs: sweeper failed", "err", err)
		}
	}
}

// runSweeper calls f, turning a panic into an error: housekeeping must never
// take the Runner down.
func runSweeper(ctx context.Context, f func(context.Context) error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic in sweeper: %v", p)
		}
	}()
	return f(ctx)
}

func (r Runner) execute(handlerCtx context.Context, j *job) {
	log := r.Logger.With("job_id", j.ID, "kind", j.Kind, "attempt", j.Attempt)
	start := time.Now()
	log.Info("jobs: claimed")

	var lastErr string
	var runErr error
	timedOut := false
	switch handler, ok := r.Handlers[j.Kind]; {
	case !ok:
		lastErr = errorText("unknown job kind: " + j.Kind)
	case j.ArgsErr != nil:
		lastErr = errorText("invalid args: " + j.ArgsErr.Error())
	default:
		jobCtx := handlerCtx
		timeout := r.Timeouts[j.Kind]
		if timeout > 0 {
			var cancel context.CancelFunc
			jobCtx, cancel = context.WithTimeout(handlerCtx, timeout)
			defer cancel()
		}
		runErr = r.call(jobCtx, handlerCtx, j, handler, log)
		// A failure after the job's own deadline passed (and not because of
		// shutdown) is a timeout.
		timedOut = runErr != nil && handlerCtx.Err() == nil && errors.Is(jobCtx.Err(), context.DeadlineExceeded)
		switch {
		case timedOut:
			lastErr = fmt.Sprintf("job timed out after %s", timeout)
		case runErr != nil:
			lastErr = errorText(runErr.Error())
		}
	}

	// Writes after the handler must survive shutdown, or a finished job would
	// be run again.
	wctx := context.WithoutCancel(handlerCtx)
	if runErr != nil && handlerCtx.Err() != nil {
		// The shutdown grace cut the handler short. Whatever it returned
		// (context canceled, "signal: killed" from a killed subprocess, ...),
		// the job did not complete: give it back for the next process.
		owned, err := release(wctx, r.DB, j.ID, j.Attempt)
		switch {
		case err != nil:
			log.Error("jobs: release failed; it will run again once its lock is stale", "err", err)
		case !owned:
			log.Warn("jobs: interrupted by shutdown but no longer owned, leaving it alone")
		default:
			log.Warn("jobs: interrupted by shutdown, released", "duration", time.Since(start), "error", errorText(runErr.Error()))
		}
		return
	}
	var owned bool
	err := retry(wctx, func(c context.Context) (e error) {
		owned, e = finish(c, r.DB, j.ID, j.Attempt, lastErr)
		return e
	})
	switch {
	case err != nil:
		log.Error("jobs: could not mark finished; it will run again once its lock is stale", "err", err)
	case !owned:
		log.Warn("jobs: lost ownership before finishing (lock went stale and it was claimed again); leaving it to the new run", "duration", time.Since(start))
	case lastErr != "":
		log.Error("jobs: failed", "duration", time.Since(start), "error", lastErr)
	default:
		log.Info("jobs: finished", "duration", time.Since(start))
	}
}

// call runs the handler with a heartbeat, turning a panic into an error. The
// heartbeat follows base (the shutdown context), not the job's own deadline, so
// a handler that outlives its deadline still holds its lock until it returns.
func (r Runner) call(jobCtx, base context.Context, j *job, h Handler, log *slog.Logger) (err error) {
	hbCtx, stopHB := context.WithCancel(base)
	var hb sync.WaitGroup
	hb.Go(func() { r.beat(hbCtx, j, log) })
	defer func() {
		stopHB()
		hb.Wait()
		if p := recover(); p != nil {
			log.Error("jobs: panic in handler", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			err = errors.New(errorText(fmt.Sprintf("panic: %v", p)))
		}
	}()
	return h(jobCtx, j.Args)
}

func (r Runner) beat(ctx context.Context, j *job, log *slog.Logger) {
	tick := time.NewTicker(r.HeartbeatEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		bctx, cancel := context.WithTimeout(ctx, finishTimeout)
		owned, err := heartbeat(bctx, r.DB, j.ID, j.Attempt)
		cancel()
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			log.Warn("jobs: heartbeat failed", "err", err)
		case !owned:
			log.Warn("jobs: heartbeat found the job no longer owned; stopping it")
			return
		}
	}
}

// retry runs op up to three times with a short pause, each under its own
// timeout, for the writes that must not be lost to a transient failure.
func retry(ctx context.Context, op func(context.Context) error) error {
	var err error
	for attempt := range 3 {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		c, cancel := context.WithTimeout(ctx, finishTimeout)
		err = op(c)
		cancel()
		if err == nil {
			return nil
		}
	}
	return err
}
