package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"serve-html-markdown/internal/store"
	"serve-html-markdown/internal/video"
)

// ingestRunnable are the statuses a video may have for an ingest run to work on
// it (and for its failure to be recorded): a fresh row, a row an earlier run was
// cut short on, and a failed one (the retry a failed download or the
// re-authorization link promises), as long as no YouTube id is stored.
var ingestRunnable = []string{"downloading", "uploading", "failed"}

// errSuperseded: the row moved on while this run worked; whoever moved it owns it.
var errSuperseded = errors.New("twitter video moved on")

// keepRowError wraps an error after which the row must NOT be failed: the
// upload is recorded and the video is waiting for its captions.
type keepRowError struct{ error }

func (e keepRowError) Unwrap() error { return e.error }

// NewTwitterVideoIngest returns the handler of TwitterVideoIngestJob: download
// (or reuse) the video, upload it to YouTube unlisted, hand it over to the
// captions job.
//
// Flow and order of side effects, as in Ruby: status uploading; upload; ONE
// write with youtube_id, status awaiting_captions, upload_completed_at and
// caption_attempts 0; Slack "uploaded" notice; first caption check queued 5
// minutes out. Slack "downloaded" follows a fresh download. Any failure marks
// the video failed with a Slack notice (a dead OAuth grant gets the
// re-authorization link) and returns nil, as the Ruby rescue did.
//
// Safe to run again (queue.go: a crash or shutdown re-runs the job). What
// Ruby's job lacked, and the Go one adds:
//
//   - Guard: a video that already has a YouTube id, or is past the upload
//     (awaiting_captions, summarizing, publishing, done), is never downloaded or
//     uploaded again. If it is awaiting_captions with an id and no caption job
//     is pending, the first check is queued (the earlier run died or failed to
//     enqueue); otherwise the run does nothing. A failed row WITHOUT an id is a
//     retry and runs. (Retrying a failed row that already has an id is the
//     caller's decision: this handler leaves it alone rather than risk a
//     duplicate YouTube video.)
//   - The id is recorded in the single statement that moves the row to
//     awaiting_captions, before the Slack notice and the enqueue, on a context
//     detached from the job's, and never replaces an id already stored.
//   - Status writes are guarded by the current status, so a late writer cannot
//     move a row that finished or was failed meanwhile.
//   - A run cut short by shutdown (ctx cancelled) fails nothing: the error is
//     returned so the Runner releases the job. The row keeps downloading or
//     uploading and the re-run starts over from the file on disk. An upload
//     that was cut short before it returned an id is repeated, which is the one
//     window that remains (see video.YoutubeUploader.Upload: if Google accepted
//     the video but the answer was lost, a second video appears).
//   - If only the enqueue after the upload fails, the video is NOT failed (it
//     is uploaded and recorded): the error is returned, and the re-run or
//     RequeueStaleCaptionChecks queues the check.
//
// The deadline of Runner.Timeouts (RecommendedIngestTimeout) fails the video
// with the generic detail (Ruby had no timeout) and returns the error.
func NewTwitterVideoIngest(deps VideoDeps) Handler {
	deps = deps.withDefaults()
	return func(ctx context.Context, args Args) error {
		return runIngest(ctx, deps, args.ID)
	}
}

func runIngest(ctx context.Context, d VideoDeps, id int64) error {
	log := d.Logger.With("twitter_video_id", id, "job", "twitter_video_ingest")
	if err := ctx.Err(); err != nil {
		return err
	}
	v, err := store.FindTwitterVideo(ctx, d.DB, id)
	if err != nil {
		return fmt.Errorf("find twitter video %d: %w", id, err)
	}
	if v == nil {
		log.Warn("twitter video not found, skipping")
		return nil
	}

	if deref(v.YoutubeID) != "" || !isIngestRunnable(v.Status) {
		if v.Status == "awaiting_captions" && deref(v.YoutubeID) != "" {
			return ensureCaptionCheck(ctx, d, log, v)
		}
		log.Info("already past the upload, skipping", "status", v.Status, "youtube_id", deref(v.YoutubeID))
		return nil
	}

	log.Info("starting", "source_url", v.SourceURL)
	err = ingest(ctx, d, log, v)
	switch {
	case err == nil, errors.Is(err, errSuperseded):
		return nil
	}
	var keep keepRowError
	if errors.As(err, &keep) {
		log.Error("uploaded, but the first caption check could not be queued", "err", keep.error)
		return keep.error
	}
	return d.failRun(ctx, log, id, ingestRunnable, err, ingestFailureText(d, id, err))
}

func isIngestRunnable(status string) bool {
	for _, s := range ingestRunnable {
		if s == status {
			return true
		}
	}
	return false
}

// ingestFailureText is Ruby's failure_message: a dead OAuth grant gets a link, the
// rest the raw error text.
func ingestFailureText(d VideoDeps, id int64, err error) string {
	if errors.Is(err, video.ErrAuthorizationExpired) {
		return msgReauthRequired(id, video.YoutubeReauthURL(d.YoutubeReauthURL, d.YoutubeHost, fmt.Sprint(id)))
	}
	return msgIngestFailed(id, rubyErrorClass(err), err.Error())
}

// ensureCaptionCheck queues the first (or next) caption check of an uploaded
// video that has none pending.
func ensureCaptionCheck(ctx context.Context, d VideoDeps, log *slog.Logger, v *store.TwitterVideo) error {
	pending, err := HasPendingJob(ctx, d.DB, KindTwitterVideoCaptions, v.ID)
	if err != nil {
		return fmt.Errorf("look for a pending caption check: %w", err)
	}
	if pending {
		log.Info("already uploaded, a caption check is pending, nothing to do")
		return nil
	}
	wait, _ := v.NextCaptionWait(d.Now()) // exhausted or no upload time: run now, the captions job decides
	log.Warn("already uploaded but no caption check was pending, queueing it", "wait", wait)
	return d.enqueueCaptions(ctx, v.ID, wait)
}

func ingest(ctx context.Context, d VideoDeps, log *slog.Logger, v *store.TwitterVideo) error {
	file, title, reused, err := reuseExisting(ctx, d, log, v)
	if err != nil {
		return err
	}
	if !reused {
		if file, title, err = download(ctx, d, log, v); err != nil {
			return err
		}
	}

	wctx, cancel := detached(ctx)
	ok, err := store.TransitionTwitterVideo(wctx, d.DB, v.ID, ingestRunnable, store.TwitterVideoChanges{Status: new("uploading")})
	cancel()
	if err != nil {
		return err
	}
	if !ok {
		log.Warn("the video moved on while it was downloaded, not uploading")
		return errSuperseded
	}

	log.Info("uploading to YouTube (unlisted)")
	uploader, err := d.NewUploader(ctx)
	if err != nil {
		return err
	}
	uploadTitle := title
	if blankText(uploadTitle) {
		uploadTitle = fallbackTitle
	}
	youtubeID, err := uploader.Upload(ctx, file, uploadTitle, "")
	if err != nil {
		return err
	}

	// The video exists on YouTube now: record it before anything else.
	wctx, cancel = detached(ctx)
	recorded, err := store.RecordTwitterVideoUpload(wctx, d.DB, v.ID, youtubeID)
	cancel()
	if err != nil {
		log.Error("uploaded but could not record the YouTube id", "youtube_id", youtubeID, "err", err)
		return fmt.Errorf("uploaded as YouTube video %s but could not record it: %w", youtubeID, err)
	}
	if !recorded {
		log.Error("a duplicate YouTube video was uploaded; the video already has another id", "youtube_id", youtubeID)
		d.failure(ctx, fmt.Sprintf("[twitter-video #%d] uploaded a duplicate YouTube video (%s): the video already has another id", v.ID, youtubeID))
		return errSuperseded
	}
	log.Info("uploaded", "youtube_id", youtubeID)
	d.success(ctx, msgUploaded(v.ID, title, youtubeID))

	wait := time.Duration(v.Checkpoints()[0]) * time.Minute
	log.Info("scheduling the first caption check", "wait", wait)
	if err := d.enqueueCaptions(ctx, v.ID, wait); err != nil {
		return keepRowError{err}
	}
	return nil
}

// download fetches the video into VideosDir and records its path and title.
func download(ctx context.Context, d VideoDeps, log *slog.Logger, v *store.TwitterVideo) (path, title string, err error) {
	log.Info("downloading via yt-dlp", "dir", d.VideosDir)
	got, err := d.Ytdlp.Download(ctx, v.SourceURL, d.VideosDir)
	if err != nil {
		return "", "", err
	}
	path, title = cleanText(got.Path), cleanText(got.Title)
	wctx, cancel := detached(ctx)
	defer cancel()
	if err := store.UpdateTwitterVideo(wctx, d.DB, v.ID, store.TwitterVideoChanges{YoutubeTitle: &title, VideoPath: &path}); err != nil {
		return "", "", err
	}
	log.Info("downloaded", "title", title, "path", path)
	d.success(ctx, msgDownloaded(v.ID, title))
	return path, title, nil
}

// reuseExisting looks for a download of the same tweet that is still on disk:
// the row's own first, then the siblings newest first. The path is verified (see
// regularFileUnder). It returns the path to upload and the title.
func reuseExisting(ctx context.Context, d VideoDeps, log *slog.Logger, v *store.TwitterVideo) (path, title string, reused bool, err error) {
	siblings, err := store.DownloadedSiblings(ctx, d.DB, v)
	if err != nil {
		return "", "", false, err
	}
	for _, s := range siblings {
		real, ok := regularFileUnder(d.VideosDir, deref(s.VideoPath))
		if !ok {
			continue
		}
		title = deref(v.YoutubeTitle)
		if blankText(title) {
			title = deref(s.YoutubeTitle)
		}
		if blankText(title) {
			title = ""
		}
		changes := store.TwitterVideoChanges{VideoPath: s.VideoPath}
		if title != "" {
			changes.YoutubeTitle = &title
		}
		wctx, cancel := detached(ctx)
		err := store.UpdateTwitterVideo(wctx, d.DB, v.ID, changes)
		cancel()
		if err != nil {
			return "", "", false, err
		}
		log.Info("reusing an earlier download", "from", s.ID)
		return real, title, true, nil
	}
	return "", "", false, nil
}
