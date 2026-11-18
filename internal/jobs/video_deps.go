package jobs

import (
	"context"
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
// (3 minutes), one summary and two HTTP calls. The handlers enforce the same
// limits themselves (VideoDeps.IngestTimeout, CaptionsTimeout); both are far
// below DefaultVideoSweepAfter.
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
	// store.ReclaimTwitterVideo): the captions timeout plus 5 minutes, so a run
	// that is merely slow is never taken over, and below the queue's 30 minute
	// stale-lock window.
	DefaultVideoStaleAfter = RecommendedCaptionsTimeout + 5*time.Minute
	// DefaultVideoSweepAfter is how long a video may sit in downloading,
	// uploading, summarizing or publishing before the sweeper fails it. It is
	// longer than the ingest timeout (2 hours) and the queue's stale window.
	DefaultVideoSweepAfter = 3 * time.Hour
	// DefaultCaptionRequeueGrace is how late a caption check must be, with no
	// job queued for it, before the sweeper queues it again.
	DefaultCaptionRequeueGrace = time.Hour
	// DefaultIngestLease is how long an ingest run keeps a video without
	// touching it before another run may take it over (see
	// store.ClaimTwitterVideoForIngest). The run touches the video every
	// lease/5, so only a dead run loses it.
	DefaultIngestLease = 10 * time.Minute
)

// VideoDeps are what the twitter_video_ingest and twitter_video_captions
// handlers need. Validate lists what is required.
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

	// VideosDir is the permanent store of downloads (config VideosDir), an
	// absolute path. As in Ruby, downloads, and the .part files of an
	// interrupted yt-dlp, stay there for good: a retry reuses the file instead of
	// downloading again, and nothing cleans it up.
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
	// IngestTimeout and CaptionsTimeout bound one run of each handler, whatever
	// the Runner does; they default to RecommendedIngestTimeout and
	// RecommendedCaptionsTimeout.
	IngestTimeout, CaptionsTimeout time.Duration
	// IngestLease defaults to DefaultIngestLease.
	IngestLease time.Duration
	// StaleAfter defaults to CaptionsTimeout + 5 minutes (DefaultVideoStaleAfter);
	// SweepAfter to DefaultVideoSweepAfter; RequeueGrace to DefaultCaptionRequeueGrace.
	StaleAfter, SweepAfter, RequeueGrace time.Duration
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

type noNotifier struct{}

func (noNotifier) Success(context.Context, string) {}
func (noNotifier) Failure(context.Context, string) {}

// Validate reports every required field that is missing: DB, Ytdlp, Cookies,
// NewUploader, Summarizer, Rulinky, Writer and a non-empty absolute VideosDir.
// Notifier, Enqueuer, Now and the time knobs have defaults.
func (d VideoDeps) Validate() error { return d.validate(true, true) }

func (d VideoDeps) validate(ingest, captions bool) error {
	var missing []string
	need := func(ok bool, what string) {
		if !ok {
			missing = append(missing, what)
		}
	}
	need(d.DB != nil, "DB")
	if ingest {
		need(d.Ytdlp != nil, "Ytdlp")
		need(d.NewUploader != nil, "NewUploader")
		switch {
		case d.VideosDir == "":
			missing = append(missing, "VideosDir")
		case !filepath.IsAbs(d.VideosDir):
			missing = append(missing, "VideosDir (must be absolute)")
		}
	}
	if captions {
		need(d.Ytdlp != nil, "Ytdlp")
		need(d.Cookies != nil, "Cookies")
		need(d.Summarizer != nil, "Summarizer")
		need(d.Rulinky != nil, "Rulinky")
		need(d.Writer != nil, "Writer")
	}
	// Ytdlp may have been listed twice (both handlers need it).
	seen := map[string]bool{}
	uniq := missing[:0]
	for _, m := range missing {
		if !seen[m] {
			seen[m] = true
			uniq = append(uniq, m)
		}
	}
	if len(uniq) > 0 {
		return fmt.Errorf("jobs: VideoDeps is incomplete, missing: %s", strings.Join(uniq, ", "))
	}
	return nil
}

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
	if d.IngestTimeout <= 0 {
		d.IngestTimeout = RecommendedIngestTimeout
	}
	if d.CaptionsTimeout <= 0 {
		d.CaptionsTimeout = RecommendedCaptionsTimeout
	}
	if d.IngestLease <= 0 {
		d.IngestLease = DefaultIngestLease
	}
	if d.StaleAfter <= 0 {
		d.StaleAfter = d.CaptionsTimeout + 5*time.Minute
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
// Runner (merge it with the other kinds' maps). It returns the error of
// Validate.
func VideoHandlers(deps VideoDeps) (map[string]Handler, error) {
	if err := deps.Validate(); err != nil {
		return nil, err
	}
	return map[string]Handler{
		KindTwitterVideoIngest:   NewTwitterVideoIngest(deps),
		KindTwitterVideoCaptions: NewTwitterVideoCaptions(deps),
	}, nil
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
// job's own context. A notifier that panics must not break the job either: the
// panic is logged and swallowed, like a failed post. An enqueuer that panics
// is an enqueue error.

func (d VideoDeps) success(ctx context.Context, text string) {
	d.notify(ctx, text, d.Notifier.Success)
}

func (d VideoDeps) failure(ctx context.Context, text string) {
	d.notify(ctx, text, d.Notifier.Failure)
}

func (d VideoDeps) notify(ctx context.Context, text string, send func(context.Context, string)) {
	wctx, cancel := detached(ctx)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			recovered(d.Logger, r)
		}
	}()
	send(wctx, text)
}

func (d VideoDeps) enqueueCaptions(ctx context.Context, id int64, wait time.Duration) (err error) {
	wctx, cancel := detached(ctx)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			err = recovered(d.Logger, r)
		}
	}()
	return d.Enqueuer.Enqueue(wctx, KindTwitterVideoCaptions, Args{ID: id}, wait)
}
