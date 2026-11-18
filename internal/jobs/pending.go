package jobs

import (
	"context"
	"fmt"

	"serve-html-markdown/internal/store"
)

// HasPendingJob reports whether a job of kind for the record id is queued or
// running (not finished). The video handlers and sweeper use it to queue the
// next step of a video only when none exists, so a re-run does not start a
// second chain of caption checks.
func HasPendingJob(ctx context.Context, db store.DB, kind string, id int64) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM go_jobs WHERE kind = $1 AND finished_at IS NULL AND args @> $2::jsonb)`,
		kind, fmt.Sprintf(`{"id": %d}`, id)).Scan(&ok)
	return ok, err
}
