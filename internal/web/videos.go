package web

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/jobs"
	"serve-html-markdown/internal/store"
	"serve-html-markdown/internal/video"
)

const (
	// MaxVideoBodyBytes caps the body of POST /summarize-video. Rails had no
	// limit; a link and the text around it fit in a fraction of this.
	MaxVideoBodyBytes = 64 << 10

	msgUnauthorized   = "Unauthorized"
	msgMissingURL     = "Missing url"
	msgVideoNotFound  = "Not found"
	msgUnsupportedURL = ": send an x.com/twitter.com post or a YouTube video link"
	msgInvalidURLText = "url must be valid text."
)

// videoWriteTimeout bounds the writes of a submission that follow the first
// one. They run detached from the request, so a client that hangs up cannot
// leave a created row that was never enqueued.
const videoWriteTimeout = 15 * time.Second

// embeddedURL is Ruby's EMBEDDED_URL: the first link in a shared blob of text
// ("Watch this! https://youtu.be/..."). \S of Ruby is [^ \t\r\n\f\v], which RE2's
// \S is not (it keeps \v out of \s).
var embeddedURL = regexp.MustCompile(`(?i)https?://[^ \t\r\n\f\v]+`)

// submittedURL is Ruby's submitted_url: params[:url] stripped, then its first
// link when it holds one, else the whole text.
func submittedURL(params requestParams) string {
	text := strings.TrimLeft(params.get("url"), " \t\n\v\f\r")
	text = strings.TrimRight(text, " \t\n\v\f\r\x00")
	if m := embeddedURL.FindString(text); m != "" {
		return m
	}
	return text
}

// createVideoSummary is POST /summarize-video and POST /twitter-video, the old
// name that iOS Shortcuts still use (VideoSummariesController#create). It is
// authorized by the bearer token only. The "url" parameter (JSON, form or
// query) is a YouTube link, which skips the download and goes straight to the
// captions job, or a tweet link, which is downloaded, uploaded and then
// followed by the captions job. It answers 202 with {"id","status"}.
//
// Retry contract with the jobs (see retryVideo): a new tweet row is only
// created, and the ingest job of an existing one is queued without touching the
// row first, because the ingest job takes a lease on the row's updated_at and
// any earlier write would make it look held. A job is queued only when none is
// already queued or running for the row.
//
// Deviations from Ruby: the body is capped (MaxVideoBodyBytes), text that is
// not valid UTF-8 is a 400 (Rails does that too), and a submission that finds
// its row already past the step a job would redo (done, summarizing,
// publishing, or awaiting captions with a check queued) queues nothing, where
// Ruby enqueued the job again.
func (s *Server) createVideoSummary(w http.ResponseWriter, r *http.Request) {
	if !auth.BearerOK(r, s.cfg.APIToken) {
		writeDetail(w, http.StatusUnauthorized, msgUnauthorized)
		return
	}
	params, err := parseParams(w, r, MaxVideoBodyBytes)
	if err != nil {
		bodyError(w, err)
		return
	}
	rawURL := submittedURL(params)
	if blank(rawURL) {
		slog.Warn("video submission rejected: no url")
		writeDetail(w, http.StatusBadRequest, msgMissingURL)
		return
	}
	if !utf8.ValidString(rawURL) || strings.ContainsRune(rawURL, 0) {
		writeDetail(w, http.StatusBadRequest, msgInvalidURLText)
		return
	}

	// From here the row may be written: finish even if the client goes away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), videoWriteTimeout)
	defer cancel()

	var v *store.TwitterVideo
	if youtubeID := video.YoutubeVideoID(rawURL); youtubeID != "" {
		v, err = s.startYoutubeVideo(ctx, youtubeID)
	} else {
		var sourceURL string
		if sourceURL, err = video.NormalizeTwitterURL(rawURL); err != nil {
			slog.Warn("video submission rejected: unsupported url")
			writeDetail(w, http.StatusBadRequest, err.Error()+msgUnsupportedURL)
			return
		}
		v, err = s.startTwitterVideo(ctx, sourceURL)
	}
	if err != nil {
		s.serverFailure(w, r, err)
		return
	}
	writeVideoJSON(w, http.StatusAccepted, map[string]any{"id": v.ID, "status": v.Status})
}

// startYoutubeVideo is create_from_youtube: the row for the watch link is
// reset to awaiting_captions (or created so) and the first caption check is
// queued. The returned row is the one just written.
func (s *Server) startYoutubeVideo(ctx context.Context, youtubeID string) (*store.TwitterVideo, error) {
	v, existed, err := store.StartYoutubeTwitterVideo(ctx, s.pool, video.YoutubeWatchURL(youtubeID), youtubeID)
	if err != nil {
		return nil, err
	}
	slog.Info("youtube video submitted", "id", v.ID, "retry", existed, "youtube_id", youtubeID)
	if err := s.enqueueOnce(ctx, jobs.KindTwitterVideoCaptions, v.ID); err != nil {
		return nil, err
	}
	return v, nil
}

// startTwitterVideo reuses the newest row for the tweet or creates a
// "downloading" one, and queues what the row needs next (retryVideo).
func (s *Server) startTwitterVideo(ctx context.Context, sourceURL string) (*store.TwitterVideo, error) {
	v, created, err := store.FindOrCreateTwitterVideo(ctx, s.pool, sourceURL)
	if err != nil {
		return nil, err
	}
	slog.Info("tweet video submitted", "id", v.ID, "retry", !created)
	status, _, err := s.retryVideo(ctx, v)
	if err != nil {
		return nil, err
	}
	v.Status = status
	return v, nil
}

// retryVideo queues the next job of a video that was submitted again, or whose
// YouTube authorization was renewed, and reports the status to answer with and
// whether the video is (now) waiting on a job. It is the retry contract with
// the jobs:
//
//   - No YouTube id yet, and failed, downloading or uploading: queue ingest.
//     It does NOT write the row: the ingest job leases the row through its
//     updated_at, and a row written before the job runs looks held. The job
//     decides whether to run (a held row is left to its holder). The status
//     answered is the row's as it is, like Ruby's.
//   - A YouTube id and failed: ingest would skip it, so the row is restarted
//     (store.RestartFailedYoutubeVideo, guarded) and the first caption check
//     queued.
//   - A YouTube id and awaiting_captions: queue a caption check if none is.
//   - Anything else (done, summarizing, publishing): nothing to do.
//
// A job is queued only if none of its kind is queued or running for the row.
func (s *Server) retryVideo(ctx context.Context, v *store.TwitterVideo) (status string, queued bool, err error) {
	status = v.Status
	hasID := v.YoutubeID != nil && strings.TrimSpace(*v.YoutubeID) != ""
	switch {
	case !hasID && (v.Status == "failed" || v.Status == "downloading" || v.Status == "uploading"):
		return status, true, s.enqueueOnce(ctx, jobs.KindTwitterVideoIngest, v.ID)
	case hasID && v.Status == "failed":
		ok, err := store.RestartFailedYoutubeVideo(ctx, s.pool, v.ID)
		if err != nil || !ok {
			return status, false, err
		}
		return "awaiting_captions", true, s.enqueueOnce(ctx, jobs.KindTwitterVideoCaptions, v.ID)
	case hasID && v.Status == "awaiting_captions":
		return status, true, s.enqueueOnce(ctx, jobs.KindTwitterVideoCaptions, v.ID)
	}
	return status, false, nil
}

// enqueueOnce queues a job of kind for the video, now, unless one is already
// queued or running. Nothing is marked on failure: the row stays as it is and
// the caller answers 500 (a stuck new row is failed by the sweeper).
func (s *Server) enqueueOnce(ctx context.Context, kind string, id int64) error {
	pending, err := jobs.HasPendingJob(ctx, s.pool, kind, id)
	if err != nil {
		return err
	}
	if pending {
		return nil
	}
	return s.enqueuer.Enqueue(ctx, kind, jobs.Args{ID: id}, 0)
}

// videoStatus is the JSON of GET /summarize-video/{id}: exactly these keys, in
// this order, nulls included (Ruby's video.slice).
type videoStatus struct {
	ID           int64   `json:"id"`
	Status       string  `json:"status"`
	ErrorDetail  *string `json:"error_detail"`
	HTMLFilename *string `json:"html_filename"`
	YoutubeID    *string `json:"youtube_id"`
}

// showVideoSummary is GET /summarize-video/{id} and /twitter-video/{id}
// (VideoSummariesController#show). Like Rails it needs no authorization. A
// missing row and an id too large for int64 are the same 404.
func (s *Server) showVideoSummary(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeDetail(w, http.StatusNotFound, msgVideoNotFound)
		return
	}
	v, err := store.FindTwitterVideo(r.Context(), s.pool, id)
	if err != nil {
		s.serverFailure(w, r, err)
		return
	}
	if v == nil {
		writeDetail(w, http.StatusNotFound, msgVideoNotFound)
		return
	}
	writeVideoJSON(w, http.StatusOK, videoStatus{
		ID: v.ID, Status: v.Status, ErrorDetail: v.ErrorDetail, HTMLFilename: v.HTMLFilename, YoutubeID: v.YoutubeID,
	})
}

func writeVideoJSON(w http.ResponseWriter, code int, body any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(code)
	_, _ = w.Write(marshalJSON(body))
}
