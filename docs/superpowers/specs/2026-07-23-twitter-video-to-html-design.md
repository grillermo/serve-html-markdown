# Twitter Video → HTML Design

**Date:** 2026-07-23
**Status:** Approved design, pending implementation plan

## Goal

Add a feature that takes a Twitter/X video URL, downloads it, uploads it to
YouTube (unlisted) to obtain auto-generated captions, summarizes the transcript
with Gemini, saves the summary as a served HTML page, and pushes a link to the
summary page into the [rulinky](../../../../rulinky) link-saver app. Every
pipeline stage reports success or failure to Slack.

The YouTube round-trip exists solely to get captions: Twitter videos carry no
subtitles, YouTube auto-generates them for uploaded videos.

## Non-goals

- No UI. This is an API-triggered pipeline (like the existing `/file/new`).
- No ffmpeg normalization. YouTube re-encodes on upload, so the iOS-compat
  transcode that patatatube does is unnecessary here.
- No caption translation or multi-language handling. First available
  auto-caption track (English preferred) is used.

## Pipeline stages

1. **Download** — shell out to `yt-dlp` against the Twitter/X status URL,
   producing a local `mp4` under `tmp/`. Capture tweet title/author from
   yt-dlp metadata (`--print`) for the YouTube video title.
2. **Upload** — `google-apis-youtube_v3` gem, OAuth refresh-token flow,
   resumable upload as **unlisted**. Video title = tweet text/author from
   step 1. Store `youtube_id`. Delete the local mp4 after upload.
3. **Await captions** — self-rescheduling job. At each checkpoint, run
   `yt-dlp --write-auto-subs --skip-download` against the uploaded YouTube
   video; if an auto-caption VTT track exists, download it and continue.
   Checkpoints are **elapsed time from upload completion**: +5, +10, +20,
   +30, +40, +60, +120 minutes (7 attempts). Give up and fail at +120 min.
4. **Summarize** — `gemini-flash-lite-latest` receives the transcript text and
   returns a summary plus a title. New `GeminiSummarizer` service, sibling to
   the existing `GeminiFormatter`.
5. **Save HTML** — build an HTML page (title + formatted summary), write to
   `files/<slug-from-title>.html`, and call `ServedFile.record`. Reuse the
   collision-avoidance / slug logic from `FilesController#unique_file_path`
   (extracted to a shared helper, targeting `.html` instead of `.md`).
6. **Push to rulinky** — `POST {RULINKY_HOST}/api/links` with
   `link = https://{HOST}/<file>.html`, `note = title`, bearer
   `RULINKY_API_TOKEN`. Store returned rulinky link id.

## State — new `twitter_videos` table

One row per ingest request, tracking the pipeline:

| column            | type    | notes                                        |
|-------------------|---------|----------------------------------------------|
| id                | pk      |                                              |
| source_url        | string  | original Twitter/X URL                       |
| status            | string  | enum, see below                              |
| youtube_id        | string  | null until upload done                       |
| youtube_title     | string  | tweet text/author                            |
| caption_attempts  | integer | count of checkpoints tried                   |
| upload_completed_at | datetime | anchor for checkpoint elapsed-time math    |
| html_filename     | string  | null until HTML saved                        |
| rulinky_link_id   | string  | null until pushed                            |
| error_detail      | text    | populated on failure                         |
| created_at / updated_at | datetime |                                        |

**Status enum:** `downloading → uploading → awaiting_captions →
summarizing → publishing → done`, plus terminal `failed`.

Enables a `GET /twitter-video/:id` status endpoint, resumability, and retry
visibility.

## Jobs

ActiveJob **AsyncAdapter** (in-process, concurrent-ruby) is the existing queue.
It supports scheduled enqueue (`set(wait:).perform_later`), which the caption
backoff relies on.

- **`TwitterVideoIngestJob`** — stages 1–2 (download + upload). On success sets
  `upload_completed_at`, status `awaiting_captions`, and enqueues the first
  caption poll with `set(wait: 5.minutes)`.
- **`TwitterVideoCaptionsJob`** — one checkpoint attempt.
  - Captions present → run stages 4–6 inline (or delegate to a
    `TwitterVideoPublishJob`), status → `done`.
  - Captions absent and more checkpoints remain → self-reschedule to the next
    checkpoint (`wait` = next elapsed offset minus time already elapsed).
  - Captions absent and at final checkpoint (+120 min) → status `failed`.

**Architectural choice (decided):** self-rescheduling caption job rather than a
single long-lived job that sleeps between checks. Reasons: keeps worker threads
free between checks, survives idle periods, and each attempt is independently
logged and observable via `caption_attempts`.

## Services

- **`GeminiSummarizer`** — new. Raw `Net::HTTP` like `GeminiFormatter`. Model
  `gemini-flash-lite-latest`. Input: transcript. Output: `{ title:, summary_html: }`.
- **`YoutubeUploader`** — wraps `google-apis-youtube_v3` + signet OAuth. Handles
  token refresh and resumable upload. Input: file path + title. Output:
  `youtube_id`.
- **`YtdlpClient`** — thin wrapper for the two yt-dlp invocations (download,
  caption fetch). Builds argv, runs, parses output. Configurable `YTDLP_BIN`.
- **`RulinkyClient`** — `POST /api/links` with bearer token. Input: link + note.
  Output: rulinky link id.
- **`SlackNotifier`** — posts to a webhook. `notify_success(stage, video)` and
  `notify_failure(stage, video, error)`. Two webhooks (success vs failure),
  configured via env. Called at the end of each stage (both jobs). Failures to
  post to Slack are logged and swallowed — Slack must never break the pipeline.

## Endpoint & auth

- **`POST /twitter-video`** — body `{ url: "<twitter status url>" }`. Reuses the
  `API_TOKEN` bearer auth (same pattern as `/file/new`). Validates the URL is an
  x.com/twitter status URL (port the `_normalize_twitter_url` logic from
  patatatube's router). Creates a `twitter_videos` row (status `downloading`)
  and enqueues `TwitterVideoIngestJob`. Returns `{ id, status }` (202).
- **`GET /twitter-video/:id`** — returns the row's status/error for polling.

## Configuration (new env vars)

| var                    | purpose                                            |
|------------------------|----------------------------------------------------|
| `YOUTUBE_CLIENT_ID`    | OAuth client id                                    |
| `YOUTUBE_CLIENT_SECRET`| OAuth client secret                                |
| `YOUTUBE_REFRESH_TOKEN`| long-lived refresh token for the uploading account |
| `RULINKY_API_TOKEN`    | bearer token for rulinky `/api/links`              |
| `RULINKY_HOST`         | e.g. `https://rulinky.example.com`                 |
| `YTDLP_BIN`            | yt-dlp path (default `/opt/homebrew/bin/yt-dlp`)   |
| `SLACK_SUCCESS_WEBHOOK`| Slack incoming webhook for per-stage success       |
| `SLACK_FAILURE_WEBHOOK`| Slack incoming webhook for per-stage failure       |

Slack webhook values provided by the user look like Slack's documentation
placeholder format; they go in env, never hardcoded.

## README additions

A **YouTube API setup** section:

1. Create a Google Cloud project.
2. Enable the **YouTube Data API v3**.
3. Configure the OAuth consent screen (external, add self as test user).
4. Create an **OAuth client ID** (Desktop app) → get client id + secret.
5. Run a one-time helper script (`bin/youtube_auth` or a rake task) that opens
   the consent URL, exchanges the auth code, and prints the **refresh token**.
6. Put client id, secret, and refresh token into `.env`.

Plus documentation of the new `/twitter-video` endpoint and the rulinky/Slack
env vars.

## Testing

- **Unit:**
  - `GeminiSummarizer` — stubbed HTTP, asserts request shape and title/summary
    parsing.
  - `YtdlpClient` — argv builders for download and caption fetch; output parsing.
  - Slug/title extraction and `.html` collision handling.
  - `RulinkyClient` — stubbed HTTP, asserts payload and bearer header.
  - `SlackNotifier` — stubbed HTTP; verifies success vs failure webhook routing
    and that a Slack post failure is swallowed.
- **Job:**
  - `TwitterVideoIngestJob` — state transitions, first caption poll scheduled.
  - `TwitterVideoCaptionsJob` — checkpoint backoff math (elapsed-time offsets),
    self-reschedule on miss, publish on hit, give-up at +120 min.
- **Request:**
  - `/twitter-video` auth (401 without token), URL validation, row creation +
    enqueue, 202 response.
  - `/twitter-video/:id` status read.
- Real yt-dlp, YouTube, Gemini, rulinky, and Slack are all stubbed in tests.

## Open risks

- YouTube auto-caption timing is unpredictable; +120 min cap may still miss very
  slow generations. Acceptable — failure is reported to Slack and the row is
  retriable.
- `google-apis-youtube_v3` + `signet` add dependency weight to an otherwise lean
  app. Justified by robust resumable upload + token refresh.
- yt-dlp fetching captions from the just-uploaded video assumes the caption
  track is publicly readable for an unlisted video (it is, for the owner's
  auto-captions via the watch URL). If this proves unreliable, fall back to the
  YouTube Data API `captions.download` (owner OAuth) — noted but not built.
