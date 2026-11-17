package store_test

import (
	"errors"
	"testing"
	"time"

	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/store"
)

func TestFindUserByEmailIsCaseInsensitiveAndStripsWhitespace(t *testing.T) {
	pool := testdb.New(t)
	created, err := store.UpsertUser(ctx, pool, "admin@x.com", "hash")
	must(t, err)
	got, err := store.FindUserByEmail(ctx, pool, "  Admin@X.com \n")
	must(t, err)
	if got == nil || got.ID != created.ID || got.Email != "admin@x.com" || got.EncryptedPassword != "hash" {
		t.Fatalf("got %+v, created %+v", got, created)
	}
}

func TestFindUserByEmailMissingIsNil(t *testing.T) {
	pool := testdb.New(t)
	got, err := store.FindUserByEmail(ctx, pool, "nobody@x.com")
	must(t, err)
	if got != nil {
		t.Fatalf("want nil, got %+v", got)
	}
}

func TestUpsertUserCreatesThenUpdates(t *testing.T) {
	pool := testdb.New(t)
	advance := clock(t, t0)
	first, err := store.UpsertUser(ctx, pool, " Admin@X.com ", "hash1")
	must(t, err)
	if first.Email != "admin@x.com" {
		t.Fatalf("email not normalized: %q", first.Email)
	}
	if first.ExpansionMode != "create_new" {
		t.Fatalf("default mode = %q, want create_new", first.ExpansionMode)
	}
	if first.RememberCreatedAt != nil {
		t.Fatalf("remember_created_at = %v, want nil", first.RememberCreatedAt)
	}
	advance(time.Hour)
	second, err := store.UpsertUser(ctx, pool, "admin@x.com", "hash2")
	must(t, err)
	if second.ID != first.ID || second.EncryptedPassword != "hash2" {
		t.Fatalf("first %+v second %+v", first, second)
	}
	if !second.CreatedAt.Equal(t0) || !second.UpdatedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("timestamps: first %+v second %+v", first, second)
	}
	var n int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n))
	if n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
}

func TestUpsertUserRejectsBadEmail(t *testing.T) {
	pool := testdb.New(t)
	for _, email := range []string{"", "   ", "no-at-sign", "a b@x.com", "@x.com", "a@", "a@b@c", "a\x00@x.com", "a\xff@x.com"} {
		if _, err := store.UpsertUser(ctx, pool, email, "hash"); !errors.Is(err, store.ErrInvalidEmail) {
			t.Errorf("%q: err = %v, want ErrInvalidEmail", email, err)
		}
	}
}

func TestSetExpansionMode(t *testing.T) {
	pool := testdb.New(t)
	u, err := store.UpsertUser(ctx, pool, "m@x.com", "h")
	must(t, err)
	must(t, store.SetExpansionMode(ctx, pool, u.ID, "edit_in_place"))
	got, err := store.FindUserByEmail(ctx, pool, "m@x.com")
	must(t, err)
	if got.ExpansionMode != "edit_in_place" {
		t.Fatalf("mode = %q", got.ExpansionMode)
	}
	must(t, store.SetExpansionMode(ctx, pool, u.ID, "create_new"))
}

func TestSetExpansionModeRejectsUnknown(t *testing.T) {
	pool := testdb.New(t)
	u, err := store.UpsertUser(ctx, pool, "m@x.com", "h")
	must(t, err)
	for _, mode := range []string{"nonsense", "", "Create_New"} {
		if err := store.SetExpansionMode(ctx, pool, u.ID, mode); !errors.Is(err, store.ErrInvalidExpansionMode) {
			t.Errorf("%q: err = %v", mode, err)
		}
	}
	got, _ := store.FindUserByEmail(ctx, pool, "m@x.com")
	if got.ExpansionMode != "create_new" {
		t.Fatalf("mode changed to %q", got.ExpansionMode)
	}
}

func TestSetExpansionModeUnknownUser(t *testing.T) {
	pool := testdb.New(t)
	if err := store.SetExpansionMode(ctx, pool, 999, "create_new"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestFindUserByEmailWithNulIsNotFound(t *testing.T) {
	pool := testdb.New(t)
	pool.Close() // must not touch the DB at all
	got, err := store.FindUserByEmail(ctx, pool, "a\x00@x.com")
	if err != nil || got != nil {
		t.Fatalf("got %+v, %v; want nil, nil", got, err)
	}
}

func TestFindUserByID(t *testing.T) {
	pool := testdb.New(t)
	u, err := store.UpsertUser(ctx, pool, "id@x.com", "hash")
	must(t, err)
	got, err := store.FindUserByID(ctx, pool, u.ID)
	must(t, err)
	if got == nil || got.ID != u.ID || got.Email != "id@x.com" {
		t.Fatalf("got %+v", got)
	}
	missing, err := store.FindUserByID(ctx, pool, u.ID+1000)
	must(t, err)
	if missing != nil {
		t.Fatalf("want nil, got %+v", missing)
	}
}
