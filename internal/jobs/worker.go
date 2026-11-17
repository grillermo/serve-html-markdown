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
// the package comment): a crashed process leaves its job to be claimed again.
// The context is not cancelled when the Runner's context is; it is cancelled
// only when ShutdownGrace runs out. A handler that ignores it and never
// returns blocks Run.
type Handler func(ctx context.Context, args Args) error

// Defaults for the Runner's knobs.
const (
	DefaultWorkers        = 3 // config/queue.yml: threads 3
	DefaultPoll           = time.Second
	DefaultStaleAfter     = 30 * time.Minute
	DefaultHeartbeatEvery = 5 * time.Minute
	DefaultShutdownGrace  = 30 * time.Second
	DefaultCleanupEvery   = time.Hour
	finishTimeout         = 10 * time.Second
)

// Runner polls go_jobs and runs jobs. Zero-valued knobs take the defaults
// above; the time knobs exist mainly so tests can shrink them.
type Runner struct {
	DB       store.DB
	Handlers map[string]Handler // by job kind
	Workers  int                // goroutines claiming jobs
	Poll     time.Duration      // idle sleep between claims, plus up to 20% jitter

	StaleAfter     time.Duration // lock age after which an unfinished job is reclaimed
	HeartbeatEvery time.Duration // how often a running job refreshes its lock
	ShutdownGrace  time.Duration // how long in-flight handlers get after Run's context is cancelled
	CleanupEvery   time.Duration // how often finished jobs are purged

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
	if r.Logger == nil {
		r.Logger = slog.Default()
	}
	return r
}

// Run starts the workers and an hourly cleanup, and blocks until ctx is
// cancelled and every in-flight job has finished (graceful shutdown on
// SIGTERM). After cancellation no new jobs are claimed; running handlers keep
// a live context for ShutdownGrace, then theirs is cancelled. A handler that
// then returns context.Canceled leaves its job unfinished and unlocked, so the
// next process picks it up; any other outcome is recorded as usual.
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

	var wg sync.WaitGroup
	for range cfg.Workers {
		wg.Go(func() { cfg.work(ctx, handlerCtx) })
	}
	wg.Go(func() { cfg.cleanupLoop(ctx) })
	wg.Wait()
	close(done)
}

func (r Runner) work(ctx, handlerCtx context.Context) {
	for ctx.Err() == nil {
		// Claiming must not be abandoned halfway: a cancelled request whose
		// UPDATE had already committed would leave the job locked for 30 minutes.
		claimCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
		j, err := claim(claimCtx, r.DB, r.StaleAfter)
		cancel()
		switch {
		case err != nil:
			r.Logger.Error("jobs: claim failed", "err", err)
			r.sleep(ctx)
		case j == nil:
			r.sleep(ctx)
		default:
			r.execute(handlerCtx, j)
		}
	}
}

// sleep waits one poll interval (with jitter) or until ctx is cancelled.
func (r Runner) sleep(ctx context.Context) {
	d := r.Poll + rand.N(r.Poll/5+1)
	t := time.NewTimer(d)
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
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		cctx, cancel := context.WithTimeout(ctx, finishTimeout)
		n, err := Cleanup(cctx, r.DB)
		cancel()
		if err != nil {
			r.Logger.Error("jobs: cleanup failed", "err", err)
		} else if n > 0 {
			r.Logger.Info("jobs: cleaned up finished jobs", "deleted", n)
		}
	}
}

func (r Runner) execute(handlerCtx context.Context, j *job) {
	log := r.Logger.With("job_id", j.ID, "kind", j.Kind, "attempt", j.Attempt)
	start := time.Now()
	log.Info("jobs: claimed")

	var lastErr string
	var runErr error
	switch handler, ok := r.Handlers[j.Kind]; {
	case !ok:
		lastErr = "unknown job kind"
	default:
		if j.ArgsErr != nil {
			lastErr = errorText("invalid args: " + j.ArgsErr.Error())
			break
		}
		runErr = r.call(handlerCtx, j.ID, handler, j.Args, log)
		if runErr != nil {
			lastErr = errorText(runErr.Error())
		}
	}

	// Writes after the handler must survive shutdown, or a finished job would
	// be run again.
	wctx := context.WithoutCancel(handlerCtx)
	if runErr != nil && errors.Is(runErr, context.Canceled) && handlerCtx.Err() != nil {
		if err := retry(wctx, func(c context.Context) error { return release(c, r.DB, j.ID) }); err != nil {
			log.Error("jobs: release failed", "err", err)
		}
		log.Warn("jobs: interrupted by shutdown, released", "duration", time.Since(start))
		return
	}
	if err := retry(wctx, func(c context.Context) error { return finish(c, r.DB, j.ID, lastErr) }); err != nil {
		log.Error("jobs: could not mark finished; it will run again once its lock is stale", "err", err)
		return
	}
	if lastErr != "" {
		log.Error("jobs: failed", "duration", time.Since(start), "error", lastErr)
	} else {
		log.Info("jobs: finished", "duration", time.Since(start))
	}
}

// call runs the handler with a heartbeat, turning a panic into an error.
func (r Runner) call(ctx context.Context, id int64, h Handler, args Args, log *slog.Logger) (err error) {
	hbCtx, stopHB := context.WithCancel(ctx)
	var hb sync.WaitGroup
	hb.Go(func() { r.beat(hbCtx, id, log) })
	defer func() {
		stopHB()
		hb.Wait()
		if p := recover(); p != nil {
			log.Error("jobs: panic in handler", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			err = errors.New(errorText(fmt.Sprintf("panic: %v", p)))
		}
	}()
	return h(ctx, args)
}

func (r Runner) beat(ctx context.Context, id int64, log *slog.Logger) {
	tick := time.NewTicker(r.HeartbeatEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		bctx, cancel := context.WithTimeout(ctx, finishTimeout)
		err := heartbeat(bctx, r.DB, id)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Warn("jobs: heartbeat failed", "err", err)
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
