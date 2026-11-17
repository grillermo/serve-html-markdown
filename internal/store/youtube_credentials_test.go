package store_test

import (
	"errors"
	"testing"
	"time"

	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/store"
)

func insertCredential(t *testing.T, pool store.DB, token string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := pool.Exec(ctx, `INSERT INTO youtube_credentials (refresh_token, obtained_at, created_at, updated_at) VALUES ($1, $2, $2, $2)`, token, now)
	must(t, err)
}

func TestYoutubeRefreshTokenPrefersStoredRow(t *testing.T) {
	pool := testdb.New(t)
	insertCredential(t, pool, "from-db")
	got, err := store.YoutubeRefreshToken(ctx, pool, "from-env")
	must(t, err)
	if got != "from-db" {
		t.Fatalf("got %q", got)
	}
}

func TestYoutubeRefreshTokenLatestRowWins(t *testing.T) {
	pool := testdb.New(t)
	insertCredential(t, pool, "old")
	insertCredential(t, pool, "new")
	got, err := store.YoutubeRefreshToken(ctx, pool, "from-env")
	must(t, err)
	if got != "new" {
		t.Fatalf("got %q", got)
	}
}

func TestYoutubeRefreshTokenFallsBackToEnv(t *testing.T) {
	pool := testdb.New(t)
	got, err := store.YoutubeRefreshToken(ctx, pool, "from-env")
	must(t, err)
	if got != "from-env" {
		t.Fatalf("got %q", got)
	}
}

func TestYoutubeRefreshTokenBlankLatestRowFallsBackToEnv(t *testing.T) {
	pool := testdb.New(t)
	insertCredential(t, pool, "older")
	insertCredential(t, pool, "  ")
	got, err := store.YoutubeRefreshToken(ctx, pool, "from-env")
	must(t, err)
	if got != "from-env" {
		t.Fatalf("got %q", got)
	}
}

func TestYoutubeRefreshTokenEmptyWhenNothing(t *testing.T) {
	pool := testdb.New(t)
	for _, env := range []string{"", "   "} {
		got, err := store.YoutubeRefreshToken(ctx, pool, env)
		must(t, err)
		if got != "" {
			t.Fatalf("env %q: got %q", env, got)
		}
	}
}

func TestStoreYoutubeCredentialKeepsSingleRowAndOverwrites(t *testing.T) {
	pool := testdb.New(t)
	advance := clock(t, t0)
	must(t, store.StoreYoutubeCredential(ctx, pool, "first"))
	advance(time.Hour)
	must(t, store.StoreYoutubeCredential(ctx, pool, "second"))

	var n int
	var token string
	var created2, obtained2, updated2 time.Time
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM youtube_credentials`).Scan(&n))
	must(t, pool.QueryRow(ctx, `SELECT refresh_token, created_at, obtained_at, updated_at FROM youtube_credentials`).Scan(&token, &created2, &obtained2, &updated2))
	if n != 1 || token != "second" {
		t.Fatalf("rows = %d, token = %q", n, token)
	}
	if !created2.Equal(t0) || !obtained2.Equal(t0.Add(time.Hour)) || !updated2.Equal(obtained2) {
		t.Fatalf("created %v obtained %v updated %v", created2, obtained2, updated2)
	}
}

func TestStoreYoutubeCredentialUpdatesLatestOfSeveralRows(t *testing.T) {
	pool := testdb.New(t)
	insertCredential(t, pool, "old")
	insertCredential(t, pool, "new")
	must(t, store.StoreYoutubeCredential(ctx, pool, "replaced"))
	var tokens []string
	rows, err := pool.Query(ctx, `SELECT refresh_token FROM youtube_credentials ORDER BY id`)
	must(t, err)
	for rows.Next() {
		var s string
		must(t, rows.Scan(&s))
		tokens = append(tokens, s)
	}
	must(t, rows.Err())
	if len(tokens) != 2 || tokens[0] != "old" || tokens[1] != "replaced" {
		t.Fatalf("tokens = %v", tokens)
	}
}

func TestStoreYoutubeCredentialRejectsBlank(t *testing.T) {
	pool := testdb.New(t)
	for _, tok := range []string{"", "  \n"} {
		if err := store.StoreYoutubeCredential(ctx, pool, tok); !errors.Is(err, store.ErrBlankToken) {
			t.Errorf("%q: err = %v", tok, err)
		}
	}
}
