package testdb

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestCheckWipeable(t *testing.T) {
	tests := []struct {
		name        string
		dsn         string
		allowRemote string
		wantErr     bool
	}{
		{"rails test db", "postgres://localhost:5432/serve_html_markdown_test", "", false},
		{"rails parallel db", "postgres://localhost:5432/serve_html_markdown_test_3", "", false},
		{"loopback ip", "postgres://127.0.0.1:5432/app_test", "", false},
		{"ipv6 loopback", "postgres://[::1]:5432/app_test", "", false},
		{"unix socket", "postgres:///app_test?host=/var/run/postgresql", "", false},
		{"prod-like name", "postgres://localhost/contest_prod", "", true},
		{"postgres db", "postgres://localhost/postgres", "", true},
		{"backup suffix", "postgres://localhost/serve_html_markdown_test_backup", "", true},
		{"remote host", "postgres://db.example.com:5432/app_test", "", true},
		{"remote host allowed", "postgres://db.example.com:5432/app_test", "1", false},
		{"remote host allowed but bad name", "postgres://db.example.com:5432/app", "1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TESTDB_ALLOW_REMOTE", tt.allowRemote)
			cfg, err := pgx.ParseConfig(tt.dsn)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkWipeable(cfg); (err != nil) != tt.wantErr {
				t.Fatalf("checkWipeable(%q) error = %v, wantErr %v", tt.dsn, err, tt.wantErr)
			}
		})
	}
}
