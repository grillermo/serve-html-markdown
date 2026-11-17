// Package db opens the Postgres connection pool and applies migrations.
package db

import (
	"context"
	"fmt"
	"net"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"serve-html-markdown/internal/config"
)

// URL builds a postgres:// connection string from the database settings.
// User and password are percent-escaped.
func URL(c config.Config) string {
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(c.DatabaseHost, c.DatabasePort), Path: "/" + c.DatabaseName}
	if c.DatabaseUser != "" {
		u.User = url.UserPassword(c.DatabaseUser, c.DatabasePassword)
	}
	u.RawQuery = "sslmode=disable"
	return u.String()
}

// Open creates a connection pool. When prepared is false (PgBouncer in
// transaction mode, which cannot track prepared statements) the pool uses the
// simple query protocol. maxConns below 1 is raised to 1. Open does not
// connect: callers should Ping at startup to fail fast on a bad configuration.
func Open(ctx context.Context, dsn string, maxConns int, prepared bool) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if maxConns < 1 {
		maxConns = 1
	}
	cfg.MaxConns = int32(maxConns)
	if !prepared {
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}
