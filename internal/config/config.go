package config

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/joho/godotenv"
)

// Config holds every setting the app reads from the environment.
type Config struct {
	AppEnv, Root, Port, Host                                    string
	APIToken, SecretKeyBase                                     string
	OpenAIKey, ExpansionLLMKey, ClaudeModel                     string
	DatabaseHost, DatabasePort, DatabaseUser, DatabasePassword  string
	DatabaseName                                                string
	DatabasePool                                                int
	PreparedStatements                                          bool
	AdminEmail, AdminPassword                                   string
	YtdlpBin, VideosDir, FilesDir                               string
	YoutubeClientID, YoutubeClientSecret, YoutubeRefreshToken   string
	YoutubeRedirectURI, YoutubeReauthURL                        string
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

// Load reads .env (if present) without overriding the real environment, like dotenv-rails.
func Load(root string) Config {
	_ = godotenv.Load(filepath.Join(root, ".env"))
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
		OpenAIKey: os.Getenv("OPENAI_API_KEY"), ExpansionLLMKey: os.Getenv("EXPANSION_LLM_API_KEY"),
		ClaudeModel:  get("EXPANSION_CLAUDE_MODEL", "sonnet"),
		DatabaseHost: get("DATABASE_HOST", "localhost"), DatabasePort: get("DATABASE_PORT", "5432"),
		DatabaseUser: os.Getenv("DATABASE_USER"), DatabasePassword: os.Getenv("DATABASE_PASSWORD"),
		DatabaseName: get("DATABASE_NAME", "serve_html_markdown_"+env),
		// Rails default: RAILS_MAX_THREADS (3) + 2.
		DatabasePool:       getInt("DATABASE_POOL", 5),
		PreparedStatements: os.Getenv("DATABASE_PREPARED_STATEMENTS") != "false",
		AdminEmail:         os.Getenv("ADMIN_EMAIL"), AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		YtdlpBin: get("YTDLP_BIN", "/opt/homebrew/bin/yt-dlp"), VideosDir: os.Getenv("VIDEOS_DIR"),
		YoutubeClientID: os.Getenv("YOUTUBE_CLIENT_ID"), YoutubeClientSecret: os.Getenv("YOUTUBE_CLIENT_SECRET"),
		YoutubeRefreshToken: os.Getenv("YOUTUBE_REFRESH_TOKEN"),
		YoutubeRedirectURI:  os.Getenv("YOUTUBE_REDIRECT_URI"), YoutubeReauthURL: os.Getenv("YOUTUBE_REAUTH_URL"),
		RulinkyHost: os.Getenv("RULINKY_HOST"), RulinkyToken: os.Getenv("RULINKY_API_TOKEN"),
		SlackSuccessURL: os.Getenv("SLACK_SUCCESS_WEBHOOK"), SlackFailureURL: os.Getenv("SLACK_FAILURE_WEBHOOK"),
		JobConcurrency: getInt("JOB_CONCURRENCY", 3),
	}
}
