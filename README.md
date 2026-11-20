# Serve HTML & Markdown

A small Go server that serves trusted HTML and Markdown files from the `files/`
directory.

- `GET /name.html` serves HTML with a small expansion script injected.
- Select text on any page to queue a linked AI answer page. Multiple expansions run in the background; fixed status bars remain only while the current page is open (requires the `claude` CLI; falls back to `codex`).
- `GET /name.md` and `GET /name.markdown` render Markdown with a dark theme.
- `GET /` and `GET /last` redirect to the most recently modified supported file.
- `HEAD /health` returns `200 OK`.
- `GET /new?<url>` downloads the `.html`/`.md` at that URL, saves it as a new file and redirects to it. Paste the URL as is after `?` (no escaping needed). Needs a session or the bearer token.
- `POST /file/new` formats content with `agy:gemini-3.8-flash-medium` through sota-llms and saves it as Markdown.

It started as a Rails app and was ported to Go; `docs/superpowers/plans/2026-11-17-go-migration.md`
has the plan and the behaviour that was kept.

## Requirements

- Go 1.26
- PostgreSQL (the app's tables are in `db/schema.sql`; its own `go_jobs` queue table is created by `migrate`)
- `yt-dlp` for the video summaries (on `PATH`, or set `YTDLP_BIN`)
- the `claude` CLI (falls back to `codex`) for expansions

## Setup

Copy `.env.example` to `.env`, fill it in, then:

```sh
go build -o tmp/serve-html-markdown ./cmd/serve-html-markdown
tmp/serve-html-markdown migrate
tmp/serve-html-markdown seed-admin
```

The main values:

```dotenv
API_TOKEN=replace-with-a-long-random-token
OPENAI_API_KEY=replace-with-your-sota-llms-api-key  # bearer key for https://sota-llms.chiq.me (SOTA_LLMS_API_KEY there)
SECRET_KEY_BASE=replace-with-a-long-random-secret
HOST=example.com
```

The dotenv file and served files are ignored by Git. Real environment
variables win over the dotenv file.

## Commands

`serve-html-markdown <command>`:

| Command | What it does |
|---|---|
| `serve` (default) | Runs the HTTP server and the job workers. `--no-jobs` leaves the workers off. |
| `migrate` | Applies `db/migrations/*.sql` (use a direct connection, not PgBouncer). |
| `seed-admin` | Creates or updates the admin user from `ADMIN_EMAIL` / `ADMIN_PASSWORD`. |
| `youtube-reauth-url` | Prints the URL that re-authorizes YouTube uploads. |
| `sync-files` | Makes the `served_files` table match the `files/` directory. |

`PORT` (default 8009), `FILES_DIR` (default `./files`), `VIDEOS_DIR` and the
`DATABASE_*` variables are read from the environment. With PgBouncer in
transaction mode, set `DATABASE_PREPARED_STATEMENTS=false`.

## Video summaries (X/Twitter and YouTube) → HTML

`POST /summarize-video` (bearer `API_TOKEN`, body `{ "url": "<x.com post or YouTube video url>" }`)
downloads a tweet's video and uploads it unlisted to YouTube for auto-captions (YouTube links skip straight to captions),
summarizes the transcript with `agy:gemini-3.8-flash-medium` through sota-llms, saves an HTML page, and pushes a link to
[rulinky](../rulinky). Poll `GET /summarize-video/:id` for status.

### YouTube API setup

1. Create a project at <https://console.cloud.google.com/>.
2. Enable the **YouTube Data API v3** (APIs & Services → Library).
3. Configure the **OAuth consent screen** (External; add your Google account as
   a test user).
4. Create an **OAuth client ID** of type **Web application**, with the app's
   `/youtube/callback` URL registered as an authorized redirect URI. Copy the
   client ID and secret into the dotenv file as `YOUTUBE_CLIENT_ID` / `YOUTUBE_CLIENT_SECRET`.
5. Sign in to the app and open `/youtube/reauth` (or run
   `serve-html-markdown youtube-reauth-url` to print that URL) to authorize; the refresh
   token is stored in the database. See
   [`docs/configure-twitter-video-to-html.md`](docs/configure-twitter-video-to-html.md)
   for the full walkthrough, including publishing the OAuth app so tokens
   stop expiring after 7 days.

### rulinky + Slack

Set `RULINKY_HOST` and `RULINKY_API_TOKEN` (a rulinky user's API token), plus
`SLACK_SUCCESS_WEBHOOK` and `SLACK_FAILURE_WEBHOOK` incoming-webhook URLs.

## Authentication

Viewing served files requires signing in. Configure `ADMIN_EMAIL` and
`ADMIN_PASSWORD`, then run `serve-html-markdown seed-admin` to create or update the
admin user. API uploads remain authenticated separately with the `API_TOKEN`
bearer token.

## Run

```sh
./serve
```

The server listens on <http://localhost:8009>. `./serve` builds the binary,
migrates and (re)starts it in the `serve-html-markdown` tmux session.

## Usage

Put `.html`, `.md`, or `.markdown` files in `files/`, then open them by name:

```text
http://localhost:8009/example.html
http://localhost:8009/example.md
```

Create an OpenAI-formatted Markdown file with a bearer token:

```sh
curl -X POST http://localhost:8009/file/new \
  -H "Authorization: Bearer $API_TOKEN" \
  --data-urlencode "content=some text" \
  --data-urlencode "filename=note"
```

The response contains the public HTTPS URL. Existing names receive a numeric
suffix such as `note-1.md`.

## Test

```sh
go vet ./... && go test ./...
```

Tests that use the database need a Postgres they can recreate: set
`TEST_DATABASE_URL`, or the `DATABASE_*` variables with `APP_ENV=test`.
`script/parity.sh` compares a running Go server with a Rails one page by page;
it only makes sense against an old Rails checkout.

## Trust boundary

This application intentionally serves `.html` files and enables raw
HTML inside Markdown to preserve the original behavior. Served .html responses
have a small script tag injected before </body> to enable the text-expansion
feature, so they are no longer byte-for-byte verbatim. Only place trusted
content in `files/` and only share the upload bearer token with trusted clients.
