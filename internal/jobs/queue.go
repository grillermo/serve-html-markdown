// Package jobs is the background job queue: one Postgres table (go_jobs),
// claimed with a single FOR UPDATE SKIP LOCKED statement, and a Runner that
// polls it. It replaces Solid Queue.
//
// Semantics, in parity with the Rails app:
//
//   - A job runs once. Errors and panics are recorded in last_error and the
//     job is finished; nothing is retried (every Rails job rescued
//     StandardError and there was no retry_on/discard_on).
//   - The exceptions are a crash and a deploy. A job that was claimed but never
//     finished (the process died) is claimed again once its lock is older than
//     the stale window (30 minutes). A job whose handler was cut short by the
//     shutdown grace period is released at once and runs again in the next
//     process. So handlers must be safe to run again after a partial run:
//     the generate_expansion handler is, precisely: it claims the expansion
//     first (a finished or running one is a no-op); a run cut short by shutdown
//     puts it back to pending; a run that died leaves a processing row that
//     the next run takes over once it is 15 minutes untouched; the processor
//     stores the result url on the row as soon as its files are written, and a
//     re-run that finds one completes with it, so files are never written
//     twice; and a sweeper finishes rows no job will ever claim. What it
//     cannot undo: a crash after the LLM answered but before any file was
//     written repeats the (paid) LLM call, and a crash between writing the
//     files and storing the url (two statements apart) can still write a
//     second result. TwitterVideoCaptionsJob is safe too (it only acts on
//     status awaiting_captions, and its writes are guarded by status and by
//     the attempt number, see NewTwitterVideoCaptions), and so is
//     TwitterVideoIngestJob, which Ruby was not: Task 9.6 added the
//     guard. A video that already has a youtube_id (or is past the upload) is
//     never downloaded or uploaded again, and the id is recorded in the
//     statement that moves the row on, before anything else. What remains is
//     the window of an upload that was cut short before it returned an id: a
//     re-run uploads again (see NewTwitterVideoIngest).
//   - Running jobs refresh their lock (heartbeat), so a long handler such as a
//     yt-dlp download is never mistaken for a crashed one. The flip side: the
//     heartbeat also keeps a hung job locked forever, so handlers that can
//     hang (yt-dlp, HTTP calls) must set their own deadlines, and the Runner
//     has per-kind Timeouts for it (the serve wiring passes them).
//   - Heartbeat, finish and release only act while the worker still owns the
//     claim (the attempts counter returned by claim is the token), so a worker
//     whose lock went stale and was re-claimed elsewhere cannot touch the job.
//
// Time: every timestamp here is a timestamptz taken from the database clock
// (now()), unlike the timestamp-without-time-zone columns in package store,
// which get UTC from the app. timestamptz is absolute, so nothing depends on
// the session time zone, and using one clock (the DB's) for run_at, locked_at
// and the claim comparison means app/DB clock skew cannot make a job early,
// late or prematurely stale. The delay is computed in SQL from the wait in
// seconds (a float, so sub-millisecond rounding and multi-day waits are both
// exact enough).
//
// Everything works through PgBouncer in transaction mode: each operation is a
// single statement, with no prepared statements, LISTEN/NOTIFY or session
// locks.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"serve-html-markdown/internal/store"
)

// Job kinds, one per Rails job class.
const (
	KindGenerateExpansion    = "generate_expansion"
	KindTwitterVideoIngest   = "twitter_video_ingest"
	KindTwitterVideoCaptions = "twitter_video_captions"
)

// ErrInvalidJob is returned by Enqueue for a blank kind or a non-positive id
// (every job refers to a saved record).
var ErrInvalidJob = errors.New("jobs: invalid job")

// Args is the payload of every job: the id of the record to work on.
type Args struct {
	ID int64 `json:"id"`
}

// Enqueuer schedules jobs; handlers and web handlers depend on it so tests can
// use a fake.
type Enqueuer interface {
	Enqueue(ctx context.Context, kind string, args Args, wait time.Duration) error
}

// DBEnqueuer enqueues into go_jobs.
type DBEnqueuer struct{ DB store.DB }

var _ Enqueuer = DBEnqueuer{}

// Enqueue implements Enqueuer.
func (e DBEnqueuer) Enqueue(ctx context.Context, kind string, args Args, wait time.Duration) error {
	return Enqueue(ctx, e.DB, kind, args, wait)
}

// Enqueue inserts a job that becomes runnable after wait (0 or negative: now).
func Enqueue(ctx context.Context, db store.DB, kind string, args Args, wait time.Duration) error {
	if strings.TrimSpace(kind) == "" || args.ID <= 0 {
		return fmt.Errorf("%w: kind %q, id %d", ErrInvalidJob, kind, args.ID)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	if wait < 0 {
		wait = 0
	}
	_, err = db.Exec(ctx,
		`INSERT INTO go_jobs (kind, args, run_at) VALUES ($1, $2::jsonb, now() + $3::float8 * interval '1 second')`,
		kind, string(raw), wait.Seconds())
	return err
}

type job struct {
	ID      int64
	Kind    string
	Args    Args
	ArgsErr error // the stored args did not decode; the worker finishes the job with it
	Attempt int
}

// claimSQL takes the oldest runnable job in one statement. $1 is the stale
// lock window in seconds.
const claimSQL = `UPDATE go_jobs SET locked_at = now(), attempts = attempts + 1
WHERE id = (
  SELECT id FROM go_jobs
  WHERE finished_at IS NULL AND run_at <= now()
    AND (locked_at IS NULL OR locked_at < now() - $1::float8 * interval '1 second')
  ORDER BY run_at, id
  FOR UPDATE SKIP LOCKED
  LIMIT 1)
RETURNING id, kind, args::text, attempts`

// claim returns the next runnable job, or nil, nil when there is none.
func claim(ctx context.Context, db store.DB, staleAfter time.Duration) (*job, error) {
	rows, err := db.Query(ctx, claimSQL, staleAfter.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var j job
	var args string
	if err := rows.Scan(&j.ID, &j.Kind, &args, &j.Attempt); err != nil {
		return nil, err
	}
	j.ArgsErr = json.Unmarshal([]byte(args), &j.Args)
	rows.Close()
	return &j, rows.Err()
}

// The three writes below take the attempts value returned by claim as an
// ownership token and report whether the row was still theirs.

// heartbeat refreshes the lock of a job that is still running.
func heartbeat(ctx context.Context, db store.DB, id int64, attempt int) (bool, error) {
	tag, err := db.Exec(ctx, `UPDATE go_jobs SET locked_at = now()
WHERE id = $1 AND finished_at IS NULL AND locked_at IS NOT NULL AND attempts = $2`, id, attempt)
	return tag.RowsAffected() > 0, err
}

// finish marks a job done; lastErr is empty on success.
func finish(ctx context.Context, db store.DB, id int64, attempt int, lastErr string) (bool, error) {
	var e *string
	if lastErr != "" {
		e = &lastErr
	}
	tag, err := db.Exec(ctx, `UPDATE go_jobs SET finished_at = now(), last_error = $3
WHERE id = $1 AND finished_at IS NULL AND attempts = $2`, id, attempt, e)
	return tag.RowsAffected() > 0, err
}

// release unlocks a job interrupted by shutdown so the next process runs it
// without waiting for the stale window.
func release(ctx context.Context, db store.DB, id int64, attempt int) (bool, error) {
	tag, err := db.Exec(ctx, `UPDATE go_jobs SET locked_at = NULL
WHERE id = $1 AND finished_at IS NULL AND attempts = $2`, id, attempt)
	return tag.RowsAffected() > 0, err
}

// Cleanup deletes jobs finished more than 24 hours ago (what
// clear_solid_queue_finished_jobs did) and returns how many.
func Cleanup(ctx context.Context, db store.DB) (int64, error) {
	tag, err := db.Exec(ctx, `DELETE FROM go_jobs WHERE finished_at < now() - interval '24 hours'`)
	return tag.RowsAffected(), err
}

// maxErrorBytes bounds last_error.
const maxErrorBytes = 4096

// errorText makes a failure message storable: no NUL bytes, valid UTF-8, at
// most maxErrorBytes. It is only ever err.Error() or a panic value, never
// arguments or environment.
func errorText(s string) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "�")
	if len(s) <= maxErrorBytes {
		return s
	}
	cut := maxErrorBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
