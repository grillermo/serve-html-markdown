package testdb_test

import (
	"context"
	"testing"

	"serve-html-markdown/internal/db/testdb"
)

func TestNewReturnsCleanDatabaseWithSchema(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()

	for _, table := range []string{"users", "expansions", "scroll_positions", "served_files", "twitter_videos", "youtube_credentials", "go_jobs"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s: want empty, got %d rows", table, n)
		}
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
