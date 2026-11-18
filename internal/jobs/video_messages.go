package jobs

import "fmt"

// The texts the two video jobs send to Slack and store, copied from the Ruby
// jobs. testdata/golden/video/messages.json (script/golden_video_messages.rb)
// holds what the Ruby jobs actually produced; TestVideoMessagesGolden compares
// the Go jobs with it.

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

// Texts of the Go port with no Ruby original (not in the golden file).

// msgReuploading warns that an upload may have been repeated: the run before
// was interrupted while uploading, and Google may have kept that video.
func msgReuploading(id int64) string {
	return fmt.Sprintf("[twitter-video #%d] re-uploading after an interrupted run; check YouTube for a duplicate", id)
}

// msgSweptFailed announces a video the sweeper gave up on, in the style of the
// jobs' own failure texts.
func msgSweptFailed(id int64, status, detail string) string {
	stage := "ingest"
	if status == "summarizing" || status == "publishing" {
		stage = "captions/publish"
	}
	return fmt.Sprintf("[twitter-video #%d] %s failed: %s", id, stage, detail)
}

// msgDuplicateUpload reports a second upload of a video that already has an id.
func msgDuplicateUpload(id int64, youtubeID string) string {
	return fmt.Sprintf("[twitter-video #%d] uploaded a duplicate YouTube video (%s): the video already has another id", id, youtubeID)
}
