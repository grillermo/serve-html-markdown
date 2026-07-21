# Profiling the expansion pipeline

`bin/profile_expansion` drives the real expansion flow (browser click → job → LLM call → file
write → poll discovery) against a live dev server and reports per-stage timing.

## Prerequisites

- Dev database migrated and seeded with an admin user:

  ```bash
  RAILS_ENV=development bin/rails db:migrate
  RAILS_ENV=development bin/rails db:seed
  ```

- `ADMIN_EMAIL` / `ADMIN_PASSWORD` — credentials for the seeded admin user (same values used by
  `db/seeds.rb`).
- `EXPANSION_LLM_API_KEY` — required for the `openai` provider group only.
- The `claude` CLI on `PATH` — required for the `claude` and `codex` (fallback) provider groups.
- If your shell exports `RAILS_ENV` globally (e.g. in `.zshrc`), export it explicitly on the
  command line below anyway — the script shells out to `bin/rails runner` to read timings, and
  that subprocess inherits ambient env. A stale global `RAILS_ENV` will make it silently query the
  wrong database.

## Running it

Terminal 1 — start the server:

```bash
RAILS_ENV=development bin/dev
```

Terminal 2 — run the driver:

```bash
ADMIN_EMAIL=... ADMIN_PASSWORD=... RAILS_ENV=development bin/profile_expansion
```

Flags:
- `--runs N` — runs per provider (default 4)
- `--host URL` — server host (default `http://localhost:3000`)

## What it does

1. Logs in via the Devise sign-in form and keeps the session cookie.
2. Runs the `openai` provider group first (`--runs` real requests, needs `EXPANSION_LLM_API_KEY`).
3. Runs the `claude` provider group (real `claude` CLI subprocess calls, no key needed).
4. Before the `codex` provider group, prompts:

   ```
   Restart the server with EXPANSION_CLAUDE_MODEL=<invalid-model> then press Enter:
   ```

   In terminal 1: Ctrl-C, then restart with an invalid model name so the claude CLI genuinely
   fails and falls back to codex:

   ```bash
   EXPANSION_CLAUDE_MODEL=nonexistent-model RAILS_ENV=development bin/dev
   ```

   Then press Enter in the driver to continue.

Between every run, `reset_state!` restores `files/tech-lead-prep.html` to its pristine committed
state (`git checkout --`) and deletes any generated `--expand-*.html` files, so occurrence/link
state is identical run to run.

## Output

Per-run: a chronological table of stages with delta-from-previous and cumulative time, e.g.:

```
--- openai run 1 ---
client_clicked                0ms  (cumulative        0ms)
request_received              2ms  (cumulative        2ms)
job_enqueued                  5ms  (cumulative        7ms)
job_started                   2ms  (cumulative        9ms)
source_read                   1ms  (cumulative       10ms)
llm_request_start             2ms  (cumulative       12ms)
llm_response               25413ms  (cumulative    25425ms)
lock_acquired                  5ms  (cumulative    25430ms)
link_rewritten               346ms  (cumulative    25776ms)
files_written                  4ms  (cumulative    25780ms)
completed                      3ms  (cumulative    25783ms)
poll_discovery                533ms  (cumulative    26316ms)
```

After all provider groups: an aggregate section with per-provider medians for:

- click → request received
- request received → job started
- job started → llm request
- llm request → response (the number — includes claude-failure time on codex runs)
- response → files written
- files written → completed
- completed → poll discovery

## Failure handling

- A failed LLM run is reported (with whatever stamps exist) and excluded from medians, not
  counted as an error.
- A hung run times out after 180s of polling.
- Any error in a single run (timeout, non-2xx response, connection failure) is caught, reported as
  `ERRORED` with the provider/run number/message, and the matrix continues with the next run — one
  bad run doesn't kill the whole session.

## Known gotcha

`files/tech-lead-prep.html` is force-tracked in git specifically so `reset_state!`'s
`git checkout --` works (`files/**` is gitignored by default otherwise). If you ever need to swap
the fixture file the script profiles against, force-add it the same way:
`git add -f files/<your-file>`.
