package store

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"serve-html-markdown/internal/video"
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
		n.SourceURL, n.Status, n.CaptionAttempts, utcTimePtr(n.UploadCompletedAt), now))
}

// FindTwitterVideo returns the row, or nil, nil when there is none.
func FindTwitterVideo(ctx context.Context, db DB, id int64) (*TwitterVideo, error) {
	return nilIfNoRows(scanTwitterVideo(db.QueryRow(ctx, `SELECT `+twitterVideoColumns+` FROM twitter_videos WHERE id = $1`, id)))
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
	return nilIfNoRows(scanTwitterVideo(db.QueryRow(ctx, `SELECT `+twitterVideoColumns+` FROM twitter_videos
		WHERE `+forSourceURLWhere+` ORDER BY id DESC LIMIT 1`, canonical, EscapeLike(canonical)+"?%")))
}

// FindOrCreateTwitterVideo is the controller's twitter path: retry the newest
// row for the tweet, or create a "downloading" one. created reports which.
//
// Like Rails, this is a SELECT followed by an INSERT and source_url has no
// unique index, so two concurrent identical requests may both insert (duplicate
// rows). That parity with the Rails app is accepted.
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
//
// Like Rails, this is a SELECT followed by a write and source_url has no unique
// index, so two concurrent identical requests may both insert (duplicate rows).
// That parity with the Rails app is accepted.
func StartYoutubeTwitterVideo(ctx context.Context, db DB, watchURL, youtubeID string) (v *TwitterVideo, existed bool, err error) {
	if blank(watchURL) {
		return nil, false, &InvalidTwitterVideoError{"source_url", "can't be blank"}
	}
	if !validText(watchURL) {
		return nil, false, &InvalidTwitterVideoError{"source_url", "is invalid"}
	}
	if blank(youtubeID) {
		return nil, false, &InvalidTwitterVideoError{"youtube_id", "can't be blank"}
	}
	if !validText(youtubeID) {
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
// statement. A nil field means "leave unchanged"; there is no way to set a
// column to NULL (none of the Ruby jobs need that; StartYoutubeTwitterVideo
// clears error_detail itself). An invalid status gives an *InvalidTwitterVideoError; a missing
// row gives ErrNotFound.
func UpdateTwitterVideo(ctx context.Context, db DB, id int64, c TwitterVideoChanges) error {
	sets, args, err := twitterVideoSets(id, c)
	if err != nil {
		return err
	}
	return requireRow(db.Exec(ctx, `UPDATE twitter_videos SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...))
}

// twitterVideoSets turns changes into SET clauses and their arguments: $1 is
// the id, $2 the updated_at stamp, further placeholders follow.
func twitterVideoSets(id int64, c TwitterVideoChanges) (sets []string, args []any, err error) {
	args = []any{id, utcNow()}
	sets = []string{"updated_at = $2"}
	set := func(column string, value any) {
		args = append(args, value)
		sets = append(sets, column+" = $"+strconv.Itoa(len(args)))
	}
	if c.Status != nil {
		if err := validTwitterStatus(*c.Status); err != nil {
			return nil, nil, err
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
			return nil, nil, &InvalidTwitterVideoError{f.column, "is invalid"}
		}
		set(f.column, *f.value)
	}
	if c.UploadCompletedAt != nil {
		set("upload_completed_at", c.UploadCompletedAt.UTC())
	}
	if c.CaptionAttempts != nil {
		set("caption_attempts", *c.CaptionAttempts)
	}
	return sets, args, nil
}

// TransitionTwitterVideo is UpdateTwitterVideo guarded by the current status:
// the row is only changed while its status is one of from, in the same
// statement, so a late writer (a re-run job, a sweeper that got there first)
// cannot move a row backwards. It reports whether a row was changed; a missing
// row and a row in another status are both "false, nil". from must be
// non-empty and hold valid statuses.
func TransitionTwitterVideo(ctx context.Context, db DB, id int64, from []string, c TwitterVideoChanges) (bool, error) {
	if len(from) == 0 {
		return false, errors.New("store: TransitionTwitterVideo needs at least one from status")
	}
	sets, args, err := twitterVideoSets(id, c)
	if err != nil {
		return false, err
	}
	placeholders := make([]string, len(from))
	for i, s := range from {
		if err := validTwitterStatus(s); err != nil {
			return false, err
		}
		args = append(args, s)
		placeholders[i] = "$" + strconv.Itoa(len(args))
	}
	tag, err := db.Exec(ctx, `UPDATE twitter_videos SET `+strings.Join(sets, ", ")+
		` WHERE id = $1 AND status IN (`+strings.Join(placeholders, ", ")+`)`, args...)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// FailTwitterVideoFrom is FailTwitterVideo guarded by status (see
// TransitionTwitterVideo): a row already done, or failed by someone else, is
// left alone and false is returned. The detail is sanitised like
// FailTwitterVideo's.
func FailTwitterVideoFrom(ctx context.Context, db DB, id int64, from []string, detail string) (bool, error) {
	detail = sanitizeDetail(detail)
	return TransitionTwitterVideo(ctx, db, id, from, TwitterVideoChanges{Status: new("failed"), ErrorDetail: &detail})
}

// FailTwitterVideo records a failure: status "failed" and the detail. It is the
// last resort of the jobs' rescue blocks, so it never rejects odd text: NUL
// bytes are dropped and invalid UTF-8 replaced (see sanitizeDetail).
func FailTwitterVideo(ctx context.Context, db DB, id int64, detail string) error {
	detail = sanitizeDetail(detail)
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

// checkpointsFor is the caption schedule in minutes after upload: the short one
// for a YouTube link, the long one otherwise.
func checkpointsFor(youtube bool) []int {
	if youtube {
		return YoutubeLinkCheckpoints
	}
	return CaptionCheckpoints
}

// Checkpoints is the caption schedule of this video (a YouTube link gets the short one, see video.YoutubeVideoID).
func (v *TwitterVideo) Checkpoints() []int {
	return checkpointsFor(video.YoutubeVideoID(v.SourceURL) != "")
}

// HasUploadTime reports whether upload_completed_at is set. Videos in
// "awaiting_captions" always have it; check this before NextCaptionWait if a
// missing time should not be read as "checkpoints exhausted".
func (v *TwitterVideo) HasUploadTime() bool { return v.UploadCompletedAt != nil }

// NextCaptionWait is how long to wait before the next caption check, given the
// attempts made so far: upload_completed_at plus the checkpoint for that
// attempt, minus now, in whole seconds and never negative. ok is false once the
// checkpoints are used up. It is also false when upload_completed_at is NULL
// (Ruby raised NoMethodError there, which the job's rescue turned into a
// failed video); that is indistinguishable from "exhausted" in the result, so
// callers (the captions job) must decide on purpose how to treat a NULL upload
// time, using HasUploadTime, instead of reporting "captions never became
// available".
func (v *TwitterVideo) NextCaptionWait(now time.Time) (wait time.Duration, ok bool) {
	if v.UploadCompletedAt == nil || v.CaptionAttempts < 0 {
		return 0, false
	}
	checkpoints := v.Checkpoints()
	if v.CaptionAttempts >= len(checkpoints) {
		return 0, false
	}
	target := v.UploadCompletedAt.Add(time.Duration(checkpoints[v.CaptionAttempts]) * time.Minute)
	return max(target.Sub(now).Truncate(time.Second), 0), true
}

// AdvanceCaptionAttempt counts one caption check as made, but only if the
// video is still awaiting_captions with exactly `from` attempts: the compare and
// the increment are one statement, so of several jobs that read the same
// attempt only one advances it (and so only one schedules the next check). It
// returns the new count and whether this call advanced it.
func AdvanceCaptionAttempt(ctx context.Context, db DB, id int64, from int) (attempts int, advanced bool, err error) {
	err = db.QueryRow(ctx, `UPDATE twitter_videos SET caption_attempts = caption_attempts + 1, updated_at = $3
		WHERE id = $1 AND status = 'awaiting_captions' AND caption_attempts = $2
		RETURNING caption_attempts`, id, from, utcNow()).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return attempts, err == nil, err
}

// RecordTwitterVideoUpload records a finished YouTube upload in ONE statement:
// youtube_id, status awaiting_captions, upload_completed_at now and
// caption_attempts 0. It is guarded by the id being unset (NULL or blank), not
// by the status: the video exists on YouTube whatever happened to the row
// meanwhile (a sweeper may have failed it during a long upload), and an id that
// is already stored is never replaced. false means an id was already stored (or
// the row is gone).
func RecordTwitterVideoUpload(ctx context.Context, db DB, id int64, youtubeID string) (bool, error) {
	if blank(youtubeID) || !validText(youtubeID) {
		return false, &InvalidTwitterVideoError{"youtube_id", "can't be blank"}
	}
	now := utcNow()
	tag, err := db.Exec(ctx, `UPDATE twitter_videos
		SET status = 'awaiting_captions', youtube_id = $2, upload_completed_at = $3, caption_attempts = 0, updated_at = $3
		WHERE id = $1 AND COALESCE(youtube_id, '') = ''`, id, youtubeID, now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ReclaimTwitterVideo puts a video left in summarizing or publishing by a run
// that died back to awaiting_captions, so the next run of its captions job
// starts over (it re-reads the captions, which is safe). Only rows untouched for
// staleAfter are taken: a live run moves its row through these statuses in
// seconds to minutes, and the caller passes a staleAfter well below the queue's
// stale-lock window. It reports whether the row was reclaimed.
func ReclaimTwitterVideo(ctx context.Context, db DB, id int64, staleAfter time.Duration) (bool, error) {
	now := utcNow()
	tag, err := db.Exec(ctx, `UPDATE twitter_videos SET status = 'awaiting_captions', updated_at = $2
		WHERE id = $1 AND status IN ('summarizing', 'publishing') AND updated_at < $3`, id, now, now.Add(-staleAfter))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// TwitterVideoFailedDetail is the error_detail of a video the sweeper gave up on.
const TwitterVideoFailedDetail = "Processing failed."

// staleVideoStatuses are the statuses a run is expected to move a video out of
// in minutes; awaiting_captions is not among them (it waits up to two days).
const staleVideoStatuses = `('downloading', 'uploading', 'summarizing', 'publishing')`

// StaleTwitterVideos lists up to limit videos stuck in downloading, uploading,
// summarizing or publishing and untouched (every status change refreshes
// updated_at) for staleAfter, oldest first: the ones whose job died or never ran.
func StaleTwitterVideos(ctx context.Context, db DB, staleAfter time.Duration, limit int) ([]TwitterVideo, error) {
	return collectTwitterVideos(db.Query(ctx, `SELECT `+twitterVideoColumns+` FROM twitter_videos
		WHERE status IN `+staleVideoStatuses+` AND updated_at < $1 ORDER BY id LIMIT $2`,
		utcNow().Add(-staleAfter), limit))
}

// FailStaleTwitterVideo fails one video that is still stuck (see
// StaleTwitterVideos) with TwitterVideoFailedDetail. One statement, guarded by
// status and age, so a video that moved on or was touched since the listing is
// left alone. It reports whether the row was failed.
func FailStaleTwitterVideo(ctx context.Context, db DB, id int64, staleAfter time.Duration) (bool, error) {
	now := utcNow()
	tag, err := db.Exec(ctx, `UPDATE twitter_videos SET status = 'failed', error_detail = $2, updated_at = $3
		WHERE id = $1 AND status IN `+staleVideoStatuses+` AND updated_at < $4`,
		id, TwitterVideoFailedDetail, now, now.Add(-staleAfter))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ClaimTwitterVideoForIngest is the lease an ingest run takes before it works on
// a video, so two runs (a resubmit while the first still downloads, a
// re-authorization retry) never work on one row. It succeeds, in one statement,
// for a video without a YouTube id that is
//
//   - failed (a retry: the row becomes downloading), or
//   - downloading or uploading and either never written since it was created
//     (updated_at = created_at: a fresh row nobody has claimed, or one whose
//     claim was released, see ReleaseTwitterVideoClaim) or untouched for lease
//     (the run that held it died).
//
// The claim stamps updated_at (never equal to created_at, so a claimed row is
// not mistaken for a fresh one); the holder keeps the lease by touching the row
// (TouchTwitterVideo) more often than lease. It reports whether the claim was
// taken. This needs no column of its own: the lease is the row's own updated_at.
// The cost is a contract, that nothing else writes a queued ingest's row before
// the run starts, or the run waits out the lease.
func ClaimTwitterVideoForIngest(ctx context.Context, db DB, id int64, lease time.Duration) (bool, error) {
	now := utcNow()
	tag, err := db.Exec(ctx, `UPDATE twitter_videos
		SET status = CASE WHEN status = 'failed' THEN 'downloading' ELSE status END,
		    updated_at = GREATEST($2::timestamp, created_at + interval '1 microsecond')
		WHERE id = $1 AND COALESCE(youtube_id, '') = ''
		  AND (status = 'failed'
		       OR (status IN ('downloading', 'uploading') AND (updated_at = created_at OR updated_at < $3)))`,
		id, now, now.Add(-lease))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// TouchTwitterVideo refreshes updated_at of a video an ingest run holds (the
// lease heartbeat). false means the video is no longer downloading or uploading
// without an id (finished, failed, uploaded).
func TouchTwitterVideo(ctx context.Context, db DB, id int64) (bool, error) {
	tag, err := db.Exec(ctx, `UPDATE twitter_videos SET updated_at = GREATEST($2::timestamp, created_at + interval '1 microsecond')
		WHERE id = $1 AND status IN ('downloading', 'uploading') AND COALESCE(youtube_id, '') = ''`, id, utcNow())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ReleaseTwitterVideoClaim gives the lease back after an interrupted run
// (shutdown): updated_at returns to created_at, the "nobody holds it" mark, so
// the re-run of the job can claim the row at once.
func ReleaseTwitterVideoClaim(ctx context.Context, db DB, id int64) (bool, error) {
	tag, err := db.Exec(ctx, `UPDATE twitter_videos SET updated_at = created_at
		WHERE id = $1 AND status IN ('downloading', 'uploading') AND COALESCE(youtube_id, '') = ''`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// AwaitingCaptionVideos lists up to limit videos in awaiting_captions, oldest
// first: the ones that should have a caption check queued.
func AwaitingCaptionVideos(ctx context.Context, db DB, limit int) ([]TwitterVideo, error) {
	return collectTwitterVideos(db.Query(ctx, `SELECT `+twitterVideoColumns+` FROM twitter_videos
		WHERE status = 'awaiting_captions' ORDER BY id LIMIT $1`, limit))
}
