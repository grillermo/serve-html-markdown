# Expansion Pipeline Profiling — Design

**Date:** 2026-07-21
**Goal:** Measure every stage of the text-expansion flow (browser click → link ready) to find where time goes. Target: whole flow under 2 seconds, LLM generation included.

## Background

Current pipeline: `expand.js` POSTs `/expansions` → `ExpansionsController#create` inserts an `Expansion` row and enqueues `GenerateExpansionJob` (ActiveJob AsyncAdapter, in-process thread pool) → `ExpansionProcessor` reads the source file, calls `ClaudeExpandService` (claude CLI subprocess by default, codex CLI on claude failure, OpenAI HTTP when `use_openai`), then under a file lock runs `SelectionLinker.link`, writes the expansion HTML and rewrites the source file → browser polls `/expansions/:id` until `completed` and renders the link.

Nothing is timed today; the only visibility is a few unstructured log lines.

## Instrumentation

### `timings` column

Migration adds `timings` (text, serialized JSON, default `{}`) plus `provider_used` (string) and `html_bytes` (integer) to `expansions`.

`Expansion#stamp!(stage)` records `timings[stage] = epoch_ms` and saves. Stages, in chronological order:

| Stage | Stamped where |
|---|---|
| `client_clicked` | Sent by browser in POST body (`client_clicked_at`, epoch ms) |
| `request_received` | `ExpansionsController#create`, before insert |
| `job_enqueued` | Controller, after `perform_later` |
| `job_started` | `GenerateExpansionJob#perform`, after `claim!` |
| `source_read` | `ExpansionProcessor`, after source file read |
| `llm_request_start` | `ClaudeExpandService`, before HTTP request / subprocess spawn |
| `llm_first_failure` | Service, when the claude CLI fails and codex fallback begins (fallback runs only) |
| `llm_response` | Service, after response parsed and HTML validated |
| `lock_acquired` | Processor, after flock obtained |
| `link_rewritten` | Processor, after `SelectionLinker.link` returns |
| `files_written` | Processor, after expansion HTML + rewritten source written |
| `completed` | `Expansion#complete!` |

The service and processor receive the expansion (they already do) and call `stamp!` directly. Stamps are wall-clock epoch ms so client and server stamps share one axis; clock skew is irrelevant when client and server are the same machine.

`provider_used` records which backend actually produced the HTML (`openai`, `claude`, `codex`). `html_bytes` records response size, to correlate generation time with output length.

### Poll interval

`POLL_INTERVAL_MS` in `expand.js` changes 1500 → 500. Production change, not test-only. Driver script polls at the same 500ms so measured discovery latency matches what users see.

### Claude-failure simulation for codex runs

`ClaudeExpandService::CLAUDE_MODEL` becomes overridable via env: `ENV.fetch("EXPANSION_CLAUDE_MODEL", "sonnet")`. Codex-path profiling runs set it to an invalid model name so the claude CLI genuinely starts, errors, and exits — the real cost of a claude failure (startup + error) lands in `llm_first_failure - llm_request_start`, then codex time follows. No instant fake failure.

## Test driver: `bin/profile_expansion`

Ruby script, run against a dev server started manually. Behaves exactly like the browser:

1. Logs in via devise form POST, keeps session cookie.
2. Per run: POST `/expansions` with `file_name: tech-lead-prep.html`, a fixed selected word, `question: "explain this word further"`, `client_clicked_at: now_ms`, and `use_openai` per provider.
3. Polls `GET /expansions/:id` every 500ms; records `poll_seen` epoch ms when status flips to `completed`/`failed`.
4. Resets state between runs: `git checkout -- files/tech-lead-prep.html`, delete `files/tech-lead-prep--expand-*.html`, so occurrence and link state are identical every run.

Matrix: 4 runs × 3 providers (OpenAI HTTP, claude CLI, codex via failed claude). Codex runs require the server started with `EXPANSION_CLAUDE_MODEL=<invalid>`; the script prompts for the server restart between provider groups (env var lives in the server process, not the script).

## Report

Script output, per run: chronological table of stages with delta from previous stage and cumulative time. Aggregate: per-provider median for each delta:

- click → request received
- request received → job started (queue latency)
- job started → llm request (file read, setup)
- llm request → response (**the** number; includes claude-failure time on codex runs, split out)
- response → files written (lock + linking + writes)
- files written → completed
- completed → poll discovery (perceived tail)

## Error handling

- Failed runs (LLM error) reported as `failed` with whatever stamps exist; excluded from medians but printed.
- `stamp!` never raises into the pipeline: rescue + log, profiling must not break expansions.
- Driver aborts a run after 180s of polling.

## Testing

- Model test: `stamp!` persists stages, tolerates concurrent completion.
- Controller test: `client_clicked_at` param stored, missing param fine.
- Service tests: stamps recorded around stubbed provider calls; `EXPANSION_CLAUDE_MODEL` override respected.
- Existing expansion tests keep passing (timings additive, optional).

## Out of scope

Any actual optimization. This produces the numbers; the follow-up decides where to cut (likely LLM strategy — a full HTML page from any current model will not fit in 2s alone, which the data will make explicit).
