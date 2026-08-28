# Inline-Element Selection Linking — Design

**Date:** 2026-07-21
**Status:** Approved for planning

## Problem

Creating a link expansion fails with "Selection not found in source — select a
plainer run of text." whenever the selected text spans inline markup. Example:
source contains `this is <span> some </span> code`, the user highlights
"some code" in the rendered page.

Root cause: the browser sends `selection.toString()` — rendered text with tags
stripped and whitespace collapsed — while `SelectionLinker#match_index` does a
literal `String#index` against the raw source file. Any inline markup or
whitespace difference inside the selected run means no literal match.

The same failure class affects markdown files (rendered via commonmarker):
selections crossing `` `code` ``, `*emphasis*`, or link syntax fail identically.

A related latent bug: the frontend counts the `occurrence` index in rendered
text (`document.body` text content), while the server counts occurrences in raw
source — two different spaces that disagree whenever markup appears near the
selection.

## Requirements

1. Support selections spanning inline markup in both HTML (`.html`) and
   Markdown (`.md`, `.markdown`) sources.
2. **Snap to boundary:** when a selection partially overlaps an inline element
   (starts or ends mid-element), extend the link outward to cover the whole
   element rather than rejecting.
3. **Segment around existing links:** when a selection overlaps an existing
   link (`<a>` in HTML, `[label](url)` in markdown), leave the old link
   untouched and wrap the non-link parts of the selection in new link segments,
   all pointing at the same expansion.
4. **Reject cross-block selections:** selections spanning multiple blocks
   (paragraphs, list items, headings) are rejected with a clear error.
5. Whitespace differences between rendered selection and source (newlines,
   collapsed runs) must not prevent matching.
6. Only the spliced link bytes change in the source file; everything else stays
   byte-identical. No full-document re-serialization.

## Architecture

`SelectionLinker.link(source:, extension:, selected_text:, occurrence:, url:)`
keeps its public API and error classes (`SelectionLinker::Error`,
`NotFound`, `UnsafeMatch`), so `ExpansionProcessor` and
`GenerateExpansionJob` are untouched. Internals become a four-stage pipeline:

```
raw source ─► TextMap (per format) ─► Locator ─► Planner ─► Writer ─► spliced source
```

### 1. Text maps — `SelectionLinker::HtmlMap`, `SelectionLinker::MarkdownMap`

Offset-preserving tokenizers over the raw source. Each produces:

- **`plain`** — a rendered-text projection of the source:
  - HTML: tags dropped, entities decoded (named, decimal, hex), `<script>` and
    `<style>` contents skipped (not rendered as selectable text).
  - Markdown: inline markers dropped — backtick code-span delimiters, `*`/`_`
    emphasis delimiters, link/image syntax (label text kept, URL dropped),
    backslash escapes resolved, raw inline HTML tags dropped.
- **Span table** — mapping between plain-text offsets and source byte ranges.
  Entities and multi-byte markers are atomic: a mapping never splits one.
- **Structure**:
  - Inline element ranges: HTML inline tags (`span`, `em`, `strong`, `code`,
    `b`, `i`, `u`, `small`, `sub`, `sup`, `mark`, `abbr`, `time`, …); markdown
    delimiter pairs (code spans, emphasis).
  - Existing-link ranges: `<a>…</a>`; markdown `[label](url)` and images.
  - Block boundaries: HTML block tags (`p`, `div`, `li`, `h1`–`h6`, `ul`,
    `ol`, `blockquote`, `pre`, `table`, `tr`, `td`, `th`, `section`,
    `article`, `header`, `footer`, `figure`, …); markdown blank lines, heading
    lines, list-item starts, blockquote boundaries.
  - Unlinkable zones: markdown fenced/indented code blocks (a link spliced
    there would render literally). Positions inside an HTML tag need no zone:
    the projection drops tag bytes, so no match can land there by
    construction.

### 2. Locator

- Normalize whitespace (collapse runs to single space, trim) in both the
  selection and the plain projection, preserving the offset mapping.
- Find all occurrences of the normalized selection in normalized plain text;
  pick index `occurrence`, falling back to the first (current behavior:
  `indices.fetch(@occurrence, indices.first)`).
- This is (approximately) the same text space the frontend counts occurrences
  in, fixing the occurrence-space mismatch.
- No occurrences → `NotFound` with the existing message: "Selection not found
  in source — select a plainer run of text."

### 3. Planner

Applies rules to the matched source range, in order:

1. **Cross-block reject:** if the range crosses any block boundary →
   `UnsafeMatch`, "Selection spans multiple paragraphs — select within one."
2. **Unlinkable reject:** if the range touches an unlinkable zone (markdown
   code block) → `UnsafeMatch`, "Selection is inside a code block."
3. **Snap:** while any inline element is partially overlapped (its open inside
   the range but close outside, or vice versa), extend the range to cover that
   element entirely. Repeat until stable. Snapping never crosses a block
   boundary (if it would, reject as cross-block).
4. **Segment:** split the range around existing-link ranges. Old links stay
   byte-identical. Segments that contain no plain text are dropped. All
   segments empty (selection entirely inside an existing link) →
   `UnsafeMatch`, "Selection overlaps an existing link."

Output: ordered list of source byte ranges to wrap.

### 4. Writers

- HTML: wrap each segment in `<a href="URL">…</a>`.
- Markdown: wrap each segment in `[…](URL)`, escaping `]` in the label (current
  behavior). Inline code inside a label (`` [`code`](url) ``) is valid
  commonmark and allowed.
- Splice segments last-to-first so earlier offsets stay valid.
- The URL is the server-generated expansion path (existing
  `ERB::Util.url_encode`d basename); no additional escaping concerns.

## Error handling

Same exception classes; messages become more specific. `GenerateExpansionJob`
already rescues `SelectionLinker::Error` and surfaces `error.message` in the
status bar, so no frontend or job changes.

| Condition | Class | Message |
| --- | --- | --- |
| No match in plain projection | `NotFound` | existing message |
| Crosses block boundary | `UnsafeMatch` | "Selection spans multiple paragraphs — select within one." |
| Inside code block (md) | `UnsafeMatch` | "Selection is inside a code block." |
| Entirely inside existing link | `UnsafeMatch` | "Selection overlaps an existing link." |
| Inside `<script>`/`<style>` | `NotFound` | existing message (their text is not in the projection) |

## Files

- `app/services/selection_linker.rb` — orchestrator; public API and errors.
- `app/services/selection_linker/html_map.rb`
- `app/services/selection_linker/markdown_map.rb`
- `app/services/selection_linker/planner.rb` — snap/segment/reject rules;
  writers live here or in the orchestrator (small).
- `test/services/selection_linker_test.rb` — extended.
- Unit tests for maps/planner as needed
  (`test/services/selection_linker/…`).

## Testing (TDD)

HTML:
- Plain literal match still works (regression).
- Selection across `<span>` (the motivating case): `this is <span> some
  </span> code`, select "some code" → `this is <a href="…"><span> some
  </span> code</a>` (snap covers the span; whitespace tolerated).
- Whitespace collapse: newline/multiple spaces in source vs single space in
  selection.
- Entity decode: source `A &amp; B`, selection "A & B".
- Snap: selection starting mid-`<em>` extends to cover the whole element.
- Segment: selection containing an existing `<a>` produces two new anchors
  around the untouched old one.
- Cross-paragraph selection rejected.
- Selection matching only `<script>` content → `NotFound`.
- nth `occurrence` picks the right match in rendered-text space.

Markdown:
- Selection across `` `code` `` and `*emphasis*`.
- Snap on partial emphasis/code-span overlap.
- Segment around existing `[label](url)`.
- Cross-block (blank line, heading, list item) rejected.
- Selection inside fenced code block rejected.
- `]` in label still escaped.

## Scope limits

- Markdown inline scanner covers: code spans, emphasis (`*`/`_`, single and
  double), links, images, backslash escapes, raw inline HTML. Not full
  commonmark (no autolink extension nuances, no tables-inline edge cases).
  Sufficient for this tool's AI-generated documents.
- Occurrence space matches the file's rendered text, not literally
  `document.body` (which may include injected UI text); the existing
  first-match fallback covers disagreements.
- No frontend changes.
