package store

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Ruby's \w is ASCII-only here, and \A...\z (not ^...$) so a trailing newline
// is rejected. Go's \z matches only at the very end of the text.
var anchorPattern = regexp.MustCompile(`\A[A-Za-z0-9_\-:.]+\z`)

// UpsertScrollPosition remembers the anchor the user last reached in a file.
// One row exists per (user, file). The file name must not be blank
// (ErrInvalidFileName) and the anchor must match [\w\-:.]+ (ErrInvalidAnchor),
// since it is later interpolated into a page.
func UpsertScrollPosition(ctx context.Context, db DB, userID int64, fileName, anchor string) error {
	if strings.TrimSpace(fileName) == "" {
		return ErrInvalidFileName
	}
	if !anchorPattern.MatchString(anchor) {
		return ErrInvalidAnchor
	}
	now := time.Now().UTC()
	_, err := db.Exec(ctx, `INSERT INTO scroll_positions (user_id, file_name, anchor, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4)
		ON CONFLICT (user_id, file_name) DO UPDATE SET anchor = EXCLUDED.anchor, updated_at = EXCLUDED.updated_at`,
		userID, fileName, anchor, now)
	return err
}

// FindScrollPosition returns the stored anchor, or "" when there is none.
func FindScrollPosition(ctx context.Context, db DB, userID int64, fileName string) (string, error) {
	var anchor string
	err := db.QueryRow(ctx, `SELECT anchor FROM scroll_positions WHERE user_id = $1 AND file_name = $2`,
		userID, fileName).Scan(&anchor)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return anchor, err
}
