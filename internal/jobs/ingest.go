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

// ingestHeld are the statuses of a video an ingest run holds.
var ingestHeld = []string{"downloading", "uploading"}

// errSuperseded: the row moved on while this run worked; whoever moved it owns it.
var errSuperseded = errors.New("twitter video moved on")

// keepRowError wraps an error after which the row must NOT be failed: the
// upload is recorded and the video is waiting for its captions.
type keepRowError struct{ error }

func (e keepRowError) Unwrap() error { return e.error }

// recordRetryDelay is the pause before the first retry of the write that
// records an upload; the second retry waits twice as long. A variable for tests.
var recordRetryDelay = 100 * time.Millisecond

// NewTwitterVideoIngest returns the handler of TwitterVideoIngestJob: download
// (or reuse) the video, upload it to YouTube unlisted, hand it over to the
// captions job. It panics if deps lack what ingest needs (see
// VideoDeps.Validate; VideoHandlers returns the error instead).
//
// Flow and order of side effects, as in Ruby: status uploading; upload; ONE
// write with youtube_id, status awaiting_captions, upload_completed_at and
// caption_attempts 0; Slack "uploaded" notice; first caption check queued 5
// minutes out. Slack "downloaded" follows a fresh download. Any failure marks
// the video failed with a Slack notice (a dead OAuth grant gets the
// re-authorization link) and returns nil, as the Ruby rescue did. A panic of a
// collaborator is such a failure too (generic detail, stack in the log).
//
// # Safe to run again, and twice at once
//
// What Ruby's job lacked, and the Go one adds:
//
//   - Lease. Before it works, a run claims the video
//     (store.ClaimTwitterVideoForIngest): atomically, and only if the video has
//     no YouTube id and is failed (a retry; it becomes downloading), or
//     downloading/uploading and not held. "Held" is the video's own updated_at:
//     a run touches it every IngestLease/5 while it works, and releases the claim
//     when the shutdown grace cuts it short, so only a dead run's lease runs out
//     (IngestLease, 10 minutes). A run that finds the video held does nothing
//     and returns nil: the holder's own job finishes it (and if the holder
//     dies, the queue hands that job out again after its stale window). This
//     closes the races of a resubmit while a download runs and of a
//     re-authorization retry, across processes too, and needs no column or
//     advisory lock. Residual windows: (1) a holder that stalls for longer than
//     the lease without dying (the database unreachable for 10 minutes) can be
//     joined by a second run; the guarded writes below still stop a second
//     youtube_id from replacing the first, but a second upload is possible;
//     (2) the contract: nothing else may write the row of a queued ingest before
//     it starts (that would make it look held until the lease runs out), and the
//     web handler should enqueue an ingest only when HasPendingJob(
//     KindTwitterVideoIngest, id) is false, which also keeps the queue free of
//     duplicate jobs.
//   - Guard. A video that already has a YouTube id, or is past the upload
//     (awaiting_captions, summarizing, publishing, done), is never downloaded or
//     uploaded again. If it is awaiting_captions with an id and no caption job
//     is pending, the first check is queued (the earlier run died or failed to
//     enqueue); otherwise the run does nothing. A failed row WITHOUT an id is a
//     retry and runs. (Retrying a failed row that already has an id is the
//     caller's decision: this handler leaves it alone rather than risk a
//     duplicate YouTube video.)
//   - The id is recorded in the single statement that moves the row to
//     awaiting_captions, before the Slack notice and the enqueue, on a context
//     detached from the job's, retried twice if the write fails, and never
//     replaces an id already stored.
//   - Status writes are guarded by the current status, so a late writer cannot
//     move a row that finished or was failed meanwhile.
//   - A run cut short by shutdown (ctx cancelled) fails nothing: it releases the
//     lease and returns the error, so the Runner releases the job. The row keeps
//     downloading or uploading and the re-run starts over from the file on disk.
//     An upload that was cut short before it returned an id is repeated, which
//     is the one window that remains (see video.YoutubeUploader.Upload: if
//     Google accepted the video but the answer was lost, a second video
//     appears); the re-run that finds the row uploading says so in Slack.
//   - If only the enqueue after the upload fails, the video is NOT failed (it
//     is uploaded and recorded) and the job ends with that error in last_error;
//     the Runner does not retry it. The caption check is queued by the re-run of
//     the job, if there is one (a shutdown or crash), or by
//     RequeueStaleCaptionChecks about an hour after the check was due.
//
// The run is bounded by VideoDeps.IngestTimeout: when it passes, the video is
// failed with the generic detail (Ruby had no timeout) and the error returned.
// Downloaded files and yt-dlp's .part files stay in VideosDir, as in Ruby.
func NewTwitterVideoIngest(deps VideoDeps) Handler {
	if err := deps.validate(true, false); err != nil {
		panic(err)
	}
	deps = deps.withDefaults()
	return func(ctx context.Context, args Args) error {
		return runIngest(ctx, deps, args.ID)
	}
}

func runIngest(ctx context.Context, d VideoDeps, id int64) (err error) {
	ctx, cancel := context.WithTimeout(ctx, d.IngestTimeout)
	defer cancel()
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

	wctx, wcancel := detached(ctx)
	claimed, err := store.ClaimTwitterVideoForIngest(wctx, d.DB, id, d.IngestLease)
	wcancel()
	if err != nil {
		return fmt.Errorf("claim twitter video %d: %w", id, err)
	}
	if !claimed {
		log.Info("another run holds the video, skipping", "status", v.Status)
		return nil
	}
	// From here this run holds the video: every exit records an outcome or releases it.
	stopLease := keepLease(ctx, d, log, id)
	defer stopLease()
	defer func() {
		if r := recover(); r != nil {
			perr := recovered(log, r)
			err = d.failRun(ctx, log, id, ingestHeld, perr, ingestFailureText(d, id, perr))
		}
	}()

	if v.Status == "uploading" {
		log.Warn("an earlier run was interrupted while uploading: uploading again")
		d.failure(ctx, msgReuploading(id))
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
	if errors.Is(ctx.Err(), context.Canceled) {
		// Shutdown: give the lease back so the re-run can take the video at once.
		wctx, wcancel := detached(ctx)
		defer wcancel()
		if _, rerr := store.ReleaseTwitterVideoClaim(wctx, d.DB, id); rerr != nil {
			log.Error("could not release the lease after shutdown; the re-run waits for it to expire", "err", rerr)
		}
		return err
	}
	return d.failRun(ctx, log, id, ingestHeld, err, ingestFailureText(d, id, err))
}

func isIngestRunnable(status string) bool {
	return status == "downloading" || status == "uploading" || status == "failed"
}

// keepLease touches the held video every IngestLease/5 until the returned
// function is called, so the lease of a live run never expires. The touches use
// a context that outlives the job's (a shutdown releases the lease explicitly).
func keepLease(ctx context.Context, d VideoDeps, log *slog.Logger, id int64) (stop func()) {
	hctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	every := max(d.IngestLease/5, 5*time.Millisecond)
	go func() {
		defer close(done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-t.C:
				tctx, tcancel := context.WithTimeout(hctx, finalWriteTimeout)
				if _, err := store.TouchTwitterVideo(tctx, d.DB, id); err != nil && hctx.Err() == nil {
					log.Warn("could not refresh the lease of the video", "err", err)
				}
				tcancel()
			}
		}
	}()
	return func() { cancel(); <-done }
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
	ok, err := store.TransitionTwitterVideo(wctx, d.DB, v.ID, ingestHeld, store.TwitterVideoChanges{Status: new("uploading")})
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
	recorded, err := recordUpload(ctx, d, log, v.ID, youtubeID)
	if err != nil {
		log.Error("uploaded but could not record the YouTube id", "youtube_id", youtubeID, "err", err)
		return fmt.Errorf("uploaded as YouTube video %s but could not record it: %w", youtubeID, err)
	}
	if !recorded {
		log.Error("a duplicate YouTube video was uploaded; the video already has another id", "youtube_id", youtubeID)
		d.failure(ctx, msgDuplicateUpload(v.ID, youtubeID))
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

// recordUpload writes the id of a finished upload, trying up to three times on
// a context detached from the job's: losing this write is the one way a video
// gets uploaded twice.
func recordUpload(ctx context.Context, d VideoDeps, log *slog.Logger, id int64, youtubeID string) (bool, error) {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(recordRetryDelay << (attempt - 1))
		}
		wctx, cancel := detached(ctx)
		var recorded bool
		recorded, err = store.RecordTwitterVideoUpload(wctx, d.DB, id, youtubeID)
		cancel()
		if err == nil {
			return recorded, nil
		}
		if errors.Is(err, store.ErrInvalidTwitterVideo) {
			return false, err // retrying cannot help
		}
		log.Warn("could not record the upload, retrying", "attempt", attempt+1, "err", err)
	}
	return false, err
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
// regularFileUnder); a sibling whose path fails the check is skipped and
// logged. It returns the path to upload and the title.
func reuseExisting(ctx context.Context, d VideoDeps, log *slog.Logger, v *store.TwitterVideo) (path, title string, reused bool, err error) {
	siblings, err := store.DownloadedSiblings(ctx, d.DB, v)
	if err != nil {
		return "", "", false, err
	}
	for _, s := range siblings {
		real, ok := regularFileUnder(d.VideosDir, deref(s.VideoPath))
		if !ok {
			log.Info("not reusing an earlier download: the path is not a regular file under the videos dir",
				"from", s.ID, "video_path", deref(s.VideoPath))
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
