# YouTube Re-authorization Link Design

**Date:** 2026-09-11
**Status:** Approved design, pending implementation plan

## Goal

When a twitter-video ingest fails because the YouTube OAuth grant is dead, the
Slack failure message should carry a link that fixes the authorization in a
browser and resumes the stalled video — instead of today's raw exception dump
that leaves no path forward.

Today's message:

```
[twitter-video #2] ingest failed: Google::Auth::AuthorizationError: Authorization failed.  Server message:
{
  "error": "invalid_grant",
  "error_description": "Bad Request"
}
```

Target message:

```
[twitter-video #2] ingest failed: YouTube authorization expired (invalid_grant).
re-authorize: https://serve.chiq.me/youtube/reauth?video_id=2
the video is already downloaded — the upload retries by itself once you're done.
```

## Background

`YoutubeUploader#build_service` (`app/services/youtube_uploader.rb`) calls
`authorizer.fetch_access_token!` on a `Signet::OAuth2::Client`. When the refresh
token is dead, googleauth's Signet wrapper (`googleauth/signet.rb:127`) converts
`Signet::AuthorizationError` into `Google::Auth::AuthorizationError`, which is a
*subclass* of `Signet::AuthorizationError` (`googleauth/errors.rb:91`). The
uploader only rescues `Google::Apis::Error`, so the error escapes untranslated to
`TwitterVideoIngestJob`'s generic `rescue StandardError`, which posts
`error.class: error.message` to Slack.

`YoutubeUploader` is the only OAuth consumer; `TwitterVideoCaptionsJob` does not
touch it.

The root cause of the recurring failure was the project's OAuth publishing status
("Testing" issues refresh tokens that expire after 7 days). That has been fixed
out-of-band by publishing the project to **In production** and registering a Web
application client with `https://serve.chiq.me/youtube/callback` as an authorized
redirect URI. This design assumes that console work is done.

## Non-goals

- No new Slack formatting/blocks. Plain text, as today.
- No token encryption. The refresh token currently lives in plaintext in `.env`;
  a plaintext DB column is the same trust level, and this app has no
  `master.key` or ActiveRecord encryption setup to build on.
- No multi-account or multi-channel support. One YouTube identity, one row.
- No automatic retry loop. The job retries once, when the human finishes the
  browser flow.

## Components

### 1. `youtube_credentials` table + `YoutubeCredential` model

Single-row store so a new token takes effect without editing `.env` or
restarting the server.

| column | type | notes |
| --- | --- | --- |
| `refresh_token` | text, not null | plaintext, same sensitivity as `.env` |
| `obtained_at` | datetime, not null | when the human last completed consent |

Model API:

- `.refresh_token` — the stored row's token, falling back to
  `ENV["YOUTUBE_REFRESH_TOKEN"]` when no row exists (bootstrap path, and keeps
  existing deployments working before the first re-auth).
- `.store!(token)` — updates the single row or creates it; never accumulates
  rows.

### 2. `YoutubeAuthorization` service

Owns the OAuth dance and nothing else, so it is unit-testable without a browser.

- `REDIRECT_URI` — `ENV.fetch("YOUTUBE_REDIRECT_URI") { "https://#{ENV.fetch("HOST", "localhost:8009")}/youtube/callback" }`.
  Built from `HOST` (defaulted the way `files_controller.rb:97` and
  `twitter_video_captions_job.rb:73` already default it, so the test env needs no
  new variable), **not** from `request.base_url`: the public tunnel forwards
  as plain HTTP to port 8009, so a request-derived URI would be `http://…` and
  Google would reject it as a redirect-URI mismatch.
- `#consent_url(state:)` — Google's authorization endpoint with
  `access_type=offline`, `prompt=consent` (without it Google omits the refresh
  token on re-consent), the `youtube.upload` scope reused from
  `YoutubeUploader::SCOPE`, and the caller's `state`.
- `#exchange!(code:)` — exchanges the code and returns the refresh token;
  raises `YoutubeAuthorization::Error` if the response carries none.
- Raises `YoutubeAuthorization::ConfigurationError` naming the missing variable
  when `YOUTUBE_CLIENT_ID` / `YOUTUBE_CLIENT_SECRET` are blank.

### 3. `YoutubeAuthorizationsController`

Inherits `ApplicationController`, so Devise's `authenticate_user!` guards both
actions — the routes are publicly reachable at `serve.chiq.me` and must not be.

- `GET /youtube/reauth` — generates `state = SecureRandom.hex(16)` into the
  session along with `params[:video_id]`, then
  `redirect_to consent_url, allow_other_host: true`.
- `GET /youtube/callback` — in order: render Google's `params[:error]` if the
  user denied; compare `params[:state]` to the session value with
  `ActiveSupport::SecurityUtils.secure_compare` and return `400` on mismatch
  without writing anything; exchange the code; `YoutubeCredential.store!`;
  re-enqueue `TwitterVideoIngestJob` for the stashed `video_id` when that video
  exists; clear the session keys; render a small success page naming what
  happened.

Routes go above the `get "/:file_name"` catch-all in `config/routes.rb`.

### 4. `YoutubeUploader` — typed error

- New `AuthorizationExpired < Error`.
- `build_service` rescues `Signet::AuthorizationError` (which covers
  `Google::Auth::AuthorizationError`) and re-raises `AuthorizationExpired`,
  keeping Google's server text in the message.
- The constructor's blank-credential guard splits: a blank `refresh_token`
  raises `AuthorizationExpired` (that is the state between a client swap and the
  first re-auth, and it is fixed by the same link); a blank client id or secret
  stays a plain `Error`, since no link can fix a missing env var.

### 5. `TwitterVideoIngestJob` — the Slack link

- The `uploader` lambda reads `YoutubeCredential.refresh_token` instead of
  `ENV["YOUTUBE_REFRESH_TOKEN"]`.
- `REAUTH_URL` — `ENV.fetch("YOUTUBE_REAUTH_URL") { "https://#{ENV.fetch("HOST", "localhost:8009")}/youtube/reauth" }`.
- The `rescue` branches on `error.is_a?(YoutubeUploader::AuthorizationExpired)`:
  that case posts the target message above with `?video_id=` appended; every
  other failure keeps today's text verbatim.

### 6. `lib/tasks/youtube.rake`

Its `urn:ietf:wg:oauth:2.0:oob` flow is dead — Google blocked the out-of-band
redirect in 2022, and the browser flow supersedes it. Reduce the task to
printing the re-auth URL so existing muscle memory and docs still land
somewhere useful.

### 7. Docs

`docs/configure-twitter-video-to-html.md`: replace the rake-task section with
the browser flow, record the Web application client and the
`https://serve.chiq.me/youtube/callback` redirect URI, and state that the
project is In production so refresh tokens no longer expire after 7 days (the
"unverified app" warning screen and the 100-user cap remain, both harmless
here).

## Data flow

```
ingest job → YoutubeUploader → Signet refresh fails
  → AuthorizationExpired
  → video.fail! + Slack "re-authorize: …/youtube/reauth?video_id=N"
  → human clicks (Devise sign-in, returns via user_return_to)
  → /youtube/reauth → Google consent → /youtube/callback?code=&state=
  → YoutubeCredential.store! → TwitterVideoIngestJob.perform_later(N)
  → reuse_existing finds the mp4 on disk (no re-download)
  → upload succeeds → success Slack with the Studio link
```

## Error handling

| Case | Behavior |
| --- | --- |
| Consent denied | Success page replaced by Google's `error` value; nothing stored |
| `state` mismatch or absent | `400`, nothing stored, nothing enqueued |
| Response has no refresh token | `YoutubeAuthorization::Error`; nothing stored |
| `YOUTUBE_CLIENT_ID`/`SECRET` blank | Error page naming the missing variable |
| `video_id` missing or video deleted | Token still stored; no job enqueued; page says so |
| Slack webhook down | Unchanged — `SlackNotifier` already swallows and logs |

## Testing

Minitest, following the existing fake-collaborator style in
`test/jobs/twitter_video_ingest_job_test.rb`.

- `YoutubeUploader`: an authorizer raising `Signet::AuthorizationError` produces
  `AuthorizationExpired`; blank refresh token produces `AuthorizationExpired`;
  blank client id still produces `Error`.
- `TwitterVideoIngestJob`: an uploader raising `AuthorizationExpired` posts a
  failure containing `/youtube/reauth?video_id=<id>`; a `YtdlpClient::Error`
  keeps the existing message and does *not* contain a re-auth link.
- `YoutubeCredential`: DB row wins over ENV; ENV used when no row; `.store!`
  twice leaves one row with the newer token.
- `YoutubeAuthorization`: `consent_url` contains `access_type=offline`,
  `prompt=consent`, the upload scope, the configured redirect URI and the state;
  `exchange!` returns the refresh token and raises when the response has none.
- Integration: unauthenticated `/youtube/reauth` redirects to sign-in; signed-in
  redirects to `accounts.google.com` and sets session state; callback with a bad
  state returns 400 and writes nothing; a good callback stores the token and
  enqueues `TwitterVideoIngestJob` with the right id.

## Open risks

- The redirect URI must match the registered one byte-for-byte. A trailing slash
  or a `HOST` value carrying a scheme breaks the flow with Google's
  `redirect_uri_mismatch`. The constant is derived once, in one place, so there
  is a single thing to correct.
- Devise's return-to only survives if the sign-in happens in the same session as
  the click; if it drops the user at `/`, re-clicking the Slack link while
  signed in works.
