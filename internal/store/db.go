// Package store holds the SQL for each table as plain functions over DB.
//
// Timestamp columns are "timestamp without time zone" holding UTC. Every
// write passes time.Now().UTC() from the app (never SQL now()), and reads come
// back as UTC time.Time values. Statements avoid prepared statements and
// session state so they work through PgBouncer in transaction mode.
package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
