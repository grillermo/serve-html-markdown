package main

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/store"
)

// useTestDatabase empties the shared test database and points the commands'
// environment at it: APP_ENV=test selects serve_html_markdown_test, and a
// TEST_DATABASE_URL override is copied into DATABASE_*. It returns a pool on
// that database for assertions. Tests using it must not call t.Parallel.
func useTestDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testdb.New(t)
	for _, k := range []string{"DATABASE_HOST", "DATABASE_PORT", "DATABASE_USER", "DATABASE_PASSWORD", "DATABASE_NAME", "ADMIN_EMAIL", "ADMIN_PASSWORD"} {
		t.Setenv(k, "")
	}
	t.Setenv("APP_ENV", "test")
	if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
		cc, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("DATABASE_HOST", cc.Host)
		t.Setenv("DATABASE_PORT", strconv.Itoa(int(cc.Port)))
		t.Setenv("DATABASE_USER", cc.User)
		t.Setenv("DATABASE_PASSWORD", cc.Password)
		t.Setenv("DATABASE_NAME", cc.Database)
	}
	return pool
}

func runCmd(args ...string) (code int, out, errOut string) {
	var o, e bytes.Buffer
	code = runTo(args, &o, &e)
	return code, o.String(), e.String()
}

func TestSeedAdminCreatesThenUpdatesUser(t *testing.T) {
	pool := useTestDatabase(t)
	ctx := context.Background()
	t.Setenv("ADMIN_EMAIL", "Admin@Example.com")
	t.Setenv("ADMIN_PASSWORD", "first-password")

	code, out, errOut := runCmd("seed-admin")
	if code != 0 || out != "Seeded user Admin@Example.com\n" || errOut != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	u, err := store.FindUserByEmail(ctx, pool, "admin@example.com")
	if err != nil || u == nil {
		t.Fatalf("user not stored: %v %v", u, err)
	}
	if !auth.CheckPassword(u.EncryptedPassword, "first-password") {
		t.Error("stored hash does not match the password")
	}

	// Running it again resets the password of the existing user, like
	// `user.password = password; user.save!` in db/seeds.rb.
	t.Setenv("ADMIN_PASSWORD", "second-password")
	if code, _, errOut := runCmd("seed-admin"); code != 0 {
		t.Fatalf("second run: exit %d, stderr %q", code, errOut)
	}
	again, err := store.FindUserByEmail(ctx, pool, "admin@example.com")
	if err != nil || again == nil || again.ID != u.ID {
		t.Fatalf("user duplicated or lost: %v %v", again, err)
	}
	if !auth.CheckPassword(again.EncryptedPassword, "second-password") || auth.CheckPassword(again.EncryptedPassword, "first-password") {
		t.Error("password not replaced")
	}
}

func TestSeedAdminRequiresEnvironment(t *testing.T) {
	useTestDatabase(t)
	cases := []struct{ email, password, want string }{
		{"", "long-enough", "ADMIN_EMAIL is not set"},
		{"a@example.com", "", "ADMIN_PASSWORD is not set"},
		{"a@example.com", "short", "ADMIN_PASSWORD must be 6 to 128 characters"},
		{"a@example.com", strings.Repeat("x", 129), "ADMIN_PASSWORD must be 6 to 128 characters"},
		{"not-an-email", "long-enough", "invalid email"},
	}
	for _, c := range cases {
		t.Setenv("ADMIN_EMAIL", c.email)
		t.Setenv("ADMIN_PASSWORD", c.password)
		code, out, errOut := runCmd("seed-admin")
		if code != 1 || out != "" || !strings.Contains(errOut, c.want) {
			t.Errorf("%q/%q: exit %d, stdout %q, stderr %q; want %q", c.email, c.password, code, out, errOut, c.want)
		}
	}
}

func TestMigrateReportsAppliedAndUpToDate(t *testing.T) {
	pool := useTestDatabase(t)
	ctx := context.Background()

	if code, out, errOut := runCmd("migrate"); code != 0 || out != "Database is up to date.\n" {
		t.Fatalf("already migrated: exit %d, stdout %q, stderr %q", code, out, errOut)
	}

	// Undo the go_jobs migration, then run it again.
	if _, err := pool.Exec(ctx, `DROP TABLE go_jobs; DELETE FROM schema_migrations WHERE version = '20261117120000'`); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCmd("migrate")
	if code != 0 || out != "Applied 1 migration(s).\n" {
		t.Fatalf("pending: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('go_jobs') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		t.Errorf("go_jobs not recreated: %v %v", exists, err)
	}
}

func TestMigrateFailsWithoutDatabase(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("DATABASE_HOST", "127.0.0.1")
	t.Setenv("DATABASE_PORT", "1") // nothing listens here
	t.Setenv("DATABASE_NAME", "serve_html_markdown_test")
	code, out, errOut := runCmd("migrate")
	if code != 1 || out != "" || !strings.Contains(errOut, "migrate: connect to database") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// countMigrations must report a failing query instead of returning 0, or
// "Database is up to date." could be printed for a database it never reached.
func TestCountMigrationsPropagatesErrors(t *testing.T) {
	pool := useTestDatabase(t)
	ctx := context.Background()
	if n, err := countMigrations(ctx, pool); err != nil || n == 0 {
		t.Fatalf("healthy database: %d, %v", n, err)
	}
	if _, err := pool.Exec(ctx, `DROP TABLE schema_migrations`); err != nil {
		t.Fatal(err)
	}
	if n, err := countMigrations(ctx, pool); err != nil || n != 0 {
		t.Errorf("no schema_migrations table: %d, %v; want 0, nil", n, err)
	}
	pool.Close()
	if _, err := countMigrations(ctx, pool); err == nil {
		t.Error("closed pool: want an error")
	}
}

// Ruby: lib/tasks/youtube.rake. It needs no database and cannot fail: the URL is
// only HOST, YOUTUBE_REAUTH_URL and string work.
func TestYoutubeReauthURLPrintsTheRakeText(t *testing.T) {
	const tail = "Open this, approve access, and the new refresh token is stored for you:\n\n"
	const instructions = "\n\nYou must be signed in to the app first — the page is behind the sign-in page.\n"
	for _, c := range []struct{ name, host, override, want string }{
		{"host", "reader.example", "", "https://reader.example/youtube/reauth"},
		{"default host", "", "", "https://localhost:8009/youtube/reauth"},
		{"override", "reader.example", "https://other.example/go", "https://other.example/go"},
		{"blank override", "reader.example", "  ", "https://reader.example/youtube/reauth"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOST", c.host)
			t.Setenv("YOUTUBE_REAUTH_URL", c.override)
			code, out, errOut := runCmd("youtube-reauth-url")
			if code != 0 || errOut != "" || out != tail+c.want+instructions {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
			}
		})
	}
}
