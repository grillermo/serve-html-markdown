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

func TestMoveSolidQueueJobs(t *testing.T) {
	pool := useTestDatabase(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	job := func(class string, arg int, scheduled string, finished bool) int64 {
		t.Helper()
		var id int64
		err := pool.QueryRow(ctx, `INSERT INTO solid_queue_jobs (active_job_id, arguments, class_name, queue_name, scheduled_at, finished_at, created_at, updated_at)
			VALUES ('x', $1, $2, 'default', NULLIF($3, '')::timestamp, CASE WHEN $4 THEN now() END, now(), now()) RETURNING id`,
			`{"job_class":"`+class+`","arguments":[`+strconv.Itoa(arg)+`]}`, class, scheduled, finished).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	job("GenerateExpansionJob", 11, "", false)
	job("TwitterVideoCaptionsJob", 22, "2026-11-20 10:00:00", false)
	claimed := job("TwitterVideoIngestJob", 33, "", false)
	exec(`INSERT INTO solid_queue_claimed_executions (job_id, created_at) VALUES ($1, now())`, claimed)
	failed := job("TwitterVideoIngestJob", 44, "", false)
	exec(`INSERT INTO solid_queue_failed_executions (job_id, created_at) VALUES ($1, now())`, failed)
	job("GenerateExpansionJob", 55, "", true) // already finished
	exec(`INSERT INTO users (id, email, encrypted_password, created_at, updated_at) VALUES (1, 'a@b.c', 'x', now(), now())`)
	exec(`INSERT INTO expansions (id, user_id, file_name, selected_text, question, status, created_at, updated_at)
		VALUES (1, 1, 'a.md', 's', 'q', 'processing', now(), now()), (2, 1, 'b.md', 's', 'q', 'pending', now(), now())`)

	code, out, errOut := runCmd("move-solid-queue-jobs")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "Moved 3 job(s)") {
		t.Errorf("stdout %q", out)
	}

	rows, err := pool.Query(ctx, `SELECT kind, (args->>'id')::int, run_at <= now() FROM go_jobs ORDER BY (args->>'id')::int`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type moved struct {
		kind string
		id   int
		due  bool
	}
	var got []moved
	for rows.Next() {
		var m moved
		if err := rows.Scan(&m.kind, &m.id, &m.due); err != nil {
			t.Fatal(err)
		}
		got = append(got, m)
	}
	want := []moved{{"generate_expansion", 11, true}, {"twitter_video_captions", 22, false}, {"twitter_video_ingest", 33, true}}
	if len(got) != len(want) {
		t.Fatalf("go_jobs = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("go_jobs[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	var unfinished int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM solid_queue_jobs WHERE finished_at IS NULL`).Scan(&unfinished); err != nil {
		t.Fatal(err)
	}
	if unfinished != 1 { // the failed one stays for inspection
		t.Errorf("%d unfinished solid_queue_jobs, want only the failed one", unfinished)
	}
	var status1, detail string
	var status2 string
	if err := pool.QueryRow(ctx, `SELECT status, error_detail FROM expansions WHERE id = 1`).Scan(&status1, &detail); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM expansions WHERE id = 2`).Scan(&status2); err != nil {
		t.Fatal(err)
	}
	if status1 != "failed" || detail != "Interrupted by server migration." || status2 != "pending" {
		t.Errorf("expansions: %q %q / %q", status1, detail, status2)
	}

	// A second run finds nothing left to move.
	if code, out, _ := runCmd("move-solid-queue-jobs"); code != 0 || !strings.Contains(out, "Moved 0 job(s)") {
		t.Errorf("second run: exit %d, %q", code, out)
	}
}

func TestMoveSolidQueueJobsRefusesWhileRailsIsAlive(t *testing.T) {
	pool := useTestDatabase(t)
	if _, err := pool.Exec(context.Background(), `INSERT INTO solid_queue_processes (kind, last_heartbeat_at, name, pid, created_at)
		VALUES ('Worker', now() AT TIME ZONE 'UTC', 'w', 1, now())`); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCmd("move-solid-queue-jobs")
	if code != 1 || !strings.Contains(errOut, "still alive") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
}
