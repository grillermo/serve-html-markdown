package store_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/store"
)

var ctx = context.Background()

func names(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT name FROM served_files ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func row(t *testing.T, pool *pgxpool.Pool, name string) store.ServedFile {
	t.Helper()
	var f store.ServedFile
	err := pool.QueryRow(ctx, `SELECT id, name, created_at, updated_at FROM served_files WHERE name = $1`, name).
		Scan(&f.ID, &f.Name, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func assertNames(t *testing.T, pool *pgxpool.Pool, want ...string) {
	t.Helper()
	got := names(t, pool)
	sort.Strings(got)
	sort.Strings(want)
	if want == nil {
		want = []string{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
}

func TestRecordInsertsAllowedAndIgnoresUnsupported(t *testing.T) {
	pool := testdb.New(t)
	must(t, store.RecordServedFile(ctx, pool, "page.html"))
	must(t, store.RecordServedFile(ctx, pool, "skip.txt"))
	must(t, store.RecordServedFile(ctx, pool, "UP.MD"))
	assertNames(t, pool, "UP.MD", "page.html")
}

func TestRecordTwiceKeepsOneRowAndFrozenCreatedAt(t *testing.T) {
	pool := testdb.New(t)
	must(t, store.RecordServedFile(ctx, pool, "a.md"))
	first := row(t, pool, "a.md")
	time.Sleep(5 * time.Millisecond)
	must(t, store.RecordServedFile(ctx, pool, "a.md"))
	assertNames(t, pool, "a.md")
	if got := row(t, pool, "a.md"); !got.CreatedAt.Equal(first.CreatedAt) || !got.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("row changed: %+v -> %+v", first, got)
	}
}

func TestRemove(t *testing.T) {
	pool := testdb.New(t)
	must(t, store.RecordServedFile(ctx, pool, "page.html"))
	must(t, store.RemoveServedFile(ctx, pool, "page.html"))
	must(t, store.RemoveServedFile(ctx, pool, "missing.html"))
	assertNames(t, pool)
}

func TestRecordModificationCreatesWhenAbsent(t *testing.T) {
	pool := testdb.New(t)
	must(t, store.RecordServedFileModification(ctx, pool, "notes.md"))
	assertNames(t, pool, "notes.md")
}

func TestRecordModificationBumpsUpdatedAtOnly(t *testing.T) {
	pool := testdb.New(t)
	must(t, store.RecordServedFile(ctx, pool, "notes.md"))
	first := row(t, pool, "notes.md")
	time.Sleep(5 * time.Millisecond)
	must(t, store.RecordServedFileModification(ctx, pool, "notes.md"))
	got := row(t, pool, "notes.md")
	if !got.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("created_at moved: %v -> %v", first.CreatedAt, got.CreatedAt)
	}
	if !got.UpdatedAt.After(first.CreatedAt) {
		t.Errorf("updated_at %v not after created_at %v", got.UpdatedAt, first.CreatedAt)
	}
	assertNames(t, pool, "notes.md")
}

func TestNewestOrdersByCreatedAtThenID(t *testing.T) {
	pool := testdb.New(t)
	now := time.Now().UTC()
	for _, f := range []struct {
		name string
		at   time.Time
	}{
		{"a.html", now.Add(-48 * time.Hour)},
		{"b.html", now.Add(-24 * time.Hour)},
		{"c.html", now.Add(-24 * time.Hour)},
	} {
		_, err := pool.Exec(ctx, `INSERT INTO served_files (name, created_at, updated_at) VALUES ($1, $2, $2)`, f.name, f.at)
		must(t, err)
	}
	got, err := store.NewestServedFile(ctx, pool)
	must(t, err)
	if got == nil || got.Name != "c.html" {
		t.Fatalf("newest = %+v, want c.html", got)
	}
}

func TestNewestIsNilForEmptyTable(t *testing.T) {
	pool := testdb.New(t)
	got, err := store.NewestServedFile(ctx, pool)
	must(t, err)
	if got != nil {
		t.Fatalf("want nil, got %+v", got)
	}
}

func TestServedFilesByUpdatedNewestFirst(t *testing.T) {
	pool := testdb.New(t)
	now := time.Now().UTC()
	for i, n := range []string{"old.md", "new.md", "mid.md"} {
		at := []time.Time{now.Add(-3 * time.Hour), now, now.Add(-time.Hour)}[i]
		_, err := pool.Exec(ctx, `INSERT INTO served_files (name, created_at, updated_at) VALUES ($1, $2, $2)`, n, at)
		must(t, err)
	}
	files, err := store.ServedFilesByUpdated(ctx, pool)
	must(t, err)
	var got []string
	for _, f := range files {
		got = append(got, f.Name)
	}
	if want := []string{"new.md", "mid.md", "old.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestServedFileNamesMatchesExactOrLike(t *testing.T) {
	pool := testdb.New(t)
	for _, n := range []string{"a_b.md", "a_b--v2.md", "a_b--v3.md", "axb--v2.md", "a_b--v2.html", "other.md"} {
		must(t, store.RecordServedFile(ctx, pool, n))
	}
	got, err := store.ServedFileNames(ctx, pool, store.EscapeLike("a_b")+`--v%.md`, "a_b.md")
	must(t, err)
	if want := []string{"a_b--v2.md", "a_b--v3.md", "a_b.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEscapeLike(t *testing.T) {
	if got, want := store.EscapeLike(`50%_off\now`), `50\%\_off\\now`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := store.EscapeLike("plain"); got != "plain" {
		t.Fatalf("got %q", got)
	}
}

func writeFile(t *testing.T, dir, name string) {
	t.Helper()
	must(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644))
}

func TestSyncInsertsAllowedFilesAndIgnoresOthers(t *testing.T) {
	pool := testdb.New(t)
	dir := t.TempDir()
	for _, n := range []string{"a.html", "b.md", "c.markdown", "d.txt", ".gitkeep", ".b.md.expansion.lock"} {
		writeFile(t, dir, n)
	}
	must(t, os.Mkdir(filepath.Join(dir, "sub.md"), 0o755))
	must(t, store.SyncServedFiles(ctx, pool, dir))
	assertNames(t, pool, "a.html", "b.md", "c.markdown")
}

func TestSyncIsIdempotentAndKeepsTimestamps(t *testing.T) {
	pool := testdb.New(t)
	dir := t.TempDir()
	writeFile(t, dir, "a.html")
	must(t, store.SyncServedFiles(ctx, pool, dir))
	first := row(t, pool, "a.html")
	time.Sleep(5 * time.Millisecond)
	must(t, store.SyncServedFiles(ctx, pool, dir))
	assertNames(t, pool, "a.html")
	if got := row(t, pool, "a.html"); !got.CreatedAt.Equal(first.CreatedAt) || !got.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("row changed: %+v -> %+v", first, got)
	}
}

func TestSyncPrunesRowsWithoutFiles(t *testing.T) {
	pool := testdb.New(t)
	dir := t.TempDir()
	writeFile(t, dir, "a.html")
	writeFile(t, dir, "b.md")
	must(t, store.SyncServedFiles(ctx, pool, dir))
	must(t, os.Remove(filepath.Join(dir, "a.html")))
	must(t, store.SyncServedFiles(ctx, pool, dir))
	assertNames(t, pool, "b.md")
}

func TestSyncEmptiesTableWhenNoFilesRemain(t *testing.T) {
	pool := testdb.New(t)
	dir := t.TempDir()
	writeFile(t, dir, "a.html")
	must(t, store.SyncServedFiles(ctx, pool, dir))
	must(t, os.Remove(filepath.Join(dir, "a.html")))
	must(t, store.SyncServedFiles(ctx, pool, dir))
	assertNames(t, pool)
}

func TestSyncHandlesAwkwardNames(t *testing.T) {
	pool := testdb.New(t)
	dir := t.TempDir()
	for _, n := range []string{`it's "q".md`, `back\slash,comma.md`, "ünï.html"} {
		writeFile(t, dir, n)
	}
	must(t, store.SyncServedFiles(ctx, pool, dir))
	assertNames(t, pool, `back\slash,comma.md`, `it's "q".md`, "ünï.html")
}

func TestSyncMissingDirErrors(t *testing.T) {
	pool := testdb.New(t)
	if err := store.SyncServedFiles(ctx, pool, filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("want error")
	}
}
