package db_test

import (
	"context"
	"testing"

	"serve-html-markdown/internal/db/testdb"
)

// The test database is db/schema.sql (which still has Rails' Solid Queue
// tables) plus every migration, so the drop migration must leave none behind.
func TestDropSolidQueueMigrationRemovesItsTables(t *testing.T) {
	pool := testdb.New(t)
	var left []string
	rows, err := pool.Query(context.Background(), `SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename LIKE 'solid\_queue\_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		left = append(left, n)
	}
	if len(left) > 0 {
		t.Errorf("solid_queue tables still exist: %v", left)
	}
	var goJobs bool
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass('go_jobs') IS NOT NULL`).Scan(&goJobs); err != nil || !goJobs {
		t.Errorf("go_jobs missing: %v", err)
	}
}
