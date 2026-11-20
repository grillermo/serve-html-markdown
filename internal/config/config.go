package config

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/joho/godotenv"
)

// Config holds every setting the app reads from the environment.
type Config struct {
	AppEnv, Root, Port, Host                                   string
	APIToken, SecretKeyBase                                    string
	SotaLLMsKey, ClaudeModel                                   string
	DatabaseHost, DatabasePort, DatabaseUser, DatabasePassword string
	DatabaseName                                               string
	DatabasePool                                               int
	PreparedStatements                                         bool
	AdminEmail, AdminPassword                                  string
	// LanPort, when set, opens a second listener on which requests from
	// LanTrustedIPs act as the ADMIN_EMAIL user without signing in (see
	// internal/web/lan.go). LanTrustedIPs is the raw LAN_TRUSTED_IPS list.
	LanPort, LanTrustedIPs                                    string
	YtdlpBin, VideosDir, FilesDir                             string
	YoutubeClientID, YoutubeClientSecret, YoutubeRefreshToken string
	YoutubeRedirectURI, YoutubeReauthURL                      string
	// YoutubeHost is HOST with Ruby's YouTube default, "localhost:8009" (Host
	// defaults to "localhost"). The OAuth redirect and reauth URLs are built
	// from it: Task 9.8 passes it as video.YoutubeSettings.Host and to
	// video.YoutubeRedirectURI and video.YoutubeReauthURL.
	YoutubeHost                                                 string
	RulinkyHost, RulinkyToken, SlackSuccessURL, SlackFailureURL string
	// JobConcurrency is the number of background worker goroutines.
	JobConcurrency int
}

func get(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n > 0 {
		return n
	}
	return def
}

// Load reads root/.env (if present) and builds a Config from the environment.
// Like dotenv-rails it never overrides variables already set in the real
// environment. Note that it mutates the process environment: values from .env
// that are not already set are exported with os.Setenv. A missing .env is
// ignored; any other error reading it is logged as a warning.
func Load(root string) Config {
	if err := godotenv.Load(filepath.Join(root, ".env")); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("config: could not load .env", "root", root, "err", err)
	}
	c := FromEnv()
	c.Root = root
	c.FilesDir = get("FILES_DIR", filepath.Join(root, "files"))
	if c.VideosDir == "" {
		c.VideosDir = filepath.Join(root, "videos")
	}
	return c
}

// FromEnv builds a Config from the process environment only.
func FromEnv() Config {
	env := get("APP_ENV", "production")
	return Config{
		AppEnv: env, Port: get("PORT", "8009"), Host: get("HOST", "localhost"),
		APIToken: os.Getenv("API_TOKEN"), SecretKeyBase: os.Getenv("SECRET_KEY_BASE"),
		SotaLLMsKey:  os.Getenv("SOTA_LLMS_API_KEY"),
		ClaudeModel:  get("EXPANSION_CLAUDE_MODEL", "sonnet"),
		DatabaseHost: get("DATABASE_HOST", "localhost"), DatabasePort: get("DATABASE_PORT", "5432"),
		DatabaseUser: os.Getenv("DATABASE_USER"), DatabasePassword: os.Getenv("DATABASE_PASSWORD"),
		DatabaseName: get("DATABASE_NAME", "serve_html_markdown_"+env),
		// Rails default: RAILS_MAX_THREADS (3) + 2.
		DatabasePool:       getInt("DATABASE_POOL", 5),
		PreparedStatements: os.Getenv("DATABASE_PREPARED_STATEMENTS") != "false",
		AdminEmail:         os.Getenv("ADMIN_EMAIL"), AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		LanPort: os.Getenv("LAN_PORT"), LanTrustedIPs: os.Getenv("LAN_TRUSTED_IPS"),
		YtdlpBin: get("YTDLP_BIN", "/opt/homebrew/bin/yt-dlp"), VideosDir: os.Getenv("VIDEOS_DIR"),
		YoutubeClientID: os.Getenv("YOUTUBE_CLIENT_ID"), YoutubeClientSecret: os.Getenv("YOUTUBE_CLIENT_SECRET"),
		YoutubeRefreshToken: os.Getenv("YOUTUBE_REFRESH_TOKEN"),
		YoutubeHost:         get("HOST", "localhost:8009"),
		YoutubeRedirectURI:  os.Getenv("YOUTUBE_REDIRECT_URI"), YoutubeReauthURL: os.Getenv("YOUTUBE_REAUTH_URL"),
		RulinkyHost: os.Getenv("RULINKY_HOST"), RulinkyToken: os.Getenv("RULINKY_API_TOKEN"),
		SlackSuccessURL: os.Getenv("SLACK_SUCCESS_WEBHOOK"), SlackFailureURL: os.Getenv("SLACK_FAILURE_WEBHOOK"),
		JobConcurrency: getInt("JOB_CONCURRENCY", 3),
	}
}
