# Scroll Position Persistence — Design

Date: 2026-07-20
Status: Approved by user

## Overview

Every served document (`.md`, `.markdown`, `.html`, including generated
expansion pages) remembers where the current user last scrolled to, and
restores it automatically on the next load. Position is stored server-side,
keyed by `(user, file_name)`, so it persists across devices/browsers for the
same logged-in user.

Position is tracked as an **anchor**: the `id` of the topmost element
currently visible in the viewport, not a pixel offset or scroll-height
fraction. Restoring scrolls that element back to the top of the viewport.
This is more resilient to a document's content changing between visits
(e.g. an expansion link rewriting a paragraph above the fold) than any
purely numeric position, because the anchor identifies *what* the reader
was looking at, not *where on the page* it happened to be.

## Markdown heading IDs (prerequisite)

`FilesController::MARKDOWN_OPTIONS` does not currently enable comrak's
header-id extension, so rendered `.md`/`.markdown` headings have no `id`
attributes to anchor to. Add `header_ids: ""` to the `extension` options:

```ruby
MARKDOWN_OPTIONS = {
  render: { unsafe: true },
  extension: { autolink: true, header_ids: "" },
  parse: { smart: true }
}.freeze
```

Verified against the installed `commonmarker` 1.1.5 gem: this renders each
heading with a nested, empty, `aria-hidden` permalink anchor carrying the
slugged `id`, e.g.

```html
<h2><a href="#sub-heading" aria-hidden="true" class="anchor" id="sub-heading"></a>Sub Heading!</h2>
```

Duplicate heading text is de-duplicated (`sub-heading`, `sub-heading-1`,
`sub-heading-2`, …). This anchor tag's position in the DOM matches the
heading's top, which is what the client-side "topmost visible" scan reads.

Raw `.html` files are unaffected by this — they use whatever `id`
attributes their author already wrote, if any. A document with zero `id`
attributes anywhere (no headings, no other IDs) simply never has anything
to save an anchor for; see Edge cases.

## Data model

New table `scroll_positions`:

```ruby
create_table :scroll_positions do |t|
  t.references :user, null: false, foreign_key: true
  t.string :file_name, null: false
  t.string :anchor, null: false
  t.timestamps
end
add_index :scroll_positions, [:user_id, :file_name], unique: true
```

`ScrollPosition` model: `belongs_to :user`; validates `file_name` present;
validates `anchor` present **and** format-restricted to
`/\A[\w\-:.]+\z/` (word characters, hyphen, colon, period — covers every
comrak-generated slug and any reasonable hand-written HTML `id`). `User`
gets `has_many :scroll_positions, dependent: :destroy`.

The format restriction is a security control, not a UX one: `anchor` is
client-submitted and gets echoed back into an inline `<script>` tag on
every subsequent load of that document (see Restore path). Rejecting
anything outside a safe identifier charset at write time means no
quote/angle-bracket/backslash sequence a client sends can ever reach the
embed point — this is the actual defense, not the JSON-encoding used at
embed time (which is defense in depth, not the primary control).

The table is keyed by `file_name` only — it does not resolve or validate
the file exists (unlike `ResolvesServedFiles`). Scroll position is opaque
per-key storage; no need to read the file to store or restore an anchor.

## Backend endpoint — `PATCH /scroll_position`

New `ScrollPositionsController`, behind the app's default
`authenticate_user!` and standard CSRF protection (no `skip_forgery_protection`,
unlike `FilesController#create`).

Params: `file_name` (string, required), `anchor` (string, required).

```ruby
def update
  file_name = params[:file_name].to_s
  anchor = params[:anchor].to_s
  raise ActionController::BadRequest, "Missing file_name." if file_name.blank?
  raise ActionController::BadRequest, "Missing anchor." if anchor.blank?

  record = current_user.scroll_positions.find_or_initialize_by(file_name: file_name)
  record.update!(anchor: anchor)
  head :no_content
end
```

A request whose `anchor` fails the model's format validation (see Data
model) returns a validation error via `update!` → the controller should
rescue `ActiveRecord::RecordInvalid` and respond 422, since this is the one
case where the *value* (not just presence) determines validity — client JS
should never generate an anchor outside the safe charset in practice (it
only ever sends `id` attributes already present in the DOM), but a
malicious or buggy client could.

`rescue_from ActionController::BadRequest` → 400, same pattern as the other
two controllers. `rescue_from ActiveRecord::RecordInvalid` → 422.

## Restore path

`FilesController#show` looks up the saved anchor for the current user and
this file before rendering:

```ruby
scroll_anchor = current_user.scroll_positions.find_by(file_name: file_path.basename.to_s)&.anchor
```

The value is embedded as an inline script right next to the existing CSRF
meta tag / `expand.js` script tag, so both delivery paths (markdown layout,
raw `.html` injection) share one code path:

```html
<script>window.__scrollAnchor = "sub-heading";</script>
```

The value is JSON-encoded before interpolation (`anchor.to_json`), and
since it has already passed the model's `/\A[\w\-:.]+\z/` format
validation, it can never contain a `"`, `<`, `>`, `/`, or `\` that would
let it escape the string literal or the surrounding `<script>` tag —
JSON-encoding here is redundant-but-cheap defense in depth on top of that
validation, not the primary control.

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

**Finding the topmost visible anchor** — a scrollspy-style scan over every
`id`-bearing element in document order: the last one whose top is at or
above the viewport top is "topmost visible"; if none qualify (still above
the first anchor, e.g. at the very top of the document), there is nothing
to save.

```js
function topmostVisibleAnchor() {
  const anchored = document.querySelectorAll("[id]");
  let current = null;
  for (const el of anchored) {
    if (el.getBoundingClientRect().top <= 1) {
      current = el.id;
    } else {
      break;
    }
  }
  return current;
}
```

**Restore**, on `DOMContentLoaded`:

```js
if (typeof window.__scrollAnchor === "string") {
  const target = document.getElementById(window.__scrollAnchor);
  if (target) target.scrollIntoView();
}
```

`scrollIntoView()` with no options performs an instant (non-smooth) jump by
default, aligning the element to the top of the viewport — no separate
"no smooth-scroll" handling needed, unlike the fraction-based approach's
manual `scrollTo` math.

**Save**, on `scroll` (debounced ~500ms) and on `pagehide`:

```js
function saveScrollPosition() {
  const anchor = topmostVisibleAnchor();
  if (!anchor) return; // nothing anchored yet, or document has no id-bearing elements

  const body = JSON.stringify({
    file_name: decodeURIComponent(location.pathname.slice(1)),
    anchor: anchor,
    authenticity_token: CSRF()
  });

  if (navigator.sendBeacon) {
    navigator.sendBeacon("/scroll_position", new Blob([body], { type: "application/json" }));
  } else {
    fetch("/scroll_position", {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body,
      keepalive: true
    });
  }
}
```

`navigator.sendBeacon` cannot set custom headers, so the CSRF token travels
as an `authenticity_token` field inside the JSON body instead of an
`X-CSRF-Token` header — Rails reads `params[:authenticity_token]` from a
parsed JSON body exactly as it would a header, so one body shape covers
both the `sendBeacon` and `fetch` send paths without needing a query-string
method-override trick. `sendBeacon` only issues `POST`, so the route
accepts both `PATCH` (normal `fetch` path) and `POST` (`sendBeacon` path)
on the same action. Debounced scroll listener and `pagehide` listener both
call `saveScrollPosition`; `pagehide` additionally cancels any pending
debounce timer first so it doesn't double-fire.

Route: `match "/scroll_position", to: "scroll_positions#update", via:
[:patch, :post]`.

## Edge cases

- No saved position for this user+file → `window.__scrollAnchor` omitted,
  restore is a no-op.
- Document has zero `id`-bearing elements anywhere (no headings — e.g. a
  markdown file that's a single paragraph with no `#` headings at all — and
  no manually-authored HTML ids) → `topmostVisibleAnchor()` always returns
  `null`, nothing is ever saved for that file, restore is permanently a
  no-op for it. Accepted; see Known trade-offs.
- Reader is scrolled above the first anchor in the document (e.g. at the
  very top, before any heading) → no anchor "qualifies" as topmost-visible
  yet, so `saveScrollPosition` no-ops rather than saving a stale one. This
  matches the natural expectation that reloading near the top lands near
  the top.
- An anchor's target element is later removed from the document (e.g. a
  heading deleted, or an expansion link rewrites over it) →
  `document.getElementById` returns `null` on restore, `target &&
  target.scrollIntoView()` guard makes it a no-op; the reader lands at the
  page's natural top instead of erroring.
- Client somehow submits an anchor outside the safe charset → model
  validation rejects it, controller responds 422, nothing is persisted (the
  previous saved anchor for that file, if any, is left untouched since
  `update!` raises before writing).
- Unauthenticated request to `/scroll_position` → redirected/401 by the
  existing default `authenticate_user!`, same as every other route.
- Rapid navigation away before the debounce fires → `pagehide` covers it.

## Testing

- **Model** (`test/models/scroll_position_test.rb`): validates `anchor`
  presence and charset (rejects e.g. `"</script>"`, spaces, quotes),
  uniqueness scoped to `[user_id, file_name]`.
- **Controller** (`test/controllers/scroll_positions_controller_test.rb`):
  first save creates a record; second save for same user+file upserts
  rather than duplicating; missing `file_name`/`anchor` → 400;
  charset-invalid `anchor` → 422; unauthenticated → 401.
- **Files controller** (`test/controllers/files_controller_test.rb`):
  `window.__scrollAnchor` script present when a record exists for
  `current_user` + the requested file, for both `.md` and `.html` files;
  absent when no record exists; markdown headings render with `id`
  attributes now that `header_ids` is enabled.
- JS untested (no JS test infra in repo, matches existing `expand.js`
  precedent).

## Known trade-offs (accepted)

- A document with no `id`-bearing elements at all never gets a saved
  position, regardless of how much a reader scrolls in it. This is a
  strictly narrower guarantee than a fraction/pixel-based approach (which
  would work for every scrollable document). Accepted because it's the
  direct consequence of the explicit anchor-based requirement, and every
  markdown file with at least one heading is covered.
- Anchor granularity is coarse — restoring lands at the top of whichever
  heading/element was topmost, not the exact line the reader was on.
  Accepted — same spirit as the existing occurrence-index trade-off in text
  expansion.
- `sendBeacon` requests aren't observable/debuggable the way a normal
  `fetch` is (no response handling); acceptable since save is fire-and-forget
  and a missed beacon just means next load doesn't restore as precisely.
- No cleanup job for `scroll_positions` rows whose `file_name` no longer
  exists on disk (file deleted/renamed) or whose `anchor` no longer exists
  in that file (heading removed/renamed). Rows are cheap and inert; out of
  scope for v1.

## Out of scope

- Cross-user shared scroll position.
- Backfilling or migrating positions when a file is renamed, or when a
  heading's text (and therefore its slug) changes.
- Any anchor scheme for documents with no `id`-bearing elements (e.g.
  generating synthetic paragraph-level ids). v1 relies entirely on
  existing heading ids (markdown) or author-written ids (`.html`).
