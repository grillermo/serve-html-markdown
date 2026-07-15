# Text Expansion Feature — Design

Date: 2026-07-15
Status: Approved by user

## Overview

Readers can select text on any served page, ask a question about the selection,
and get an AI-generated HTML page that answers the question with additional
depth. The selection is rewritten in the source file as a link to the new page.
Generated pages are served like any other file, so they are themselves
expandable (recursive digging).

Generation shells out to the `claude` CLI (model `sonnet`, no system prompt),
falling back to the `codex` CLI (model `earth`) on failure — the same
`Open3.capture3` pattern as yosubee's `claude_search_word_service.rb`.

## User flow

1. User selects text on a served `.md` or `.html` page.
2. A floating button with an expand icon (⤢) appears above the selection.
3. Clicking it opens a popover form: a textarea ("Ask about this selection…")
   and an "Expand" submit button.
4. Submit blocks (spinner on the button) while the server generates the page
   synchronously (10–60s, up to 120s timeout).
5. On success the page reloads; the selected text is now a link to the
   generated page. On error, the message is shown in the popover.

## Components

### 1. Client JS — `public/expand.js` (or app/assets, served statically)

Vanilla JS, no bundler (app has none).

- `mouseup` with a non-empty selection → position floating expand button above
  the selection using the selection's bounding rect.
- Button click → popover form near the selection.
- Submit → `fetch POST /expansions` with JSON body:
  - `file_name` — current page's file name (from `location.pathname`)
  - `selected_text` — `selection.toString()`
  - `occurrence` — zero-based index of which occurrence of `selected_text`
    within `document.body.textContent` the selection is (disambiguation)
  - `question` — textarea value
  - CSRF token from `<meta name="csrf-token">` in `X-CSRF-Token` header.
- Success (`{ url }`) → `location.reload()`.
- Failure (`{ detail }`) → show message in popover, re-enable form.
- Clicking elsewhere / pressing Escape dismisses button and popover.

Delivery:

- Markdown pages: add `csrf_meta_tags` and the script tag to
  `app/views/layouts/markdown.html.erb`.
- HTML files: `FilesController#show` injects
  `<meta name="csrf-token" …><script src="/expand.js" defer></script>` before
  `</body>` (case-insensitive); if no `</body>` exists, append to the end.
  This applies to all served `.html`, including generated expansion pages.

### 2. Endpoint — `POST /expansions` (`ExpansionsController`)

Behind Devise session auth (`authenticate_user!`, default) and CSRF protection.

1. Validate params: `file_name`, `selected_text`, `question` required
   (400 on missing/blank); `occurrence` optional integer, default 0.
2. Resolve and validate the source file path. Reuse `FilesController`'s
   path-safety logic by extracting `resolve_file_path` (and its error classes
   plus `FILES_DIR`/`ALLOWED_EXTENSIONS`) into a shared module
   `app/controllers/concerns/resolves_served_files.rb`, included by both
   controllers.
3. Locate `selected_text` in the raw source file: find the `occurrence`-th
   literal occurrence; if the source has fewer occurrences than `occurrence`,
   fall back to the first occurrence; if zero occurrences (selection crossed
   markdown/HTML formatting), return
   422 `{ detail: "Selection not found in source — select a plainer run of text." }`.
4. Call `ClaudeExpandService.expand(file_name:, document:, selection:,
   question:)` → full HTML string.
5. Write the generated page to `files/<stem>--expand-<n>.html`, where `<n>`
   starts at 1 and increments until unique (same loop style as
   `unique_file_path`).
6. Rewrite the source file, replacing the located occurrence:
   - `.md` / `.markdown`: `[<selected_text>](/<new-file>.html)`
   - `.html`: `<a href="/<new-file>.html"><selected_text></a>`
7. Respond `{ url: "/<stem>--expand-<n>.html" }`.

Error mapping: service failure → 502 `{ detail: "Generation failed." }`
(log details server-side); bad path / unsupported file → existing 400/404
behavior from the shared module.

### 3. Service — `app/services/claude_expand_service.rb`

Follows the yosubee `ClaudeSearchWordService` pattern.

- Primary:
  `Open3.capture3("claude", "-p", prompt, "--model", "sonnet", "--output-format", "json")`
  — deliberately **no** `--system-prompt`. Parse stdout JSON; raise on
  non-zero exit or `is_error: true`; take the `result` field.
- Fallback (any primary failure): write prompt handling via
  `codex exec -m earth -s read-only --skip-git-repo-check -o <tmpfile> <prompt>`;
  read the final message from the tmpfile. (Flags verified against codex-cli
  0.144.4.)
- Both attempts wrapped in a 120-second timeout (`Timeout.timeout` around
  `Open3.capture3` with process cleanup, or `popen3` + `wait` with deadline).
- Post-process: strip a single wrapping markdown code fence
  (```` ```html … ``` ````) if present; verify output contains `<html`
  (case-insensitive) else raise `ClaudeExpandService::Error`.
- Both CLIs fail → raise `ClaudeExpandService::Error`; controller maps to 502.
- Log start/success/failure like the yosubee service (never log full document).

### 4. Prompt

Built by the service:

```
You are given a document, a text selection from it, and a reader's question about that selection.

Write a complete standalone HTML page that answers the question and expands on the selected text with additional depth: background, context, related concepts, and concrete details the original document leaves out.

Requirements:
- Output ONLY the HTML document, starting with <!DOCTYPE html>. No markdown fences, no commentary.
- Dark theme, readable typography (max-width ~70ch, generous line-height), semantic HTML.
- Title the page after the selection.
- Ground the answer in the document's context, but bring in outside knowledge freely.

<document filename="{FILE_NAME}">
{FULL_DOCUMENT_CONTENT}
</document>

<selection>
{SELECTED_TEXT}
</selection>

<question>
{QUESTION}
</question>
```

## Routes

```ruby
post "/expansions", to: "expansions#create"
```

Must be declared before the catch-all `/:file_name` route.

## Testing

- **Service** (`test/services/claude_expand_service_test.rb`): stub
  `Open3.capture3` / codex invocation. Cases: claude success; claude failure →
  codex fallback success; both fail → raises; fence stripping; non-HTML output
  raises.
- **Controller** (`test/controllers/expansions_controller_test.rb`): point
  `FILES_DIR` at a tmp dir (or use fixture files), stub the service. Cases:
  happy path writes new `.html` file, rewrites source with markdown link
  (`.md`) and anchor (`.html`), returns url; `occurrence` picks the right
  match; selection not in source → 422; missing params → 400;
  unauthenticated → redirected/401; service error → 502.
- **Files controller**: `.html` responses include the injected script tag;
  markdown layout includes csrf meta + script.
- JS untested (no JS test infra in repo).

## Known trade-offs (accepted)

- Selection must exist verbatim in the raw source file; selections crossing
  formatting (e.g. across `**bold**`) are rejected with a clear 422. v1 limit.
- Synchronous generation — the browser waits up to ~2 minutes.
- Script injection into `.html` responses softens the "served verbatim"
  guarantee; README trust-boundary section updated to note the injected tag.

## Out of scope

- Background jobs / async generation.
- Editing or deleting expansions.
- Multiple questions per selection.
