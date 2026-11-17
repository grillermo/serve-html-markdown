# Rails → Go Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Rails 8 app with a single Go binary that serves the same URLs, reads and writes the same Postgres database and `files/` directory, and keeps every existing client (expand.js, iOS Shortcuts, Slack links, Google OAuth redirect URI) working unchanged.

**Architecture:** One `net/http` server (Go 1.22+ pattern routing) with hand-written SQL over `pgx/v5`, an in-process Postgres-backed job queue that replaces Solid Queue, and packages that are line-for-line ports of the Ruby services. The existing Ruby code and its Minitest suite are the specification: every Go package ports the matching Ruby test file first, then the code. Cutover is a big-bang switch on port 8009 after a shadow run on port 8019; the database schema stays as-is (one new table), so rollback is "start Rails again".

**Tech Stack:** Go 1.26, `github.com/jackc/pgx/v5` (pgxpool), `github.com/yuin/goldmark` + `github.com/yuin/goldmark-highlighting/v2` (chroma), `golang.org/x/crypto/bcrypt`, `golang.org/x/oauth2` + `google.golang.org/api/youtube/v3`, `github.com/joho/godotenv`, stdlib `html/template`, `embed`, `log/slog`, `crypto/pbkdf2`, `net/http.CrossOriginProtection`.

---

## What exists today (the contract to preserve)

| Rails piece | Behaviour that must survive |
|---|---|
| `config/routes.rb` | Every route below, same paths, verbs, status codes and JSON shapes |
| Devise (`users`) | Email + password sign-in at `/users/sign_in`; bcrypt hashes in `users.encrypted_password` (cost 12, no pepper) |
| `FilesController` | `GET /{name}.html` injects the expand bootstrap before `</body>`; `.md/.markdown` rendered by Commonmarker in the `markdown` layout; `/` and `/last` redirect to newest `served_files` row; `/index` lists files; `POST /file/new` + `POST /file/upload` behind `Bearer API_TOKEN` |
| `ScrollPositionsController` | `PATCH|POST /scroll_position` (JSON, also via `sendBeacon`) upserts per user+file, anchor `/\A[\w\-:.]+\z/` |
| `ExpansionsController` + `GenerateExpansionJob` + `ExpansionProcessor` + `ClaudeExpandService` + `SelectionLinker` | `POST /expansions` → 202 `{id,status}`; `GET /expansions/:id`; job runs `claude` CLI (fallback `codex`, or OpenAI when `use_openai`), writes `foo--expand-N.html` and links the selection, or writes `foo--vN.md` in edit-in-place mode |
| `VideoSummariesController` + ingest/captions jobs | `POST /summarize-video` and legacy `POST /twitter-video` (bearer); `GET /summarize-video/:id` and `/twitter-video/:id` (public); yt-dlp → YouTube upload → caption checkpoints (5…2880 min) → OpenAI summary → HTML → rulinky + Slack |
| `YoutubeAuthorizationsController` | `GET /youtube/reauth`, `/youtube/callback`, `/youtube/debug` (signed-in only) |
| Misc | `HEAD /health` 200, `GET /favicon.ico` 204, static `public/` files, `rake youtube:refresh_token`, `db:seed` admin user |
| `./serve` | tmux pane `web` on 8009 through PgBouncer (6432, transaction mode, no prepared statements) |

Things deliberately **not** ported: `bin/watch` / `FileWatcher` (disabled in `./serve` "due to lack of use"; `ServedFile.sync!` becomes a CLI subcommand instead), Propshaft (replaced by `embed`), PWA views (commented out), the `solid_queue_*` tables (replaced, dropped in the last phase).

## Key decisions

1. **Same database, same schema.** Go reads Rails' tables directly. Timestamp columns are `timestamp without time zone` holding UTC: Go always writes `time.Now().UTC()` from the app, never SQL `now()`, and reads with `time.UTC`.
2. **Job queue:** one new table `go_jobs`, claimed with `FOR UPDATE SKIP LOCKED` in a single statement (works through PgBouncer transaction mode), polled every second by 3 worker goroutines (matches `config/queue.yml`). No retries on failure, same as today (every job rescues `StandardError`).
3. **Sessions:** a signed (HMAC-SHA256 from `SECRET_KEY_BASE`) cookie instead of Rails' encrypted cookie. Consequence: **everyone signs in once after cutover.** Single-user app; accepted.
4. **CSRF:** Go's `http.CrossOriginProtection` (Fetch-Metadata / Origin based) replaces authenticity tokens. Non-browser callers (iOS Shortcuts with bearer tokens) send no `Sec-Fetch-Site`/`Origin` and pass. `expand.js` keeps sending its (now empty) token; harmless.
5. **Markdown parity is verified, not assumed.** Commonmarker 1.x (comrak) defaults differ from goldmark (heading anchor markup, hard breaks, tagfilter, syntax highlighting theme). Phase 0 captures golden output from the Ruby renderer; the Go renderer must match it. Heading ids matter: `scroll_positions.anchor` stores them.
6. **Selection linker works on byte offsets, not character offsets.** Ruby indexes by character; Go by byte. Every token the scanners look for is ASCII, so byte offsets give identical splices. Multi-byte characters produced from entities map every byte to the whole entity run (same rule the Ruby `Map` documents for chars).
7. **External CLIs stay external:** `claude`, `codex`, `yt-dlp`, `/usr/bin/security`, `/usr/bin/sqlite3` are still shelled out to, exactly as today. No cgo.

## File structure

```
go.mod / go.sum
cmd/serve-html-markdown/main.go         subcommands: serve, migrate, seed-admin, youtube-reauth-url, sync-files
internal/config/config.go               env (+ .env) → Config struct
internal/db/db.go                       pgxpool setup (PgBouncer-aware)
internal/db/migrate.go                  runs db/migrations/*.sql, records in Rails' schema_migrations
internal/db/testdb/testdb.go            test helper: loads db/schema.sql into the test DB, truncates between tests
internal/store/{users,served_files,scroll_positions,expansions,twitter_videos,youtube_credentials}.go
internal/auth/{password,session,middleware,handlers}.go
internal/files/{resolve,versions,unique,writer}.go
internal/markdown/{render,ids,heading}.go
internal/linker/{map,html_map,markdown_map,locator,planner,linker,cgi}.go
internal/llm/{openai_responses,formatter,summarizer,expand}.go
internal/expansion/processor.go
internal/video/{twitter_url,youtube_url,vtt,ytdlp,ego_cookies,slack,rulinky,youtube_uploader,youtube_authorization}.go
internal/jobs/{queue,worker,generate_expansion,ingest,captions}.go
internal/web/{server,routes,files,expansions,scroll,uploads,videos,youtube,assets,templates}.go
web/templates/*.html                    ported ERB views
web/assets/{expand.js,application.css,markdown.css}   moved from app/assets
web/public/*                            moved from public/
db/schema.sql                           pg_dump --schema-only of production (Phase 0)
db/migrations/20261117120000_create_go_jobs.sql
testdata/golden/markdown/*.{md,html}    Commonmarker output captured from Rails
testdata/golden/linker/*.json           SelectionLinker inputs/outputs captured from Rails
script/parity.sh                        shadow-run comparison
```

Each Ruby test file `test/**/foo_test.rb` becomes `internal/<pkg>/foo_test.go`. Port **every** test case; the Ruby file is the list of cases.

Common commands used below:

```bash
go test ./internal/<pkg>/... -run <TestName> -v
go vet ./... && go test ./...
```

Test database: `serve_html_markdown_test` (already exists for Rails). Tests read `TEST_DATABASE_URL`, default `postgres://localhost:5432/serve_html_markdown_test?sslmode=disable`.

---

## Phase 0 — Capture the Ruby behaviour (Ruby still running)

### Task 0.1: Schema dump

**Files:** Create `db/schema.sql`

- [ ] **Step 1:** Dump the schema the Go code will run against.

```bash
pg_dump --schema-only --no-owner --no-privileges serve_html_markdown_development > db/schema.sql
```

- [ ] **Step 2:** Check it contains `users`, `expansions`, `scroll_positions`, `served_files`, `twitter_videos`, `youtube_credentials`, `schema_migrations`.

```bash
grep -E '^CREATE TABLE public\.(users|expansions|scroll_positions|served_files|twitter_videos|youtube_credentials|schema_migrations) ' db/schema.sql | wc -l
```

Expected: `7`

- [ ] **Step 3:** Commit `db/schema.sql` ("chore: dump Postgres schema for the Go port").

### Task 0.2: Record Commonmarker's effective options and golden Markdown output

**Files:** Create `script/golden_markdown.rb`, `testdata/golden/markdown/*.md`, `testdata/golden/markdown/*.html`

- [ ] **Step 1:** Write the corpus. One `.md` per feature, each small: `headings.md` (duplicate headings, punctuation, accents, emoji, `## Café & “quotes”`), `smart.md` (quotes, `--`, `---`, `...`, `<<x>>`), `breaks.md` (soft line breaks inside a paragraph), `autolink.md` (bare `https://x.com/a`, `www.example.com`, emails), `tables.md`, `strike.md` (`~~x~~`), `tasks.md` (`- [ ] a`), `rawhtml.md` (`<div>`, `<script>`, `<iframe>`, `<style>` — checks tagfilter), `code.md` (fenced ruby, go, no-language, indented code). Also copy 5 real documents from the macmini's `~/c/serve-html-markdown/files/*.md` (biggest ones).

- [ ] **Step 2:** Write the capture script.

```ruby
# script/golden_markdown.rb — run with: bin/rails runner script/golden_markdown.rb
dir = Rails.root.join("testdata/golden/markdown")
puts "Commonmarker::Config::OPTIONS = #{Commonmarker::Config::OPTIONS.inspect}"
puts "Commonmarker::Config::PLUGINS = #{Commonmarker::Config::PLUGINS.inspect}"
Dir[dir.join("*.md")].sort.each do |path|
  html = Commonmarker.to_html(File.read(path), options: FilesController::MARKDOWN_OPTIONS)
  File.write(path.sub(/\.md\z/, ".html"), html)
  puts "wrote #{File.basename(path, ".md")}.html"
end
```

- [ ] **Step 3:** Run it and paste the two printed `OPTIONS`/`PLUGINS` lines into the top of `testdata/golden/markdown/README.md`. These decide the goldmark configuration in Task 4.3 (expect `hardbreaks`, `tagfilter`, `table`, `strikethrough`, `tasklist`, and a `syntax_highlighter` theme such as `base16-ocean.dark`).

```bash
bin/rails runner script/golden_markdown.rb
```

- [ ] **Step 4:** Commit ("test: capture Commonmarker golden output for the Go port").

### Task 0.3: Golden SelectionLinker cases

**Files:** Create `script/golden_linker.rb`, `testdata/golden/linker/cases.json`

- [ ] **Step 1:** Write `script/golden_linker.rb`. It holds an array of `{name, extension, source, selected_text, occurrence}` hashes (seed it with every case from `test/services/selection_linker*_test.rb` plus: a selection across `**bold**`, inside `` `code` ``, across an existing link, with `&amp;`, `&nbsp;` and `&#233;`, multi-byte text `“ñandú” — café`, a second occurrence, a cross-paragraph selection, an HTML `<p>` with `<em>` and `<br>`), runs `SelectionLinker.link(..., url: "/x--expand-1.html")`, and writes each case with either `"output"` or `"error": "<class>: <message>"` to `cases.json`.

```ruby
cases = [ { name: "md_bold", extension: ".md", source: "Hello **big** world\n", selected_text: "big world", occurrence: 0 },
          # … every case listed above …
        ]
out = cases.map do |c|
  result = SelectionLinker.link(source: c[:source], extension: c[:extension], selected_text: c[:selected_text],
                                occurrence: c[:occurrence], url: "/x--expand-1.html")
  c.merge(output: result)
rescue SelectionLinker::Error => e
  c.merge(error: "#{e.class.name.demodulize}: #{e.message}")
end
File.write(Rails.root.join("testdata/golden/linker/cases.json"), JSON.pretty_generate(out))
```

- [ ] **Step 2:** Run `bin/rails runner script/golden_linker.rb`, check the file has one entry per case, commit ("test: capture SelectionLinker golden cases").

---

## Phase 1 — Skeleton

### Task 1.1: Module, config, main

**Files:** Create `go.mod`, `cmd/serve-html-markdown/main.go`, `internal/config/config.go`, `internal/config/config_test.go`; Modify `.gitignore` (add `/tmp/serve-html-markdown`)

- [ ] **Step 1:** `go mod init serve-html-markdown` and add deps:

```bash
go mod init serve-html-markdown
go get github.com/jackc/pgx/v5 github.com/joho/godotenv golang.org/x/crypto github.com/yuin/goldmark github.com/yuin/goldmark-highlighting/v2 golang.org/x/oauth2 google.golang.org/api/youtube/v3
```

- [ ] **Step 2: Failing test** — defaults match `.env.example` / `config/database.yml` / `config/puma.rb`.

```go
// internal/config/config_test.go
package config

import "testing"

func TestDefaults(t *testing.T) {
	t.Setenv("DATABASE_PORT", "")
	t.Setenv("PORT", "")
	t.Setenv("DATABASE_PREPARED_STATEMENTS", "false")
	c := FromEnv()
	if c.Port != "8009" { t.Fatalf("port %q", c.Port) }
	if c.DatabasePort != "5432" { t.Fatalf("db port %q", c.DatabasePort) }
	if c.PreparedStatements { t.Fatal("prepared statements should be off") }
	if c.YtdlpBin != "/opt/homebrew/bin/yt-dlp" { t.Fatalf("ytdlp %q", c.YtdlpBin) }
	if c.ClaudeModel != "sonnet" { t.Fatalf("claude model %q", c.ClaudeModel) }
}
```

- [ ] **Step 3:** Run `go test ./internal/config -v` → FAIL (`FromEnv` undefined).

- [ ] **Step 4: Implement.** One field per env var the Ruby code reads (grep `ENV\[` and `ENV.fetch` in `app/ config/ lib/` to confirm the list): `API_TOKEN, OPENAI_API_KEY, EXPANSION_LLM_API_KEY, EXPANSION_CLAUDE_MODEL, SECRET_KEY_BASE, HOST, PORT, DATABASE_{HOST,PORT,USER,PASSWORD,NAME,POOL,PREPARED_STATEMENTS}, ADMIN_EMAIL, ADMIN_PASSWORD, YTDLP_BIN, YOUTUBE_{CLIENT_ID,CLIENT_SECRET,REFRESH_TOKEN,REDIRECT_URI,REAUTH_URL}, RULINKY_HOST, RULINKY_API_TOKEN, SLACK_SUCCESS_WEBHOOK, SLACK_FAILURE_WEBHOOK, VIDEOS_DIR, JOB_CONCURRENCY`, plus `FilesDir` (`<app>/files`) and `AppEnv` (`APP_ENV`, default `production`; database name `serve_html_markdown_<env>`).

```go
// internal/config/config.go
package config

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	AppEnv, Root, Port, Host                                      string
	APIToken, SecretKeyBase                                       string
	OpenAIKey, ExpansionLLMKey, ClaudeModel                       string
	DatabaseHost, DatabasePort, DatabaseUser, DatabasePassword    string
	DatabaseName                                                  string
	DatabasePool                                                  int
	PreparedStatements                                            bool
	AdminEmail, AdminPassword                                     string
	YtdlpBin, VideosDir, FilesDir                                 string
	YoutubeClientID, YoutubeClientSecret, YoutubeRefreshToken     string
	YoutubeRedirectURI, YoutubeReauthURL                          string
	RulinkyHost, RulinkyToken, SlackSuccessURL, SlackFailureURL   string
	JobConcurrency                                                int
}

func get(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Load reads .env (if present) without overriding the real environment, like dotenv-rails.
func Load(root string) Config {
	_ = godotenv.Load(filepath.Join(root, ".env"))
	c := FromEnv()
	c.Root = root
	c.FilesDir = filepath.Join(root, "files")
	if c.VideosDir == "" {
		c.VideosDir = filepath.Join(root, "videos")
	}
	return c
}

func FromEnv() Config {
	env := get("APP_ENV", "production")
	pool, _ := strconv.Atoi(get("DATABASE_POOL", "12"))
	jobs, _ := strconv.Atoi(get("JOB_CONCURRENCY", "3"))
	return Config{
		AppEnv: env, Port: get("PORT", "8009"), Host: get("HOST", "localhost"),
		APIToken: os.Getenv("API_TOKEN"), SecretKeyBase: os.Getenv("SECRET_KEY_BASE"),
		OpenAIKey: os.Getenv("OPENAI_API_KEY"), ExpansionLLMKey: os.Getenv("EXPANSION_LLM_API_KEY"),
		ClaudeModel:  get("EXPANSION_CLAUDE_MODEL", "sonnet"),
		DatabaseHost: get("DATABASE_HOST", "localhost"), DatabasePort: get("DATABASE_PORT", "5432"),
		DatabaseUser: os.Getenv("DATABASE_USER"), DatabasePassword: os.Getenv("DATABASE_PASSWORD"),
		DatabaseName:       get("DATABASE_NAME", "serve_html_markdown_"+env),
		DatabasePool:       pool,
		PreparedStatements: os.Getenv("DATABASE_PREPARED_STATEMENTS") != "false",
		AdminEmail:         os.Getenv("ADMIN_EMAIL"), AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		YtdlpBin:           get("YTDLP_BIN", "/opt/homebrew/bin/yt-dlp"), VideosDir: os.Getenv("VIDEOS_DIR"),
		YoutubeClientID:    os.Getenv("YOUTUBE_CLIENT_ID"), YoutubeClientSecret: os.Getenv("YOUTUBE_CLIENT_SECRET"),
		YoutubeRefreshToken: os.Getenv("YOUTUBE_REFRESH_TOKEN"),
		YoutubeRedirectURI:  os.Getenv("YOUTUBE_REDIRECT_URI"), YoutubeReauthURL: os.Getenv("YOUTUBE_REAUTH_URL"),
		RulinkyHost:         os.Getenv("RULINKY_HOST"), RulinkyToken: os.Getenv("RULINKY_API_TOKEN"),
		SlackSuccessURL:     os.Getenv("SLACK_SUCCESS_WEBHOOK"), SlackFailureURL: os.Getenv("SLACK_FAILURE_WEBHOOK"),
		JobConcurrency:      jobs,
	}
}
```

Note: `JOB_CONCURRENCY` in Rails means *processes*; in Go it is worker goroutines, default 3 (Rails' `threads: 3`).

- [ ] **Step 5:** `go test ./internal/config -v` → PASS.

- [ ] **Step 6:** `main.go` with a subcommand switch (`serve` default; others print "not implemented" until their tasks) and `slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))`. `go build ./...` must succeed.

- [ ] **Step 7:** Commit ("feat(go): module skeleton and config").

### Task 1.2: Database pool, migrator, test helper

**Files:** Create `internal/db/db.go`, `internal/db/migrate.go`, `internal/db/migrate_test.go`, `internal/db/testdb/testdb.go`, `db/migrations/20261117120000_create_go_jobs.sql`

- [ ] **Step 1:** Pool. When prepared statements are off (PgBouncer transaction mode), use the simple protocol.

```go
// internal/db/db.go
package db

import (
	"context"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"serve-html-markdown/internal/config"
)

func URL(c config.Config) string {
	u := url.URL{Scheme: "postgres", Host: c.DatabaseHost + ":" + c.DatabasePort, Path: "/" + c.DatabaseName}
	if c.DatabaseUser != "" {
		u.User = url.UserPassword(c.DatabaseUser, c.DatabasePassword)
	}
	u.RawQuery = "sslmode=disable"
	return u.String()
}

func Open(ctx context.Context, dsn string, maxConns int, prepared bool) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = int32(maxConns)
	if !prepared {
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}
```

- [ ] **Step 2:** The one new table.

```sql
-- db/migrations/20261117120000_create_go_jobs.sql
CREATE TABLE go_jobs (
  id          bigserial PRIMARY KEY,
  kind        text        NOT NULL,
  args        jsonb       NOT NULL DEFAULT '{}',
  run_at      timestamptz NOT NULL DEFAULT now(),
  attempts    integer     NOT NULL DEFAULT 0,
  locked_at   timestamptz,
  finished_at timestamptz,
  last_error  text,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX index_go_jobs_runnable ON go_jobs (run_at, id) WHERE finished_at IS NULL;
```

- [ ] **Step 3: Failing test** for the migrator: applying twice is a no-op, and the version lands in Rails' `schema_migrations` (so a Rails rollback's `db:migrate` ignores it).

```go
// internal/db/migrate_test.go
package db_test

import (
	"context"
	"testing"

	"serve-html-markdown/internal/db"
	"serve-html-markdown/internal/db/testdb"
)

func TestMigrateIsIdempotent(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := db.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version = '20261117120000'`).Scan(&n)
	if n != 1 {
		t.Fatalf("want 1 row, got %d", n)
	}
}
```

- [ ] **Step 4:** Implement `Migrate`: `//go:embed` is not possible from `internal/db` into `db/migrations`, so put `embed.go` at repo root (`package migrations` in `db/migrations/embed.go` with `//go:embed *.sql` and `var FS embed.FS`). `Migrate` lists files sorted, takes the 14-digit prefix as version, skips versions present in `schema_migrations`, runs each file in a transaction and inserts the version. Run it on a **direct** connection (port 5432), not PgBouncer — same reason `./serve` runs `db:migrate` directly today.

- [ ] **Step 5:** `testdb.New(t)`: connects to `TEST_DATABASE_URL`, once per process drops/recreates `public` and executes `db/schema.sql` then `db.Migrate`; per test `TRUNCATE users, expansions, scroll_positions, served_files, twitter_videos, youtube_credentials, go_jobs RESTART IDENTITY CASCADE`. Mark tests that use it with `t.Parallel()` **off** (shared DB).

- [ ] **Step 6:** `go test ./internal/db/... -v` → PASS. Commit ("feat(go): pgx pool, migrator, go_jobs table").

### Task 1.3: Server with health, favicon, public files

**Files:** Create `internal/web/server.go`, `internal/web/routes.go`, `internal/web/routes_test.go`; Move `public/*` → `web/public/` (`git mv`), create `web/embed.go`

- [ ] **Step 1: Failing test**

```go
func TestHealthAndFavicon(t *testing.T) {
	srv := newTestServer(t) // helper: builds web.Server with testdb + temp FilesDir
	cases := []struct{ method, path string; want int }{
		{"HEAD", "/health", 200},
		{"GET", "/favicon.ico", 204},
		{"GET", "/robots.txt", 200},
		{"GET", "/icon.svg", 200},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.want {
			t.Errorf("%s %s: got %d want %d", c.method, c.path, rec.Code, c.want)
		}
	}
}
```

- [ ] **Step 2:** Run → FAIL. **Step 3:** Implement `routes.go` with the full route table now (unported handlers return 501 until their task), so precedence is fixed in one place:

```go
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("HEAD /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	for _, name := range []string{"robots.txt", "icon.svg", "icon.png", "400.html", "404.html", "406-unsupported-browser.html", "422.html", "500.html"} {
		mux.Handle("GET /"+name, s.publicFile(name))
	}
	mux.Handle("GET /assets/{name}", s.assets())

	mux.HandleFunc("GET /users/sign_in", s.signInForm)
	mux.HandleFunc("POST /users/sign_in", s.signIn)
	mux.HandleFunc("DELETE /users/sign_out", s.signOut)

	mux.HandleFunc("POST /file/new", s.createFile)       // bearer
	mux.HandleFunc("POST /file/upload", s.uploadFile)    // bearer
	mux.HandleFunc("POST /summarize-video", s.createVideoSummary) // bearer
	mux.HandleFunc("POST /twitter-video", s.createVideoSummary)   // legacy name for iOS Shortcuts
	mux.HandleFunc("GET /summarize-video/{id}", s.showVideoSummary) // public, like Rails
	mux.HandleFunc("GET /twitter-video/{id}", s.showVideoSummary)

	mux.Handle("GET /youtube/reauth", s.requireUser(s.youtubeReauth))
	mux.Handle("GET /youtube/callback", s.requireUser(s.youtubeCallback))
	mux.Handle("GET /youtube/debug", s.requireUser(s.youtubeDebug))
	mux.Handle("POST /expansions", s.requireUser(s.createExpansion))
	mux.Handle("GET /expansions/{id}", s.requireUser(s.showExpansion))
	mux.Handle("PATCH /scroll_position", s.requireUser(s.updateScrollPosition))
	mux.Handle("POST /scroll_position", s.requireUser(s.updateScrollPosition))

	mux.Handle("GET /{$}", s.requireUser(s.lastFile))
	mux.Handle("GET /last", s.requireUser(s.lastFile))
	mux.Handle("GET /index", s.requireUser(s.indexFiles))
	mux.Handle("GET /{file_name}", s.requireUser(s.showFile))

	return s.securityHeaders(http.NewCrossOriginProtection().Handler(s.logRequests(mux)))
}
```

`{id}` handlers must reject non-digits with 404 (Rails `constraints: { id: /\d+/ }`). `securityHeaders` sets `Strict-Transport-Security: max-age=63072000; includeSubDomains` (Rails `force_ssl` with `assume_ssl`). Static `public/` files get `Cache-Control: public, max-age=31536000` like `production.rb`.

- [ ] **Step 4:** PASS. Commit ("feat(go): http server, route table, health and public files").

---

## Phase 2 — Store layer

One file per table, plain functions over `*pgxpool.Pool` (or a `DB` interface satisfied by pool and `pgx.Tx`). Port the model tests from `test/models/*_test.rb`.

### Task 2.1: served_files

**Files:** Create `internal/store/served_files.go`, `internal/store/served_files_test.go`

- [ ] **Step 1: Failing tests** (port `test/models/served_file_test.rb`, plus): `Record("a.md")` twice → 1 row; `Record("a.txt")` → 0 rows; `Newest` orders by `created_at DESC, id DESC`; `RecordModification` bumps `updated_at` of an existing row and creates a missing one; `Sync(dir)` inserts disk files with allowed extensions and deletes rows with no file.

- [ ] **Step 2:** Run → FAIL. **Step 3:** Implement:

```go
package store

var AllowedExtensions = map[string]bool{".html": true, ".md": true, ".markdown": true}

func allowed(name string) bool { return AllowedExtensions[strings.ToLower(filepath.Ext(name))] }

type ServedFile struct {
	ID        int64
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func RecordServedFile(ctx context.Context, db DB, name string) error {
	if !allowed(name) {
		return nil
	}
	now := time.Now().UTC()
	_, err := db.Exec(ctx, `INSERT INTO served_files (name, created_at, updated_at) VALUES ($1, $2, $2)
		ON CONFLICT (name) DO NOTHING`, name, now)
	return err
}

func RecordServedFileModification(ctx context.Context, db DB, name string) error {
	now := time.Now().UTC()
	_, err := db.Exec(ctx, `INSERT INTO served_files (name, created_at, updated_at) VALUES ($1, $2, $2)
		ON CONFLICT (name) DO UPDATE SET updated_at = EXCLUDED.updated_at`, name, now)
	return err
}

func NewestServedFile(ctx context.Context, db DB) (*ServedFile, error) { /* ORDER BY created_at DESC, id DESC LIMIT 1; pgx.ErrNoRows → nil, nil */ }
func ServedFilesByUpdated(ctx context.Context, db DB) ([]ServedFile, error) { /* ORDER BY updated_at DESC */ }
func ServedFileNames(ctx context.Context, db DB, likePattern, exact string) ([]string, error) { /* WHERE name = $1 OR name LIKE $2 ESCAPE '\' */ }
func SyncServedFiles(ctx context.Context, db DB, dir string) error { /* list dir; insert ON CONFLICT DO NOTHING; DELETE WHERE NOT (name = ANY($1)) */ }
```

- [ ] **Step 4:** PASS. Commit.

### Task 2.2: users, scroll_positions, youtube_credentials

**Files:** Create `internal/store/{users,scroll_positions,youtube_credentials}.go` + tests

- [ ] **Step 1: Failing tests:**
  - `FindUserByEmail(ctx, db, "  Admin@X.com ")` finds `admin@x.com` (Devise `case_insensitive_keys` + `strip_whitespace_keys`).
  - `UpsertUser(email, bcryptHash)` creates then updates (for `seed-admin`).
  - `SetExpansionMode` rejects anything but `create_new`/`edit_in_place`.
  - `UpsertScrollPosition(userID, "a.md", "intro")` twice → 1 row, anchor updated; anchor `"bad anchor!"` → `ErrInvalidAnchor` (`^[\w\-:.]+$`, Ruby `\w` is ASCII here); empty file name → error.
  - `FindScrollPosition` returns `""` when missing.
  - `YoutubeRefreshToken(ctx, db, envFallback)`: latest row by id wins; blank row → env fallback. `StoreYoutubeCredential(token)` updates the latest row or inserts one, sets `obtained_at`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement with the same SQL shape as Task 2.1 (UTC timestamps, `ON CONFLICT (user_id, file_name) DO UPDATE` for scroll positions). **Step 4:** PASS. **Step 5:** Commit.

### Task 2.3: expansions

**Files:** Create `internal/store/expansions.go` + test

- [ ] **Step 1: Failing tests** (port `test/models/expansion_test.rb`):
  - `CreateExpansion` validates presence of file_name/selected_text/question, `occurrence >= 0`, mode in `MODES`.
  - `Claim(id)` returns true once, false the second time (`UPDATE … WHERE id=$1 AND status='pending'` affected 1 row).
  - `Complete(id, url)` sets status/url, clears error, stamps `completed`.
  - `Fail(id, detail)` sets status/error, clears url.
  - `Stamp(id, "job_started", ms)` merges into `timings` JSON without losing other keys; concurrent stamps from 10 goroutines all land.
  - `RecordResponse(id, provider, bytes)` sets `provider_used`, `html_bytes`.
  - `FindUserExpansion(userID, id)` → `nil` for another user's row.
- [ ] **Step 2:** FAIL. **Step 3:** Stamp is one atomic statement (replaces `with_lock`); errors are logged, never returned, like the Ruby rescue:

```go
func StampExpansion(ctx context.Context, db DB, id int64, stage string, epochMS int64) {
	_, err := db.Exec(ctx, `UPDATE expansions
		SET timings = (COALESCE(NULLIF(timings, '')::jsonb, '{}'::jsonb) || jsonb_build_object($2::text, $3::bigint))::text,
		    updated_at = $4
		WHERE id = $1`, id, stage, epochMS, time.Now().UTC())
	if err != nil {
		slog.Error("[Expansion] stamp failed", "stage", stage, "err", err)
	}
}

func NowMS() int64 { return time.Now().UnixMilli() }
```

- [ ] **Step 4:** PASS. **Step 5:** Commit.

### Task 2.4: twitter_videos

**Files:** Create `internal/store/twitter_videos.go` + test

- [ ] **Step 1: Failing tests** (port `test/models/twitter_video_test.rb`):
  - `ForSourceURL("https://x.com/a/status/1?s=20")` matches rows stored as `https://x.com/a/status/1` and `https://x.com/a/status/1?t=x`, not `…/status/12`.
  - `Checkpoints()` is `[5 10 20 30 40 60 120]` for a YouTube source URL, else `[5 10 20 30 40 60 120 1440 2880]` (minutes).
  - `NextCaptionWait(now)`: attempts=0, uploaded 2 min ago → 180s; uploaded 10 min ago → 0; attempts past the list → `ok=false`.
  - Status must be one of `downloading uploading awaiting_captions summarizing publishing done failed`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. `ForSourceURL` SQL: `source_url = $1 OR source_url LIKE $2` with `$2 = escapeLike(canonical) + "?%"`. **Step 4:** PASS. **Step 5:** Commit.

---

## Phase 3 — Authentication

### Task 3.1: Passwords and session cookie

**Files:** Create `internal/auth/password.go`, `internal/auth/session.go`, `internal/auth/session_test.go`

- [ ] **Step 1: Failing tests:**

```go
func TestDeviseHashVerifies(t *testing.T) {
	// Generate one with: bin/rails runner 'puts User.new(password: "secret123").encrypted_password'
	hash := "$2a$12$...paste-the-real-output-here..."
	if !CheckPassword(hash, "secret123") { t.Fatal("devise bcrypt hash should verify") }
	if CheckPassword(hash, "wrong") { t.Fatal("wrong password verified") }
}

func TestSessionRoundTrip(t *testing.T) {
	codec := NewCodec("test-secret-key-base")
	value := codec.Encode(Session{UserID: 7, Salt: "abc", ExpiresAt: time.Now().Add(time.Hour)})
	got, ok := codec.Decode(value)
	if !ok || got.UserID != 7 { t.Fatalf("round trip failed: %+v %v", got, ok) }
	if _, ok := codec.Decode(value + "x"); ok { t.Fatal("tampered cookie accepted") }
	expired := codec.Encode(Session{UserID: 7, ExpiresAt: time.Now().Add(-time.Second)})
	if _, ok := codec.Decode(expired); ok { t.Fatal("expired cookie accepted") }
}
```

(Before running, replace the placeholder hash by running the `bin/rails runner` command in the comment; this is the one value that must come from Ruby.)

- [ ] **Step 2:** FAIL. **Step 3:** Implement.

```go
// internal/auth/password.go
package auth

import "golang.org/x/crypto/bcrypt"

const Cost = 12 // config.stretches in config/initializers/devise.rb

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), Cost)
	return string(b), err
}

// SaltOf mirrors Devise's authenticatable_salt: the bcrypt salt, so a password
// change invalidates existing sessions.
func SaltOf(hash string) string {
	if len(hash) < 29 {
		return hash
	}
	return hash[:29]
}
```

```go
// internal/auth/session.go
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

type Session struct {
	UserID    int64     `json:"u"`
	Salt      string    `json:"s"`
	ExpiresAt time.Time `json:"e"`
}

type Codec struct{ key []byte }

func NewCodec(secretKeyBase string) Codec {
	mac := hmac.New(sha256.New, []byte(secretKeyBase))
	mac.Write([]byte("serve-html-markdown session"))
	return Codec{key: mac.Sum(nil)}
}

func (c Codec) sign(payload string) string {
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (c Codec) Encode(s Session) string {
	raw, _ := json.Marshal(s)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + c.sign(payload)
}

func (c Codec) Decode(value string) (Session, bool) {
	payload, sig, ok := strings.Cut(value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(c.sign(payload))) {
		return Session{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	var s Session
	if err != nil || json.Unmarshal(raw, &s) != nil || time.Now().After(s.ExpiresAt) {
		return Session{}, false
	}
	return s, true
}
```

The same `Codec` signs the short-lived YouTube OAuth state cookie (Task 9.8).

- [ ] **Step 4:** PASS. **Step 5:** Commit.

### Task 3.2: Middleware, sign-in/out, bearer check, seed-admin

**Files:** Create `internal/auth/middleware.go`, `internal/web/auth_handlers.go`, `internal/web/auth_test.go`, `web/templates/layout.html`, `web/templates/sign_in.html`; Modify `cmd/serve-html-markdown/main.go`

- [ ] **Step 1: Failing tests** (port `test/controllers/sessions_test.rb`, plus):
  - `GET /index` without a cookie → 302 to `/users/sign_in?return_to=%2Findex`.
  - `GET /expansions/1` with `Accept: application/json` and no cookie → 401 JSON `{"error":"You need to sign in or sign up before continuing."}` (Devise's message).
  - `POST /users/sign_in` with right credentials → 303 to `return_to` (only if it starts with `/` and not `//`), else `/`; sets cookie `_shm_session`, `HttpOnly`, `Secure`, `SameSite=Lax`.
  - With `remember_me=1` cookie has `Max-Age` 14 days (Devise `remember_for` default); without, it is a browser-session cookie whose signed `ExpiresAt` is 14 days too.
  - Wrong password → 422 with "Invalid Email or password." in the body.
  - A cookie whose `Salt` no longer matches the user's hash → treated as signed out.
  - `DELETE /users/sign_out` clears the cookie, 303 to `/`.
  - `BearerOK(r, token)`: false when token empty; constant-time compare of the whole `Authorization` header against `"Bearer "+token`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. `requireUser` loads the user (id + salt check) and stores it in the request context (`auth.UserFrom(ctx)`). Port `app/views/devise/sessions/new.html.erb` and `layouts/application.html.erb` to `html/template` (`{{template "layout" .}}`), keeping class names so `application.css` still applies. Bearer:

```go
func BearerOK(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) == 1
}
```

- [ ] **Step 4:** `seed-admin` subcommand: reads `ADMIN_EMAIL`/`ADMIN_PASSWORD` (fail if missing, like `ENV.fetch`), `UpsertUser`, prints `Seeded user <email>`.
- [ ] **Step 5:** PASS. Commit ("feat(go): sign-in, sessions, bearer auth, seed-admin").

---

## Phase 4 — Serving files

### Task 4.1: Path resolution and unique names

**Files:** Create `internal/files/resolve.go`, `internal/files/unique.go`, tests

- [ ] **Step 1: Failing tests** (port the path cases in `test/controllers/files_controller_test.rb`):
  - `Resolve(dir, "../etc/passwd.md")` → `ErrBadRequest("Invalid file path.")`.
  - `Resolve(dir, "a.txt")` → `ErrUnsupported("Only .html, .md, and .markdown files are supported.")`.
  - `Resolve(dir, "missing.md")` → `ErrMissing("File not found: missing.md")`.
  - A symlink inside `dir` pointing outside → `ErrBadRequest`.
  - `UniquePath(dir, "notes", ".md")` → `notes.md`, then `notes-1.md` once that exists; `"..\\evil"` normalises backslashes then takes the basename; stems `""`, `.`, `..` → `Invalid filename.`; `"foo--v2"` → `Filenames may not use the reserved --v<number> suffix.`
- [ ] **Step 2:** FAIL. **Step 3:** Implement:

```go
func Resolve(dir, name string) (string, error) {
	root, _ := filepath.Abs(dir)
	p, _ := filepath.Abs(filepath.Join(root, name))
	if !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", BadRequest("Invalid file path.")
	}
	if !store.AllowedExtensions[strings.ToLower(filepath.Ext(p))] {
		return "", ErrUnsupported
	}
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return "", Missing("File not found: " + name)
	}
	real, err := filepath.EvalSymlinks(p)
	realRoot, err2 := filepath.EvalSymlinks(root)
	if err != nil || err2 != nil || !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
		return "", BadRequest("Invalid file path.")
	}
	return real, nil
}
```

- [ ] **Step 4:** PASS. **Step 5:** Commit.

### Task 4.2: FileVersions

**Files:** Create `internal/files/versions.go`, `internal/files/versions_test.go`

- [ ] **Step 1: Failing tests** — port every case of `test/services/file_versions_test.rb`. Must include: `foo.md` → v1; `foo--v2.md` → stem `foo`, v2; `foo--v1.md` and `foo--v02.md` → their own v1 documents; `foo--v10.md` → v10; `NextPath` skips existing files; `FamilyNames` sorts by version and excludes `foo-bar--v2.md` for stem `foo`, and escapes `%`/`_` in `LIKE`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement with `regexp.MustCompile(`^(.+?)--v([2-9]|[1-9]\d+)$`)` and reserved `--v\d+$`. `FamilyNames` uses `store.ServedFileNames(db, escapedStem+"--v%"+ext, baseName)`.
- [ ] **Step 4:** PASS. **Step 5:** Commit.

### Task 4.3: Markdown renderer with golden parity

**Files:** Create `internal/markdown/render.go`, `internal/markdown/ids.go`, `internal/markdown/heading.go`, `internal/markdown/golden_test.go`

- [ ] **Step 1: Failing test** — every golden pair from Task 0.2 must match exactly:

```go
func TestGolden(t *testing.T) {
	inputs, _ := filepath.Glob("../../testdata/golden/markdown/*.md")
	for _, in := range inputs {
		t.Run(filepath.Base(in), func(t *testing.T) {
			src, _ := os.ReadFile(in)
			want, _ := os.ReadFile(strings.TrimSuffix(in, ".md") + ".html")
			got, err := Render(src)
			if err != nil { t.Fatal(err) }
			if string(got) != string(want) {
				t.Errorf("mismatch\n--- want\n%s\n--- got\n%s", want, got)
			}
		})
	}
}
```

- [ ] **Step 2:** FAIL. **Step 3:** Implement the starting configuration; adjust each option against the `OPTIONS` line recorded in Task 0.2:

```go
// internal/markdown/render.go
var md = goldmark.New(
	goldmark.WithExtensions(
		extension.Table, extension.Strikethrough, extension.Linkify, extension.TaskList, // comrak GFM defaults
		extension.NewTypographer(extension.WithTypographicSubstitutions(extension.TypographicSubstitutions{
			extension.LeftAngleQuote: nil, extension.RightAngleQuote: nil, // comrak "smart" does not do << >>
		})),
		highlighting.NewHighlighting(
			highlighting.WithCustomStyle(base16OceanDark), // see below
			highlighting.WithFormatOptions(chromahtml.WithClasses(false), chromahtml.WithPreWrapper(comrakPre{})),
		),
	),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(
		html.WithUnsafe(),    // render: { unsafe: true }
		html.WithHardWraps(), // only if OPTIONS shows hardbreaks: true
		renderer.WithNodeRenderers(util.Prioritized(headingRenderer{}, 100)),
	),
)

func Render(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	ctx := parser.NewContext(parser.WithIDs(newComrakIDs()))
	err := md.Convert(src, &buf, parser.WithContext(ctx))
	return tagFilter(buf.Bytes()), err // tagFilter only if OPTIONS shows tagfilter: true
}
```

Heading ids follow comrak's anchorizer (lowercase; drop everything except letters, marks, numbers, connector punctuation, space and `-`; spaces → `-`; duplicates get `-1`, `-2`, …):

```go
// internal/markdown/ids.go
var rejected = regexp.MustCompile(`[^\p{L}\p{M}\p{N}\p{Pc} -]`)

type comrakIDs struct{ seen map[string]bool }

func newComrakIDs() *comrakIDs { return &comrakIDs{seen: map[string]bool{}} }

func (c *comrakIDs) Generate(value []byte, _ ast.NodeKind) []byte {
	base := strings.ReplaceAll(rejected.ReplaceAllString(strings.ToLower(string(value)), ""), " ", "-")
	id := base
	for i := 1; c.seen[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	c.seen[id] = true
	return []byte(id)
}

func (c *comrakIDs) Put(value []byte) { c.seen[string(value)] = true }
```

comrak puts the id on an empty anchor inside the heading, not on the heading. If the golden `.html` shows `<h2><a inert href="#x" aria-hidden="true" class="anchor" id="x"></a>…`, use this renderer (copy the attribute order from the golden file exactly):

```go
// internal/markdown/heading.go
type headingRenderer struct{}

func (headingRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindHeading, func(w util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		h := n.(*ast.Heading)
		if !entering {
			fmt.Fprintf(w, "</h%d>\n", h.Level)
			return ast.WalkContinue, nil
		}
		fmt.Fprintf(w, "<h%d>", h.Level)
		if raw, ok := h.AttributeString("id"); ok {
			id := stdhtml.EscapeString(string(raw.([]byte)))
			fmt.Fprintf(w, `<a inert href="#%s" aria-hidden="true" class="anchor" id="%s"></a>`, id, id)
		}
		return ast.WalkContinue, nil
	})
}
```

`base16OceanDark` is a chroma style built with `chroma.MustNewStyle("base16-ocean.dark", chroma.StyleEntries{…})` from the base16-ocean palette (bg `#2b303b`, fg `#c0c5ce`, comment `#65737e`, red `#bf616a`, orange `#d08770`, yellow `#ebcb8b`, green `#a3be8c`, cyan `#96b5b4`, blue `#8fa1b3`, magenta `#b48ead`). Syntect and chroma tokenise differently, so `code.md` will not match byte-for-byte: for that one file compare after stripping `<span …>`/`</span>` tags and require the `<pre>`/`<code class="language-…">` wrappers and text to match. Every other golden file must match exactly; if one cannot, write the difference into `testdata/golden/markdown/README.md` and get it approved before moving on.

`tagFilter` (GFM tagfilter): replace `<` with `&lt;` when it opens `title|textarea|style|xmp|iframe|noembed|noframes|script|plaintext` (case-insensitive, open or close tag).

- [ ] **Step 4:** Iterate until `go test ./internal/markdown -v` passes. **Step 5:** Commit ("feat(go): goldmark renderer matching Commonmarker output").

### Task 4.4: Assets and templates

**Files:** `git mv app/assets/javascripts/expand.js app/assets/stylesheets/*.css web/assets/`; Create `internal/web/assets.go`, `web/templates/{markdown.html,index.html,expand_bootstrap.html}`, test

- [ ] **Step 1: Failing test:** `AssetPath("expand.js")` returns `/assets/expand-<first 8 hex of sha256>.js`; `GET` that path → 200, `Content-Type: text/javascript; charset=utf-8`, `Cache-Control: public, max-age=31536000, immutable`; `GET /assets/expand-deadbeef.js` → 404.
- [ ] **Step 2:** FAIL. **Step 3:** Implement with `//go:embed assets` in `web/embed.go`, computing digests at startup. Templates (ported 1:1 from ERB):

```html
{{/* web/templates/expand_bootstrap.html — ExpandBootstrapHelper */}}
{{define "expand_bootstrap"}}<script>{{if .ScrollAnchor}}window.__scrollAnchor = {{.ScrollAnchor}};{{end}}window.__fileVersions = {{.FileVersions}};window.__expansionMode = {{.ExpansionMode}};</script><script src="{{.ExpandJS}}" defer></script>{{end}}
```

`html/template` JSON-encodes values inside `<script>`, matching `to_json`. `FileVersions` must marshal as `[]` (not `null`) when empty — use a non-nil slice. Field names in JSON: `name, url, version, current`.

`markdown.html` reproduces `layouts/markdown.html.erb` (Bookerly font link, `markdown.css`, bootstrap, `{{.Rendered}}` as `template.HTML`). `index.html` formats `updated_at` in `America/Mexico_City` as `Jan 2, 2006 3:04 PM` (Ruby `%b %-d, %Y %-I:%M %p`).

- [ ] **Step 4:** PASS. **Step 5:** Commit.

### Task 4.5: show / index / last / scroll_position handlers

**Files:** Create `internal/web/files.go`, `internal/web/scroll.go`, `internal/web/files_test.go`, `internal/web/scroll_test.go`

- [ ] **Step 1: Failing tests** — port `files_controller_test.rb` (show/index/last parts) and `scroll_positions_controller_test.rb`. Must include:
  - `.html` with `</BODY>` (any case) gets the bootstrap inserted before the **first** match; without `</body>` it is appended.
  - `.md` renders inside the markdown layout with `<title>name.md</title>`.
  - Saved scroll anchor shows as `window.__scrollAnchor = "intro";`; absent → no assignment.
  - `foo.md` with `foo--v2.md` in `served_files` → `__fileVersions` has 2 entries, `current` on the right one; only one member → `[]`.
  - `/last` and `/` → 302 to `/` + url-encoded newest name; no rows → 404 `{"detail":"No files found."}`.
  - Errors: bad path 400, unsupported 404, missing 404, all JSON `{"detail": …}`.
  - scroll: JSON body (also `text/plain` beacons — read the body as JSON regardless of content type, since `sendBeacon` sends a Blob typed `application/json`) and form bodies; missing fields 400; bad anchor 422 `{"detail":"Anchor is invalid"}`; success 204.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. URL-encoding helper matching `ERB::Util.url_encode`:

```go
func URLEncode(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
```

Params helper `param(r, key)`: JSON body if `Content-Type` contains `json` or the body starts with `{`, else form values — Rails merges both into `params`.

- [ ] **Step 4:** PASS. **Step 5:** Commit ("feat(go): serve HTML and Markdown files").

---

## Phase 5 — Upload API

### Task 5.1: OpenAI Responses client and formatter

**Files:** Create `internal/llm/openai_responses.go`, `internal/llm/formatter.go`, tests

- [ ] **Step 1: Failing tests** — port `test/services/openai_formatter_test.rb`; use `httptest.NewServer` as the endpoint (client takes `Endpoint` and `HTTPClient` fields). Cases: posts `{"model":"gpt-6-luna","input":…}` with bearer key; optional `text.format`; joins every `output_text` of every `message` item; non-2xx → `ErrOpenAI("OpenAI request failed.")` and the upstream body never appears in the error; bad JSON → "OpenAI returned an unparseable response."; no text → "OpenAI returned no output."; missing key → `ErrConfiguration`. Key: `OPENAI_API_KEY`, falling back to `EXPANSION_LLM_API_KEY`.
- [ ] **Step 2–4:** FAIL → implement → PASS. Formatter prompt copied verbatim from `app/services/openai_formatter.rb`.
- [ ] **Step 5:** Commit.

### Task 5.2: POST /file/new and /file/upload

**Files:** Create `internal/web/uploads.go`, `internal/web/uploads_test.go`

- [ ] **Step 1: Failing tests** — port the create/upload cases of `files_controller_test.rb`: 401 without bearer; `/file/new` missing `content` → 400 `Missing content.`; formats via the injected formatter and writes `<stem>.md`; formatter error → 502 `OpenAI formatting failed.`; `/file/upload` multipart `file` (+ optional `filename`), extension map `.html/.htm → .html`, `.md/.markdown → .md`, others 400; invalid UTF-8 → 400 `File must be UTF-8 text.`; response `{"url":"https://$HOST/<name>"}`; a `served_files` row is recorded.
- [ ] **Step 2–4:** FAIL → implement (`r.ParseMultipartForm(64 << 20)`, `utf8.Valid`) → PASS. **Step 5:** Commit.

---

## Phase 6 — Job queue

### Task 6.1: Queue and workers

**Files:** Create `internal/jobs/queue.go`, `internal/jobs/worker.go`, `internal/jobs/queue_test.go`

- [ ] **Step 1: Failing tests:**
  - `Enqueue(ctx, db, "k", Args{ID: 1}, 0)` then `claim` returns it once; a second concurrent `claim` returns nothing (run 2 goroutines).
  - A job with `run_at` 1 minute in the future is not claimed.
  - A job locked 31 minutes ago and unfinished is claimed again (crash recovery).
  - Worker marks `finished_at` after the handler returns, records `last_error` if it returned an error, and never re-runs it.
  - A panicking handler is recovered, logged and marked finished with the panic text.
  - `Cleanup` deletes finished jobs older than 24 h (replaces `clear_solid_queue_finished_jobs`).
- [ ] **Step 2:** FAIL. **Step 3:** Implement.

```go
// internal/jobs/queue.go
package jobs

type Args struct {
	ID int64 `json:"id"`
}

func Enqueue(ctx context.Context, db store.DB, kind string, args Args, wait time.Duration) error {
	raw, _ := json.Marshal(args)
	_, err := db.Exec(ctx, `INSERT INTO go_jobs (kind, args, run_at) VALUES ($1, $2, now() + $3::interval)`,
		kind, raw, fmt.Sprintf("%d milliseconds", wait.Milliseconds()))
	return err
}

type job struct {
	ID   int64
	Kind string
	Args Args
}

const claimSQL = `UPDATE go_jobs SET locked_at = now(), attempts = attempts + 1
WHERE id = (
  SELECT id FROM go_jobs
  WHERE finished_at IS NULL AND run_at <= now()
    AND (locked_at IS NULL OR locked_at < now() - interval '30 minutes')
  ORDER BY run_at, id
  FOR UPDATE SKIP LOCKED
  LIMIT 1)
RETURNING id, kind, args`
```

```go
// internal/jobs/worker.go
type Handler func(ctx context.Context, args Args) error

type Runner struct {
	DB       *pgxpool.Pool
	Handlers map[string]Handler
	Workers  int
	Poll     time.Duration // 1s
}

// Run blocks until ctx is cancelled, then waits for in-flight jobs (graceful shutdown on SIGTERM).
func (r *Runner) Run(ctx context.Context) { /* r.Workers goroutines: loop claim → run with recover → finish; sleep Poll when idle; plus an hourly Cleanup ticker */ }
```

Job kinds: `generate_expansion`, `twitter_video_ingest`, `twitter_video_captions`.

- [ ] **Step 4:** PASS. **Step 5:** Commit ("feat(go): Postgres job queue replacing Solid Queue").

---

## Phase 7 — Selection linker

All of `internal/linker` is a line-for-line port of `app/services/selection_linker*.rb`. Translation rules, apply them everywhere:

- Offsets are **byte** offsets into the source string; `Map.Plain` is a `[]byte` built byte by byte, with `Starts`, `Ends`, `BlockIDs` one entry per plain byte. When an entity decodes to a multi-byte character, every byte gets the entity's full `[start,end)` (same rule as Ruby's per-char rule).
- `Range` is `struct{ Begin, End int }`, half-open, like Ruby's `a...b`.
- Ruby `str.match(RE, pos)` → `loc := re.FindStringIndex(src[pos:])`, then add `pos`. None of the patterns use `\A`/`^`, so this is equivalent.
- Ruby `/\s/` is ASCII whitespace; Go `\s` is ASCII too. Keep them.
- Go RE2 has no lookbehind/lookahead. Two places need hand-written scans (code below).
- `CGI.unescapeHTML` only decodes `&amp; &lt; &gt; &quot; &apos;` and numeric references; **not** `&nbsp;` etc. Use the port below, not `html.UnescapeString`, or splices will differ from Ruby.
- Ruby `each_line` keeps the trailing `\n`; split with `strings.SplitAfter(src, "\n")` and drop a final empty element.
- Errors: `ErrNotFound` and `ErrUnsafeMatch` both wrap `linker.Error` (`errors.As` target), messages copied verbatim.

### Task 7.1: Map, cgi unescape, locator

**Files:** Create `internal/linker/{map,cgi,locator}.go`, `internal/linker/locator_test.go`, `internal/linker/cgi_test.go`

- [ ] **Step 1: Failing tests** — port `test/services/selection_linker/selection_locator_test.rb`; add `cgiUnescape("&amp;&nbsp;&#233;&#xE9;&bogus;")` == `"&&nbsp;éé&bogus;"`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement.

```go
// internal/linker/cgi.go
var numericOrBasic = regexp.MustCompile(`&(amp|lt|gt|quot|apos|#[0-9]+|#[xX][0-9A-Fa-f]+);`)

func cgiUnescape(s string) string {
	return numericOrBasic.ReplaceAllStringFunc(s, func(m string) string {
		switch body := m[1 : len(m)-1]; {
		case body == "amp":
			return "&"
		case body == "lt":
			return "<"
		case body == "gt":
			return ">"
		case body == "quot":
			return `"`
		case body == "apos":
			return "'"
		case body[1] == 'x' || body[1] == 'X':
			n, err := strconv.ParseInt(body[2:], 16, 32)
			if err != nil || !utf8.ValidRune(rune(n)) {
				return m
			}
			return string(rune(n))
		default:
			n, err := strconv.ParseInt(body[1:], 10, 32)
			if err != nil || !utf8.ValidRune(rune(n)) {
				return m
			}
			return string(rune(n))
		}
	})
}
```

```go
// internal/linker/locator.go
const notFoundMessage = "Selection not found in source — select a plainer run of text."

func Locate(m *Map, selected string, occurrence int) (Range, error) {
	tokens := strings.Fields(selected) // Ruby split(/\s+/) on ASCII whitespace
	if len(tokens) == 0 {
		return Range{}, notFound()
	}
	quoted := make([]string, len(tokens))
	for i, t := range tokens {
		quoted[i] = regexp.QuoteMeta(t)
	}
	re := regexp.MustCompile(strings.Join(quoted, `\s+`))
	var matches []Range
	plain := string(m.Plain)
	for pos := 0; pos <= len(plain); {
		loc := re.FindStringIndex(plain[pos:])
		if loc == nil {
			break
		}
		start := pos + loc[0]
		matches = append(matches, Range{start, pos + loc[1]})
		_, size := utf8.DecodeRuneInString(plain[start:])
		pos = start + max(size, 1) // Ruby steps one character
	}
	if len(matches) == 0 {
		return Range{}, notFound()
	}
	if occurrence < len(matches) {
		return matches[occurrence], nil
	}
	return matches[0], nil
}
```

`strings.Fields` splits on Unicode whitespace while Ruby's `\s` is ASCII-only; use `strings.FieldsFunc(selected, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' || r == '\v' })` instead.

- [ ] **Step 4:** PASS. **Step 5:** Commit.

### Task 7.2: HtmlMap

**Files:** Create `internal/linker/html_map.go`, `internal/linker/html_map_test.go`

- [ ] **Step 1: Failing tests** — port every case of `test/services/selection_linker/html_map_test.rb`.
- [ ] **Step 2:** FAIL. **Step 3:** Port `html_map.rb`. Token regexp:

```go
var htmlToken = regexp.MustCompile(`(?s)<!--.*?-->|</?[A-Za-z][^>]*>|<![^>]*>|&(?:[A-Za-z][A-Za-z0-9]*|#[0-9]+|#x[0-9A-Fa-f]+);`)
```

Raw-text close tag: `regexp.MustCompile("(?i)</" + regexp.QuoteMeta(name) + `\s*>`)`. Tag name: `(?:^</?)([A-Za-z][A-Za-z0-9-]*)`, lowercased.

- [ ] **Step 4:** PASS. **Step 5:** Commit.

### Task 7.3: MarkdownMap

**Files:** Create `internal/linker/markdown_map.go`, `internal/linker/markdown_map_test.go`

- [ ] **Step 1: Failing tests** — port every case of `test/services/selection_linker/markdown_map_test.rb`.
- [ ] **Step 2:** FAIL. **Step 3:** Port `markdown_map.rb`. Regexps:

```go
var (
	fence        = regexp.MustCompile("^(`{3,}|~{3,})")
	heading      = regexp.MustCompile(`^#{1,6}[ \t]+`)
	listItem     = regexp.MustCompile(`^[ \t]*(?:[-*+]|\d{1,9}[.)])[ \t]+`)
	blockquote   = regexp.MustCompile(`^[ \t]{0,3}>[ \t]?`)
	indentedCode = regexp.MustCompile(`^(?: {4}|\t)`)
	inlineToken  = regexp.MustCompile("\\\\[!-/:-@\\[-`{-~]|`+|!?\\[|</?[A-Za-z][^>\\n]*>|&(?:[A-Za-z][A-Za-z0-9]*|#[0-9]+|#x[0-9A-Fa-f]+);|\\*{1,2}|_{1,2}")
)
```

(These `^` anchors are applied to the single line `content`, where Ruby's `\A` meant the same thing.)

The code-span closer `(?<!`)`{n}(?!`)` becomes a scan for the first run of exactly `n` backticks:

```go
// findClosingTicks returns [start,end) of the first backtick run of exactly n at or after from.
func findClosingTicks(src string, from, n int) (int, int, bool) {
	for i := from; i < len(src); {
		if src[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(src) && src[j] == '`' {
			j++
		}
		if j-i == n {
			return i, j, true
		}
		i = j
	}
	return 0, 0, false
}
```

Note the Ruby closer search is not bounded by `upto`; it checks `closer.end(0) > upto` afterwards. Keep that order.

- [ ] **Step 4:** PASS. **Step 5:** Commit.

### Task 7.4: Planner and Link, with golden cases

**Files:** Create `internal/linker/{planner,linker}.go`, `internal/linker/planner_test.go`, `internal/linker/golden_test.go`

- [ ] **Step 1: Failing tests** — port `planner_test.rb` and `test/services/selection_linker_test.rb`, then the golden test:

```go
func TestGoldenCases(t *testing.T) {
	raw, _ := os.ReadFile("../../testdata/golden/linker/cases.json")
	var cases []struct {
		Name, Extension, Source, SelectedText, Output, Error string
		Occurrence                                           int
	}
	if err := json.Unmarshal(raw, &cases); err != nil { t.Fatal(err) }
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := Link(c.Source, c.Extension, c.SelectedText, c.Occurrence, "/x--expand-1.html")
			if c.Error != "" {
				if err == nil || errorLabel(err) != c.Error {
					t.Fatalf("want error %q, got %v", c.Error, err)
				}
				return
			}
			if err != nil || got != c.Output {
				t.Fatalf("got %q (%v)\nwant %q", got, err, c.Output)
			}
		})
	}
}
```

(`json` field tags: `selected_text`, the rest match lowercase; add struct tags. `errorLabel` returns `"NotFound: msg"` / `"UnsafeMatch: msg"`.)

- [ ] **Step 2:** FAIL. **Step 3:** Port `planner.rb` and `selection_linker.rb`. The Markdown label escape `(?<!\\)\]` → `\]` is a byte loop:

```go
func escapeLabel(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == ']' && (i == 0 || s[i-1] != '\\') {
			b.WriteString(`\]`)
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
```

Splice segments sorted by `Begin` descending, replacing `src[seg.Begin:seg.End]`.

- [ ] **Step 4:** PASS (all golden cases). **Step 5:** Commit ("feat(go): port SelectionLinker").

---

## Phase 8 — Expansions

### Task 8.1: Expand service (claude → codex fallback, or OpenAI)

**Files:** Create `internal/llm/expand.go`, `internal/llm/expand_test.go`

- [ ] **Step 1: Failing tests** — port `test/services/claude_expand_service_test.rb`. Inject a `Runner func(ctx context.Context, argv []string) (stdout, stderr []byte, err error)` and an OpenAI endpoint. Cases: claude argv is exactly `claude -p <prompt> --model <EXPANSION_CLAUDE_MODEL|sonnet> --output-format json --tools ""`; `is_error: true` → falls back to codex (`codex exec -m earth -s read-only --skip-git-repo-check --color never -o <tmpfile> <prompt>`, result read from the tmp file); both fail → `ErrExpand`; `use_openai` never falls back and posts model `gpt-5.6-terra` with `reasoning.effort: medium` using `EXPANSION_LLM_API_KEY`; fenced output ```` ```html … ``` ```` is unwrapped; `Expand` requires `<html` (case-insensitive); `Rewrite` of `.md` only requires non-blank and keeps surrounding whitespace; a 120 s timeout kills the process; `RecordResponse` and stamps `llm_request_start`, `llm_first_failure`, `llm_response` are called on the expansion.
- [ ] **Step 2:** FAIL. **Step 3:** Implement; copy both prompt templates verbatim (Ruby `%{name}` → Go `strings.NewReplacer("%{file_name}", …)`, so a `%` in a document cannot break formatting). Default runner:

```go
func execRunner(ctx context.Context, argv []string) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = nil
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, nil, fmt.Errorf("%s timed out after 120s", argv[0])
	}
	return out.Bytes(), errb.Bytes(), err
}
```

`./serve` starts the server inside tmux from the user's shell, so `claude`/`codex` stay on `PATH` as today.

- [ ] **Step 4:** PASS. **Step 5:** Commit.

### Task 8.2: Expansion processor

**Files:** Create `internal/expansion/processor.go`, `internal/expansion/processor_test.go`

- [ ] **Step 1: Failing tests** — port `test/services/expansion_processor_test.rb`, with a fake expander. Must include: create-new writes `foo--expand-1.html` (then `-2`), links the selection in `foo.md` with `/foo--expand-1.html`, records both files in `served_files` (`RecordModification` for the source), returns the URL; edit-in-place writes `foo--v2.md` (`foo--v3.md` when v2 exists, also when the request was for `foo--v2.md`), replaces only the **first** `⟦EXPANSION_ANCHOR⟧` with `<a id="expansion-anchor"></a>` and drops the rest, returns `/foo--v2.md?fallback=<enc>#expansion-anchor` (no `?fallback` when blank); rewrite shorter than half the source (in **characters**: use `utf8.RuneCountInString`, as Ruby's `length`) → `ErrTruncatedRewrite("Rewrite looked truncated.")`; the source is re-read under the lock before linking.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. Lock (same lock file names as Ruby, so Rails and Go would even serialize against each other during a shadow run on one directory):

```go
func withLock(filesDir, keyName string, fn func() error) error {
	path := filepath.Join(filesDir, "."+keyName+".expansion.lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
```

- [ ] **Step 4:** PASS. **Step 5:** Commit.

### Task 8.3: generate_expansion job and expansion endpoints

**Files:** Create `internal/jobs/generate_expansion.go`, `internal/web/expansions.go`, tests

- [ ] **Step 1: Failing tests** — port `generate_expansion_job_test.rb` and `expansions_controller_test.rb`:
  - Job: not claimable → no-op; success → completed with URL; `ErrExpand` → failed `Generation failed.`; linker errors, bad request, truncated rewrite, unsupported/missing file → failed with **their message**; anything else → `Expansion failed.`
  - `POST /expansions` (JSON): missing fields → 400 `Missing file_name, selected_text, or question.`; unknown mode → 400 `Unknown mode.`; success → 202 `{"id":…,"status":"pending"}`, `go_jobs` row of kind `generate_expansion`, user's `expansion_mode` updated, stamps `client_clicked` (when `client_clicked_at` given), `request_received`, `job_enqueued`; `occurrence` negative → 0; `use_openai` accepts `true`, `"true"`, `"1"`, `1`.
  - `GET /expansions/{id}`: other user's → 404 `{"detail":"Expansion not found."}`; `completed` adds `url`; `failed` adds `detail`.
- [ ] **Step 2–4:** FAIL → implement → PASS. **Step 5:** Commit ("feat(go): expansions end to end").

---

## Phase 9 — Video summaries

Each service gets the Ruby test file ported first. All HTTP clients take an injectable base URL / `*http.Client`; all CLIs take an injectable runner, like the Ruby `runner:`/`connection:` seams.

### Task 9.1: TwitterUrl, YoutubeUrl, VttText

**Files:** `internal/video/{twitter_url,youtube_url,vtt}.go` + tests (port `twitter_url_test.rb`, `youtube_url_test.rb`, `vtt_text_test.rb` verbatim as table tests)

- [ ] Steps: failing tests → FAIL → port → PASS → commit. `with_scheme`: prefix `https://` unless `^[a-z][a-z0-9+.-]*://` (case-insensitive). VTT un-escaping uses `cgiUnescape` semantics (move it to `internal/textutil` and import from both packages). `squeeze(" ")` collapses runs of spaces only. Consecutive duplicate lines collapse (`chunk_while`).

### Task 9.2: YtdlpClient

**Files:** `internal/video/ytdlp.go` + test (port `ytdlp_client_test.rb`)

- [ ] Steps as above. argv copied exactly from `ytdlp_client.rb`, including `--ignore-no-formats-error`. Failure message: `ERROR:` lines from stderr joined by ` | `, else last 3 non-empty lines. `FetchAutoSubs` returns the lexically first `*.vtt` in the dir or `""`.

### Task 9.3: EgoCookies

**Files:** `internal/video/ego_cookies.go` + test (port `ego_cookies_test.rb`)

- [ ] Steps as above. Keep shelling out to `/usr/bin/security find-generic-password -w -s "ego safe storage"` and `/usr/bin/sqlite3 -readonly -separator "\t"` with a 10 s timeout. Crypto: `pbkdf2.Key(sha1.New, password, []byte("saltysalt"), 1003, 16)` (stdlib `crypto/pbkdf2` in Go 1.24+: `pbkdf2.Key(sha1.New, password, salt, iter, keyLen)` returns `([]byte, error)`); AES-128-CBC, IV = 16 spaces, strip the `v10` prefix, PKCS#7 unpad (bad padding → skip the cookie, like `CipherError`), drop the first 32 bytes when meta `version >= 24`. Expiry: `expires_utc/1_000_000 - 11_644_473_600`, `0` stays `0`. De-duplicate by `(host, name, path)` keeping the first row. Copy the DB to a temp dir first.

### Task 9.4: SlackNotifier, RulinkyClient, OpenaiSummarizer, ServedHtmlWriter

**Files:** `internal/video/{slack,rulinky}.go`, `internal/llm/summarizer.go`, `internal/files/writer.go` + tests (port `slack_notifier_test.rb`, `rulinky_client_test.rb`, `openai_summarizer_test.rb`, `served_html_writer_test.rb`)

- [ ] Steps as above. Slack: blank URL → no request; any error logged and swallowed. Rulinky: `POST <host>/api/links` with bearer, returns `id` (JSON number or string → keep as string, column is `string`). Summarizer: same prompt and `json_schema` text format, strips ```` ```json ```` fences, requires non-empty `title` and `summary_html`. Writer: slug `[^a-z0-9]+ → -` after lowercasing, trimmed, default `summary`; `-1`, `-2` suffixes; title HTML-escaped; stylesheet copied verbatim; records `served_files`. Compare the writer's output for a fixed title/body byte-for-byte with Ruby's (`bin/rails runner 'puts ServedHtmlWriter.document("A & B", "<p>x</p>")'`, saved as `testdata/golden/served_html.html`).

### Task 9.5: YouTube uploader and authorization

**Files:** `internal/video/{youtube_uploader,youtube_authorization}.go` + tests (port `youtube_uploader_test.rb`, `youtube_authorization_test.rb`)

- [ ] **Step 1: Failing tests:** consent URL contains `access_type=offline`, `prompt=consent`, `scope=https://www.googleapis.com/auth/youtube.upload`, the state, and `redirect_uri` = `YOUTUBE_REDIRECT_URI` or `https://$HOST/youtube/callback` (never request-derived); `ReauthURL(videoID)` appends `?video_id=`; exchange with no refresh token → error `Google returned no refresh token for this code.`; uploader: blank client id/secret → `Error("YouTube OAuth credentials are not configured.")`, blank refresh token → `ErrAuthorizationExpired`; token endpoint answering `{"error":"invalid_grant"}` → `ErrAuthorizationExpired`; uploads with `privacyStatus: unlisted`, parts `snippet,status`, returns the video id. Use an `httptest` server for both the token and upload endpoints (`oauth2.Config.Endpoint.TokenURL` and `option.WithEndpoint`).
- [ ] **Step 2:** FAIL. **Step 3:** Implement with `golang.org/x/oauth2` (`AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt","consent"))`, `Exchange`) and `youtube.NewService(ctx, option.WithTokenSource(cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: rt})))`. Force the token fetch before uploading (`ts.Token()`) so an expired grant is detected before streaming the file; detect `invalid_grant` with `errors.As(err, &*oauth2.RetrieveError)` and `ErrorCode == "invalid_grant"`. Upload errors wrap as `YouTube upload failed: <status> <message> body=<body>`.
- [ ] **Step 4:** PASS. **Step 5:** Commit.

### Task 9.6: Ingest and captions jobs

**Files:** `internal/jobs/{ingest,captions}.go` + tests (port `twitter_video_ingest_job_test.rb`, `twitter_video_captions_job_test.rb`)

- [ ] **Step 1: Failing tests** — every case in the two Ruby files, with fakes for ytdlp, uploader, cookies, summarizer, rulinky, slack, writer (a `Deps` struct of interfaces replaces the Ruby `class << self` lambdas). Key ones: reuse an existing on-disk download from any sibling row (current row first, then newest id); first caption check enqueued 5 min after upload; `AuthorizationExpired` sends the Slack message with the reauth link (text copied verbatim); caption fetch errors count as "not ready"; reschedule uses `NextCaptionWait`; exhausted → failed `Auto-captions never became available.` and a Slack failure mentioning the last checkpoint (`2880 minutes` — Ruby printed `2880.minutes.inspect`, i.e. `"2880 minutes"`; match that string); success flow walks statuses `summarizing → publishing → done` and stores `html_filename`, `rulinky_link_id`.
- [ ] **Step 2–4:** FAIL → implement → PASS. **Step 5:** Commit.

### Task 9.7: Video summary endpoints

**Files:** `internal/web/videos.go` + test (port `video_summaries_controller_test.rb`)

- [ ] Steps as above. Must include: first `https?://\S+` in a shared text blob wins; YouTube link → upsert row with `awaiting_captions`, `caption_attempts=0`, `upload_completed_at=now`, enqueue captions immediately; tweet → reuse newest matching row or create `downloading`, enqueue ingest; invalid → 400 `Unsupported URL: send an x.com/twitter.com post or a YouTube video link`; `GET` returns exactly `id, status, error_detail, html_filename, youtube_id` (nulls included); both route names behave identically.

### Task 9.8: YouTube re-auth endpoints and CLI

**Files:** `internal/web/youtube.go` + test (port `youtube_authorizations_controller_test.rb`), `web/templates/{youtube_create,youtube_problem}.html`; Modify `main.go` (`youtube-reauth-url` prints the same three lines as `lib/tasks/youtube.rake`)

- [ ] Steps as above. State (`hex(16)`) and `video_id` go into a signed cookie `_shm_oauth` (10 min, `SameSite=Lax` so it survives Google's top-level redirect back). Callback: `error` param → 400 problem page; state mismatch (constant-time) → 400 `Authorization state did not match. Start again from the Slack link.`; config error → 500; exchange error → 502; success stores the credential, enqueues ingest for the stored `video_id` if that row exists, clears the cookie, renders the success page. `/youtube/debug` prints the same keys as the Ruby `debug_report` (`Rails.env` → `APP_ENV`, `Rails.root` → app root).

---

## Phase 10 — Parity check (shadow run)

### Task 10.1: Shadow server and comparison script

**Files:** Create `script/parity.sh`

- [ ] **Step 1:** On the macmini, copy `files/` to `/tmp/shm-shadow-files` and start Go against the **production DB** with the job runner **off** (add a `--no-jobs` flag to `serve`) and `FILES_DIR` pointing at the copy, on port 8019:

```bash
go build -o tmp/serve-html-markdown ./cmd/serve-html-markdown && FILES_DIR=/tmp/shm-shadow-files PORT=8019 tmp/serve-html-markdown serve --no-jobs
```

(Add `FILES_DIR` override to `config.Load` for this.)

- [ ] **Step 2:** `script/parity.sh` signs in to both servers (curl cookie jars; Rails needs the authenticity token from the sign-in form), then for every name in `served_files` fetches `/<name>` from 8009 and 8019 and diffs them after removing the lines that legitimately differ (`csrf-token` meta, asset digests in `<script src>`/`<link href>`). It also diffs `/index`, `/last` (Location header) and `GET /summarize-video/<last 5 ids>`. Prints `OK <name>` or the `difft` of the pair.

- [ ] **Step 3:** Run it. Every `.html` file must match; `.md` files may differ only inside highlighted code blocks (Task 4.3). Fix and re-run until clean. Record the result in the PR description.

- [ ] **Step 4:** By hand on 8019 (phone + desktop): sign in, open a Markdown page, scroll and reload (anchor restored), queue one create-new and one edit-in-place expansion **on a copied file** — the copy lives in `/tmp/shm-shadow-files`, so production files are untouched — and confirm both finish (temporarily run with jobs on and `FILES_DIR` on the copy for this step only).

- [ ] **Step 5:** Commit `script/parity.sh`.

---

## Phase 11 — Cutover

### Task 11.1: New `./serve`

**Files:** Modify `serve`

- [ ] **Step 1:** Replace the Ruby steps:

```bash
#!/usr/bin/env bash
# (Re)start serve-html-markdown in tmux session "serve-html-markdown", in place.
# Run by ../server/after-reboot.sh at boot and by ./activate after git-sync lands new code.
set -euo pipefail
APP_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$APP_DIR"
SESSION=serve-html-markdown
# shellcheck source=/dev/null
. "$HOME/c/server/serve-in-place.sh"

echo "==> go build"; go build -o tmp/serve-html-markdown ./cmd/serve-html-markdown
echo "==> migrate";  tmp/serve-html-markdown migrate   # direct connection (5432): migrations need a session

# The web pane goes through PgBouncer (transaction mode), so no prepared statements.
serve_pane web 8009 "env DATABASE_PORT=6432 DATABASE_PREPARED_STATEMENTS=false DATABASE_POOL=12 sh -c 'tmp/serve-html-markdown serve 2>&1 | tee -a log/production.log'"
serve_attach
```

- [ ] **Step 2:** `shellcheck serve` → clean. `serve` must handle SIGTERM by stopping the listener (`http.Server.Shutdown`, 30 s) and then the job runner, so a restart never cuts an in-flight expansion short of its DB update.
- [ ] **Step 3:** Commit (do **not** push yet: git-sync would deploy it).

### Task 11.2: Move pending Solid Queue jobs

**Files:** Create `db/cutover/move_solid_queue_jobs.sql`

- [ ] **Step 1:** Inspect what is pending (expect a few `TwitterVideoCaptionsJob` scheduled up to 2 days out):

```bash
psql serve_html_markdown_production -c "SELECT j.id, j.class_name, j.scheduled_at, j.arguments::jsonb -> 'arguments' AS args FROM solid_queue_jobs j WHERE j.finished_at IS NULL AND NOT EXISTS (SELECT 1 FROM solid_queue_failed_executions f WHERE f.job_id = j.id) ORDER BY j.scheduled_at"
```

- [ ] **Step 2:** The move:

```sql
-- db/cutover/move_solid_queue_jobs.sql — run once, with Rails stopped
BEGIN;
INSERT INTO go_jobs (kind, args, run_at)
SELECT CASE j.class_name
         WHEN 'GenerateExpansionJob'    THEN 'generate_expansion'
         WHEN 'TwitterVideoIngestJob'   THEN 'twitter_video_ingest'
         WHEN 'TwitterVideoCaptionsJob' THEN 'twitter_video_captions'
       END,
       jsonb_build_object('id', (j.arguments::jsonb -> 'arguments' ->> 0)::bigint),
       COALESCE(j.scheduled_at AT TIME ZONE 'UTC', now())
FROM solid_queue_jobs j
WHERE j.finished_at IS NULL
  AND j.class_name IN ('GenerateExpansionJob', 'TwitterVideoIngestJob', 'TwitterVideoCaptionsJob')
  AND NOT EXISTS (SELECT 1 FROM solid_queue_failed_executions f WHERE f.job_id = j.id)
  AND NOT EXISTS (SELECT 1 FROM solid_queue_claimed_executions c WHERE c.job_id = j.id);
UPDATE solid_queue_jobs SET finished_at = now() WHERE finished_at IS NULL;
COMMIT;
```

Expansions stuck in `processing` (claimed by a Rails job that was cut off) are marked failed: `UPDATE expansions SET status='failed', error_detail='Interrupted by server migration.' WHERE status='processing';`.

- [ ] **Step 3:** Commit.

### Task 11.3: Switch

- [ ] **Step 1:** On the macmini: stop the Rails pane (`tmux kill-session -t serve-html-markdown`), then run Task 11.2's SQL against production (direct port 5432).
- [ ] **Step 2:** Push the branch / merge to `main`; git-sync lands it and `./activate` runs the new `./serve`. (Or run `./serve` by hand.)
- [ ] **Step 3:** Verify:

```bash
curl -sI https://serve.chiq.me/health | head -1
```

Expected: `HTTP/2 200`. Then sign in from the phone, open `/last`, queue an expansion, send one tweet through the iOS Shortcut and watch `go_jobs` / Slack.

- [ ] **Step 4: Rollback (if anything is wrong):** `git revert` the cutover commit (or check out the previous `serve`), run it; Rails ignores `go_jobs`. Any Go-queued jobs would need the reverse of Task 11.2 — for a rollback within hours it is simpler to re-submit the few videos by hand.

---

## Phase 12 — Cleanup (a week after a clean cutover)

### Task 12.1: Remove Ruby

- [ ] Delete `app/ bin/ config/ lib/ test/ Gemfile Gemfile.lock Rakefile config.ru .ruby-version .ruby-lsp/ script/golden_*.rb`; keep `db/schema.sql`, `db/migrations`, `docs/`, `files/`, `public` (already moved). Rewrite `README.md` (Requirements: Go 1.26, Postgres, yt-dlp, claude/codex CLIs; Setup: `go build`, `.env`; Run: `./serve`; `seed-admin` and `youtube-reauth-url` replace `db:seed` and `rake youtube:refresh_token`). Update `docs/configure-twitter-video-to-html.md` references. `go vet ./... && go test ./...` green. Commit.

### Task 12.2: Drop Solid Queue tables

- [ ] Add `db/migrations/2026MMDDhhmmss_drop_solid_queue.sql` dropping the eleven `solid_queue_*` tables, `./serve`, verify `\dt solid_queue*` is empty. Commit.

---

## Risks and how the plan covers them

| Risk | Mitigation |
|---|---|
| Markdown output drifts (heading ids → saved scroll anchors, hard breaks, smart quotes) | Golden files from the real Commonmarker (0.2, 4.3) + parity script over every served file (10.1) |
| SelectionLinker offset bugs (bytes vs chars, no lookbehind, CGI vs html unescape) | Explicit translation rules, ported unit tests, golden cases from Ruby including multi-byte and entity input (0.3, 7.x) |
| In-flight jobs lost at cutover | 11.2 moves pending Solid Queue rows; `processing` expansions marked failed |
| PgBouncer transaction mode | Simple protocol when prepared statements are off; queue claim is a single statement; migrations on 5432 |
| Timestamps shifted by DB session time zone | App always writes `time.Now().UTC()` into `timestamp` columns (Phase 2) |
| Everyone logged out | Accepted (one user); documented in 3.1 |
| CSRF semantics change | `CrossOriginProtection`; bearer API clients unaffected; tested in 3.2 / 4.5 |
| YouTube OAuth regressions | Redirect URI never request-derived; `/youtube/debug` kept; ported tests (9.5, 9.8) |
