package testdb_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"serve-html-markdown/internal/db/testdb"
)

func TestNewReturnsCleanDatabaseWithSchema(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()

	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename NOT IN ('schema_migrations', 'ar_internal_metadata')`)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	hasJobs := false
	for _, table := range tables {
		hasJobs = hasJobs || table == "go_jobs"
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()).Scan(&n); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s: want empty, got %d rows", table, n)
		}
	}
	if !hasJobs || len(tables) < 2 {
		t.Errorf("schema looks incomplete: tables %v", tables)
	}

	var path string
	if err := pool.QueryRow(ctx, `SHOW search_path`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Error("search_path is empty; schema.sql setting leaked into the pool")
	}
}

func TestNewTruncatesBetweenTests(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	if _, err := pool.Exec(ctx, `INSERT INTO go_jobs (kind) VALUES ('x')`); err != nil {
		t.Fatal(err)
	}
	pool2 := testdb.New(t)
	var n int
	if err := pool2.QueryRow(ctx, `SELECT count(*) FROM go_jobs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("want go_jobs truncated, got %d rows", n)
	}
}
