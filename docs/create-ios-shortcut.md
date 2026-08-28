# iOS Shortcut: send a tweet to the twitter-video → HTML pipeline

This walks through building a Shortcuts.app shortcut that appears in the
iOS **Share Sheet** when you're viewing a tweet/X post — tap it, and it POSTs
the tweet URL to this app's `POST /twitter-video` endpoint, kicking off the
download → YouTube upload → summarize → publish pipeline.

## What you'll need first

- Your server's public URL (e.g. `https://your-app.example.com`) — must be
  reachable from your phone (i.e. deployed, or on the same network/tunnel if
  running locally).
- Your `API_TOKEN` value from `.env` — the shortcut needs it as a bearer
  token.

## Steps

1. Open **Shortcuts.app** on iOS.
2. Tap **+** (top right) to create a new shortcut.
3. Tap **Add Action**, search for **Receive**, and add **Receive Input from
   Share Sheet**.
   - Set it to accept **URLs** and **Text** (a shared tweet can arrive as
     either, depending on how you invoked share).
4. Tap **Add Action** again, search for **If**, and add an **If** block:
   - Condition: `Shortcut Input` **is not** *empty* — this just guards
     against running with no input. (Optional but recommended.)
5. Inside the `If`, tap **Add Action**, search for **Text**, and add a
   **Text** action. Set its value to `Shortcut Input` (tap the input field,
   then pick the magic-variable "Shortcut Input" from the popup) — this
   normalizes whatever you received into a plain string you can reference
   below.
6. Tap **Add Action**, search for **Get Contents of URL**, and add it.
   - **URL**: `https://your-app.example.com/twitter-video` (replace with your
     actual host from `HOST`/your deploy URL).
   - Tap **Show More** to expand options:
     - **Method**: `POST`
     - **Headers**: add one header:
       - Key: `Authorization`
       - Value: `Bearer YOUR_API_TOKEN` (paste your actual `API_TOKEN` value
         after `Bearer `)
     - **Request Body**: `JSON`
       - Add a field with key `url`, and for its value tap the field and
         select the **Text** variable from step 5 (the normalized tweet URL).
7. (Optional) Tap **Add Action**, search for **Show Notification**, and add
   it after the `Get Contents of URL` step to confirm the request was sent —
   set its text to something like `Sent to twitter-video pipeline`.
8. Tap the shortcut's name at the top (default "New Shortcut") and rename it
   to something short, e.g. **Twitter → HTML**.
9. Tap the settings icon (below the name) and enable **Show in Share Sheet**.
   Under **Share Sheet Types**, make sure **URLs** (and **Text**, if you
   enabled it in step 3) are checked.
10. Tap **Done**.

## Using it

1. Open the X/Twitter app (or Safari) on a tweet with a video.
2. Tap **Share**.
3. Scroll the Share Sheet and tap your **Twitter → HTML** shortcut.
4. It POSTs the tweet URL to `/twitter-video`, which responds `202 Accepted`
   with `{ "id": <video_id>, "status": "downloading" }`. The pipeline then
   runs asynchronously — download, YouTube upload, caption polling,
   summarize, publish, and a link pushed to rulinky, with progress posted to
   your configured Slack webhooks.

## Checking status later (optional)

`GET /twitter-video/:id` returns the current status (no auth required — see
`docs/twitter-security-problem.md` for why that's currently unauthenticated).
You can add a second shortcut, or extend this one, to poll that endpoint
using the `id` from the first response if you want an in-app status check
instead of relying on Slack notifications.

## Troubleshooting

- **401 Unauthorized** — the `Authorization` header value doesn't exactly
  match `Bearer <API_TOKEN>` (check for extra spaces, or that `.env`'s
  `API_TOKEN` matches what you pasted into the shortcut).
- **400 Bad Request** — the shared URL wasn't a valid `x.com`/`twitter.com`
  status URL (`TwitterUrl.normalize` rejects anything without `/status/<id>`
  in the path).
- **Shortcut doesn't appear in the X app's Share Sheet** — X's own share
  sheet sometimes needs "More" tapped to reveal all extensions/shortcuts;
  also confirm step 9 (Share Sheet Types) includes whatever content type X
  is actually sharing (check with **Show Notification** temporarily set to
  show `Shortcut Input` to see what X sends).
- **Nothing happens / silent failure** — Shortcuts silently swallows network
  errors unless you add error handling; temporarily add a **Show Result**
  action right after **Get Contents of URL** to see the raw JSON response
  and HTTP status while debugging.
