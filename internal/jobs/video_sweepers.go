package jobs

import (
	"context"
	"errors"
	"time"

	"serve-html-markdown/internal/store"
)

// VideoSweepers is the sibling of VideoHandlers for Runner.Sweepers: it fails
// the videos nothing is working on any more (telling Slack for each) and queues
// again the caption checks that were lost. It returns the error of Validate.
func VideoSweepers(deps VideoDeps) ([]func(context.Context) error, error) {
	if err := deps.Validate(); err != nil {
		return nil, err
	}
	deps = deps.withDefaults()
	return []func(context.Context) error{FailStaleVideos(deps), RequeueStaleCaptionChecks(deps)}, nil
}

// maxSweptVideos bounds one pass of a video sweeper.
const maxSweptVideos = 500

// FailStaleVideos is the sweeper that fails videos stuck in downloading,
// uploading, summarizing or publishing for SweepAfter (store.StaleTwitterVideos)
// and posts a Slack failure for each. A video with a job queued or running is
// left alone: that job (a re-run after a release, a retry) will deal with it.
// awaiting_captions is not swept: it legitimately waits up to two days.
func FailStaleVideos(deps VideoDeps) func(context.Context) error {
	deps = deps.withDefaults()
	return func(ctx context.Context) error {
		stale, err := store.StaleTwitterVideos(ctx, deps.DB, deps.SweepAfter, maxSweptVideos)
		if err != nil {
			return err
		}
		var errs []error
		for i := range stale {
			v := &stale[i]
			kind := KindTwitterVideoCaptions
			if v.Status == "downloading" || v.Status == "uploading" {
				kind = KindTwitterVideoIngest
			}
			pending, err := HasPendingJob(ctx, deps.DB, kind, v.ID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if pending {
				continue
			}
			failed, err := store.FailStaleTwitterVideo(ctx, deps.DB, v.ID, deps.SweepAfter)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if failed {
				deps.Logger.Warn("video sweeper: failed a stuck video", "twitter_video_id", v.ID, "status", v.Status)
				deps.failure(ctx, msgSweptFailed(v.ID, v.Status, store.TwitterVideoFailedDetail))
			}
		}
		return errors.Join(errs...)
	}
}

// RequeueStaleCaptionChecks is the sweeper that queues the caption check of a
// video that is awaiting captions, is overdue by more than RequeueGrace and has
// no caption job pending (queued or running). That heals a check lost to a failed
// enqueue (the handlers return the error, the job ends, and the row stays
// awaiting) or a deleted job row; with the default grace it acts about 65
// minutes after the first check was due. A duplicate it may rarely cause (a check
// queued between the pending test and the insert) is harmless: see
// NewTwitterVideoCaptions.
//
// What it does not cover: a video stuck in downloading/uploading is failed by
// FailStaleVideos instead (the user resubmits it).
func RequeueStaleCaptionChecks(deps VideoDeps) func(context.Context) error {
	deps = deps.withDefaults()
	return func(ctx context.Context) error {
		videos, err := store.AwaitingCaptionVideos(ctx, deps.DB, maxSweptVideos)
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
