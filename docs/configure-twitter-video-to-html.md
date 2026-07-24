# Configuring YouTube env vars for the twitter-video → HTML pipeline

`.env.example` lists three YouTube-related vars the pipeline needs:

```dotenv
YOUTUBE_CLIENT_ID=
YOUTUBE_CLIENT_SECRET=
YOUTUBE_REFRESH_TOKEN=
```

They're consumed by `YoutubeUploader` (`app/services/youtube_uploader.rb`), which
authorizes a `Google::Apis::YoutubeV3::YouTubeService` via a
`Signet::OAuth2::Client` refresh-token flow to upload each downloaded tweet
video as an **unlisted** YouTube video (so auto-captions get generated).

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
   listed here can complete the OAuth flow — this is normal and fine for a
   single-account pipeline; you don't need to submit the app for
   verification.

## 4. Create OAuth client credentials

1. **APIs & Services → Credentials → Create Credentials → OAuth client ID**.
2. Application type: **Desktop app**.
3. Name it anything (e.g. "twitter-video pipeline").
4. Copy the generated **Client ID** and **Client secret** into `.env`:

   ```dotenv
   YOUTUBE_CLIENT_ID=your-client-id.apps.googleusercontent.com
   YOUTUBE_CLIENT_SECRET=your-client-secret
   ```

## 5. Obtain a refresh token

The repo ships a rake task for this (`lib/tasks/youtube.rake`):

```bash
rake youtube:refresh_token
```

It will:

1. Print a Google consent URL — open it in a browser signed into the test-user
   account from step 3.
2. Approve access (you'll see a warning that the app is unverified — this is
   expected for a "Testing"-status app; click through **Advanced → Go to
   [app name] (unsafe)**).
3. Google shows you an authorization code. Paste it back into the terminal
   prompt.
4. The task exchanges the code for tokens and prints:

   ```
   YOUTUBE_REFRESH_TOKEN=1//0g...
   ```

Copy that value into `.env`.

## Done

With all three vars set, `YoutubeUploader.new` (built by
`TwitterVideoIngestJob` from `ENV["YOUTUBE_CLIENT_ID"]`,
`ENV["YOUTUBE_CLIENT_SECRET"]`, `ENV["YOUTUBE_REFRESH_TOKEN"]`) can silently
refresh its access token on every upload — no further manual steps needed
unless the refresh token is revoked (see Troubleshooting).

## Troubleshooting

- **`invalid_grant` when running the rake task** — the authorization code was
  already used, expired (they're short-lived), or was copied with extra
  whitespace. Re-run `rake youtube:refresh_token` and paste the fresh code
  immediately.
- **`access_denied` in the browser** — the Google account isn't listed under
  **Test users** on the OAuth consent screen (step 3). Add it and retry.
- **Refresh token stops working after ~7 days** — apps in "Testing"
  publishing status get refresh tokens that expire after 7 days. Either
  re-run `rake youtube:refresh_token` periodically, or move the OAuth consent
  screen to "In production" (no Google review is required merely to request
  the `youtube.upload` scope for your own test users, but check current
  Google policy — this can change).
- **`quotaExceeded` on upload** — the YouTube Data API v3 has a default daily
  quota (10,000 units/day); a single video insert costs 1,600 units, so this
  pipeline can upload roughly 6 videos/day before hitting the default quota.
  Request a quota increase in the Cloud Console if you need more.
