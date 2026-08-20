# In-Place Expansion and File Versions

## Problem

The expansion form has one behavior: the LLM writes a standalone HTML page,
the page is saved as `foo--expand-1.html`, and the selected text in the source
file becomes a link to it. The reader leaves the document to read the
expansion.

Sometimes the wanted outcome is the opposite — the expansion belongs *in* the
document, and the reader should stay where they are. This adds a second mode
that rewrites the document itself, and the version history needed to make
rewriting safe.

## Decisions

These were settled during brainstorming and constrain everything below.

- **Two modes**, chosen from a dropdown in the expansion form: `create new`
  (today's behavior, unchanged) and `edit in place`.
- **Edit in place rewrites the whole file** via the LLM, rather than splicing a
  fragment around the selection.
- **Versions are separate files.** `foo.md` is v1; each rewrite writes
  `foo--v2.md`, `foo--v3.md`. `/foo.md` keeps serving v1. Every version has a
  permanent, independent URL.
- **Only in-place expansions create versions.** Uploads, `files#create`, and
  the `create new` path do not.
- **Version families are derived from filenames**, not tracked in the database.
  The files directory stays the single source of truth, consistent with the
  watcher and `ServedFile.sync!`.
- **Scroll uses an LLM-emitted anchor, falling back to the nearest preceding
  heading.**
- **The mode preference is stored on the user**, not in localStorage.

## Version Families

A family is the set of files sharing a stem: `foo.md` (v1), `foo--v2.md`,
`foo--v3.md`. Version numbers start at 2 — v1 is the bare name.

A new value object `app/services/file_versions.rb` owns the entire `--v`
vocabulary:

- parse a basename into `(stem, version, extension)`
- list a family in version order from `ServedFile`
- compute the next free version path for a family

No other file learns the convention, so the naming scheme has exactly one
definition.

The family query is `name = "foo.md" OR name LIKE "foo--v%.md"`. The stem must
have `%` and `_` escaped before interpolation into the `LIKE` pattern, or a
file named `a_b.md` will pull in unrelated `axb--v2.md`.

Next-version resolution takes the maximum existing version in the family and
increments, then confirms the path is free on disk before returning it.

### Reserved suffix

`files#create` and `files#upload` must reject posted filenames whose stem ends
in `--v<digits>`, so user-supplied names cannot collide with a version slot.

Both endpoints already funnel through `unique_file_path`, which is therefore
the single place the check belongs. It raises `ActionController::BadRequest`
with `"Filenames may not use the reserved --v<number> suffix."`

Rejection, not silent stripping: these are API endpoints, and a caller who
asked for `report--v2.md` should be told it was refused rather than quietly
handed a different name.

This guards the two HTTP write paths only. Files placed in `files/` by hand or
by the watcher still join families by name. That is the filename-derived
approach working as designed, not a gap.

## Rewrite Pipeline

### Schema

`expansions` gains `mode` (string, `NOT NULL`, default `"create_new"`),
validated with `inclusion: { in: %w[create_new edit_in_place] }`.

### Controller

`ExpansionsController#create` reads `mode` from the request, rejects unknown
values with `400`, and passes it to the created expansion.

### Processor

`ExpansionProcessor#process` branches on `expansion.mode`. The `create_new`
branch is today's code, unmodified.

The `edit_in_place` branch:

1. Resolves and reads the source file.
2. Calls the expander for a full rewritten document.
3. Inside the existing `with_source_lock` on the source file, resolves the next
   version path via `FileVersions`, writes the rewritten document there, and
   records the new `ServedFile`.
4. Completes the expansion with the new version's URL.

The lock is held so two concurrent expansions on one file cannot claim the same
version number.

**The source file is never modified.** v1 stays byte-identical, no link is
spliced into it, and `SelectionLinker` is not involved in this path.

### Expander

`ClaudeExpandService` currently hardcodes `ensure_html` as its output check,
which is wrong for a Markdown rewrite. The runner is parameterized with a
prompt template and an output validator; `expand` becomes a thin wrapper over
it so the `create_new` path stays behaviorally identical.

The rewrite prompt instructs the model to:

- return the complete document in its original format (Markdown stays
  Markdown, HTML stays HTML)
- preserve everything unrelated to the selection verbatim
- expand the selected passage in light of the reader's question
- emit the sentinel `⟦EXPANSION_ANCHOR⟧` on its own line immediately before the
  expanded passage
- output only the document, with no commentary or fences

Validation is format-dependent: HTML rewrites must still contain `<html`;
Markdown rewrites need only be non-blank.

### Truncation guard

Whole-file rewrite's main failure mode is silently returning a shortened
document. If the rewritten output is under 50% of the source's length, the
expansion fails with `"Rewrite looked truncated."` and nothing is written.

The version file makes a bad rewrite survivable; this guard makes it visible.

## Anchor and Scroll

The server replaces the first `⟦EXPANSION_ANCHOR⟧` in the rewritten document
with `<a id="expansion-anchor"></a>` and strips any additional occurrences.
This renders in both formats: the Commonmarker config already runs with
`render: { unsafe: true }`.

For the fallback, `expand.js` walks backward from the selection to the nearest
element carrying an `id` and submits it with the expansion. Because the page
reloads, the fallback cannot live in JS memory — it travels in the URL.

The completed expansion's URL is `/foo--v2.md?fallback=<id>#expansion-anchor`.
On load, `expand.js` scrolls to the hash target; if that element is absent, it
scrolls to the `fallback` element instead. Rails ignores the unknown query
param.

Markdown headings carry ids (`header_ids: ""`), so the fallback is reliable
there. Hand-written HTML files may have no ids, in which case the reload lands
at the top of the page — reachable only when the model also dropped the
sentinel.

## Version Navigation

The controller injects the family as data (`window.__fileVersions`, plus the
current version) and `expand.js` renders the navigation bar. This avoids
building the same UI twice — once in `markdown.html.erb` and once in the
string-injected HTML path.

This requires pulling the currently-duplicated bootstrap (`csrf-token`,
`window.__scrollAnchor`, the `expand.js` script tag) out of both
`markdown.html.erb` and `FilesController#inject_expand_script` into a single
helper both call. That is a cleanup of existing duplication directly in the
path of this work.

The bar is fixed to the bottom of the viewport, shows `‹ v2 of 3 ›` with the
end arrows disabled, and renders only when the family has more than one member.
It shares the bottom edge with the expansion sheet, so the sheet takes the
higher `z-index` and the bar hides while the sheet is open.

## Mode Preference

`users` gains `expansion_mode` (string, default `"create_new"`), injected as
`window.__expansionMode` in the same bootstrap. The dropdown initializes from
it, and `ExpansionsController#create` writes the submitted mode back when it
differs from the stored one — so the preference follows the user across
browsers and devices.

## Testing

- `FileVersions`: parsing, family ordering, next-version resolution, and the
  `LIKE`-escaping case.
- `ExpansionProcessor` in `edit_in_place` mode with a stubbed expander: happy
  path, truncation guard, sentinel-missing, and confirmation that the source
  file is untouched.
- `FilesController`: reserved-suffix rejection on both `create` and `upload`.
- `ExpansionsController`: mode validation and preference persistence.
- Rendering: the bootstrap and version data appear for both Markdown and HTML
  files.

## Accepted Consequences

Two follow from the decisions above and are accepted deliberately:

- `/foo.md` permanently serves v1, so a saved or shared link keeps showing the
  pre-rewrite text. After several rewrites the most obvious URL is the stalest
  one, and the arrows are the only way forward from it.
- `files#last` and the index order by `ServedFile` timestamps, so each new
  version appears as its own entry rather than folding into its family.
