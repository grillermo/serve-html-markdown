package main

import (
	"context"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/config"
	"serve-html-markdown/internal/db"
	"serve-html-markdown/internal/store"
)

// Devise's validatable bounds (config.password_length in devise.rb); the Rails
// seed raised on a password outside them.
const (
	minPasswordLength = 6
	maxPasswordLength = 128
)

// commandTimeout bounds one maintenance command.
const commandTimeout = 2 * time.Minute

// migrate applies the embedded migrations over a direct (non-PgBouncer)
// connection, since advisory locks and multi-statement DDL need a stable
// session. It prints how many migrations it applied.
func migrate(cfg config.Config, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	pool, err := db.Open(ctx, db.URL(cfg), 1, true)
	if err != nil {
		fmt.Fprintf(stderr, "migrate: %v\n", err)
		return 1
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintf(stderr, "migrate: connect to database %q: %v\n", cfg.DatabaseName, err)
		return 1
	}

	before, err := countMigrations(ctx, pool)
	if err != nil {
		fmt.Fprintf(stderr, "migrate: %v\n", err)
		return 1
	}
	if err := db.Migrate(ctx, pool); err != nil {
		fmt.Fprintf(stderr, "migrate: %v\n", err)
		return 1
	}
	after, err := countMigrations(ctx, pool)
	if err != nil {
		fmt.Fprintf(stderr, "migrate: %v\n", err)
		return 1
	}
	if n := after - before; n > 0 {
		fmt.Fprintf(stdout, "Applied %d migration(s).\n", n)
	} else {
		fmt.Fprintln(stdout, "Database is up to date.")
	}
	return 0
}

// countMigrations is the number of recorded versions. A database Rails never
// touched has no schema_migrations table yet, which counts as 0; any other
// failure is returned.
func countMigrations(ctx context.Context, q store.DB) (int, error) {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return 0, fmt.Errorf("count migrations: %w", err)
	}
	if !exists {
		return 0, nil
	}
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count migrations: %w", err)
	}
	return n, nil
}

// seedAdmin is db/seeds.rb: it creates the user ADMIN_EMAIL, or resets the
// password of the existing one, to ADMIN_PASSWORD. Like ENV.fetch, a missing
// variable is an error; like Devise's validatable, so is a bad email or a
// password outside 6..128 characters.
func seedAdmin(cfg config.Config, stdout, stderr io.Writer) int {
	if cfg.AdminEmail == "" {
		fmt.Fprintln(stderr, "seed-admin: ADMIN_EMAIL is not set")
		return 1
	}
	if cfg.AdminPassword == "" {
		fmt.Fprintln(stderr, "seed-admin: ADMIN_PASSWORD is not set")
		return 1
	}
	if n := utf8.RuneCountInString(cfg.AdminPassword); n < minPasswordLength || n > maxPasswordLength {
		fmt.Fprintf(stderr, "seed-admin: ADMIN_PASSWORD must be %d to %d characters long\n", minPasswordLength, maxPasswordLength)
		return 1
	}
	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		fmt.Fprintf(stderr, "seed-admin: hash password: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	pool, err := db.Open(ctx, db.URL(cfg), 1, true)
	if err != nil {
		fmt.Fprintf(stderr, "seed-admin: %v\n", err)
		return 1
	}
	defer pool.Close()
	if _, err := store.UpsertUser(ctx, pool, cfg.AdminEmail, hash); err != nil {
		fmt.Fprintf(stderr, "seed-admin: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Seeded user %s\n", cfg.AdminEmail)
	return 0
}
