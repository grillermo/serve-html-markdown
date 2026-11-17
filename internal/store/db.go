// Package store holds the SQL for each table as plain functions over DB.
//
// Conventions:
//
//   - Finders that look a row up by key (FindUserByEmail, FindUserByID,
//     NewestServedFile) return nil, nil when there is no such row; check the
//     result for nil. FindScrollPosition returns "" and YoutubeRefreshToken
//     returns "" likewise. Writes that target a row that does not exist (an
//     unknown user id) return ErrNotFound.
//   - Timestamp columns are "timestamp without time zone" holding UTC. Every
//     write passes utcNow() from the app (never SQL now()), and reads come back
//     as UTC time.Time values.
//   - Statements avoid prepared statements and session state so they work
//     through PgBouncer in transaction mode (simple protocol).
//   - Text containing NUL bytes or invalid UTF-8 can never be stored by
//     Postgres; inputs like that are treated as not found or rejected up front
//     instead of failing in the database.
package store

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	_ DB = (*pgxpool.Pool)(nil)
	_ DB = (pgx.Tx)(nil)
	_ DB = (*pgx.Conn)(nil)
)

// utcNow is the clock for every write; tests replace it (see export_test.go).
var utcNow = func() time.Time { return time.Now().UTC() }

// NowMS is the current time in epoch milliseconds, from the package clock.
func NowMS() int64 { return utcNow().UnixMilli() }

// blank reports whether s is empty or only whitespace (Rails' blank?).
func blank(s string) bool { return strings.TrimSpace(s) == "" }

// utcTimePtr converts a timestamp for a "timestamp without time zone" column,
// keeping nil as NULL.
func utcTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// sanitizeDetail makes free-form failure text storable: NUL bytes are dropped
// and invalid UTF-8 becomes U+FFFD.
func sanitizeDetail(s string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "\uFFFD")
}

// nilIfNoRows turns a missing row (pgx.ErrNoRows from a scan) into nil, nil,
// the convention of every finder.
func nilIfNoRows[T any](v *T, err error) (*T, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

// requireRow turns "no row was updated" into ErrNotFound.
func requireRow(tag pgconn.CommandTag, err error) error {
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// validText reports whether s can be stored in a Postgres text column.
func validText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
