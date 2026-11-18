package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
const (
	msgGenerationFailed = "Generation failed."
	msgExpansionFailed  = "Expansion failed."
)

// ExpansionDeps are what the generate_expansion handler needs.
type ExpansionDeps struct {
	DB        store.DB
	Processor ExpansionProcessor
	// StaleAfter defaults to DefaultExpansionStaleAfter.
	StaleAfter time.Duration
	// Logger defaults to slog.Default().
	Logger *slog.Logger
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
// than StaleAfter (store.ReclaimExpansion), tried before the claim.
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

func runGenerateExpansion(ctx context.Context, deps ExpansionDeps, id int64) error {
	log := deps.Logger.With("expansion_id", id)
	e, err := store.FindExpansion(ctx, deps.DB, id)
	if err != nil {
		return fmt.Errorf("find expansion %d: %w", id, err)
	}
	if e == nil {
		log.Warn("expansion job: expansion not found, nothing to do")
		return nil
	}
	if reclaimed, err := store.ReclaimExpansion(ctx, deps.DB, id, deps.StaleAfter); err != nil {
		return fmt.Errorf("reclaim expansion %d: %w", id, err)
	} else if reclaimed {
		log.Warn("expansion job: took over an expansion left processing by a run that died")
	}
	claimed, err := store.ClaimExpansion(ctx, deps.DB, id)
	if err != nil {
		return fmt.Errorf("claim expansion %d: %w", id, err)
	}
	if !claimed {
		return nil // being worked on, or already finished
	}
	store.StampExpansion(ctx, deps.DB, id, "job_started", store.NowMS())
	// Process reads the row it is given; reload so it sees the claim and stamp.
	if fresh, err := store.FindExpansion(ctx, deps.DB, id); err == nil && fresh != nil {
		e = fresh
	}

	url, procErr := process(ctx, deps.Processor, e)

	// The outcome is written on a context that survives the job's.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalWriteTimeout)
	defer cancel()

	if procErr == nil {
		if err := store.CompleteExpansion(wctx, deps.DB, id, url); err != nil {
			log.Error("expansion job: could not record the result", "err", err)
			return failExpansion(wctx, deps, log, id, msgExpansionFailed)
		}
		return nil
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		// Shutdown, not a failure of the expansion: give it back.
		if _, err := store.ReleaseExpansion(wctx, deps.DB, id); err != nil {
			log.Error("expansion job: could not release the expansion after shutdown; it is retried once stale", "err", err)
		}
		return procErr
	}
	log.Error("expansion job failed", "err", procErr, "type", fmt.Sprintf("%T", procErr))
	if err := failExpansion(wctx, deps, log, id, failureDetail(procErr)); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return procErr // deadline: let the Runner record the timeout
	}
	return nil
}

func failExpansion(ctx context.Context, deps ExpansionDeps, log *slog.Logger, id int64, detail string) error {
	if err := store.FailExpansion(ctx, deps.DB, id, detail); err != nil {
		log.Error("expansion job: could not record the failure", "err", err)
		return fmt.Errorf("fail expansion %d: %w", id, err)
	}
	return nil
}

// process calls the processor, turning a panic into an error.
func process(ctx context.Context, p ExpansionProcessor, e *store.Expansion) (url string, err error) {
	defer func() {
		if r := recover(); r != nil {
			// The panic value is not logged by the caller (it could quote
			// the document); log it here without the expansion's text.
			slog.Error("expansion job: panic in processor", "expansion_id", e.ID, "panic", fmt.Sprint(r))
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
		return msgExpansionFailed
	}
}
