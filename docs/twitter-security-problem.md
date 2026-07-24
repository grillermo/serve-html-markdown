# Twitter-video pipeline: unauthenticated status endpoint can leak internal error strings

**Status:** open, accepted as-is for now. Documented per human decision on 2026-07-24; not fixed in the initial implementation.

## The chain

1. **`GET /twitter-video/:id` has no auth.** (`app/controllers/twitter_videos_controller.rb`) Unlike every other content-serving action in this app (`FilesController` requires a Devise login on `show`/`index`/`last`; `create` here requires the `API_TOKEN` bearer), `show` on `TwitterVideosController` is open to anyone who can guess or enumerate a sequential integer id. It returns `{id, status, error_detail, html_filename, youtube_id}`.

2. **`error_detail` can carry an unrescued upstream error message.** `YoutubeUploader#upload` (`app/services/youtube_uploader.rb`) only rescues `Google::Apis::Error`. Its lazily-built `service` calls `Signet::OAuth2::Client#fetch_access_token!` during OAuth refresh, which can raise `Signet::AuthorizationError` or a Faraday connection error — neither is caught, so it propagates unwrapped up through the job and into `TwitterVideo#error_detail` via `fail!`.

3. **A similar gap exists in `GeminiSummarizer#summarize`** (`app/services/gemini_summarizer.rb`): the outer `JSON.parse(response.body)` that parses Gemini's response envelope sits outside the `rescue JSON::ParserError` that only guards the inner model-JSON parse. A malformed 200 response would raise a raw `JSON::ParserError`, whose message can include a fragment of the response body — the same class of leak the plan's own global constraint ("never leak upstream response bodies into error messages") was meant to prevent. This mirrors a pre-existing, already-accepted gap in `GeminiFormatter#format`.

Put together: a credential failure or a malformed upstream response can end up as a raw exception message in `error_detail`, and that field is readable by anyone who can guess a `TwitterVideo` id, with no auth required.

## Why this shipped as-is

All three gaps are **plan-mandated** — they're exactly what `docs/superpowers/plans/2026-07-23-twitter-video-to-html.md` specifies verbatim (Task 8's error rescue, Task 11's route auth, Task 5's parse rescue). Every implementing task and per-task review flagged this correctly; nothing here is an implementer deviation from the plan. The final whole-branch review flagged the chain as the single highest-value fix before merge, but the call on whether/how to close it is a product decision (how sensitive is `error_detail` really, is this endpoint meant to be public, is the tool low-traffic/internal-only), not a code-correctness one — so it's logged here instead of auto-fixed.

## If/when this gets fixed

Cheapest correct fix, in order of value:
1. Wrap `Signet::AuthorizationError` and Faraday errors in `YoutubeUploader::Error` (mirrors the existing `Google::Apis::Error` rescue in `app/services/youtube_uploader.rb`).
2. Wrap the outer `JSON.parse(response.body)` in `GeminiSummarizer#summarize` with the same generic-error rescue that already guards the inner parse.
3. Either require the existing `API_TOKEN` bearer auth on `GET /twitter-video/:id` (matching `create`), or keep it open but replace `error_detail` in the response with a generic string, logging the real detail server-side only.

## Related, lower-severity findings (same final review, not blocking)

- `SlackNotifier` swallows non-2xx webhook responses silently — only exceptions are logged (`app/services/slack_notifier.rb`).
- `TwitterVideoIngestJob` could mark an already-successfully-uploaded video `failed` if the caption-job enqueue itself raises after upload succeeds (`app/jobs/twitter_video_ingest_job.rb`).
- `TwitterVideoCaptionsJob`'s `rescue StandardError` failure path (summarizer/writer/rulinky raising) has zero test coverage.
- If `RulinkyClient#create_link` raises after `ServedHtmlWriter.write` already succeeded, the served HTML file and its `ServedFile` DB row persist as an orphan with no back-reference.
- `TwitterVideo.create!` in the controller is unguarded against `ActiveRecord::RecordInvalid` (would 500 instead of a handled 4xx) — effectively unreachable today since `TwitterUrl.normalize` validates first.

Full per-task review detail: `.superpowers/sdd/progress.md`.
