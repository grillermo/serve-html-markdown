package store

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// TwitterVideoStatuses are the allowed values of twitter_videos.status, in the
// order of a video's life ("failed" can happen at any point). New rows start
// "downloading".
var TwitterVideoStatuses = []string{"downloading", "uploading", "awaiting_captions", "summarizing", "publishing", "done", "failed"}

// CaptionCheckpoints are the minutes after upload_completed_at at which the
// captions are looked for. The last two are 1 and 2 days out, for videos whose
// auto-captions only show up long after upload.
var CaptionCheckpoints = []int{5, 10, 20, 30, 40, 60, 120, 1440, 2880}

// YoutubeLinkCheckpoints are used for a YouTube link: its video is already
// public, so if the captions have not shown up within two hours they almost
// certainly never will.
var YoutubeLinkCheckpoints = []int{5, 10, 20, 30, 40, 60, 120}

// InvalidTwitterVideoError reports which field was rejected. Message is
// Rails' wording ("can't be blank", "is not included in the list").
type InvalidTwitterVideoError struct {
	Field   string
	Message string
}

func (e *InvalidTwitterVideoError) Error() string {
	return ErrInvalidTwitterVideo.Error() + ": " + e.Field + " " + e.Message
}

// Is makes errors.Is(err, ErrInvalidTwitterVideo) true.
func (e *InvalidTwitterVideoError) Is(target error) bool { return target == ErrInvalidTwitterVideo }

// TwitterVideo is a row of twitter_videos (it also tracks YouTube links).
// Nullable columns are pointers (nil is NULL).
type TwitterVideo struct {
	ID                int64
	SourceURL         string
	Status            string
	CaptionAttempts   int
	UploadCompletedAt *time.Time
	VideoPath         *string
	YoutubeID         *string
	YoutubeTitle      *string
	HTMLFilename      *string
	RulinkyLinkID     *string
	ErrorDetail       *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

const twitterVideoColumns = `id, source_url, status, caption_attempts, upload_completed_at, video_path, youtube_id,
	youtube_title, html_filename, rulinky_link_id, error_detail, created_at, updated_at`

func scanTwitterVideo(row pgx.Row) (*TwitterVideo, error) {
	var v TwitterVideo
	err := row.Scan(&v.ID, &v.SourceURL, &v.Status, &v.CaptionAttempts, &v.UploadCompletedAt, &v.VideoPath, &v.YoutubeID,
		&v.YoutubeTitle, &v.HTMLFilename, &v.RulinkyLinkID, &v.ErrorDetail, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func collectTwitterVideos(rows pgx.Rows, err error) ([]TwitterVideo, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (TwitterVideo, error) {
		v, err := scanTwitterVideo(row)
		if err != nil {
			return TwitterVideo{}, err
		}
		return *v, nil
	})
}

func validTwitterStatus(status string) *InvalidTwitterVideoError {
	if !slices.Contains(TwitterVideoStatuses, status) {
		return &InvalidTwitterVideoError{"status", "is not included in the list"}
	}
	return nil
}

// NewTwitterVideo is a row to insert. Status "" means "downloading". The
// caption fields exist for rows that are created further along their life.
type NewTwitterVideo struct {
	SourceURL         string
	Status            string
	CaptionAttempts   int
	UploadCompletedAt *time.Time
}

// CreateTwitterVideo inserts the row. A blank source URL or a status outside
// TwitterVideoStatuses gives an *InvalidTwitterVideoError.
func CreateTwitterVideo(ctx context.Context, db DB, n NewTwitterVideo) (*TwitterVideo, error) {
	if n.Status == "" {
		n.Status = "downloading"
	}
	if blank(n.SourceURL) {
		return nil, &InvalidTwitterVideoError{"source_url", "can't be blank"}
	}
	if !validText(n.SourceURL) {
		return nil, &InvalidTwitterVideoError{"source_url", "is invalid"}
	}
	if err := validTwitterStatus(n.Status); err != nil {
		return nil, err
	}
	now := utcNow()
	return scanTwitterVideo(db.QueryRow(ctx, `INSERT INTO twitter_videos
		(source_url, status, caption_attempts, upload_completed_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)
		RETURNING `+twitterVideoColumns,
		n.SourceURL, n.Status, n.CaptionAttempts, utcPtr(n.UploadCompletedAt), now))
}

// utcPtr converts a timestamp for a "timestamp without time zone" column.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// FindTwitterVideo returns the row, or nil, nil when there is none.
func FindTwitterVideo(ctx context.Context, db DB, id int64) (*TwitterVideo, error) {
	v, err := scanTwitterVideo(db.QueryRow(ctx, `SELECT `+twitterVideoColumns+` FROM twitter_videos WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

// forSourceURLWhere matches the stored URL exactly or followed by a query
// string. $1 is the canonical URL, $2 its escaped LIKE prefix.
const forSourceURLWhere = `(source_url = $1 OR source_url LIKE $2)`

// canonicalSourceURL drops the query string, like Ruby's url.to_s.split("?").first.
// Normalising a submitted link (TwitterUrl.normalize) is the caller's job.
func canonicalSourceURL(sourceURL string) string {
	canonical, _, _ := strings.Cut(sourceURL, "?")
	return canonical
}

// TwitterVideosForSourceURL returns the rows for the same tweet, newest first,
// ignoring any tracking query string: for "https://x.com/a/status/1?s=20" it
// finds rows stored as "https://x.com/a/status/1" and
// "https://x.com/a/status/1?t=x" but not ".../status/12". The URL is expected
// to be normalized already (the port of TwitterUrl.normalize lives elsewhere);
// only a query string is stripped here, as in Ruby.
func TwitterVideosForSourceURL(ctx context.Context, db DB, sourceURL string) ([]TwitterVideo, error) {
	if !validText(sourceURL) {
		return nil, nil
	}
	canonical := canonicalSourceURL(sourceURL)
	return collectTwitterVideos(db.Query(ctx, `SELECT `+twitterVideoColumns+` FROM twitter_videos
		WHERE `+forSourceURLWhere+` ORDER BY id DESC`, canonical, EscapeLike(canonical)+"?%"))
}

// LatestTwitterVideoForSourceURL is the newest of TwitterVideosForSourceURL, or
// nil, nil.
func LatestTwitterVideoForSourceURL(ctx context.Context, db DB, sourceURL string) (*TwitterVideo, error) {
	if !validText(sourceURL) {
		return nil, nil
	}
	canonical := canonicalSourceURL(sourceURL)
	v, err := scanTwitterVideo(db.QueryRow(ctx, `SELECT `+twitterVideoColumns+` FROM twitter_videos
		WHERE `+forSourceURLWhere+` ORDER BY id DESC LIMIT 1`, canonical, EscapeLike(canonical)+"?%"))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

// FindOrCreateTwitterVideo is the controller's twitter path: retry the newest
// row for the tweet, or create a "downloading" one. created reports which.
func FindOrCreateTwitterVideo(ctx context.Context, db DB, sourceURL string) (v *TwitterVideo, created bool, err error) {
	if v, err = LatestTwitterVideoForSourceURL(ctx, db, sourceURL); v != nil || err != nil {
		return v, false, err
	}
	v, err = CreateTwitterVideo(ctx, db, NewTwitterVideo{SourceURL: sourceURL})
	return v, err == nil, err
}

// StartYoutubeTwitterVideo is the controller's YouTube path: the newest row with
// exactly this watch URL (or a new one) is reset to "awaiting_captions" with the
// video id, no caption attempts, upload_completed_at now and the error cleared.
// existed reports whether it was a retry.
func StartYoutubeTwitterVideo(ctx context.Context, db DB, watchURL, youtubeID string) (v *TwitterVideo, existed bool, err error) {
	if blank(watchURL) {
		return nil, false, &InvalidTwitterVideoError{"source_url", "can't be blank"}
	}
	if !validText(watchURL) || !validText(youtubeID) {
		return nil, false, &InvalidTwitterVideoError{"youtube_id", "is invalid"}
	}
	var id int64
	err = db.QueryRow(ctx, `SELECT id FROM twitter_videos WHERE source_url = $1 ORDER BY id DESC LIMIT 1`, watchURL).Scan(&id)
	now := utcNow()
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		v, err = scanTwitterVideo(db.QueryRow(ctx, `INSERT INTO twitter_videos
			(source_url, status, youtube_id, caption_attempts, upload_completed_at, created_at, updated_at)
			VALUES ($1, 'awaiting_captions', $2, 0, $3, $3, $3)
			RETURNING `+twitterVideoColumns, watchURL, youtubeID, now))
		return v, false, err
	case err != nil:
		return nil, false, err
	}
	v, err = scanTwitterVideo(db.QueryRow(ctx, `UPDATE twitter_videos
		SET status = 'awaiting_captions', youtube_id = $2, caption_attempts = 0, upload_completed_at = $3,
		    error_detail = NULL, updated_at = $3
		WHERE id = $1
		RETURNING `+twitterVideoColumns, id, youtubeID, now))
	if errors.Is(err, pgx.ErrNoRows) { // deleted in between
		return nil, false, ErrNotFound
	}
	return v, true, err
}

// DownloadedSiblings are the rows for the same tweet (query string ignored)
// that have a video_path, the given row first and then newest first: the
// candidates for reusing an earlier download.
func DownloadedSiblings(ctx context.Context, db DB, v *TwitterVideo) ([]TwitterVideo, error) {
	if !validText(v.SourceURL) {
		return nil, nil
	}
	canonical := canonicalSourceURL(v.SourceURL)
	return collectTwitterVideos(db.Query(ctx, `SELECT `+twitterVideoColumns+` FROM twitter_videos
		WHERE `+forSourceURLWhere+` AND video_path IS NOT NULL
		ORDER BY CASE WHEN id = $3 THEN 0 ELSE 1 END, id DESC`, canonical, EscapeLike(canonical)+"?%", v.ID))
}

// TwitterVideoChanges lists the columns to set; nil fields are left alone.
// (Go 1.26: new("done") makes a *string.)
type TwitterVideoChanges struct {
	Status            *string
	VideoPath         *string
	YoutubeID         *string
	YoutubeTitle      *string
	HTMLFilename      *string
	RulinkyLinkID     *string
	UploadCompletedAt *time.Time
	CaptionAttempts   *int
	ErrorDetail       *string
}

// UpdateTwitterVideo sets the given columns and bumps updated_at in one
// statement. An invalid status gives an *InvalidTwitterVideoError; a missing
// row gives ErrNotFound.
func UpdateTwitterVideo(ctx context.Context, db DB, id int64, c TwitterVideoChanges) error {
	args := []any{id, utcNow()}
	sets := []string{"updated_at = $2"}
	set := func(column string, value any) {
		args = append(args, value)
		sets = append(sets, column+" = $"+strconv.Itoa(len(args)))
	}
	if c.Status != nil {
		if err := validTwitterStatus(*c.Status); err != nil {
			return err
		}
		set("status", *c.Status)
	}
	for _, f := range []struct {
		column string
		value  *string
	}{
		{"video_path", c.VideoPath}, {"youtube_id", c.YoutubeID}, {"youtube_title", c.YoutubeTitle},
		{"html_filename", c.HTMLFilename}, {"rulinky_link_id", c.RulinkyLinkID}, {"error_detail", c.ErrorDetail},
	} {
		if f.value == nil {
			continue
		}
		if !validText(*f.value) {
			return &InvalidTwitterVideoError{f.column, "is invalid"}
		}
		set(f.column, *f.value)
	}
	if c.UploadCompletedAt != nil {
		set("upload_completed_at", c.UploadCompletedAt.UTC())
	}
	if c.CaptionAttempts != nil {
		set("caption_attempts", *c.CaptionAttempts)
	}
	return requireRow(db.Exec(ctx, `UPDATE twitter_videos SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...))
}

// FailTwitterVideo records a failure: status "failed" and the detail.
func FailTwitterVideo(ctx context.Context, db DB, id int64, detail string) error {
	return UpdateTwitterVideo(ctx, db, id, TwitterVideoChanges{Status: new("failed"), ErrorDetail: &detail})
}

// IncrementCaptionAttempts adds one to caption_attempts atomically and returns
// the new count. ErrNotFound for a missing row.
func IncrementCaptionAttempts(ctx context.Context, db DB, id int64) (int, error) {
	var attempts int
	err := db.QueryRow(ctx, `UPDATE twitter_videos SET caption_attempts = caption_attempts + 1, updated_at = $2
		WHERE id = $1 RETURNING caption_attempts`, id, utcNow()).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return attempts, err
}

// CheckpointsFor is the caption schedule in minutes after upload: the short one
// for a YouTube link, the long one otherwise.
func CheckpointsFor(youtube bool) []int {
	if youtube {
		return YoutubeLinkCheckpoints
	}
	return CaptionCheckpoints
}

// youtubeVideoID is the 11-character id Ruby's YoutubeUrl accepts.
var youtubeVideoID = regexp.MustCompile(`\A[\w-]{11}\z`)

// IsYoutubeSource reports whether a stored source URL is a YouTube link. The
// app only stores YouTube links as canonical watch URLs
// ("https://www.youtube.com/watch?v=<id>", see StartYoutubeTwitterVideo), so
// this recognises that form and the equivalent youtu.be one, rather than
// porting all of Ruby's YoutubeUrl.video_id (that is internal/video's job; a
// caller that has the result can use CheckpointsFor and NextCaptionWaitFor).
func IsYoutubeSource(sourceURL string) bool {
	u, err := url.Parse(strings.TrimSpace(sourceURL))
	if err != nil {
		return false
	}
	switch strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.") {
	case "youtube.com", "m.youtube.com", "music.youtube.com", "gaming.youtube.com", "youtube-nocookie.com":
		return strings.TrimSuffix(u.Path, "/") == "/watch" && youtubeVideoID.MatchString(u.Query().Get("v"))
	case "youtu.be":
		id, _, _ := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
		return youtubeVideoID.MatchString(id)
	}
	return false
}

// Checkpoints is the caption schedule of this video (see IsYoutubeSource).
func (v *TwitterVideo) Checkpoints() []int { return CheckpointsFor(IsYoutubeSource(v.SourceURL)) }

// NextCaptionWait is how long to wait before the next caption check, given the
// attempts made so far: upload_completed_at plus the checkpoint for that
// attempt, minus now, in whole seconds and never negative. ok is false once the
// checkpoints are used up, or when the upload time is unknown (Ruby raised on
// that, which also ended the video as failed).
func (v *TwitterVideo) NextCaptionWait(now time.Time) (wait time.Duration, ok bool) {
	return NextCaptionWaitFor(v.Checkpoints(), v.CaptionAttempts, v.UploadCompletedAt, now)
}

// NextCaptionWaitFor is NextCaptionWait with the schedule and the video's
// fields passed in.
func NextCaptionWaitFor(checkpoints []int, attempts int, uploadCompletedAt *time.Time, now time.Time) (time.Duration, bool) {
	if attempts < 0 || attempts >= len(checkpoints) || uploadCompletedAt == nil {
		return 0, false
	}
	target := uploadCompletedAt.Add(time.Duration(checkpoints[attempts]) * time.Minute)
	return max(target.Sub(now).Truncate(time.Second), 0), true
}
