package db_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"serve-html-markdown/internal/db"
	"serve-html-markdown/internal/db/testdb"
)

func TestMigrateIsIdempotent(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := db.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version = '20261117120000'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 row, got %d", n)
	}
}

func TestMigrateFSFailureRollsBack(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	fsys := fstest.MapFS{
		"20990101000000_bad.sql": {Data: []byte(`CREATE TABLE migrate_bad_tmp (id int); SELECT * FROM does_not_exist;`)},
	}
	err := db.MigrateFS(ctx, pool, fsys)
	if err == nil {
		t.Fatal("want an error from the bad migration")
	}
	if !strings.Contains(err.Error(), "20990101000000_bad.sql") {
		t.Errorf("error should name the file: %v", err)
	}
	var recorded, tableExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = '20990101000000')`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded {
		t.Error("failed migration recorded its version")
	}
	if err := pool.QueryRow(ctx, `SELECT to_regclass('migrate_bad_tmp') IS NOT NULL`).Scan(&tableExists); err != nil {
		t.Fatal(err)
	}
	if tableExists {
		t.Error("failed migration was not rolled back")
	}
}

func TestMigrateFSRejectsBadFileNames(t *testing.T) {
	pool := testdb.New(t)
	for _, name := range []string{"123_short.sql", "2099010100000_13digits.sql", "202601011200000_15digits.sql", "create_things.sql", "20990101000000.sql"} {
		fsys := fstest.MapFS{name: {Data: []byte(`SELECT 1`)}}
		err := db.MigrateFS(context.Background(), pool, fsys)
		if err == nil || !strings.Contains(err.Error(), "14-digit") || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: want a clear 14-digit error naming the file, got %v", name, err)
		}
	}
}

func TestMigrateFSIgnoresNonSQLFiles(t *testing.T) {
	pool := testdb.New(t)
	fsys := fstest.MapFS{"embed.go": {Data: []byte("package x")}, "README.md": {Data: []byte("hi")}}
	if err := db.MigrateFS(context.Background(), pool, fsys); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateFSRejectsDuplicateVersions(t *testing.T) {
	pool := testdb.New(t)
	fsys := fstest.MapFS{
		"20990101000000_a.sql": {Data: []byte(`SELECT 1`)},
		"20990101000000_b.sql": {Data: []byte(`SELECT 1`)},
	}
	if err := db.MigrateFS(context.Background(), pool, fsys); err == nil || !strings.Contains(err.Error(), "share version") {
		t.Fatalf("want duplicate version error, got %v", err)
	}
}

func TestMigrateFSConcurrentCallsApplyOnce(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	t.Cleanup(func() {
		pool.Exec(ctx, `DROP TABLE IF EXISTS migrate_conc_tmp; DELETE FROM schema_migrations WHERE version = '20990202000000'`)
	})
	// A second apply would fail with "already exists".
	fsys := fstest.MapFS{
		"20990202000000_conc.sql": {Data: []byte(`CREATE TABLE migrate_conc_tmp (id int); SELECT pg_sleep(0.3);`)},
	}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = db.MigrateFS(ctx, pool, fsys)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version = '20990202000000'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 recorded row, got %d", n)
	}
}
