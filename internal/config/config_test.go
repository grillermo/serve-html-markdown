package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	t.Setenv("DATABASE_PORT", "")
	t.Setenv("PORT", "")
	t.Setenv("DATABASE_PREPARED_STATEMENTS", "false")
	c := FromEnv()
	if c.Port != "8009" {
		t.Fatalf("port %q", c.Port)
	}
	if c.DatabasePort != "5432" {
		t.Fatalf("db port %q", c.DatabasePort)
	}
	if c.PreparedStatements {
		t.Fatal("prepared statements should be off")
	}
	if c.YtdlpBin != "/opt/homebrew/bin/yt-dlp" {
		t.Fatalf("ytdlp %q", c.YtdlpBin)
	}
	if c.ClaudeModel != "sonnet" {
		t.Fatalf("claude model %q", c.ClaudeModel)
	}
}

func TestDefaultsPoolJobsAndName(t *testing.T) {
	for _, k := range []string{"DATABASE_POOL", "JOB_CONCURRENCY", "DATABASE_NAME", "APP_ENV", "DATABASE_PREPARED_STATEMENTS"} {
		t.Setenv(k, "")
	}
	c := FromEnv()
	if c.DatabasePool != 5 {
		t.Fatalf("pool %d", c.DatabasePool)
	}
	if c.JobConcurrency != 3 {
		t.Fatalf("jobs %d", c.JobConcurrency)
	}
	if c.AppEnv != "production" || c.DatabaseName != "serve_html_markdown_production" {
		t.Fatalf("env %q name %q", c.AppEnv, c.DatabaseName)
	}
	if !c.PreparedStatements {
		t.Fatal("prepared statements should default on")
	}
}

func TestLoadFilesDirAndVideosDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FILES_DIR", "")
	t.Setenv("VIDEOS_DIR", "")
	c := Load(root)
	if c.FilesDir != filepath.Join(root, "files") {
		t.Fatalf("files dir %q", c.FilesDir)
	}
	if c.VideosDir != filepath.Join(root, "videos") {
		t.Fatalf("videos dir %q", c.VideosDir)
	}

	t.Setenv("FILES_DIR", "/somewhere/else")
	if c := Load(root); c.FilesDir != "/somewhere/else" {
		t.Fatalf("files dir override %q", c.FilesDir)
	}
}

func TestLoadDotenvDoesNotOverrideRealEnv(t *testing.T) {
	root := t.TempDir()
	contents := "API_TOKEN=from-dotenv\nOPENAI_API_KEY=from-dotenv\n"
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("API_TOKEN", "from-env")
	// Register OPENAI_API_KEY for restore, then unset so the file supplies it.
	t.Setenv("OPENAI_API_KEY", "")
	os.Unsetenv("OPENAI_API_KEY")

	c := Load(root)
	if c.APIToken != "from-env" {
		t.Fatalf("real env was overridden: %q", c.APIToken)
	}
	if c.OpenAIKey != "from-dotenv" {
		t.Fatalf("dotenv file not loaded: %q", c.OpenAIKey)
	}
}
