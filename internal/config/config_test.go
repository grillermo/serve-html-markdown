package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// configEnvKeys lists every environment variable Load/FromEnv reads.
var configEnvKeys = []string{
	"APP_ENV", "PORT", "HOST", "API_TOKEN", "SECRET_KEY_BASE",
	"OPENAI_API_KEY", "EXPANSION_LLM_API_KEY", "EXPANSION_CLAUDE_MODEL",
	"DATABASE_HOST", "DATABASE_PORT", "DATABASE_USER", "DATABASE_PASSWORD",
	"DATABASE_NAME", "DATABASE_POOL", "DATABASE_PREPARED_STATEMENTS",
	"ADMIN_EMAIL", "ADMIN_PASSWORD",
	"YTDLP_BIN", "VIDEOS_DIR", "FILES_DIR",
	"YOUTUBE_CLIENT_ID", "YOUTUBE_CLIENT_SECRET", "YOUTUBE_REFRESH_TOKEN",
	"YOUTUBE_REDIRECT_URI", "YOUTUBE_REAUTH_URL",
	"RULINKY_HOST", "RULINKY_API_TOKEN",
	"SLACK_SUCCESS_WEBHOOK", "SLACK_FAILURE_WEBHOOK",
	"JOB_CONCURRENCY",
}

// clearConfigEnv blanks every variable the config reads so a test sees
// defaults regardless of the developer's shell. Values are restored on cleanup.
func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, k := range configEnvKeys {
		t.Setenv(k, "")
	}
}

// unsetEnv makes k unset for the test and guarantees it is restored (or left
// unset) afterwards, even if Load exports it from a dotenv file.
func unsetEnv(t *testing.T, k string) {
	t.Helper()
	t.Setenv(k, "")
	os.Unsetenv(k)
}

func TestDefaults(t *testing.T) {
	clearConfigEnv(t)
	c := FromEnv()
	checks := []struct {
		name      string
		got, want any
	}{
		{"Port", c.Port, "8009"},
		{"Host", c.Host, "localhost"},
		{"DatabaseHost", c.DatabaseHost, "localhost"},
		{"DatabasePort", c.DatabasePort, "5432"},
		{"DatabasePool", c.DatabasePool, 5},
		{"PreparedStatements", c.PreparedStatements, true},
		{"YtdlpBin", c.YtdlpBin, "/opt/homebrew/bin/yt-dlp"},
		{"ClaudeModel", c.ClaudeModel, "sonnet"},
		{"JobConcurrency", c.JobConcurrency, 3},
		{"AppEnv", c.AppEnv, "production"},
		{"DatabaseName", c.DatabaseName, "serve_html_markdown_production"},
	}
	for _, ch := range checks {
		if ch.got != ch.want {
			t.Errorf("%s = %v, want %v", ch.name, ch.got, ch.want)
		}
	}
}

func TestPreparedStatementsOff(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("DATABASE_PREPARED_STATEMENTS", "false")
	if FromEnv().PreparedStatements {
		t.Fatal("prepared statements should be off")
	}
}

func TestDatabaseNameFollowsAppEnv(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_ENV", "development")
	c := FromEnv()
	if c.DatabaseName != "serve_html_markdown_development" {
		t.Fatalf("name %q", c.DatabaseName)
	}
	t.Setenv("DATABASE_NAME", "custom")
	if c := FromEnv(); c.DatabaseName != "custom" {
		t.Fatalf("override %q", c.DatabaseName)
	}
}

func TestGetIntFallsBackToDefault(t *testing.T) {
	clearConfigEnv(t)
	for _, v := range []string{"", "abc", "0", "-4", "1.5"} {
		t.Setenv("DATABASE_POOL", v)
		t.Setenv("JOB_CONCURRENCY", v)
		c := FromEnv()
		if c.DatabasePool != 5 || c.JobConcurrency != 3 {
			t.Errorf("%q: pool %d jobs %d", v, c.DatabasePool, c.JobConcurrency)
		}
	}
	t.Setenv("DATABASE_POOL", "9")
	t.Setenv("JOB_CONCURRENCY", "7")
	if c := FromEnv(); c.DatabasePool != 9 || c.JobConcurrency != 7 {
		t.Fatalf("valid values ignored: pool %d jobs %d", c.DatabasePool, c.JobConcurrency)
	}
}

func TestLoadFilesDirAndVideosDir(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	c := Load(root)
	if c.Root != root {
		t.Fatalf("root %q", c.Root)
	}
	if c.FilesDir != filepath.Join(root, "files") {
		t.Fatalf("files dir %q", c.FilesDir)
	}
	if c.VideosDir != filepath.Join(root, "videos") {
		t.Fatalf("videos dir %q", c.VideosDir)
	}

	t.Setenv("FILES_DIR", "/somewhere/else")
	t.Setenv("VIDEOS_DIR", "/videos/else")
	c = Load(root)
	if c.FilesDir != "/somewhere/else" || c.VideosDir != "/videos/else" {
		t.Fatalf("overrides: files %q videos %q", c.FilesDir, c.VideosDir)
	}
}

func TestLoadWithoutDotenvFile(t *testing.T) {
	clearConfigEnv(t)
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	c := Load(t.TempDir())
	if c.Port != "8009" {
		t.Fatalf("port %q", c.Port)
	}
	if logs.Len() != 0 {
		t.Fatalf("missing dotenv file should not log, got %q", logs.String())
	}
}

func TestLoadUnreadableDotenvWarns(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	// A directory where the file should be makes reading fail with something
	// other than "not exist".
	if err := os.Mkdir(filepath.Join(root, ".env"), 0o755); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	Load(root)
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("expected a warning, got %q", logs.String())
	}
}

func TestLoadDotenvDoesNotOverrideRealEnv(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	contents := "API_TOKEN=from-dotenv\nOPENAI_API_KEY=from-dotenv\n"
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("API_TOKEN", "from-env")
	// The file supplies this one; make sure it is unset beforehand and is
	// cleaned up afterwards (Load exports it into the process environment).
	unsetEnv(t, "OPENAI_API_KEY")

	c := Load(root)
	if c.APIToken != "from-env" {
		t.Fatalf("real env was overridden: %q", c.APIToken)
	}
	if c.OpenAIKey != "from-dotenv" {
		t.Fatalf("dotenv file not loaded: %q", c.OpenAIKey)
	}
}
