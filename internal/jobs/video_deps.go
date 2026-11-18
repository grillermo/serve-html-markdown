package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"serve-html-markdown/internal/files"
	"serve-html-markdown/internal/llm"
	"serve-html-markdown/internal/store"
	"serve-html-markdown/internal/video"
)

// The collaborators of the two video jobs. They replace the `class << self`
// lambdas (TwitterVideoIngestJob.ytdlp = ...) of the Ruby jobs: the production
// types satisfy them, tests pass fakes.

// Ytdlp downloads videos and fetches captions: *video.YtdlpClient.
type Ytdlp interface {
	Download(ctx context.Context, url, destDir string) (video.Downloaded, error)
	FetchAutoSubs(ctx context.Context, youtubeURL, destDir, cookies string) (string, error)
}

// CookieExporter writes the signed-in cookies file for yt-dlp: *video.EgoCookies.
type CookieExporter interface {
	Export(ctx context.Context, destPath string) (string, error)
}

// Uploader puts a video on YouTube, unlisted: *video.YoutubeUploader.
type Uploader interface {
	Upload(ctx context.Context, filePath, title, description string) (string, error)
}

// Summarizer turns captions into an article: *llm.Summarizer.
type Summarizer interface {
	Summarize(ctx context.Context, captions string) (llm.Summary, error)
}

// LinkPublisher pushes a link to Rulinky and returns its id: *video.RulinkyClient.
type LinkPublisher interface {
	CreateLink(ctx context.Context, link, note string) (string, error)
}

// Notifier posts to Slack: *video.SlackNotifier. It never fails.
type Notifier interface {
	Success(ctx context.Context, text string)
	Failure(ctx context.Context, text string)
}

// PageWriter writes a summary as a served page and returns its file name:
// *files.HTMLWriter (which also records it in served_files).
type PageWriter interface {
	Write(ctx context.Context, title, summaryHTML string) (string, error)
}

var (
	_ Ytdlp          = (*video.YtdlpClient)(nil)
	_ CookieExporter = (*video.EgoCookies)(nil)
	_ Uploader       = (*video.YoutubeUploader)(nil)
	_ Summarizer     = (*llm.Summarizer)(nil)
	_ LinkPublisher  = (*video.RulinkyClient)(nil)
	_ Notifier       = (*video.SlackNotifier)(nil)
	_ PageWriter     = (*files.HTMLWriter)(nil)
)

// Recommended Runner.Timeouts, to be passed by the serve wiring (see
// VideoTimeouts). The queue's heartbeat keeps a hung job locked forever, so the
// kinds that call out need a deadline. Ingest: a download (the yt-dlp client has
// its own 30 minute limit) plus an upload of a large file; captions: one fetch
// (3 minutes), one summary and two HTTP calls. Both are far below
// DefaultVideoSweepAfter.
const (
	RecommendedIngestTimeout   = 2 * time.Hour
	RecommendedCaptionsTimeout = 15 * time.Minute
)

// VideoTimeouts is the Runner.Timeouts entries for the two video kinds; merge
// it with those of other kinds.
func VideoTimeouts() map[string]time.Duration {
	return map[string]time.Duration{
		KindTwitterVideoIngest:   RecommendedIngestTimeout,
		KindTwitterVideoCaptions: RecommendedCaptionsTimeout,
	}
}

// Defaults of VideoDeps.
const (
	// DefaultVideoStaleAfter is how long a video may sit in summarizing or
	// publishing before the next run of its captions job takes it over (see
	// store.ReclaimTwitterVideo). Half the queue's stale-lock window, like
	// DefaultExpansionStaleAfter.
	DefaultVideoStaleAfter = 15 * time.Minute
	// DefaultVideoSweepAfter is how long a video may sit in downloading,
	// uploading, summarizing or publishing before the sweeper fails it. It is
	// longer than the ingest timeout (2 hours) and the queue's stale window.
	DefaultVideoSweepAfter = 3 * time.Hour
	// DefaultCaptionRequeueGrace is how late a caption check must be, with no
	// job queued for it, before the sweeper queues it again.
	DefaultCaptionRequeueGrace = time.Hour
)

// VideoDeps are what the twitter_video_ingest and twitter_video_captions
// handlers need.
type VideoDeps struct {
	DB store.DB
	// Enqueuer schedules the caption checks; defaults to DBEnqueuer{DB}.
	Enqueuer Enqueuer
	// Notifier defaults to one that sends nothing.
	Notifier Notifier

	Ytdlp   Ytdlp
	Cookies CookieExporter
	// NewUploader builds the uploader of one ingest run. Ruby built it per run
	// from the database's refresh token; see UploaderSource. An error from it
	// (a missing token is video.ErrAuthorizationExpired) is treated like an
	// error of the upload.
	NewUploader func(ctx context.Context) (Uploader, error)
	Summarizer  Summarizer
	Rulinky     LinkPublisher
	Writer      PageWriter

	// VideosDir is the permanent store of downloads (config VideosDir).
	VideosDir string
	// Host is the public host of the served pages (config Host; Ruby's ENV
	// HOST, "localhost" by default): the link pushed to Rulinky is
	// https://<Host>/<page>.
	Host string
	// YoutubeHost and YoutubeReauthURL build the re-authorization link in the
	// Slack message (config YoutubeHost, YoutubeReauthURL).
	YoutubeHost, YoutubeReauthURL string

	// Now defaults to time.Now.
	Now func() time.Time
	// StaleAfter defaults to DefaultVideoStaleAfter; SweepAfter to
	// DefaultVideoSweepAfter; RequeueGrace to DefaultCaptionRequeueGrace.
	StaleAfter, SweepAfter, RequeueGrace time.Duration
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

type noNotifier struct{}

func (noNotifier) Success(context.Context, string) {}
func (noNotifier) Failure(context.Context, string) {}

func (d VideoDeps) withDefaults() VideoDeps {
	if d.Enqueuer == nil {
		d.Enqueuer = DBEnqueuer{DB: d.DB}
	}
	if d.Notifier == nil {
		d.Notifier = noNotifier{}
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.StaleAfter <= 0 {
		d.StaleAfter = DefaultVideoStaleAfter
	}
	if d.SweepAfter <= 0 {
		d.SweepAfter = DefaultVideoSweepAfter
	}
	if d.RequeueGrace <= 0 {
		d.RequeueGrace = DefaultCaptionRequeueGrace
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return d
}

// VideoHandlers is the kind-to-handler map for the two video kinds, for the
// Runner (merge it with the other kinds' maps).
func VideoHandlers(deps VideoDeps) map[string]Handler {
	return map[string]Handler{
		KindTwitterVideoIngest:   NewTwitterVideoIngest(deps),
		KindTwitterVideoCaptions: NewTwitterVideoCaptions(deps),
	}
}

// VideoSweepers is the sibling of VideoHandlers for Runner.Sweepers: it fails
// the videos nothing is working on any more and queues again the caption checks
// that were lost.
func VideoSweepers(deps VideoDeps) []func(context.Context) error {
	deps = deps.withDefaults()
	return []func(context.Context) error{FailStaleVideos(deps), RequeueStaleCaptionChecks(deps)}
}

// FailStaleVideos is the sweeper that fails videos stuck in downloading,
// uploading, summarizing or publishing for SweepAfter (store.FailStaleTwitterVideos).
// awaiting_captions is left alone: it legitimately waits up to two days.
func FailStaleVideos(deps VideoDeps) func(context.Context) error {
	deps = deps.withDefaults()
	return func(ctx context.Context) error {
		n, err := store.FailStaleTwitterVideos(ctx, deps.DB, deps.SweepAfter)
		if err == nil && n > 0 {
			deps.Logger.Warn("video sweeper: failed stuck videos", "count", n)
		}
		return err
	}
}

// maxSweptCaptionVideos bounds one pass of RequeueStaleCaptionChecks.
const maxSweptCaptionVideos = 500

// RequeueStaleCaptionChecks is the sweeper that queues the caption check of a
// video that is awaiting captions, is overdue by more than RequeueGrace and has
// no caption job pending (queued or running). That heals a check lost to a failed
// enqueue (the handlers return the error and leave the row awaiting) or a
// deleted job row. A duplicate it may rarely cause (a check queued between the
// pending test and the insert) is harmless: see NewTwitterVideoCaptions.
//
// What it does not cover: a video stuck in downloading/uploading is failed by
// FailStaleVideos instead (the user resubmits it), because only the ingest
// job could resume it and it may have died mid-upload.
func RequeueStaleCaptionChecks(deps VideoDeps) func(context.Context) error {
	deps = deps.withDefaults()
	return func(ctx context.Context) error {
		videos, err := store.AwaitingCaptionVideos(ctx, deps.DB, maxSweptCaptionVideos)
		if err != nil {
			return err
		}
		now := deps.Now()
		var errs []error
		for i := range videos {
			v := &videos[i]
			if !captionCheckOverdue(v, now, deps.RequeueGrace) {
				continue
			}
			pending, err := HasPendingJob(ctx, deps.DB, KindTwitterVideoCaptions, v.ID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if pending {
				continue
			}
			if err := deps.Enqueuer.Enqueue(ctx, KindTwitterVideoCaptions, Args{ID: v.ID}, 0); err != nil {
				errs = append(errs, err)
				continue
			}
			deps.Logger.Warn("video sweeper: queued a lost caption check", "twitter_video_id", v.ID)
		}
		return errors.Join(errs...)
	}
}

// captionCheckOverdue reports whether the next check of v was due more than
// grace ago. With the checkpoints used up it is due since the last one (a run
// fails the video); a video without an upload time has no schedule, so its
// last write counts.
func captionCheckOverdue(v *store.TwitterVideo, now time.Time, grace time.Duration) bool {
	if !v.HasUploadTime() {
		return now.Sub(v.UpdatedAt) > grace
	}
	checkpoints := v.Checkpoints()
	idx := max(0, min(v.CaptionAttempts, len(checkpoints)-1))
	due := v.UploadCompletedAt.Add(time.Duration(checkpoints[idx]) * time.Minute)
	return now.Sub(due) > grace
}

// UploaderSource is NewUploader for production: like Ruby's default uploader it
// takes the refresh token from the database (store.YoutubeRefreshToken), with
// the YOUTUBE_REFRESH_TOKEN value as the bootstrap fallback. The error of a
// missing token is video.ErrAuthorizationExpired, so the ingest job answers it
// with the re-authorization link.
func UploaderSource(db store.DB, clientID, clientSecret, envRefreshToken string) func(ctx context.Context) (Uploader, error) {
	return func(ctx context.Context) (Uploader, error) {
		token, err := store.YoutubeRefreshToken(ctx, db, envRefreshToken)
		if err != nil {
			return nil, err
		}
		u, err := video.NewYoutubeUploader(clientID, clientSecret, token)
		if err != nil {
			return nil, err
		}
		return u, nil
	}
}

// NewLinkPublisher returns the Rulinky client for the settings. Ruby built the
// client when the job ran, so an unconfigured Rulinky failed the video at the
// push, not at boot: the same here, the returned publisher answers every call
// with the configuration error.
func NewLinkPublisher(host, token string) LinkPublisher {
	c, err := video.NewRulinkyClient(host, token)
	if err != nil {
		return failingPublisher{err}
	}
	return c
}

type failingPublisher struct{ err error }

func (f failingPublisher) CreateLink(context.Context, string, string) (string, error) {
	return "", f.err
}

// --- messages ---------------------------------------------------------------
//
// Copied from the Ruby jobs; TestMessagesMatchTheRubySource compares every
// fragment with the Ruby source.

const (
	// studioEditURL is where the uploaded (private-by-default) video is made unlisted.
	studioEditURL = "https://studio.youtube.com/video/%s/edit"
	// fallbackTitle is the YouTube title of a download that has none.
	fallbackTitle = "Twitter video"
	// msgCaptionsNeverAvailable is the error_detail of a video whose checkpoints ran out.
	msgCaptionsNeverAvailable = "Auto-captions never became available."
	// msgUploadTimeMissing replaces Ruby's NoMethodError on a nil upload_completed_at.
	msgUploadTimeMissing = "Upload completion time is missing."
)

func msgDownloaded(id int64, title string) string {
	return fmt.Sprintf("[twitter-video #%d] downloaded: %s", id, title)
}

func msgUploaded(id int64, title, youtubeID string) string {
	return fmt.Sprintf("[twitter-video #%d] uploaded: %s\nset it to unlisted: %s", id, title, fmt.Sprintf(studioEditURL, youtubeID))
}

func msgIngestFailed(id int64, class, message string) string {
	return fmt.Sprintf("[twitter-video #%d] ingest failed: %s: %s", id, class, message)
}

// msgReauthRequired is the one failure a human can fix from a phone, so it gets
// a link instead of a stack trace.
func msgReauthRequired(id int64, reauthURL string) string {
	return fmt.Sprintf("[twitter-video #%d] ingest failed: YouTube authorization expired.\n"+
		"re-authorize: %s\n"+
		"the video is already downloaded — the upload retries by itself once you're done.", id, reauthURL)
}

func msgCaptionsFailed(id int64, class, message string) string {
	return fmt.Sprintf("[twitter-video #%d] captions/publish failed: %s: %s", id, class, message)
}

func msgCaptionsTimedOut(id int64, after string) string {
	return fmt.Sprintf("[twitter-video #%d] captions timed out after %s", id, after)
}

func msgCaptionsNotReady(id int64, attempt int) string {
	return fmt.Sprintf("[twitter-video #%d] captions not ready (attempt %d); retrying", id, attempt)
}

func msgSummarized(id int64, title string) string {
	return fmt.Sprintf("[twitter-video #%d] summarized: %s", id, title)
}

func msgPublished(id int64, link string) string {
	return fmt.Sprintf("[twitter-video #%d] published: %s", id, link)
}

// publishedLink is the public address of a written page.
func publishedLink(host, filename string) string {
	return "https://" + host + "/" + filename
}

// minutesInspect is Ruby's n.minutes.inspect (ActiveSupport::Duration keeps
// minutes as minutes): "1 minute", "2880 minutes".
func minutesInspect(n int) string {
	if n == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", n)
}

// --- error taxonomy ----------------------------------------------------------

// detailError is a failure with a reader-safe text of this package's own.
type detailError struct{ msg string }

func (e *detailError) Error() string { return e.msg }

// rubyErrorClass names the class Ruby would have raised, for the Slack text
// ("#{error.class}: #{error.message}").
func rubyErrorClass(err error) string {
	var (
		ytdlp  *video.YtdlpError
		yt     *video.YoutubeError
		ego    *video.EgoCookiesError
		rulink *video.RulinkyError
		openai *llm.Error
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
	}
	return "StandardError"
}

// videoFailureDetail is the error_detail stored for a failed video, which the public
// status endpoint shows. The errors the service clients write for that purpose
// (Ruby: their messages) are stored as they are; anything else, an exec start
// failure or a database error, could quote a path, so it is stored as the
// generic text and the real error goes to the log and to Slack only.
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

// --- small helpers -----------------------------------------------------------

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func blankText(s string) bool { return strings.TrimSpace(s) == "" }

// cleanText makes yt-dlp output storable: no NUL bytes, valid UTF-8.
func cleanText(s string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "�")
}

// regularFileUnder resolves path (symbolic links included) and returns the
// resolved path if it is a regular file inside root. video_path comes from the
// database, so it is not trusted: a relative path, a link out of root, a
// directory or a ".." walk is refused.
func regularFileUnder(root, path string) (string, bool) {
	if path == "" || root == "" || !filepath.IsAbs(path) {
		return "", false
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(realRoot, real)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	info, err := os.Stat(real)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return real, true
}

// notify and enqueue run on detached contexts: a notice about, or the next step
// after, something that already happened must not be lost to the end of the
// job's own context.

func (d VideoDeps) success(ctx context.Context, text string) {
	wctx, cancel := detached(ctx)
	defer cancel()
	d.Notifier.Success(wctx, text)
}

func (d VideoDeps) failure(ctx context.Context, text string) {
	wctx, cancel := detached(ctx)
	defer cancel()
	d.Notifier.Failure(wctx, text)
}

func (d VideoDeps) enqueueCaptions(ctx context.Context, id int64, wait time.Duration) error {
	wctx, cancel := detached(ctx)
	defer cancel()
	return d.Enqueuer.Enqueue(wctx, KindTwitterVideoCaptions, Args{ID: id}, wait)
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
	log.Error("video job failed", "err", err, "class", rubyErrorClass(err))
	wctx, cancel := detached(ctx)
	defer cancel()
	ok, ferr := store.FailTwitterVideoFrom(wctx, d.DB, id, from, videoFailureDetail(err))
	if ferr != nil {
		log.Error("video job: could not record the failure; the sweeper fails the video", "err", ferr)
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
