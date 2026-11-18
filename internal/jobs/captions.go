package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"serve-html-markdown/internal/store"
	"serve-html-markdown/internal/video"
)

// maxCaptionBytes bounds the caption file read into memory (a day-long
// stream's captions are a few MB).
const maxCaptionBytes = 32 << 20

// captionsInFlight are the statuses of a video a run is publishing; every status
// a captions run may fail a video from.
var (
	captionsInFlight = []string{"summarizing", "publishing"}
	captionsFailable = []string{"awaiting_captions", "summarizing", "publishing"}
)

// NewTwitterVideoCaptions returns the handler of TwitterVideoCaptionsJob: look
// for the video's auto-captions; on a hit summarize them, write the page, push
// the link to Rulinky and finish; on a miss, wait for the next checkpoint or
// give up when they are used up.
//
// Flow and order of side effects, as in Ruby: only a video awaiting_captions
// is worked on. A fetch error of yt-dlp counts as a miss. A miss counts the
// attempt, then either fails the video ("Auto-captions never became
// available." and a Slack notice naming the last checkpoint) or posts "not
// ready" to Slack and queues the next check after video.NextCaptionWait. A hit
// walks the status summarizing, publishing, done (summarize, Slack notice,
// write the page, push to Rulinky, store html_filename and rulinky_link_id,
// Slack notice). Any other error fails the video with a Slack notice and
// returns nil (the rescue of Ruby's job).
//
// Safe to run again, and safe to run twice at once (a duplicate check, a
// requeue by the sweeper):
//
//   - Entry is guarded by status; the claim of a hit is a guarded write
//     awaiting_captions -> summarizing, so of two runs that both found captions
//     one publishes and the other stops.
//   - A miss counts the attempt with store.AdvanceCaptionAttempt, which only
//     succeeds for the attempt number this run started from. Of two runs for the
//     same check one counts it and queues the successor; the other stops without
//     counting or queueing. Duplicates therefore never multiply or skip
//     checkpoints (apart from the case below).
//   - A run that died after counting and before queueing leaves the video
//     awaiting_captions with no job: the next run of the same (reclaimed) job
//     does the check as the next attempt, or RequeueStaleCaptionChecks queues it.
//   - Shutdown (ctx cancelled): nothing is failed; a video this run had moved to
//     summarizing/publishing is put back to awaiting_captions and the error is
//     returned so the Runner releases the job; the re-run repeats the (paid)
//     summary.
//   - A run that died while publishing leaves summarizing/publishing; the
//     re-run takes such a row over once it has been untouched for
//     VideoDeps.StaleAfter (store.ReclaimTwitterVideo) and publishes from
//     scratch. What remains: a crash between writing the page (or pushing to
//     Rulinky) and storing "done" repeats those two steps, so a second page or
//     link can appear.
//   - Slack notices, the status writes after the claim and the enqueue run on
//     contexts detached from the job's, so a shutdown does not lose them.
//
// The deadline of Runner.Timeouts (RecommendedCaptionsTimeout) fails the video
// with the generic detail and returns the error.
func NewTwitterVideoCaptions(deps VideoDeps) Handler {
	deps = deps.withDefaults()
	return func(ctx context.Context, args Args) error {
		return runCaptions(ctx, deps, args.ID)
	}
}

func runCaptions(ctx context.Context, d VideoDeps, id int64) error {
	log := d.Logger.With("twitter_video_id", id, "job", "twitter_video_captions")
	if err := ctx.Err(); err != nil {
		return err
	}
	v, err := store.FindTwitterVideo(ctx, d.DB, id)
	if err != nil {
		return fmt.Errorf("find twitter video %d: %w", id, err)
	}
	if v != nil && (v.Status == "summarizing" || v.Status == "publishing") {
		wctx, cancel := detached(ctx)
		reclaimed, err := store.ReclaimTwitterVideo(wctx, d.DB, id, d.StaleAfter)
		cancel()
		if err != nil {
			return fmt.Errorf("reclaim twitter video %d: %w", id, err)
		}
		if reclaimed {
			log.Warn("took over a video left publishing by a run that died")
			if v, err = store.FindTwitterVideo(ctx, d.DB, id); err != nil {
				return fmt.Errorf("find twitter video %d: %w", id, err)
			}
		}
	}
	if v == nil || v.Status != "awaiting_captions" {
		log.Info("skipping (missing or not awaiting_captions)")
		return nil
	}

	log.Info("checking for auto-captions", "attempt", v.CaptionAttempts+1)
	vtt, found, err := readCaptions(ctx, d, log, v)
	if err != nil {
		return captionsFailed(ctx, d, log, v.ID, err)
	}
	if !found {
		return rescheduleOrFail(ctx, d, log, v)
	}
	log.Info("captions found", "bytes", len(vtt))
	return publish(ctx, d, log, v, vtt)
}

// captionsFailed is Ruby's rescue: fail the video, tell Slack. A cancelled
// context puts a video this run was publishing back instead.
func captionsFailed(ctx context.Context, d VideoDeps, log *slog.Logger, id int64, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		wctx, cancel := detached(ctx)
		defer cancel()
		if _, perr := store.TransitionTwitterVideo(wctx, d.DB, id, captionsInFlight, store.TwitterVideoChanges{Status: new("awaiting_captions")}); perr != nil {
			log.Error("could not put the video back after shutdown; the next run takes it over once it is stale", "err", perr)
		}
		return err
	}
	return d.failRun(ctx, log, id, captionsFailable, err, msgCaptionsFailed(id, rubyErrorClass(err), err.Error()))
}

// readCaptions fetches the captions into a private scratch directory that is
// removed afterwards. A yt-dlp failure (rate limit, network) is a miss, not an
// error, so the video waits for the next checkpoint. found is false for a miss.
func readCaptions(ctx context.Context, d VideoDeps, log *slog.Logger, v *store.TwitterVideo) (vtt string, found bool, err error) {
	dir, err := os.MkdirTemp("", "twitter-captions-") // mode 0700
	if err != nil {
		return "", false, fmt.Errorf("captions scratch dir: %w", err)
	}
	defer os.RemoveAll(dir)

	cookies, err := exportCookies(ctx, d, log, dir)
	if err != nil {
		return "", false, err
	}
	path, err := d.Ytdlp.FetchAutoSubs(ctx, video.YoutubeWatchURL(deref(v.YoutubeID)), dir, cookies)
	var ytdlpErr *video.YtdlpError
	if errors.As(err, &ytdlpErr) {
		if ctx.Err() != nil { // stopped by shutdown or deadline, not by YouTube
			return "", false, ctx.Err()
		}
		log.Warn("caption fetch errored", "err", ytdlpErr)
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if path == "" {
		return "", false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCaptionBytes+1))
	if err != nil {
		return "", false, err
	}
	if len(data) > maxCaptionBytes {
		return "", false, &detailError{"The captions file is too large."}
	}
	return string(data), true, nil
}

// exportCookies returns the path of the exported ego lite cookies, or "" when
// they cannot be exported (the fetch is then anonymous). A context that ended
// is an error, not "no cookies".
func exportCookies(ctx context.Context, d VideoDeps, log *slog.Logger, dir string) (string, error) {
	path, err := d.Cookies.Export(ctx, filepath.Join(dir, "cookies.txt"))
	var egoErr *video.EgoCookiesError
	if errors.As(err, &egoErr) {
		log.Warn("no ego lite cookies", "err", egoErr)
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return path, nil
}

func rescheduleOrFail(ctx context.Context, d VideoDeps, log *slog.Logger, v *store.TwitterVideo) error {
	wctx, cancel := detached(ctx)
	attempts, advanced, err := store.AdvanceCaptionAttempt(wctx, d.DB, v.ID, v.CaptionAttempts)
	cancel()
	if err != nil {
		return fmt.Errorf("count caption attempt of twitter video %d: %w", v.ID, err)
	}
	if !advanced {
		log.Info("another run already counted this check, stopping")
		return nil
	}
	v.CaptionAttempts = attempts

	if !v.HasUploadTime() { // Ruby: NoMethodError on nil, rescued into a failed video
		return captionsFailed(ctx, d, log, v.ID, &detailError{msgUploadTimeMissing})
	}
	wait, ok := v.NextCaptionWait(d.Now())
	if !ok {
		log.Warn("captions never became available, giving up")
		wctx, cancel := detached(ctx)
		failed, err := store.FailTwitterVideoFrom(wctx, d.DB, v.ID, []string{"awaiting_captions"}, msgCaptionsNeverAvailable)
		cancel()
		if err != nil {
			return fmt.Errorf("fail twitter video %d: %w", v.ID, err)
		}
		if failed {
			checkpoints := v.Checkpoints()
			d.failure(ctx, msgCaptionsTimedOut(v.ID, minutesInspect(checkpoints[len(checkpoints)-1])))
		}
		return nil
	}
	log.Info("captions not ready, retrying", "wait", wait, "attempt", attempts)
	d.success(ctx, msgCaptionsNotReady(v.ID, attempts))
	if err := d.enqueueCaptions(ctx, v.ID, wait); err != nil {
		// The attempt is counted and the row stays awaiting_captions: the job
		// reports the error and RequeueStaleCaptionChecks queues the check.
		return fmt.Errorf("queue the next caption check of twitter video %d: %w", v.ID, err)
	}
	return nil
}

func publish(ctx context.Context, d VideoDeps, log *slog.Logger, v *store.TwitterVideo, vtt string) error {
	move := func(from []string, c store.TwitterVideoChanges) (bool, error) {
		wctx, cancel := detached(ctx)
		defer cancel()
		return store.TransitionTwitterVideo(wctx, d.DB, v.ID, from, c)
	}
	moved := func() error {
		log.Warn("the video moved on while it was published, stopping")
		return nil
	}

	if ok, err := move([]string{"awaiting_captions"}, store.TwitterVideoChanges{Status: new("summarizing")}); err != nil {
		return captionsFailed(ctx, d, log, v.ID, err)
	} else if !ok {
		return moved() // a duplicate run claimed it
	}
	log.Info("summarizing the transcript")
	summary, err := d.Summarizer.Summarize(ctx, vtt)
	if err != nil {
		return captionsFailed(ctx, d, log, v.ID, err)
	}
	log.Info("summarized", "title", summary.Title)
	d.success(ctx, msgSummarized(v.ID, summary.Title))

	if ok, err := move([]string{"summarizing"}, store.TwitterVideoChanges{Status: new("publishing")}); err != nil {
		return captionsFailed(ctx, d, log, v.ID, err)
	} else if !ok {
		return moved()
	}
	log.Info("writing the summary page")
	filename, err := d.Writer.Write(ctx, summary.Title, summary.SummaryHTML)
	if err != nil {
		if filename == "" {
			return captionsFailed(ctx, d, log, v.ID, err)
		}
		// Only recording the page in served_files failed: it exists (the next
		// sync picks it up), and writing again would make a second page.
		log.Error("page written but not recorded in served_files", "file", filename, "err", err)
	}
	link := publishedLink(d.Host, filename)
	log.Info("wrote the page, pushing the link to rulinky", "link", link)
	rulinkyID, err := d.Rulinky.CreateLink(ctx, link, summary.Title)
	if err != nil {
		return captionsFailed(ctx, d, log, v.ID, err)
	}

	changes := store.TwitterVideoChanges{Status: new("done"), HTMLFilename: &filename}
	if rulinkyID != "" {
		changes.RulinkyLinkID = &rulinkyID
	}
	if ok, err := move([]string{"publishing"}, changes); err != nil {
		return captionsFailed(ctx, d, log, v.ID, err)
	} else if !ok {
		return moved()
	}
	log.Info("done", "link", link, "rulinky_id", rulinkyID)
	d.success(ctx, msgPublished(v.ID, link))
	return nil
}
