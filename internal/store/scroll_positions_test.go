package store_test

import (
	"errors"
	"testing"

	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/store"
)

func newUser(t *testing.T, pool store.DB, email string) int64 {
	t.Helper()
	u, err := store.UpsertUser(ctx, pool, email, "hash")
	must(t, err)
	return u.ID
}

func TestUpsertScrollPositionTwiceKeepsOneRowAndUpdatesAnchor(t *testing.T) {
	pool := testdb.New(t)
	uid := newUser(t, pool, "s@x.com")
	must(t, store.UpsertScrollPosition(ctx, pool, uid, "a.md", "intro"))
	must(t, store.UpsertScrollPosition(ctx, pool, uid, "a.md", "heading-1:part.two"))
	var n int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM scroll_positions`).Scan(&n))
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	got, err := store.FindScrollPosition(ctx, pool, uid, "a.md")
	must(t, err)
	if got != "heading-1:part.two" {
		t.Fatalf("anchor = %q", got)
	}
}

func TestScrollPositionsArePerUserAndFile(t *testing.T) {
	pool := testdb.New(t)
	u1 := newUser(t, pool, "one@x.com")
	u2 := newUser(t, pool, "two@x.com")
	must(t, store.UpsertScrollPosition(ctx, pool, u1, "a.md", "one"))
	must(t, store.UpsertScrollPosition(ctx, pool, u2, "a.md", "two"))
	must(t, store.UpsertScrollPosition(ctx, pool, u1, "b.md", "three"))
	for _, c := range []struct {
		uid  int64
		file string
		want string
	}{{u1, "a.md", "one"}, {u2, "a.md", "two"}, {u1, "b.md", "three"}} {
		got, err := store.FindScrollPosition(ctx, pool, c.uid, c.file)
		must(t, err)
		if got != c.want {
			t.Errorf("user %d %s = %q, want %q", c.uid, c.file, got, c.want)
		}
	}
}

func TestUpsertScrollPositionRejectsBadAnchors(t *testing.T) {
	pool := testdb.New(t)
	uid := newUser(t, pool, "s@x.com")
	for _, anchor := range []string{"", "</script>", "has spaces", `quote"`, "bad anchor!", "intro\n", "\nintro", "é", "a\x00"} {
		if err := store.UpsertScrollPosition(ctx, pool, uid, "a.md", anchor); !errors.Is(err, store.ErrInvalidAnchor) {
			t.Errorf("%q: err = %v, want ErrInvalidAnchor", anchor, err)
		}
	}
	var n int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM scroll_positions`).Scan(&n))
	if n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
}

func TestUpsertScrollPositionRejectsBlankFileName(t *testing.T) {
	pool := testdb.New(t)
	uid := newUser(t, pool, "s@x.com")
	for _, name := range []string{"", "  ", "a\x00.md", "bad\xff.md"} {
		if err := store.UpsertScrollPosition(ctx, pool, uid, name, "intro"); !errors.Is(err, store.ErrInvalidFileName) {
			t.Errorf("%q: err = %v, want ErrInvalidFileName", name, err)
		}
	}
}

func TestUpsertScrollPositionUnknownUserFails(t *testing.T) {
	pool := testdb.New(t)
	if err := store.UpsertScrollPosition(ctx, pool, 999, "a.md", "intro"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestFindScrollPositionMissingIsEmpty(t *testing.T) {
	pool := testdb.New(t)
	uid := newUser(t, pool, "s@x.com")
	got, err := store.FindScrollPosition(ctx, pool, uid, "nope.md")
	must(t, err)
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
