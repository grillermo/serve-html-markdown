package db_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"

	"serve-html-markdown/internal/config"
	"serve-html-markdown/internal/db"
)

func TestURL(t *testing.T) {
	t.Run("without user", func(t *testing.T) {
		got := db.URL(config.Config{DatabaseHost: "localhost", DatabasePort: "5432", DatabaseName: "app"})
		if want := "postgres://localhost:5432/app?sslmode=disable"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("with user and password", func(t *testing.T) {
		got := db.URL(config.Config{DatabaseHost: "db", DatabasePort: "6432", DatabaseUser: "bob", DatabasePassword: "pw", DatabaseName: "app"})
		if want := "postgres://bob:pw@db:6432/app?sslmode=disable"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("escapes special characters", func(t *testing.T) {
		pw := "p@ss:w/rd?#%"
		got := db.URL(config.Config{DatabaseHost: "db", DatabasePort: "5432", DatabaseUser: "bob", DatabasePassword: pw, DatabaseName: "app"})
		u, err := url.Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		if gotPw, _ := u.User.Password(); gotPw != pw {
			t.Fatalf("password round-trip: got %q, want %q (url %q)", gotPw, pw, got)
		}
		if u.Host != "db:5432" || u.Path != "/app" {
			t.Fatalf("host/path mangled: %q", got)
		}
	})
}

func TestOpenClampsMaxConns(t *testing.T) {
	for _, n := range []int{0, -3} {
		pool, err := db.Open(context.Background(), "postgres://localhost:5432/x?sslmode=disable", n, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := pool.Config().MaxConns; got != 1 {
			t.Errorf("maxConns %d: pool MaxConns = %d, want 1", n, got)
		}
		pool.Close()
	}
}

func TestOpenQueryModeFollowsPreparedFlag(t *testing.T) {
	const dsn = "postgres://localhost:5432/x?sslmode=disable"
	simple, err := db.Open(context.Background(), dsn, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	defer simple.Close()
	if got := simple.Config().ConnConfig.DefaultQueryExecMode; got != pgx.QueryExecModeSimpleProtocol {
		t.Errorf("prepared=false: mode %v, want simple protocol", got)
	}
	prepared, err := db.Open(context.Background(), dsn, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if got := prepared.Config().ConnConfig.DefaultQueryExecMode; got == pgx.QueryExecModeSimpleProtocol {
		t.Errorf("prepared=true: mode must not be simple protocol")
	}
}
