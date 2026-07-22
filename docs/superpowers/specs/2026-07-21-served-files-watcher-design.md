# Served Files Watcher + `/last` Tracking Table

Date: 2026-07-21
Status: Approved (pending spec review)

## Problem

`/last` must redirect to the file **added last** (newest), not the most recently
modified. It currently derives newness from the filesystem via
`Pathname#birthtime` (falling back to `mtime` when `birthtime` raises
`NotImplementedError`). That fallback makes a merely *edited* file look newest,
which is the bug the recent commit `d83f19c` tried to paper over. Filesystem
timestamps are the wrong source of truth.

## Solution Overview

Introduce a database table `served_files` as the source of truth for "which
files exist and when we first saw each one". A **standalone background file
watcher** keeps the table in sync with the `files/` directory in real time. On
boot the watcher does a full reconcile scan, then listens for live add/remove
events. `/last` becomes a pure read of that table.

Additionally, when an expansion completes, the expanded **source** file's row has
its `updated_at` bumped (it was modified — a link was inserted), and the new
`--expand-N.html` file is recorded immediately.

## Decisions (from brainstorming)

- **Detection**: real background file watcher (`listen` gem, native FSEvents on
  macOS), **not** on-request disk scan and **not** an in-web-process thread.
- **Runtime**: standalone process (`bin/watch`), launched by `./serve` alongside
  the Rails server using **GNU parallel**.
- **Reconcile**: boot-time full scan (insert missing + prune orphans), then live
  events.
- **Pruning**: yes — remove rows for files deleted from disk, so `/last` never
  points at a missing file.
- **`updated_at` on expansion complete**: bump the **source** file's row.
- **`/last` ordering**: first-seen `created_at` (frozen at detection), ties
  broken by `id`. `updated_at` is metadata, not used for ordering.

## Data Model

New table `served_files`:

| column       | type     | constraints                     |
|--------------|----------|---------------------------------|
| `id`         | bigint   | pk                              |
| `name`       | string   | not null, **unique index**      |
| `created_at` | datetime | not null (first-seen, frozen)   |
| `updated_at` | datetime | not null                        |

`name` is the file basename (e.g. `graphql-study-guide.html`). Unique index makes
concurrent inserts safe via `INSERT ... ON CONFLICT DO NOTHING`.

## Components

### `ServedFile` model (`app/models/served_file.rb`)

Reuses `ResolvesServedFiles::FILES_DIR` and `ALLOWED_EXTENSIONS`.

Class methods:

- `sync!` — full reconcile. Scan `FILES_DIR.children`, keep `path.file?` with an
  allowed extension. Insert names not already present via `insert_all` (skips
  conflicts; sets `created_at`/`updated_at` to `Time.current`). Prune with
  `where.not(name: disk_names).delete_all`. Called at watcher boot.
- `record(name)` — record a single newly-seen file. No-op unless the name has an
  allowed extension. Concurrency-safe insert (ignore conflict). Does **not**
  touch `created_at` of an existing row (first-seen stays frozen).
- `remove(name)` — `where(name:).delete_all` when a file disappears.
- `record_modification(name)` — `find_or_create_by(name:)` then `touch` so
  `updated_at` moves. Used by expansion completion.
- `newest` — `order(created_at: :desc, id: :desc).first`.

Extension filtering lives in the model (single source of truth) so callers pass
raw basenames.

### `FileWatcher` service (`app/services/file_watcher.rb`)

Thin wrapper over the `listen` gem.

- `start` — build a `Listen.to(FILES_DIR)` listener whose callback delegates to
  `handle(modified, added, removed)`, then `listener.start`.
- `handle(modified, added, removed)` — for each `added` path call
  `ServedFile.record(basename)`; for each `removed` path call
  `ServedFile.remove(basename)`; ignore `modified` (edits don't change first-seen
  order; expansion edits are handled via the explicit hook). `handle` is a plain
  method taking arrays of path strings so it is unit-testable **without** real
  filesystem events.

### `bin/watch` (standalone process)

Ruby script: load the Rails environment, run `ServedFile.sync!` (boot-time full
scan), start `FileWatcher`, then block (`sleep`) so the process stays alive.
Handle `INT`/`TERM` to stop the listener and exit cleanly.

### `serve` script

Keep the existing assets clobber + precompile. Replace the final `exec bin/rails
server` with GNU parallel running two long-lived jobs — the web server and the
watcher — streaming output live and halting all jobs when any one exits:

```sh
bin/rails assets:clobber
bin/rails assets:precompile

exec parallel --ungroup --halt now,done=1 ::: \
  "bin/rails server -p 8009 $*" \
  "bin/watch"
```

`--ungroup` streams each job's output immediately; `--halt now,done=1` tears down
the other job as soon as one finishes or fails, so Ctrl-C stops both.

### `FilesController#last`

Replace the `FILES_DIR.children … max_by(&:creation_time)` logic and the private
`creation_time` helper with:

```ruby
def last
  latest = ServedFile.newest
  if latest
    redirect_to "/#{ERB::Util.url_encode(latest.name)}", status: :found
  else
    render json: { detail: "No files found." }, status: :not_found
  end
end
```

Pure table read — no filesystem scan on the request path.

### `ExpansionProcessor#process`

After the source rewrite is written (inside `with_source_lock`, after
`file_path.write(rewritten…)`):

```ruby
ServedFile.record(expansion_path.basename.to_s)          # new file → newest
ServedFile.record_modification(file_path.basename.to_s)  # source edited → bump updated_at
```

Recording the expand file explicitly (rather than waiting on the async watcher)
makes `/last` correct immediately after a completion. The unique index makes the
later watcher `:added` event a harmless no-op.

## Data Flow

```
file added to files/ ─▶ Listen :added ─▶ ServedFile.record(name)  ─▶ row (created_at=now)
file deleted         ─▶ Listen :removed ─▶ ServedFile.remove(name) ─▶ row deleted
watcher boot         ─▶ ServedFile.sync! ─▶ insert missing + prune orphans
expansion completes  ─▶ ServedFile.record(expand_file)            ─▶ new newest row
                       ServedFile.record_modification(source)     ─▶ source updated_at bumped
GET /last            ─▶ ServedFile.newest ─▶ 302 redirect (or 404 if empty)
```

## Error Handling

- Concurrent inserts (watcher event + boot scan + expansion hook racing):
  unique index + `insert_all` ON CONFLICT DO NOTHING → no duplicates, no raise.
- `/last` with empty table → `404 {"detail":"No files found."}` (unchanged).
- Watcher crash: `bin/watch` exits; `--halt now,done=1` stops the web job too, so
  the operator sees it. Table remains valid (rows persist); next boot re-syncs.
- `record`/`remove` on names with disallowed extensions (lockfiles like
  `.foo.html.expansion.lock` → `.lock`, `.DS_Store`, `.gitkeep`): filtered out by
  the extension check, never inserted.

## Testing (TDD)

Model (`test/models/served_file_test.rb`):
- `sync!` inserts allowed files, skips disallowed, is idempotent, prunes orphans.
- `record` inserts allowed name, no-ops disallowed, leaves existing `created_at`
  frozen on repeat.
- `remove` deletes the row.
- `record_modification` bumps `updated_at`, creates row if absent.
- `newest` orders by `created_at` desc then `id` desc.

Watcher (`test/services/file_watcher_test.rb`):
- `handle` maps `added` → `record`, `removed` → `remove`, ignores `modified`
  (call the method directly with arrays; no real FS events).

Request (`test/controllers/files_controller_test.rb` or request test):
- `/last` redirects (302) to `newest.name`.
- `/last` with empty table → 404.

Processor (`test/services/expansion_processor_test.rb`):
- after `process`, the `--expand-N.html` row exists and the source file's row
  `updated_at` was bumped.

`bin/watch` and the `serve` script are smoke-tested manually (not unit tested).

## Dependencies

Add `gem "listen"` to the Gemfile (pulls `rb-fsevent` on macOS, `rb-inotify` on
Linux). Used by `FileWatcher` only.

## Out of Scope (YAGNI)

- In-web-process watcher threads / Puma plugin.
- Seeding `created_at` from filesystem birthtime (defeats the point; detection
  time is the stable signal).
- Debouncing / batching watcher events (unique index makes duplicates cheap).
- Tracking file content hashes or sizes in `served_files`.
