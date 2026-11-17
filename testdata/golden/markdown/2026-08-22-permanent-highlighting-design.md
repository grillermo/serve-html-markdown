# Permanent text highlighting — design

**Date:** 2026-08-22
**Status:** Draft, pending approval

A signed-in reader selects text on a manual page at `/manual-del-color-vivo/{section}`,
a floating pill reading **subrayar** appears, and clicking it persists the highlight.
The highlight is repainted every time that reader returns to the section.

Deferred out of this version and documented separately in
[`docs/highlight-next-nice-to-haves.md`](../../highlight-next-nice-to-haves.md):
a highlight-count marker in the table of contents, and a "mis subrayados" index page.

## Decisions

| Question | Decision |
| --- | --- |
| Visibility | Private to the reader who made it |
| Removal | Click an existing highlight → pill reads "quitar subrayado" |
| Highlightable surface | The article body only |
| Pill actions | "subrayar" only — no copy, no comment |
| Mobile placement | Pill below the selection, clearing the native iOS/Android menu |
| Overlaps | Merge into one highlight |
| Multi-block selections | One row per block element |
| Colour | A new `manual-theme` token, not the browser's selection blue |
| Orphaned highlights | Silently skipped at render, row kept |
| Save feedback | Optimistic, reverted on failure |
| Selection rules | Trim whitespace, minimum length, links allowed, maximum length |
| Dismissal | Clearing the selection, scrolling, or Escape |
| Accessibility | Pill is a real focusable button; keyboard selection works end to end |
| Anchoring | Block hash + verified offsets |
| Painting | CSS Custom Highlight API; pill hidden where unsupported |
| Delivery | Inline prop on `manual#show` |
| Merging | Client unions, server validates |
| JS tests | Vitest, scoped to the anchoring library |

## 1. Data model

```
highlights
  id
  user_id       bigint  not null, fk
  section_path  string  not null      # "el-origen-del-color/pigmentos"
  group_key     uuid    not null      # links rows created by one selection
  block_hash    string  not null      # FNV-1a of the block's normalized text
  block_index   integer not null      # ordinal of the block within the article
  start_offset  integer not null      # into the block's *normalized* text
  end_offset    integer not null
  text          text    not null
  timestamps

index [user_id, section_path]         # the read path
index [user_id, group_key]            # removal
check  start_offset >= 0 AND end_offset > start_offset
```

`section_path` is a string key, not a foreign key, because manual sections are not
database rows — they come from the hardcoded `Manual::TABLE_OF_CONTENTS` tree in
`app/models/manual.rb`. This follows `Comment` and `ManualVisit`.

`block_index` is consulted *only* when `block_hash` matches more than one block, so it
never causes the positional drift that pure ordinal anchoring would.

`Highlight` validates `section_path` through `Manual.path?(section_path.split("/"))`,
reusing `Comment#section_path_must_exist`, and caps `text` at 5000 characters at both
the validation and column level.

`User` gains `has_many :highlights, dependent: :destroy`.

## 2. Anchoring algorithm

Lives in `app/frontend/lib/highlights/anchor.js` as pure functions, which is what makes
it testable under Vitest.

### Normalization

A **block** is a leaf block element: `p`, `li`, `h2`, `h3`, `blockquote`, `dd` — the
innermost element that contains text and no nested block. A recipe with eight steps has
eight independent anchors, so editing step three orphans only step three.

For each block, `normalizedText` is its `textContent` with runs of whitespace collapsed
to a single space, then trimmed. A single `TreeWalker` pass produces both that string and
an index map from each normalized character position back to a `(textNode, offset)` pair.

**Every offset in the schema indexes this normalized string, never raw `textContent`.**

### Hashing

FNV-1a 32-bit over `normalizedText`, rendered as hex. Chosen over SHA-256 because
`crypto.subtle` is asynchronous; collisions across the ~10 blocks of a section are
negligible, and `block_index` breaks ties.

**This algorithm is frozen once shipped.** Changing it orphans every existing row.

### Capture

1. Reject if either endpoint of the selection falls outside the content wrapper.
2. Trim whitespace inward from both edges.
3. Enumerate the leaf blocks the range intersects.
4. Per block, map the DOM sub-range through the index map into normalized offsets.
5. Reject selections under 3 or over 5000 characters.

### Resolution

Per stored row:

1. Find candidate blocks by `block_hash`; disambiguate with `block_index`.
2. Slice `[start_offset, end_offset)` out of the block's normalized text.
3. **Verify the slice equals the stored `text`.**
4. On mismatch, fall back to `indexOf(text)` within that block.
5. If still not found, skip silently and keep the row.

Step 3 is what guarantees that a stale anchor never paints the *wrong* words — the
failure mode is an invisible highlight, never a misplaced one.

## 3. Painting

One registry entry holds every range:

```js
CSS.highlights.set('subrayado', new Highlight(...ranges))
```

```css
@theme manual-theme {
  --color-manual-highlight: /* warm tint derived from --color-manual-orange */;
}
::highlight(subrayado) { background-color: var(--color-manual-highlight); }
```

`::selection` uses the same token, so the live selection and the persisted highlight are
visually identical.

### Browser support

Feature gate on `'highlights' in CSS`. Where absent, the pill is hidden entirely, so no
reader can create a highlight they will never see.

| Browser | Shipped | Age at 2026-08 |
| --- | --- | --- |
| Chrome / Edge 105 | Sept 2022 | ~4 years |
| Safari 17.2 | Dec 2023 | ~2 yr 8 mo |
| Firefox 140 | June 2025 | ~14 months |

Baseline "newly available" since June 2025. The excluded population is essentially
iPhones that cannot run iOS 17.2 — 2017 and earlier — since every iOS browser uses
Safari's engine. Android is effectively unaffected: Chrome updates through the Play Store
independently of the OS version.

### Hit-testing for removal

`caretPositionFromPoint()`, falling back to `caretRangeFromPoint()` on WebKit, turns a
click into a block plus a normalized offset, which is then matched against the loaded
rows. More reliable than intersecting `Range.getClientRects()`.

### Re-render safety

This is the one real hazard. `Range` objects point at live DOM nodes, and the `<article>`
subtree is React-owned.

Delivery is inline, so the `highlights` prop is stable — but **the deferred `comments`
prop still re-renders the page after first paint**. The resolver therefore re-runs in a
`useEffect` keyed on `[section, highlights, comments]`. Re-resolution costs a few
milliseconds.

The CSS Custom Highlight API was chosen over `<mark>` DOM wrapping precisely because it
mutates nothing: a React re-render can invalidate ranges, but it cannot silently destroy
markup we injected.

### SSR

All of the above runs inside `useEffect`, with no browser globals at module scope, so the
Vite/Inertia SSR path in `app/frontend/ssr/ssr.jsx` is unaffected.

## 4. Interaction

Selecting text inside `<article>` shows the pill anchored below
`range.getBoundingClientRect().bottom` — below, not above, so it clears the native
iOS/Android selection menu. It is a real focusable `<button>`, so keyboard selection with
Shift+arrows works end to end. It is dismissed by collapsing the selection, by scrolling,
or by Escape.

Clicking an existing highlight shows the same pill reading **quitar subrayado**, which
removes the entire `group_key` — so a three-paragraph highlight disappears as one thing.

Saving is optimistic: the tint appears immediately and the request fires behind it. On
failure the previous ranges are restored and a transient Spanish message is shown.

## 5. Merge semantics

Before posting, the client:

1. Finds existing rows in the affected blocks whose ranges overlap or touch the new
   selection.
2. **Expands to those rows' full groups** — a merge that touches one block of a
   three-block group absorbs all three.
3. Unions the ranges per block.
4. Posts the union, replacing every affected row with a fresh `group_key`.

The invariant this preserves: what the reader sees as one continuous highlight is always
exactly one group, so "quitar" always removes exactly that.

```
POST /highlights
{
  section_path,
  blocks: [{ block_hash, block_index, start_offset, end_offset, text }],
  replaces: [id, ...]
}
```

The server validates, then performs one transaction — delete the replaced rows, insert
the new ones under a fresh `group_key`:

- `section_path` resolves through `Manual.path?`
- `end_offset > start_offset >= 0`
- `text.length == end_offset - start_offset` — a cheap consistency check that catches
  most client-side offset bugs
- every `replaces` id belongs to `Current.user` and to this `section_path`
- every replaced row genuinely overlaps a submitted block with the same
  `block_hash` and `block_index`

Rails 8's controller-level `rate_limit` guards the action, as
`Users::SessionsController` already does.

`DELETE /highlights/:id` destroys the row's whole group, scoped to `Current.user`.

## 6. Delivery

`ManualController#show` adds an inline `highlights` prop — a single query on
`[user_id, section_path]`, no serializer needed beyond a `select`. Inline rather than
`InertiaRails.defer` so highlights paint on first render, with no flash of unhighlighted
text.

Plate pages (`node[:plate]`) receive neither the prop nor the component, the same way
they already receive no comment thread.

## 7. Files

| File | Change |
| --- | --- |
| `db/migrate/*_create_highlights.rb` | new |
| `app/models/highlight.rb` | new |
| `app/models/user.rb` | `has_many :highlights` |
| `app/controllers/highlights_controller.rb` | new |
| `app/controllers/manual_controller.rb` | `highlights` prop on `#show` |
| `config/routes.rb` | `resources :highlights, only: [:create, :destroy]` |
| `app/frontend/lib/highlights/anchor.js` | new — pure anchoring functions |
| `app/frontend/components/manual/Highlighter.jsx` | new — capture, pill, painting |
| `app/frontend/components/ManualLayout.jsx` | mount `Highlighter` around the article |
| `app/frontend/entrypoints/application.css` | token, `::highlight`, `::selection` |
| `package.json`, `vitest.config.ts` | new dev dependency, jsdom environment |

## 8. Testing

**RSpec request specs** — create; multi-block create; merge-and-replace; forged
`section_path`; another user's ids in `replaces`; non-overlapping `replaces` rejected;
group destroy; destroy scoped to owner; rate limit.

**Model spec** — validations and the check constraint.

**Vitest**, scoped to `app/frontend/lib/highlights/` — normalization, the index map, hash
stability, offset resolution, verify-and-fallback, orphan skip, merge unioning. This
logic is genuinely unreachable from RSpec, which is the whole justification for adding a
JS test runner to a repo that has none.

**Manual browser verification** — pill placement on desktop and mobile, Escape
dismissal, and repaint after the deferred comments prop arrives.

## 9. Judgment calls and risks

**No per-section highlight cap.** Declined during design, but delivery is inline on every
manual page load, so the prop is unbounded. A pathological reader could bloat their own
page loads. Blast radius is one user, so this is acceptable — but it is a self-inflicted
risk on the read path, and the cap remains the obvious mitigation if it ever bites.

**`block_index` is the ordinal among leaf blocks article-wide**, not per-parent, since it
serves only as a tiebreak.

**Server-side orphan detection is impossible.** Page text lives in JSX shipped in the
build, not in the database, so the server can never know whether a row still matches
anything. "Silently skip" is necessarily a client-side decision, and any future
server-side count of a reader's highlights will overstate. This is the shared blocker for
both deferred features.

**The hash algorithm is a frozen interface.** Any change to FNV-1a, to the normalization
rules, or to the definition of a leaf block orphans every existing highlight.
