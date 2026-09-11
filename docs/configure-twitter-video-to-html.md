# Configuring YouTube env vars for the twitter-video → HTML pipeline

`.env.example` lists five YouTube-related vars. Two are required:

```dotenv
YOUTUBE_CLIENT_ID=
YOUTUBE_CLIENT_SECRET=
```

The other three are optional overrides: `YOUTUBE_REFRESH_TOKEN` (a bootstrap
fallback, see step 6), `YOUTUBE_REDIRECT_URI`, and `YOUTUBE_REAUTH_URL`.

`YoutubeUploader` (`app/services/youtube_uploader.rb`) authorizes a
`Google::Apis::YoutubeV3::YouTubeService` via a `Signet::OAuth2::Client`, using
a refresh token read from `YoutubeCredential.refresh_token` (see step 6), to
upload each downloaded tweet video as an **unlisted** YouTube video (so
auto-captions get generated).

Run `bin/rails db:migrate` before any of this — it creates the
`youtube_credentials` table that step 6 stores the token in.

## 1. Create a Google Cloud project

1. Go to <https://console.cloud.google.com/> and create a new project (or pick
   an existing one you're happy to use for this).

## 2. Enable the YouTube Data API v3

1. In the project, go to **APIs & Services → Library**.
2. Search for **YouTube Data API v3** and click **Enable**.

## 3. Configure the OAuth consent screen

1. **APIs & Services → OAuth consent screen**.
2. User type: **External** (Internal requires a Google Workspace org).
3. Fill in the required app info (name, support email). Scopes don't need to
   be added here — `YoutubeUploader` requests
   `https://www.googleapis.com/auth/youtube.upload` at authorization time.
4. Under **Test users**, add the Google account you'll actually upload videos
   from. While the app is in "Testing" publishing status, only accounts
   listed here can complete the OAuth flow. Testing is just the state you
   start in — step 5 below moves the app to "In production" once client
   credentials exist, and you still won't need to submit the app for
   verification.

## 4. Create OAuth client credentials

1. **APIs & Services → Credentials → Create Credentials → OAuth client ID**, or
   go straight to <https://console.cloud.google.com/auth/clients>.
2. Application type: **Web application**.
3. Under **Authorized redirect URIs** add both:
   - `https://serve.chiq.me/youtube/callback` — the real one.
   - `http://127.0.0.1:8009/youtube/callback` — loopback is exempt from
     Google's HTTPS rule, so the flow still works with the tunnel down.
4. Copy the **Client ID** and **Client secret** into `.env`:

   ```dotenv
   YOUTUBE_CLIENT_ID=your-client-id.apps.googleusercontent.com
   YOUTUBE_CLIENT_SECRET=your-client-secret
   ```

The redirect URI must match what the app sends byte-for-byte — no trailing
slash. The app builds it from `HOST` (override with `YOUTUBE_REDIRECT_URI`).

## 5. Publish the project so tokens stop expiring

An OAuth project whose publishing status is **Testing** issues refresh tokens
that die after 7 days. At <https://console.cloud.google.com/auth/audience>,
press **Publish app** so the status reads **In production**. Don't submit for
verification: unverified-in-production costs only the "Google hasn't verified
this app" screen (click **Advanced → Go to … (unsafe)**) and a 100-user cap,
neither of which matters for a single account.

## 6. Authorize

Open <https://serve.chiq.me/youtube/reauth> (sign in to the app first), approve
access, and the refresh token is stored in the `youtube_credentials` table.
There is nothing to copy into `.env` — `YOUTUBE_REFRESH_TOKEN` is now only a
bootstrap fallback for a checkout that has never authorized.

`rake youtube:refresh_token` just prints that URL.

When a token does die, the ingest job posts the link to Slack with the stalled
video's id, and finishing the flow re-queues that video automatically.

## Troubleshooting

- **`access_denied` in the browser** — before publishing (step 5), only
  accounts listed under **Test users** on the OAuth consent screen (step 3)
  can complete the flow. Add the account and retry. Once the app is
  published to "In production", this no longer applies.
- **Refresh token stops working after ~7 days** — apps in "Testing"
  publishing status get refresh tokens that expire after 7 days. Move the OAuth
  consent screen to "In production" (see step 5). No Google review is required
  merely to request the `youtube.upload` scope for your own test users, but
  check current Google policy — this can change.
- **`quotaExceeded` on upload** — the YouTube Data API v3 has a default daily
  quota (10,000 units/day); a single video insert costs 1,600 units, so this
  pipeline can upload roughly 6 videos/day before hitting the default quota.
  Request a quota increase in the Cloud Console if you need more.
