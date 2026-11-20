package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"serve-html-markdown/internal/expansion"
	"serve-html-markdown/internal/files"
	"serve-html-markdown/internal/linker"
	"serve-html-markdown/internal/llm"
	"serve-html-markdown/internal/store"
)

// ExpansionProcessor generates one expansion and returns its url:
// *expansion.Processor. It is an interface so tests can use a fake.
type ExpansionProcessor interface {
	Process(ctx context.Context, e *store.Expansion) (string, error)
}

var _ ExpansionProcessor = (*expansion.Processor)(nil)

// DefaultExpansionStaleAfter is how long a "processing" expansion may go
// without a stage stamp before the next run of its job takes it over (see
// store.ReclaimExpansion). It is half the queue's stale-lock window: the queue
// hands a crashed run's job out only after that window, so by then the row is
// older than this; a live run stamps its stages far more often than this.
const DefaultExpansionStaleAfter = 15 * time.Minute

// finalWriteTimeout bounds the writes that record the outcome. They run on a
// context detached from the job's, so a shutdown or a deadline that ended the
// generation does not also lose its result.
const finalWriteTimeout = 10 * time.Second

// Messages the reader sees (Ruby: GenerateExpansionJob).
const msgGenerationFailed = "Generation failed."

// ExpansionDeps are what the generate_expansion handler needs.
type ExpansionDeps struct {
	DB        store.DB
	Processor ExpansionProcessor
	// StaleAfter defaults to DefaultExpansionStaleAfter.
	StaleAfter time.Duration
	// SweepAfter is how long a pending or processing expansion may go
	// untouched before the sweeper finishes it; defaults to
	// DefaultExpansionSweepAfter.
	SweepAfter time.Duration
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

// DefaultExpansionSweepAfter is how long a pending or processing expansion may
// go untouched (stage stamps refresh updated_at) before the sweeper gives up on
// it. It is longer than the queue's stale-lock window (30 minutes) plus
// DefaultExpansionStaleAfter's take-over, so a crashed run's job gets its
// chance first.
const DefaultExpansionSweepAfter = 40 * time.Minute

// ExpansionSweeper returns the function for Runner.Sweepers that finishes the
// expansions no job is working on any more (store.FailStaleExpansions): the
// ones whose job never claimed them (a database outage), whose outcome could
// not be written, or whose process died.
func ExpansionSweeper(db store.DB, staleAfter time.Duration) func(context.Context) error {
	if staleAfter <= 0 {
		staleAfter = DefaultExpansionSweepAfter
	}
	return func(ctx context.Context) error {
		n, err := store.FailStaleExpansions(ctx, db, staleAfter)
		if err == nil && n > 0 {
			slog.Warn("expansion sweeper: finished stale expansions", "count", n)
		}
		return err
	}
}

// ExpansionSweepers is the sibling of ExpansionHandlers for wiring:
// Runner{Handlers: jobs.ExpansionHandlers(deps), Sweepers: jobs.ExpansionSweepers(deps)}.
func ExpansionSweepers(deps ExpansionDeps) []func(context.Context) error {
	return []func(context.Context) error{ExpansionSweeper(deps.DB, deps.SweepAfter)}
}

// ExpansionHandlers is the kind-to-handler map for the Runner, so wiring is
// one line: Runner{Handlers: jobs.ExpansionHandlers(deps)} (merge it with the
// other kinds' maps).
func ExpansionHandlers(deps ExpansionDeps) map[string]Handler {
	return map[string]Handler{KindGenerateExpansion: NewGenerateExpansion(deps)}
}

// NewGenerateExpansion returns the handler of GenerateExpansionJob.
//
// It claims the expansion (pending -> processing; no claim, no work: this is
// what makes a second run harmless), stamps job_started, runs the processor
// and records the outcome. The reader-facing failure text is chosen by
// failureDetail. Every failure of the generation itself ends in a failed
// expansion and a nil return (the job is done, as in Ruby where the rescue
// swallowed the error), with two exceptions where the handler returns the
// error:
//
//   - The context was cancelled (the Runner's shutdown grace ran out): the
//     expansion is put back to pending and the error returned, so the Runner
//     releases the job and the next process runs it again.
//   - The context's deadline passed (a per-kind Runner timeout): the
//     expansion is failed, and the error returned so the Runner records the
//     timeout.
//
// A row left "processing" by a run that died is taken over once it is older
// than StaleAfter (store.ReclaimExpansion), tried before the claim. If an
// earlier run wrote the files and stored the result url (the processor does, as
// soon as the files exist) but died before completing, this run completes with
// that url and calls nothing else. The claim and every write of state use
// contexts detached from the job's, so their outcome is never in doubt; rows
// that still end up stuck (a database outage) are finished by the sweeper.
func NewGenerateExpansion(deps ExpansionDeps) Handler {
	if deps.StaleAfter <= 0 {
		deps.StaleAfter = DefaultExpansionStaleAfter
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return func(ctx context.Context, args Args) error {
		return runGenerateExpansion(ctx, deps, args.ID)
	}
}

// detached is a short context for the writes that record state. It outlives the
// job's context: once a write may have reached the database, its outcome must
// be known (a cancelled claim could have committed), and a shutdown or deadline
// that ended the generation must not also lose its result.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), finalWriteTimeout)
}

func runGenerateExpansion(ctx context.Context, deps ExpansionDeps, id int64) error {
	log := deps.Logger.With("expansion_id", id)
	if err := ctx.Err(); err != nil {
		return err // nothing claimed: the queue releases or finishes the job
	}
	e, err := store.FindExpansion(ctx, deps.DB, id)
	if err != nil {
		return fmt.Errorf("find expansion %d: %w", id, err)
	}
	if e == nil {
		log.Warn("expansion job: expansion not found, nothing to do")
		return nil
	}

	var claimed bool
	e, claimed, err = claimExpansion(ctx, deps, log, e)
	if err != nil || !claimed {
		return err // not claimed: being worked on, finished, or the database failed
	}
	// From here this run owns the row: every exit records an outcome or releases it.

	if e.URL != nil && *e.URL != "" {
		// A previous run wrote the files and stored their url but died before
		// completing: finish with that result instead of generating another.
		log.Warn("expansion job: completing from the result of an earlier run")
		wctx, cancel := detached(ctx)
		defer cancel()
		return completeExpansion(wctx, deps, log, id, *e.URL)
	}

	url, procErr := process(ctx, deps.Processor, e)

	wctx, cancel := detached(ctx)
	defer cancel()
	if procErr == nil {
		return completeExpansion(wctx, deps, log, id, url)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		// Shutdown, not a failure of the expansion: give it back.
		if _, err := store.ReleaseExpansion(wctx, deps.DB, id); err != nil {
			log.Error("expansion job: could not release the expansion after shutdown; the sweeper finishes it", "err", err)
		}
		return procErr
	}
	logFailure(log, procErr)
	if err := failExpansion(wctx, deps, log, id, failureDetail(procErr)); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return procErr // deadline: let the Runner record the timeout
	}
	return nil
}

// claimExpansion takes over a stale processing row, claims the row and stamps
// job_started, all on detached contexts so the claim's outcome is never left
// to a cancelled context. It returns the row as of the claim.
func claimExpansion(ctx context.Context, deps ExpansionDeps, log *slog.Logger, e *store.Expansion) (*store.Expansion, bool, error) {
	cctx, cancel := detached(ctx)
	defer cancel()
	if reclaimed, err := store.ReclaimExpansion(cctx, deps.DB, e.ID, deps.StaleAfter); err != nil {
		return e, false, fmt.Errorf("reclaim expansion %d: %w", e.ID, err)
	} else if reclaimed {
		log.Warn("expansion job: took over an expansion left processing by a run that died")
	}
	claimed, err := store.ClaimExpansion(cctx, deps.DB, e.ID)
	if err != nil {
		return e, false, fmt.Errorf("claim expansion %d: %w", e.ID, err)
	}
	if !claimed {
		return e, false, nil
	}
	store.StampExpansion(cctx, deps.DB, e.ID, "job_started", store.NowMS())
	// Process reads the row it is given; reload so it sees the claim and stamp.
	if fresh, err := store.FindExpansion(cctx, deps.DB, e.ID); err == nil && fresh != nil {
		e = fresh
	}
	return e, true, nil
}

// completeExpansion records the result. A row that is already finished is left
// as it is; any other failure falls back to failing the expansion.
func completeExpansion(ctx context.Context, deps ExpansionDeps, log *slog.Logger, id int64, url string) error {
	err := store.CompleteExpansion(ctx, deps.DB, id, url)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrExpansionFinished):
		log.Info("expansion job: the expansion was already finished, result not recorded")
		return nil
	}
	log.Error("expansion job: could not record the result", "err", err)
	return failExpansion(ctx, deps, log, id, store.ExpansionFailedDetail)
}

// failExpansion records a failure unless the expansion is already finished.
func failExpansion(ctx context.Context, deps ExpansionDeps, log *slog.Logger, id int64, detail string) error {
	err := store.FailExpansion(ctx, deps.DB, id, detail)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrExpansionFinished):
		log.Info("expansion job: the expansion was already finished, failure not recorded")
		return nil
	}
	log.Error("expansion job: could not record the failure", "err", err)
	return fmt.Errorf("fail expansion %d: %w", id, err)
}

// logFailure logs the real error of a failed generation: its type, its text,
// the reader-facing detail it becomes, for a reader-facing error that hides one
// (a failed write) the cause, and for an LLM failure what llm.Diagnostics
// recovers (HTTP status and body, CLI exit code and stderr, the start of a
// rejected output, the error chain). Never the document, the selection or the
// question.
func logFailure(log *slog.Logger, err error) {
	attrs := []any{"err", err, "type", fmt.Sprintf("%T", err), "detail", failureDetail(err)}
	var ee *expansion.Error
	if errors.As(err, &ee) {
		if cause := errors.Unwrap(ee); cause != nil {
			attrs = append(attrs, "cause", cause.Error(), "cause_type", fmt.Sprintf("%T", cause))
		}
	}
	attrs = append(attrs, llm.Diagnostics(err)...)
	log.Error("expansion job failed", attrs...)
}

// process calls the processor, turning a panic into an error and logging its
// stack.
func process(ctx context.Context, p ExpansionProcessor, e *store.Expansion) (url string, err error) {
	defer func() {
		if r := recover(); r != nil {
			// The expansion's text is not logged; a panic value rarely quotes it.
			slog.Error("expansion job: panic in processor", "expansion_id", e.ID, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			url, err = "", fmt.Errorf("panic: %v", r)
		}
	}()
	return p.Process(ctx, e)
}

// failureDetail is the text the reader sees for a failed generation: the
// message of the errors written for readers (expansion, linker and files
// errors), a generic one for a failed LLM call, and a generic one for
// everything else, so no path, key or output ever reaches the page.
func failureDetail(err error) string {
	var ee *expansion.Error
	var le *linker.Error
	var fe *files.Error
	var xe *llm.ExpandError
	switch {
	case errors.As(err, &ee):
		return ee.Message
	case errors.As(err, &le):
		return le.Error()
	case errors.As(err, &fe):
		return fe.Message
	case errors.As(err, &xe):
		return msgGenerationFailed
	default:
		return store.ExpansionFailedDetail
	}
}
