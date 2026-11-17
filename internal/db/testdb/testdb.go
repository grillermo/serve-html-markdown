// Package testdb gives tests a clean Postgres database.
//
// It shares the Rails test database (serve_html_markdown_test) with Rails;
// Rails tests do not take the lock described below, so do not run the Go and
// Rails suites at the same time. Within Go, the first call in each test
// process takes a session-level advisory lock that is held until the process
// exits, so test binaries of different packages (go test ./...) queue up
// behind one another instead of wiping the database under each other. Tests
// using it must not call t.Parallel.
package testdb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
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
	// lockWait bounds how long a process waits for other packages' tests. It
	// stays well below go test's default 10 minute timeout.
	lockWait = 5 * time.Minute
	// setupWait bounds the schema reset, load and migration once the lock is held.
	setupWait = 2 * time.Minute
)

var testDBName = regexp.MustCompile(`_test(_\d+)?$`)

var (
	once    sync.Once
	ready   bool // set only when setup ran to completion
	initErr error
	dsn     string
	// lockConn is never closed: the open session holds the lock until the
	// process exits.
	lockConn *pgx.Conn
	// truncateSQL empties every table except Rails' bookkeeping ones.
	truncateSQL string
)

// connectError marks failures to reach Postgres, for which a hint is useful.
type connectError struct{ err error }

func (e *connectError) Error() string { return e.err.Error() }
func (e *connectError) Unwrap() error { return e.err }

// New returns a pool on the test database with the schema loaded and every
// table empty. The pool is closed when the test ends.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	once.Do(runSetup)
	if !ready {
		err := initErr
		if err == nil {
			err = errors.New("setup did not complete")
		}
		var ce *connectError
		if errors.As(err, &ce) {
			t.Fatalf("testdb: %v (is Postgres running? set TEST_DATABASE_URL to override %s)", err, defaultURL)
		}
		t.Fatalf("testdb: %v", err)
	}

	ctx := context.Background()
	// Simple protocol, like the production pool behind PgBouncer.
	pool, err := db.Open(ctx, dsn, 4, false)
	if err != nil {
		t.Fatalf("testdb: open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if truncateSQL != "" {
		if _, err := pool.Exec(ctx, truncateSQL); err != nil {
			t.Fatalf("testdb: truncate: %v", err)
		}
	}
	return pool
}

func runSetup() {
	defer func() {
		if r := recover(); r != nil {
			ready = false
			initErr = fmt.Errorf("setup panicked: %v", r)
		}
	}()
	if initErr = setup(); initErr == nil {
		ready = true
	}
}

// checkWipeable reports whether the database in cfg is safe to drop and
// recreate: a test database (name ending in _test or _test_<digits>, as Rails
// names parallel databases) on this machine. Set TESTDB_ALLOW_REMOTE=1 to
// allow a non-local host.
func checkWipeable(cfg *pgx.ConnConfig) error {
	if !testDBName.MatchString(cfg.Database) {
		return fmt.Errorf("refusing to wipe database %q: its name must end in _test or _test_<digits>", cfg.Database)
	}
	if os.Getenv("TESTDB_ALLOW_REMOTE") == "1" {
		return nil
	}
	hosts := []string{cfg.Host}
	for _, fb := range cfg.Fallbacks {
		hosts = append(hosts, fb.Host)
	}
	for _, h := range hosts {
		if !isLocalHost(h) {
			return fmt.Errorf("refusing to wipe database on remote host %q (set TESTDB_ALLOW_REMOTE=1 to allow)", h)
		}
	}
	return nil
}

func isLocalHost(h string) bool {
	if strings.HasPrefix(h, "/") {
		return true
	}
	switch h {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// redacted describes the target database without its password.
func redacted(cfg *pgx.ConnConfig) string {
	return fmt.Sprintf("%s@%s:%d/%s", cfg.User, cfg.Host, cfg.Port, cfg.Database)
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
	if err := checkWipeable(cfg); err != nil {
		return err
	}
	schema, err := os.ReadFile(filepath.Join(repoRoot(), "db", "schema.sql"))
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}

	lockCtx, cancel := context.WithTimeout(context.Background(), lockWait)
	defer cancel()
	conn, err := pgx.ConnectConfig(lockCtx, cfg)
	if err != nil {
		return &connectError{fmt.Errorf("connect to %s: %w", redacted(cfg), err)}
	}
	if _, err := conn.Exec(lockCtx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		conn.Close(context.Background())
		if lockCtx.Err() != nil {
			return fmt.Errorf("timed out after %s waiting for advisory lock %d on %s: another test process may be holding it", lockWait, lockKey, redacted(cfg))
		}
		return fmt.Errorf("take advisory lock %d: %w", lockKey, err)
	}
	lockConn = conn

	ctx, cancelSetup := context.WithTimeout(context.Background(), setupWait)
	defer cancelSetup()
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

	rows, err := conn.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename NOT IN ('schema_migrations', 'ar_internal_metadata') ORDER BY tablename`)
	if err != nil {
		return fmt.Errorf("list tables: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("list tables: %w", err)
	}
	if len(names) > 0 {
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = pgx.Identifier{n}.Sanitize()
		}
		truncateSQL = "TRUNCATE " + strings.Join(quoted, ", ") + " RESTART IDENTITY CASCADE"
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
