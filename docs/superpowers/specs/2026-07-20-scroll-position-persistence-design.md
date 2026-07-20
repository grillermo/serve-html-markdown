# Scroll Position Persistence — Design

Date: 2026-07-20
Status: Approved by user

## Overview

Every served document (`.md`, `.markdown`, `.html`, including generated
expansion pages) remembers where the current user last scrolled to, and
restores it automatically on the next load. Position is stored server-side,
keyed by `(user, file_name)`, so it persists across devices/browsers for the
same logged-in user.

## Data model

New table `scroll_positions`:

```ruby
create_table :scroll_positions do |t|
  t.references :user, null: false, foreign_key: true
  t.string :file_name, null: false
  t.float :position, null: false
  t.timestamps
end
add_index :scroll_positions, [:user_id, :file_name], unique: true
```

`ScrollPosition` model: `belongs_to :user`; validates `file_name` present,
`position` numeric and `0.0..1.0`. `User` gets `has_many :scroll_positions,
dependent: :destroy`.

`position` is a **fraction of scrollable height** (`0.0` = top, `1.0` =
bottom), not a raw pixel offset. Pixels drift when a document is edited
(e.g. an expansion link rewrites a paragraph) or when web fonts/images
change layout height between visits; a fraction degrades gracefully instead
of landing at a wrong or out-of-bounds offset.

The table is keyed by `file_name` only — it does not resolve or validate
the file exists (unlike `ResolvesServedFiles`). Scroll position is opaque
per-key storage; no need to read the file to store or restore a number.

## Backend endpoint — `PATCH /scroll_position`

New `ScrollPositionsController`, behind the app's default
`authenticate_user!` and standard CSRF protection (no `skip_forgery_protection`,
unlike `FilesController#create`).

Params: `file_name` (string, required), `position` (float, required).

```ruby
def update
  file_name = params[:file_name].to_s
  position = params[:position]
  raise ActionController::BadRequest, "Missing file_name." if file_name.blank?

  clamped = position.to_f.clamp(0.0, 1.0)
  record = current_user.scroll_positions.find_or_initialize_by(file_name: file_name)
  record.update!(position: clamped)
  head :no_content
end
```

`rescue_from ActionController::BadRequest` → 400, same pattern as the other
two controllers.

## Restore path

`FilesController#show` looks up the saved position for the current user and
this file before rendering:

```ruby
scroll_position = current_user.scroll_positions.find_by(file_name: file_path.basename.to_s)&.position
```

The value is embedded as an inline script right next to the existing CSRF
meta tag / `expand.js` script tag, so both delivery paths (markdown layout,
raw `.html` injection) share one code path:

```html
<script>window.__scrollPosition = 0.42;</script>
```

Omitted entirely when there's no saved record (JS treats `undefined` as "no
restore").

- **Markdown pages** (`app/views/layouts/markdown.html.erb`): add the
  `<script>` tag alongside the existing `csrf_meta_tags` /
  `javascript_include_tag "expand"`, driven by an instance variable
  (`@scroll_position`) set in the controller.
- **HTML files**: extend the existing `inject_expand_script` snippet in
  `FilesController` to include the scroll-position script when present,
  inserted at the same point (before `</body>`, or appended if absent).

## Frontend — `app/assets/javascripts/expand.js`

Extends the existing file (already loaded on every served page); no new
asset.

**Restore**, on `DOMContentLoaded`:

```js
if (typeof window.__scrollPosition === "number") {
  const max = document.documentElement.scrollHeight - window.innerHeight;
  if (max > 0) {
    window.scrollTo(0, window.__scrollPosition * max);
  }
}
```

No smooth-scroll — instant jump before first paint settles, to avoid a
visible scroll animation on every load.

**Save**, on `scroll` (debounced ~500ms) and on `pagehide`:

```js
function currentFraction() {
  const max = document.documentElement.scrollHeight - window.innerHeight;
  return max > 0 ? window.scrollY / max : null;
}

function saveScrollPosition() {
  const fraction = currentFraction();
  if (fraction === null) return; // page doesn't scroll, nothing meaningful to store

  const body = JSON.stringify({
    file_name: decodeURIComponent(location.pathname.slice(1)),
    position: fraction
  });

  if (navigator.sendBeacon) {
    const blob = new Blob([body], { type: "application/json" });
    navigator.sendBeacon(`/scroll_position?authenticity_token=${encodeURIComponent(CSRF())}`, blob);
  } else {
    fetch("/scroll_position", {
      method: "PATCH",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": CSRF() },
      body,
      keepalive: true
    });
  }
}
```

`sendBeacon` only does `POST`, so the beacon path uses
`POST /scroll_position` with `_method=patch` override (Rails' standard
`ActionDispatch::Request::HTTP_METHODS` override via
`?_method=patch` query param) rather than a real `PATCH` — Rails' method
override middleware handles this natively. Debounced scroll listener and
`pagehide` listener both call `saveScrollPosition`; `pagehide` additionally
cancels any pending debounce timer first so it doesn't double-fire.

Route: `patch "/scroll_position", to: "scroll_positions#update"` (Rails
already routes `POST … ?_method=patch` to a `patch` action via the built-in
method-override middleware, so no separate `post` route is needed).

## Edge cases

- No saved position for this user+file → `window.__scrollPosition` omitted,
  restore is a no-op.
- Document too short to scroll (`scrollHeight <= innerHeight`) → save is
  skipped (nothing meaningful to persist); restore's `max > 0` guard makes
  it a no-op too, so no divide-by-zero either direction.
- Stored position from a since-shortened document → still `0.0..1.0` by
  construction (validated at write time), so restore math stays in bounds
  even though the exact visual spot may have shifted.
- Unauthenticated request to `/scroll_position` → redirected/401 by the
  existing default `authenticate_user!`, same as every other route.
- Rapid navigation away before the debounce fires → `pagehide` covers it.

## Testing

- **Model** (`test/models/scroll_position_test.rb`): validates `position`
  rejects values outside `0.0..1.0` (the controller clamps before saving,
  but the model validation is the actual safety net and is tested directly,
  independent of the controller); uniqueness scoped to `[user_id, file_name]`.
- **Controller** (`test/controllers/scroll_positions_controller_test.rb`):
  first save creates a record; second save for same user+file upserts
  rather than duplicating; position outside `0..1` gets clamped; missing
  `file_name` → 400; unauthenticated → redirected.
- **Files controller** (`test/controllers/files_controller_test.rb`):
  `window.__scrollPosition` script present when a record exists for
  `current_user` + the requested file, for both `.md` and `.html` files;
  absent when no record exists.
- JS untested (no JS test infra in repo, matches existing `expand.js`
  precedent).

## Known trade-offs (accepted)

- Fraction-based position means a document edited between visits (e.g. an
  expansion link inserted mid-page) restores to an approximately-right
  spot, not an exactly-right one. Accepted — same spirit as the existing
  occurrence-index trade-off in text expansion.
- `sendBeacon` requests aren't observable/debuggable the way a normal
  `fetch` is (no response handling); acceptable since save is fire-and-forget
  and a missed beacon just means next load doesn't restore as precisely.
- No cleanup job for `scroll_positions` rows whose `file_name` no longer
  exists on disk (file deleted/renamed). Rows are cheap and inert; out of
  scope for v1.

## Out of scope

- Cross-user shared scroll position.
- Scroll position for anchors/headings (semantic position) instead of raw
  fraction.
- Backfilling or migrating positions when a file is renamed.
