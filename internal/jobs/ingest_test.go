package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"serve-html-markdown/internal/store"
	"serve-html-markdown/internal/video"
)

func expiredGrant() error {
	return &video.YoutubeError{Message: "YouTube authorization expired: invalid_grant", Expired: true}
}

// Ruby: "downloads, uploads unlisted, schedules first caption poll".
func TestIngestDownloadsUploadsSchedulesFirstCaptionCheck(t *testing.T) {
	e := newVideoEnv(t)
	v := e.newTweet()
	must(t, e.ingest(v.ID))

	got := e.reload(v.ID)
	if got.Status != "awaiting_captions" || got.YoutubeID == nil || *got.YoutubeID != "YT9" ||
		got.YoutubeTitle == nil || *got.YoutubeTitle != "Tweet Title" || got.UploadCompletedAt == nil || got.CaptionAttempts != 0 {
		t.Fatalf("got %+v", got)
	}
	if s, _ := e.slack.counts(); s < 2 {
		t.Fatalf("want download and upload notices, got %v", e.slack.successes)
	}
	q := e.queued(KindTwitterVideoCaptions, v.ID)
	if len(q) != 1 || !near(q[0].Delay, 5*time.Minute) {
		t.Fatalf("first caption check must be one job 5 minutes out, got %+v", q)
	}
	if e.uploader.callCount() != 1 || e.uploader.titles[0] != "Tweet Title" || e.uploader.descs[0] != "" ||
		e.uploader.paths[0] != filepath.Join(e.videosDir, "x.mp4") {
		t.Fatalf("upload args: %+v %+v %+v", e.uploader.paths, e.uploader.titles, e.uploader.descs)
	}
	if e.ytdlp.urls[0] != "https://x.com/foo/status/1" {
		t.Fatalf("downloaded %v", e.ytdlp.urls)
	}
}

// Ruby: "posts the studio edit link to slack after upload".
func TestIngestPostsStudioLinkAfterUpload(t *testing.T) {
	e := newVideoEnv(t)
	v := e.newTweet()
	must(t, e.ingest(v.ID))
	want := fmt.Sprintf("[twitter-video #%d] uploaded: Tweet Title\nset it to unlisted: https://studio.youtube.com/video/YT9/edit", v.ID)
	if got := e.slack.lastSuccess(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if first := e.slack.successes[0]; first != fmt.Sprintf("[twitter-video #%d] downloaded: Tweet Title", v.ID) {
		t.Fatalf("download notice = %q", first)
	}
}

// Ruby: "downloads into the permanent videos dir and records the path".
func TestIngestDownloadsIntoVideosDirAndRecordsPath(t *testing.T) {
	e := newVideoEnv(t)
	v := e.newTweet()
	must(t, e.ingest(v.ID))
	if got := e.reload(v.ID); got.VideoPath == nil || *got.VideoPath != filepath.Join(e.videosDir, "x.mp4") {
		t.Fatalf("video_path = %v", got.VideoPath)
	}
}

func TestIngestUsesFallbackTitleWhenTheDownloadHasNone(t *testing.T) {
	e := newVideoEnv(t)
	e.ytdlp.downloadFn = func(_ context.Context, _, dir string) (video.Downloaded, error) {
		must(t, os.MkdirAll(dir, 0o700))
		p := filepath.Join(dir, "x.mp4")
		return video.Downloaded{Path: p}, os.WriteFile(p, nil, 0o600)
	}
	v := e.newTweet()
	must(t, e.ingest(v.ID))
	if e.uploader.titles[0] != "Twitter video" {
		t.Fatalf("title = %q", e.uploader.titles[0])
	}
	if got := e.slack.lastSuccess(); !strings.HasPrefix(got, fmt.Sprintf("[twitter-video #%d] uploaded: \n", v.ID)) {
		t.Fatalf("blank title is shown blank, as Ruby's nil: %q", got)
	}
}

func (e *videoEnv) putVideoFile(name string) string {
	e.t.Helper()
	must(e.t, os.MkdirAll(e.videosDir, 0o700))
	p := filepath.Join(e.videosDir, name)
	must(e.t, os.WriteFile(p, []byte("v"), 0o600))
	return p
}

func (e *videoEnv) mustNotDownload() {
	e.ytdlp.downloadFn = func(context.Context, string, string) (video.Downloaded, error) {
		return video.Downloaded{}, errors.New("must not download again")
	}
}

// Ruby: "reuses an already downloaded file for the same source url".
func TestIngestReusesDownloadOfASiblingRow(t *testing.T) {
	e := newVideoEnv(t)
	existing := e.putVideoFile("reused.mp4")
	sib := e.create(store.NewTwitterVideo{SourceURL: "https://x.com/foo/status/1?s=20", Status: "done"})
	e.update(sib.ID, store.TwitterVideoChanges{VideoPath: &existing, YoutubeTitle: new("Cached Title")})
	e.mustNotDownload()
	v := e.newTweet()

	must(t, e.ingest(v.ID))

	got := e.reload(v.ID)
	if got.Status != "awaiting_captions" || got.VideoPath == nil || *got.VideoPath != existing ||
		got.YoutubeTitle == nil || *got.YoutubeTitle != "Cached Title" {
		t.Fatalf("got %+v", got)
	}
	if e.ytdlp.downloadCount() != 0 || e.uploader.paths[0] != mustReal(t, existing) || e.uploader.titles[0] != "Cached Title" {
		t.Fatalf("downloads=%d paths=%v titles=%v", e.ytdlp.downloadCount(), e.uploader.paths, e.uploader.titles)
	}
	// Reuse sends no "downloaded" notice, only the upload one.
	if s, _ := e.slack.counts(); s != 1 {
		t.Fatalf("notices %v", e.slack.successes)
	}
}

func mustReal(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	must(t, err)
	return r
}

// The current row's own file comes first, then the newest sibling.
func TestIngestReuseOrderIsOwnRowThenNewestSibling(t *testing.T) {
	e := newVideoEnv(t)
	older := e.putVideoFile("older.mp4")
	newer := e.putVideoFile("newer.mp4")
	own := e.putVideoFile("own.mp4")
	a := e.create(store.NewTwitterVideo{SourceURL: "https://x.com/foo/status/1"})
	e.update(a.ID, store.TwitterVideoChanges{VideoPath: &older, YoutubeTitle: new("Older")})
	b := e.create(store.NewTwitterVideo{SourceURL: "https://x.com/foo/status/1?t=1"})
	e.update(b.ID, store.TwitterVideoChanges{VideoPath: &newer, YoutubeTitle: new("Newer")})
	e.mustNotDownload()

	// No own file: the newest sibling wins.
	v := e.newTweet()
	must(t, e.ingest(v.ID))
	if got := e.reload(v.ID); *got.VideoPath != newer || *got.YoutubeTitle != "Newer" {
		t.Fatalf("newest sibling expected, got %+v", got)
	}

	// An own file beats a newer sibling, and an own title beats the sibling's.
	w := e.newTweet()
	e.updateFresh(w.ID, store.TwitterVideoChanges{VideoPath: &own, YoutubeTitle: new("Mine")})
	must(t, e.ingest(w.ID))
	if got := e.reload(w.ID); *got.VideoPath != own || *got.YoutubeTitle != "Mine" {
		t.Fatalf("own row expected, got %+v", got)
	}
}

// Ruby: "re-downloads when the recorded file is gone".
func TestIngestRedownloadsWhenTheRecordedFileIsGone(t *testing.T) {
	e := newVideoEnv(t)
	v := e.newTweet()
	e.updateFresh(v.ID, store.TwitterVideoChanges{VideoPath: new(filepath.Join(e.videosDir, "gone.mp4"))})
	must(t, e.ingest(v.ID))
	if got := e.reload(v.ID); *got.VideoPath != filepath.Join(e.videosDir, "x.mp4") || e.ytdlp.downloadCount() != 1 {
		t.Fatalf("got %+v downloads=%d", got, e.ytdlp.downloadCount())
	}
}

// video_path comes from the database: it is only reused when it is a regular
// file that really lives under VideosDir.
func TestIngestDoesNotReuseUntrustedPaths(t *testing.T) {
	cases := map[string]func(e *videoEnv, outside string) string{
		"outside the dir": func(e *videoEnv, outside string) string { return outside },
		"dotdot escape": func(e *videoEnv, outside string) string {
			return filepath.Join(e.videosDir, "..", filepath.Base(filepath.Dir(outside)), "secret.mp4")
		},
		"symlink escape": func(e *videoEnv, outside string) string {
			must(t, os.Symlink(outside, filepath.Join(e.videosDir, "link.mp4")))
			return filepath.Join(e.videosDir, "link.mp4")
		},
		"directory": func(e *videoEnv, _ string) string {
			must(t, os.Mkdir(filepath.Join(e.videosDir, "adir"), 0o700))
			return filepath.Join(e.videosDir, "adir")
		},
		"relative":       func(e *videoEnv, _ string) string { return "videos/keep.mp4" },
		"the dir itself": func(e *videoEnv, _ string) string { return e.videosDir },
		"sibling prefix": func(e *videoEnv, _ string) string { return e.videosDir + "-evil/x.mp4" },
		"blank":          func(e *videoEnv, _ string) string { return "" },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			e := newVideoEnv(t)
			e.putVideoFile("keep.mp4")
			outside := filepath.Join(t.TempDir(), "secret.mp4")
			must(t, os.WriteFile(outside, []byte("s"), 0o600))
			path := mk(e, outside)
			sib := e.create(store.NewTwitterVideo{SourceURL: "https://x.com/foo/status/1?z=1"})
			e.update(sib.ID, store.TwitterVideoChanges{VideoPath: &path})
			v := e.newTweet()
			must(t, e.ingest(v.ID))
			if e.ytdlp.downloadCount() != 1 {
				t.Fatalf("%q was reused instead of downloading", path)
			}
			if p := e.uploader.paths[0]; p != filepath.Join(e.videosDir, "x.mp4") {
				t.Fatalf("uploaded %q", p)
			}
		})
	}
}

// Ruby: "marks failed and notifies on download error".
func TestIngestMarksFailedAndNotifiesOnDownloadError(t *testing.T) {
	e := newVideoEnv(t)
	e.ytdlp.downloadFn = func(context.Context, string, string) (video.Downloaded, error) {
		return video.Downloaded{}, &video.YtdlpError{Message: "gone"}
	}
	v := e.newTweet()
	must(t, e.ingest(v.ID))
	got := e.reload(v.ID)
	if got.Status != "failed" || got.ErrorDetail == nil || *got.ErrorDetail != "gone" {
		t.Fatalf("got %+v", got)
	}
	if want := fmt.Sprintf("[twitter-video #%d] ingest failed: YtdlpClient::Error: gone", v.ID); e.slack.lastFailure() != want {
		t.Fatalf("slack %q want %q", e.slack.lastFailure(), want)
	}
	if e.uploader.callCount() != 0 {
		t.Fatal("uploaded after a failed download")
	}
}

// Ruby: "posts a re-authorization link when the youtube grant is dead".
func TestIngestPostsReauthorizationLinkWhenTheGrantIsDead(t *testing.T) {
	e := newVideoEnv(t)
	e.deps.YoutubeReauthURL = "https://serve.chiq.me/youtube/reauth"
	e.uploader.fn = func(context.Context, string, string) (string, error) { return "", expiredGrant() }
	v := e.newTweet()
	must(t, e.ingest(v.ID))

	got := e.reload(v.ID)
	if got.Status != "failed" || got.ErrorDetail == nil || *got.ErrorDetail != "YouTube authorization expired: invalid_grant" {
		t.Fatalf("got %+v", got)
	}
	want := fmt.Sprintf("[twitter-video #%d] ingest failed: YouTube authorization expired.\n"+
		"re-authorize: https://serve.chiq.me/youtube/reauth?video_id=%d\n"+
		"the video is already downloaded — the upload retries by itself once you're done.", v.ID, v.ID)
	if e.slack.lastFailure() != want {
		t.Fatalf("got\n%q\nwant\n%q", e.slack.lastFailure(), want)
	}
	// The file stays; the row has no YouTube id, so the retry the link promises works:
	e.uploader.fn = nil
	must(t, e.ingest(v.ID))
	if got := e.reload(v.ID); got.Status != "awaiting_captions" || *got.YoutubeID != "YT9" {
		t.Fatalf("retry after reauthorization: %+v", got)
	}
	if e.ytdlp.downloadCount() != 1 {
		t.Fatalf("the retry downloaded again (%d)", e.ytdlp.downloadCount())
	}
}

func TestIngestReauthLinkFallsBackToTheYoutubeHost(t *testing.T) {
	e := newVideoEnv(t)
	e.uploader.fn = func(context.Context, string, string) (string, error) { return "", expiredGrant() }
	v := e.newTweet()
	must(t, e.ingest(v.ID))
	if want := fmt.Sprintf("re-authorize: https://serve.test:8009/youtube/reauth?video_id=%d\n", v.ID); !strings.Contains(e.slack.lastFailure(), want) {
		t.Fatalf("got %q", e.slack.lastFailure())
	}
}

// Ruby: "keeps the raw error text for failures a link cannot fix".
func TestIngestKeepsRawErrorTextForFailuresALinkCannotFix(t *testing.T) {
	e := newVideoEnv(t)
	e.ytdlp.downloadFn = func(context.Context, string, string) (video.Downloaded, error) {
		return video.Downloaded{}, &video.YtdlpError{Message: "gone"}
	}
	v := e.newTweet()
	must(t, e.ingest(v.ID))
	if m := e.slack.lastFailure(); !strings.Contains(m, "YtdlpClient::Error: gone") || strings.Contains(m, "youtube/reauth") {
		t.Fatalf("got %q", m)
	}
}

// Other YouTube failures are not re-authorization problems.
func TestIngestOtherUploadFailuresKeepTheirText(t *testing.T) {
	e := newVideoEnv(t)
	e.uploader.fn = func(context.Context, string, string) (string, error) {
		return "", &video.YoutubeError{Message: "YouTube upload failed: 403 quota"}
	}
	v := e.newTweet()
	must(t, e.ingest(v.ID))
	want := fmt.Sprintf("[twitter-video #%d] ingest failed: YoutubeUploader::Error: YouTube upload failed: 403 quota", v.ID)
	if got := e.reload(v.ID); got.Status != "failed" || *got.ErrorDetail != "YouTube upload failed: 403 quota" || e.slack.lastFailure() != want {
		t.Fatalf("got %+v / %q", got, e.slack.lastFailure())
	}
}

// An error nobody classified (an exec start failure, say) fails the video like
// Ruby's rescue does, but its text goes to Slack and the log only: error_detail
// is shown by the public status endpoint.
func TestIngestUnclassifiedErrorFailsWithGenericDetail(t *testing.T) {
	e := newVideoEnv(t)
	e.ytdlp.downloadFn = func(context.Context, string, string) (video.Downloaded, error) {
		return video.Downloaded{}, errors.New("yt-dlp download: fork/exec /opt/secret/yt-dlp: no such file or directory")
	}
	v := e.newTweet()
	must(t, e.ingest(v.ID))
	got := e.reload(v.ID)
	if got.Status != "failed" || *got.ErrorDetail != "Processing failed." {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(e.slack.lastFailure(), "fork/exec /opt/secret/yt-dlp") {
		t.Fatalf("slack lost the real error: %q", e.slack.lastFailure())
	}
}

// Ruby: "the default uploader takes its refresh token from the database".
func TestUploaderSourceTakesTheRefreshTokenFromTheDatabase(t *testing.T) {
	e := newVideoEnv(t)
	source := UploaderSource(e.pool, "cid", "sec", "")
	// No row and no env var: the constructor guard fires.
	if _, err := source(ctx); !errors.Is(err, video.ErrAuthorizationExpired) {
		t.Fatalf("want ErrAuthorizationExpired, got %v", err)
	}
	must(t, store.StoreYoutubeCredential(ctx, e.pool, "from-db"))
	u, err := source(ctx)
	if err != nil || u == nil {
		t.Fatalf("with a stored token: %v %v", u, err)
	}
	// The environment value is only the bootstrap fallback.
	e2 := newVideoEnv(t)
	if u, err := UploaderSource(e2.pool, "cid", "sec", "from-env")(ctx); err != nil || u == nil {
		t.Fatalf("env fallback: %v %v", u, err)
	}
	// Unconfigured credentials are not an authorization problem.
	if _, err := UploaderSource(e2.pool, "", "", "x")(ctx); err == nil || errors.Is(err, video.ErrAuthorizationExpired) {
		t.Fatalf("missing client id: %v", err)
	}
}

func TestIngestMissingVideoIsSkipped(t *testing.T) {
	e := newVideoEnv(t)
	must(t, e.ingest(424242))
	if e.ytdlp.downloadCount() != 0 || e.uploader.callCount() != 0 {
		t.Fatal("worked on a missing row")
	}
}

// --- idempotency -----------------------------------------------------------

// The crash-after-upload re-run: the upload happened and was recorded, then the
// process died before the first caption check was queued. The job runs again;
// it must not upload a second video, and it must queue the check that is missing.
func TestIngestRerunAfterUploadDoesNotUploadAgain(t *testing.T) {
	e := newVideoEnv(t)
	e.deps.Enqueuer = &failingEnqueuer{next: DBEnqueuer{DB: e.pool}, fails: 1}
	v := e.newTweet()

	if err := e.ingest(v.ID); err == nil {
		t.Fatal("the failed enqueue must surface as a job error")
	}
	got := e.reload(v.ID)
	if got.Status != "awaiting_captions" || got.YoutubeID == nil || *got.YoutubeID != "YT9" {
		t.Fatalf("the upload must be recorded even though the enqueue failed: %+v", got)
	}
	if len(e.queued(KindTwitterVideoCaptions, v.ID)) != 0 {
		t.Fatal("nothing should be queued yet")
	}

	// Re-run (released or reclaimed job).
	must(t, e.ingest(v.ID))
	if n := e.uploader.callCount(); n != 1 {
		t.Fatalf("Upload called %d times: a duplicate YouTube video", n)
	}
	if e.ytdlp.downloadCount() != 1 {
		t.Fatalf("downloaded %d times", e.ytdlp.downloadCount())
	}
	if q := e.queued(KindTwitterVideoCaptions, v.ID); len(q) != 1 || !near(q[0].Delay, 5*time.Minute) {
		t.Fatalf("the missing first caption check must be queued: %+v", q)
	}

	// A third run finds the check pending and adds nothing.
	must(t, e.ingest(v.ID))
	if n := len(e.queued(KindTwitterVideoCaptions, v.ID)); n != 1 || e.uploader.callCount() != 1 {
		t.Fatalf("queued %d, uploads %d", n, e.uploader.callCount())
	}
}

// A crash right after the id was recorded, before the Slack notice and the
// enqueue (here: the state such a crash leaves). The re-run finds the upload,
// does not upload again and queues the first caption check.
func TestIngestRerunAfterCrashRightAfterUploadDoesNotUploadAgain(t *testing.T) {
	e := newVideoEnv(t)
	v := e.newTweet()
	ok, err := store.RecordTwitterVideoUpload(ctx, e.pool, v.ID, "YT9")
	must(t, err)
	if !ok {
		t.Fatal("setup")
	}
	must(t, e.ingest(v.ID))
	if n := e.uploader.callCount(); n != 0 || e.ytdlp.downloadCount() != 0 {
		t.Fatalf("uploads %d downloads %d after the re-run", n, e.ytdlp.downloadCount())
	}
	if q := e.queued(KindTwitterVideoCaptions, v.ID); len(q) != 1 || !near(q[0].Delay, 5*time.Minute) {
		t.Fatalf("the first caption check should exist after the re-run: %+v", q)
	}
}

func TestIngestSkipsVideosThatAreAlreadyPastTheUpload(t *testing.T) {
	for _, status := range []string{"summarizing", "publishing", "done", "failed"} {
		t.Run(status+" with a youtube id", func(t *testing.T) {
			e := newVideoEnv(t)
			v := e.newTweet()
			e.update(v.ID, store.TwitterVideoChanges{Status: &status, YoutubeID: new("OLD")})
			must(t, e.ingest(v.ID))
			got := e.reload(v.ID)
			if got.Status != status || *got.YoutubeID != "OLD" || e.uploader.callCount() != 0 || e.ytdlp.downloadCount() != 0 {
				t.Fatalf("got %+v", got)
			}
			if len(e.queued(KindTwitterVideoCaptions, v.ID)) != 0 || len(e.slack.failures) != 0 {
				t.Fatal("a skipped run must not enqueue or notify")
			}
		})
	}
	for _, status := range []string{"downloading", "uploading"} {
		t.Run(status+" with a youtube id", func(t *testing.T) {
			e := newVideoEnv(t)
			v := e.newTweet()
			e.update(v.ID, store.TwitterVideoChanges{Status: &status, YoutubeID: new("OLD")})
			must(t, e.ingest(v.ID))
			if e.uploader.callCount() != 0 {
				t.Fatal("uploaded although an id is stored")
			}
		})
	}
	for _, status := range []string{"summarizing", "publishing", "done"} {
		t.Run(status+" without an id", func(t *testing.T) {
			e := newVideoEnv(t)
			v := e.newTweet()
			e.update(v.ID, store.TwitterVideoChanges{Status: &status})
			must(t, e.ingest(v.ID))
			if e.uploader.callCount() != 0 || e.reload(v.ID).Status != status {
				t.Fatal("touched a row that is past the upload")
			}
		})
	}
}

// A failed row without a youtube id is a retry (a failed download, a dead
// grant): it must run.
func TestIngestRetriesAFailedVideoThatHasNoYoutubeID(t *testing.T) {
	e := newVideoEnv(t)
	v := e.newTweet()
	must(t, store.FailTwitterVideo(ctx, e.pool, v.ID, "earlier failure"))
	must(t, e.ingest(v.ID))
	if got := e.reload(v.ID); got.Status != "awaiting_captions" || e.uploader.callCount() != 1 {
		t.Fatalf("got %+v", got)
	}
}

// The shutdown grace ran out while uploading: the job is released, the row is
// not failed, and the re-run uploads (no id was ever returned).
func TestIngestShutdownDuringUploadLeavesTheRowForARerun(t *testing.T) {
	e := newVideoEnv(t)
	cctx, cancel := context.WithCancel(ctx)
	e.uploader.fn = func(ctx context.Context, _, _ string) (string, error) {
		cancel()
		return "", fmt.Errorf("upload: %w", ctx.Err())
	}
	v := e.newTweet()
	err := NewTwitterVideoIngest(e.deps)(cctx, Args{ID: v.ID})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled run must return the error so the Runner releases the job, got %v", err)
	}
	got := e.reload(v.ID)
	if got.Status != "uploading" || got.ErrorDetail != nil || got.YoutubeID != nil {
		t.Fatalf("row must stay as is: %+v", got)
	}
	if _, f := e.slack.counts(); f != 0 {
		t.Fatalf("shutdown is not a failure: %v", e.slack.failures)
	}

	e.uploader.fn = nil
	must(t, e.ingest(v.ID))
	if got := e.reload(v.ID); got.Status != "awaiting_captions" || *got.YoutubeID != "YT9" {
		t.Fatalf("re-run: %+v", got)
	}
	if e.ytdlp.downloadCount() != 1 {
		t.Fatalf("the re-run must reuse the downloaded file, downloads=%d", e.ytdlp.downloadCount())
	}
}

func TestIngestShutdownDuringDownloadLeavesTheRowForARerun(t *testing.T) {
	e := newVideoEnv(t)
	cctx, cancel := context.WithCancel(ctx)
	e.ytdlp.downloadFn = func(ctx context.Context, _, _ string) (video.Downloaded, error) {
		cancel()
		return video.Downloaded{}, fmt.Errorf("yt-dlp download: %w", ctx.Err())
	}
	v := e.newTweet()
	if err := NewTwitterVideoIngest(e.deps)(cctx, Args{ID: v.ID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if got := e.reload(v.ID); got.Status != "downloading" || got.ErrorDetail != nil {
		t.Fatalf("got %+v", got)
	}
	e.ytdlp.downloadFn = nil
	must(t, e.ingest(v.ID))
	if got := e.reload(v.ID); got.Status != "awaiting_captions" {
		t.Fatalf("got %+v", got)
	}
}

func TestIngestAlreadyCancelledContextDoesNothing(t *testing.T) {
	e := newVideoEnv(t)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	v := e.newTweet()
	if err := NewTwitterVideoIngest(e.deps)(cctx, Args{ID: v.ID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if e.ytdlp.downloadCount() != 0 {
		t.Fatal("downloaded on a cancelled context")
	}
}

// The upload returned an id and only then did the context end: the id is
// recorded and the next step queued on contexts that outlive the job's.
func TestIngestRecordsTheUploadEvenIfTheContextEndsRightAfter(t *testing.T) {
	e := newVideoEnv(t)
	cctx, cancel := context.WithCancel(ctx)
	e.uploader.fn = func(context.Context, string, string) (string, error) {
		cancel()
		return "YT9", nil
	}
	v := e.newTweet()
	must(t, NewTwitterVideoIngest(e.deps)(cctx, Args{ID: v.ID}))
	if got := e.reload(v.ID); got.Status != "awaiting_captions" || got.YoutubeID == nil || *got.YoutubeID != "YT9" {
		t.Fatalf("got %+v", got)
	}
	if len(e.queued(KindTwitterVideoCaptions, v.ID)) != 1 {
		t.Fatal("first caption check missing")
	}
}

// The Runner's per-kind deadline ran out: Ruby had no timeout, so this is a
// plain failure (generic detail), and the error is returned for the Runner.
func TestIngestDeadlineFailsTheVideo(t *testing.T) {
	e := newVideoEnv(t)
	e.deps.IngestTimeout = 50 * time.Millisecond // the handler enforces its own deadline
	e.uploader.fn = func(ctx context.Context, _, _ string) (string, error) {
		<-ctx.Done()
		return "", fmt.Errorf("upload: %w", ctx.Err())
	}
	v := e.newTweet()
	err := e.ingest(v.ID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if got := e.reload(v.ID); got.Status != "failed" || got.ErrorDetail == nil || *got.ErrorDetail != "Processing failed." {
		t.Fatalf("got %+v", got)
	}
	if _, f := e.slack.counts(); f != 1 {
		t.Fatalf("failure notice missing: %v", e.slack.failures)
	}
}

// A late writer must not drag a row that moved on back to "uploading".
func TestIngestDoesNotRegressARowThatMovedOn(t *testing.T) {
	e := newVideoEnv(t)
	v := e.newTweet()
	e.ytdlp.downloadFn = func(ctx context.Context, _, dir string) (video.Downloaded, error) {
		// While the download runs, the sweeper (or another run) finishes the row.
		must(t, store.FailTwitterVideo(ctx, e.pool, v.ID, "Processing failed."))
		must(t, store.UpdateTwitterVideo(ctx, e.pool, v.ID, store.TwitterVideoChanges{Status: new("done")}))
		must(t, os.MkdirAll(dir, 0o700))
		p := filepath.Join(dir, "x.mp4")
		return video.Downloaded{Path: p, Title: "T"}, os.WriteFile(p, nil, 0o600)
	}
	must(t, e.ingest(v.ID))
	if got := e.reload(v.ID); got.Status != "done" {
		t.Fatalf("regressed to %q", got.Status)
	}
	if e.uploader.callCount() != 0 {
		t.Fatal("uploaded for a row that is no longer ours")
	}
}

// Two uploads raced (both runs got past the guard): the second id is not
// stored over the first, and the duplicate is reported.
func TestIngestDoesNotReplaceAnExistingYoutubeID(t *testing.T) {
	e := newVideoEnv(t)
	v := e.newTweet()
	e.uploader.fn = func(ctx context.Context, _, _ string) (string, error) {
		_, err := store.RecordTwitterVideoUpload(ctx, e.pool, v.ID, "FIRST")
		must(t, err)
		return "SECOND", nil
	}
	must(t, e.ingest(v.ID))
	if got := e.reload(v.ID); *got.YoutubeID != "FIRST" {
		t.Fatalf("id replaced: %v", *got.YoutubeID)
	}
	if m := e.slack.lastFailure(); !contains(m, fmt.Sprintf("[twitter-video #%d]", v.ID), "SECOND") {
		t.Fatalf("duplicate not reported: %q", m)
	}
	if len(e.queued(KindTwitterVideoCaptions, v.ID)) != 0 {
		t.Fatal("a duplicate must not queue a second chain of caption checks")
	}
}
