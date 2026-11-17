package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// YoutubeRefreshToken returns the refresh token of the latest stored
// credential (highest id). When there is no row, or its token is blank, it
// returns envFallback (the YOUTUBE_REFRESH_TOKEN bootstrap value) unless that
// is blank too, in which case the result is "".
func YoutubeRefreshToken(ctx context.Context, db DB, envFallback string) (string, error) {
	var token string
	err := db.QueryRow(ctx, `SELECT refresh_token FROM youtube_credentials ORDER BY id DESC LIMIT 1`).Scan(&token)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if strings.TrimSpace(token) != "" {
		return token, nil
	}
	if strings.TrimSpace(envFallback) != "" {
		return envFallback, nil
	}
	return "", nil
}

// StoreYoutubeCredential overwrites the latest credential row with token (or
// inserts the first one) and stamps obtained_at. A blank token is ErrBlankToken.
func StoreYoutubeCredential(ctx context.Context, db DB, token string) error {
	if strings.TrimSpace(token) == "" {
		return ErrBlankToken
	}
	now := time.Now().UTC()
	tag, err := db.Exec(ctx, `UPDATE youtube_credentials SET refresh_token = $1, obtained_at = $2, updated_at = $2
		WHERE id = (SELECT max(id) FROM youtube_credentials)`, token, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	_, err = db.Exec(ctx, `INSERT INTO youtube_credentials (refresh_token, obtained_at, created_at, updated_at)
		VALUES ($1, $2, $2, $2)`, token, now)
	return err
}
