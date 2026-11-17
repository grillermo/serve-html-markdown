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

var t0 = time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.UTC)

// clock fixes the store's clock at start and returns a function that moves it
// forward. Timestamps are asserted exactly (microsecond precision survives the
// round trip), so tests need no sleeps.
func clock(t *testing.T, start time.Time) (advance func(time.Duration)) {
	t.Helper()
	cur := start
	store.SetNow(t, func() time.Time { return cur })
	return func(d time.Duration) { cur = cur.Add(d) }
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
	advance := clock(t, t0)
	must(t, store.RecordServedFile(ctx, pool, "a.md"))
	advance(time.Hour)
	must(t, store.RecordServedFile(ctx, pool, "a.md"))
	assertNames(t, pool, "a.md")
	got := row(t, pool, "a.md")
	if !got.CreatedAt.Equal(t0) || !got.UpdatedAt.Equal(t0) {
		t.Fatalf("row changed: %+v, want both timestamps %v", got, t0)
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
	advance := clock(t, t0)
	must(t, store.RecordServedFile(ctx, pool, "notes.md"))
	advance(time.Hour)
	must(t, store.RecordServedFileModification(ctx, pool, "notes.md"))
	got := row(t, pool, "notes.md")
	if !got.CreatedAt.Equal(t0) {
		t.Errorf("created_at moved: %v -> %v", t0, got.CreatedAt)
	}
	if want := t0.Add(time.Hour); !got.UpdatedAt.Equal(want) {
		t.Errorf("updated_at = %v, want %v", got.UpdatedAt, want)
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
	advance := clock(t, t0)
	writeFile(t, dir, "a.html")
	must(t, store.SyncServedFiles(ctx, pool, dir))
	advance(time.Hour)
	must(t, store.SyncServedFiles(ctx, pool, dir))
	assertNames(t, pool, "a.html")
	got := row(t, pool, "a.html")
	if !got.CreatedAt.Equal(t0) || !got.UpdatedAt.Equal(t0) {
		t.Fatalf("row changed: %+v, want both timestamps %v", got, t0)
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

func TestExtensionRuleMatchesRubyExtname(t *testing.T) {
	pool := testdb.New(t)
	dir := t.TempDir()
	cases := map[string]bool{
		".md":     false, // leading dot is not an extension separator
		"..md":    false,
		".x.md":   true,
		"a.MD":    true,
		"a.":      false,
		"a.md.":   false,
		"noext":   false,
		".hidden": false,
	}
	for name, ok := range cases {
		must(t, store.RecordServedFile(ctx, pool, name))
		has := len(names(t, pool)) == 1
		if has != ok {
			t.Errorf("Record(%q): recorded = %v, want %v", name, has, ok)
		}
		_, err := pool.Exec(ctx, `DELETE FROM served_files`)
		must(t, err)
		writeFile(t, dir, name)
	}
	must(t, store.SyncServedFiles(ctx, pool, dir))
	assertNames(t, pool, ".x.md", "a.MD")
}

func TestNulAndInvalidUTF8NamesAreIgnored(t *testing.T) {
	pool := testdb.New(t)
	for _, name := range []string{"a\x00.md", "bad\xff.md", "\x00"} {
		must(t, store.RecordServedFile(ctx, pool, name))
		must(t, store.RecordServedFileModification(ctx, pool, name))
		must(t, store.RemoveServedFile(ctx, pool, name))
	}
	assertNames(t, pool)
}

func TestSyncSkipsNonUTF8NameButSyncsTheRest(t *testing.T) {
	pool := testdb.New(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad\xff.md"), []byte("x"), 0o644); err != nil {
		t.Skipf("filesystem rejects non-UTF-8 names: %v", err)
	}
	writeFile(t, dir, "good.md")
	must(t, store.SyncServedFiles(ctx, pool, dir))
	assertNames(t, pool, "good.md")
}

func TestSyncFollowsSymlinksToFilesOnly(t *testing.T) {
	pool := testdb.New(t)
	dir := t.TempDir()
	target := t.TempDir()
	must(t, os.WriteFile(filepath.Join(target, "real.md"), []byte("x"), 0o644))
	must(t, os.Symlink(filepath.Join(target, "real.md"), filepath.Join(dir, "link.md")))
	must(t, os.Symlink(target, filepath.Join(dir, "dirlink.md")))
	must(t, os.Symlink(filepath.Join(target, "gone.md"), filepath.Join(dir, "broken.md")))
	must(t, store.SyncServedFiles(ctx, pool, dir))
	assertNames(t, pool, "link.md")
}

// A row recorded after the sync started must survive the prune, even though
// no file backs it in the directory snapshot.
func TestSyncPruneSparesRowsCreatedAfterSyncStart(t *testing.T) {
	pool := testdb.New(t)
	dir := t.TempDir()
	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	for name, at := range map[string]time.Time{"past.md": past, "future.md": future} {
		_, err := pool.Exec(ctx, `INSERT INTO served_files (name, created_at, updated_at) VALUES ($1, $2, $2)`, name, at)
		must(t, err)
	}
	must(t, store.SyncServedFiles(ctx, pool, dir))
	assertNames(t, pool, "future.md")
}

func TestExtMirrorsRubyFileExtname(t *testing.T) {
	for name, want := range map[string]string{
		"a.md": ".md", "A.MD": ".MD", ".md": "", "..md": "", ".x.md": ".md", "foo.": ".", "a.b.": ".", "noext": "", "": "",
	} {
		if got := store.Ext(name); got != want {
			t.Errorf("Ext(%q) = %q, want %q", name, got, want)
		}
	}
	if !store.HasAllowedExtension("x.HTML") || store.HasAllowedExtension("x.txt") || store.HasAllowedExtension(".md") {
		t.Error("HasAllowedExtension")
	}
}
