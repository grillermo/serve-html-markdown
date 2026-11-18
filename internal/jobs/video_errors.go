package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"

	"serve-html-markdown/internal/llm"
	"serve-html-markdown/internal/store"
	"serve-html-markdown/internal/video"
)

// detailError is a failure with a reader-safe text of this package's own.
type detailError struct{ msg string }

func (e *detailError) Error() string { return e.msg }

// panicError is a panic of a collaborator turned into an error. Its text is only
// for the log and Slack: the stored detail is the generic one.
type panicError struct{ value string }

func (e *panicError) Error() string { return "panic: " + e.value }

// recovered turns the value of recover() into a panicError and logs the stack.
func recovered(log *slog.Logger, r any) error {
	log.Error("video job: panic in a collaborator", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
	return &panicError{value: fmt.Sprint(r)}
}

// rubyErrorClass names the class Ruby would have raised, for the Slack text
// ("#{error.class}: #{error.message}"). An error with no Ruby counterpart gets
// the Go type of its root cause (for instance "*fs.PathError"), "panic" for a
// panic.
func rubyErrorClass(err error) string {
	var (
		ytdlp  *video.YtdlpError
		yt     *video.YoutubeError
		ego    *video.EgoCookiesError
		rulink *video.RulinkyError
		openai *llm.Error
		pe     *panicError
	)
	switch {
	case errors.As(err, &ytdlp):
		return "YtdlpClient::Error"
	case errors.As(err, &yt):
		if yt.Expired {
			return "YoutubeUploader::AuthorizationExpired"
		}
		return "YoutubeUploader::Error"
	case errors.As(err, &ego):
		return "EgoCookies::Error"
	case errors.As(err, &rulink):
		if rulink.Config {
			return "RulinkyClient::ConfigurationError"
		}
		return "RulinkyClient::Error"
	case errors.As(err, &openai):
		return "OpenaiSummarizer::Error"
	case errors.Is(err, llm.ErrConfiguration):
		return "OpenaiSummarizer::ConfigurationError"
	case errors.As(err, &pe):
		return "panic"
	}
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return fmt.Sprintf("%T", err)
		}
		err = next
	}
}

// videoFailureDetail is the error_detail stored for a failed video, which the public
// status endpoint shows. The errors the service clients write for that purpose
// (Ruby: their messages) are stored as they are; anything else, an exec start
// failure, a panic or a database error, could quote a path, so it is stored as
// the generic text and the real error goes to the log and to Slack only.
func videoFailureDetail(err error) string {
	var (
		ytdlp  *video.YtdlpError
		yt     *video.YoutubeError
		ego    *video.EgoCookiesError
		rulink *video.RulinkyError
		openai *llm.Error
		own    *detailError
	)
	switch {
	case errors.As(err, &ytdlp):
		return ytdlp.Error()
	case errors.As(err, &yt):
		return yt.Error()
	case errors.As(err, &ego):
		return ego.Error()
	case errors.As(err, &rulink):
		return rulink.Error()
	case errors.As(err, &openai):
		return openai.Error()
	case errors.As(err, &own):
		return own.Error()
	case errors.Is(err, llm.ErrConfiguration):
		return llm.ErrConfiguration.Error()
	case errors.Is(err, video.ErrInvalidURL):
		return video.ErrInvalidURL.Error()
	}
	return store.TwitterVideoFailedDetail
}

// errorChain describes err and every error it wraps, type and text each
// ("*fmt.wrapError: ... -> *fs.PathError: ..."), for the log. The service
// clients' messages never hold secrets (see their docs), so the chain is safe to log.
func errorChain(err error) string {
	var parts []string
	for i := 0; err != nil && i < 8; i++ {
		text := err.Error()
		if len(text) > 500 {
			text = text[:500] + "…"
		}
		parts = append(parts, fmt.Sprintf("%T: %s", err, text))
		err = errors.Unwrap(err)
	}
	return strings.Join(parts, " -> ")
}

// failRun records the failure of a run the way Ruby's rescue blocks do (fail the
// video, tell Slack), with these differences, which the idempotency of the
// handlers needs:
//
//   - The context was cancelled (shutdown): nothing is recorded and err is
//     returned, so the Runner releases the job; the caller has already put the
//     row where a re-run can pick it up.
//   - The write is guarded by status (from): a row that moved on, say finished by
//     a duplicate run, is not failed and no failure is announced.
//   - When the context's deadline passed, err is returned after the failure is
//     recorded, so the Runner notes the timeout.
func (d VideoDeps) failRun(ctx context.Context, log *slog.Logger, id int64, from []string, err error, slackText string) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return err
	}
	log.Error("video job failed", "err", err, "class", rubyErrorClass(err), "chain", errorChain(err))
	wctx, cancel := detached(ctx)
	defer cancel()
	ok, ferr := store.FailTwitterVideoFrom(wctx, d.DB, id, from, videoFailureDetail(err))
	if ferr != nil {
		log.Error("video job: could not record the failure; the sweeper fails the video", "err", ferr, "chain", errorChain(ferr))
		d.failure(ctx, slackText)
		return fmt.Errorf("fail twitter video %d: %w", id, ferr)
	}
	if !ok {
		log.Warn("video job: the video moved on meanwhile, failure not recorded")
		return nil
	}
	d.failure(ctx, slackText)
	if ctx.Err() != nil {
		return err // deadline: let the Runner record the timeout
	}
	return nil
}
