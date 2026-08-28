# Serve HTML & Markdown with Rails

A minimal Rails 8 application that serves trusted HTML and Markdown files from
the `files/` directory.

- `GET /name.html` serves HTML with a small expansion script injected.
- Select text on any page to queue a linked AI answer page. Multiple expansions run in the background; fixed status bars remain only while the current page is open (requires the `claude` CLI; falls back to `codex`).
- `GET /name.md` and `GET /name.markdown` render Markdown with a dark theme.
- `GET /` and `GET /last` redirect to the most recently modified supported file.
- `HEAD /health` returns `200 OK`.
- `POST /file/new` formats content with Gemini and saves it as Markdown.

## Requirements

- Ruby 3.4.7
- Bundler

Rails 8.1.3 and all application dependencies are pinned in `Gemfile.lock`.

## Setup

```sh
bundle install
cp .env.example .env
```

Set these values in `.env`:

```dotenv
API_TOKEN=replace-with-a-long-random-token
GEMINI_API_KEY=replace-with-your-gemini-api-key
HOST=example.com
```

The `.env` file and served files are ignored by Git.

## Twitter video → HTML

`POST /twitter-video` (bearer `API_TOKEN`, body `{ "url": "<x.com status url>" }`)
downloads the tweet's video, uploads it unlisted to YouTube for auto-captions,
summarizes the transcript with Gemini, saves an HTML page, and pushes a link to
[rulinky](../rulinky). Poll `GET /twitter-video/:id` for status.

Requires `yt-dlp` on `PATH` (or set `YTDLP_BIN`).

### YouTube API setup

1. Create a project at <https://console.cloud.google.com/>.
2. Enable the **YouTube Data API v3** (APIs & Services → Library).
3. Configure the **OAuth consent screen** (External; add your Google account as
   a test user).
4. Create an **OAuth client ID** of type **Desktop app**. Copy the client ID and
   secret into `.env` as `YOUTUBE_CLIENT_ID` / `YOUTUBE_CLIENT_SECRET`.
5. Run `rake youtube:refresh_token`, open the printed URL, approve, paste the
   code back. Copy the printed `YOUTUBE_REFRESH_TOKEN` into `.env`.

### rulinky + Slack

Set `RULINKY_HOST` and `RULINKY_API_TOKEN` (a rulinky user's API token), plus
`SLACK_SUCCESS_WEBHOOK` and `SLACK_FAILURE_WEBHOOK` incoming-webhook URLs.

## Authentication

Viewing served files requires signing in. Configure `ADMIN_EMAIL` and
`ADMIN_PASSWORD`, then run `bin/rails db:seed` to create or update the admin
user. Configure the database with the `DATABASE_*` environment variables.
API uploads remain authenticated separately with the `API_TOKEN` bearer token.

## Run

```sh
./serve
```

The server listens on <http://localhost:8009>.

You can also run it directly:

```sh
bin/rails server -p 8009
```

## Usage

Put `.html`, `.md`, or `.markdown` files in `files/`, then open them by name:

```text
http://localhost:8009/example.html
http://localhost:8009/example.md
```

Create a Gemini-formatted Markdown file with a bearer token:

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
bin/rails test
```

## Trust boundary

This application intentionally serves `.html` files and enables raw
HTML inside Markdown to preserve the original behavior. Served .html responses
have a small script tag injected before </body> to enable the text-expansion
feature, so they are no longer byte-for-byte verbatim. Only place trusted
content in `files/` and only share the upload bearer token with trusted clients.
