// Package testdb gives tests a clean Postgres database.
//
// It shares the Rails test database (serve_html_markdown_test). The first call
// in each test process takes a session-level advisory lock that is held until
// the process exits, so test binaries of different packages (go test ./...)
// queue up behind one another instead of wiping the database under each other.
// Tests using it must not call t.Parallel.
package testdb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"serve-html-markdown/internal/db"
)

const (
	defaultURL = "postgres://localhost:5432/serve_html_markdown_test?sslmode=disable"
	// lockKey is the advisory lock serializing test processes.
	lockKey int64 = 7351840292
	// lockWait bounds how long a process waits for other packages' tests.
	lockWait = 10 * time.Minute
)

var (
	once    sync.Once
	initErr error
	dsn     string
	// lockConn is never closed: the open session holds the lock until the
	// process exits.
	lockConn *pgx.Conn
)

// New returns a pool on the test database with the schema loaded and every
// table empty. The pool is closed when the test ends.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	once.Do(func() { initErr = setup() })
	if initErr != nil {
		t.Fatalf("testdb: %v (is Postgres running? set TEST_DATABASE_URL to override %s)", initErr, defaultURL)
	}

	ctx := context.Background()
	// Simple protocol, like the production pool behind PgBouncer.
	pool, err := db.Open(ctx, dsn, 4, false)
	if err != nil {
		t.Fatalf("testdb: open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `TRUNCATE users, expansions, scroll_positions, served_files, twitter_videos, youtube_credentials, go_jobs RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("testdb: truncate: %v", err)
	}
	return pool
}

func setup() error {
	dsn = os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = defaultURL
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse TEST_DATABASE_URL: %w", err)
	}
	// This package drops the public schema: refuse anything not named like a test DB.
	if !strings.Contains(cfg.Database, "test") {
		return fmt.Errorf("refusing to wipe database %q: its name must contain \"test\"", cfg.Database)
	}
	schema, err := os.ReadFile(filepath.Join(repoRoot(), "db", "schema.sql"))
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), lockWait)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		conn.Close(context.Background())
		return fmt.Errorf("take test lock: %w", err)
	}
	lockConn = conn

	if _, err := conn.Exec(ctx, "DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public"); err != nil {
		return fmt.Errorf("reset schema: %w", err)
	}
	if _, err := conn.Exec(ctx, string(schema)); err != nil {
		return fmt.Errorf("load db/schema.sql: %w", err)
	}
	// schema.sql blanks search_path for the whole session.
	if _, err := conn.Exec(ctx, "RESET search_path"); err != nil {
		return fmt.Errorf("reset search_path: %w", err)
	}
	if err := db.Migrate(ctx, conn); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// repoRoot finds the directory holding go.mod, starting from this source file.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Dir(file)
		}
		dir = parent
	}
}
