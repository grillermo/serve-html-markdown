# Task 6 Report: The edit_in_place branch in ExpansionProcessor

## What I implemented

- `ExpansionProcessor#process` now branches on `@expansion.edit_in_place?`:
  - `create_new` (default) path is unchanged behavior, moved verbatim into a new private `link_new_page(file_path, source)`.
  - `edit_in_place` path is handled by a new private `rewrite_in_place(file_path, source)`:
    - Calls `EXPANDER.rewrite(...)` (the whole-document rewrite from Task 5) instead of `EXPANDER.expand`.
    - Raises `ExpansionProcessor::TruncatedRewrite` (`"Rewrite looked truncated."`) if the rewrite is shorter than `MIN_REWRITE_RATIO` (0.5) of the source length. Raised *before* the lock is taken and *before* anything is written — the source file and lock are never touched on this path.
    - Otherwise inserts the anchor (`insert_anchor`) and, inside the same file lock used by `create_new`, resolves the next version path via `FileVersions.parse(...).next_path(FILES_DIR)`, writes the anchored content there, stamps `:files_written`, records the new file in `ServedFile`, and returns the URL from `version_url`.
    - The source file itself is never read again or written in this path — no `latest_source`, no `SelectionLinker`, no `ServedFile.record_modification` on the source.
  - New constants: `TruncatedRewrite = Class.new(StandardError)`, `ANCHOR_SENTINEL = ClaudeExpandService::ANCHOR_SENTINEL`, `ANCHOR_ID = "expansion-anchor"`, `MIN_REWRITE_RATIO = 0.5`.
  - `insert_anchor(document)`: no-op if the sentinel is absent (still returns a version — "the model omits the sentinel" case); otherwise splits on the sentinel, keeps everything before the first occurrence, inserts `<a id="expansion-anchor"></a>`, and joins the remainder of the *rest* array back with no separator — this drops any additional sentinel occurrences (they simply vanish, since `split` consumed them as the delimiter and `rest.join` reassembles the text around them without reinserting the sentinel).
  - `version_url(version_path)`: builds `/<url-encoded-basename>` and appends `?fallback=<url-encoded fallback_anchor>` only when `@expansion.fallback_anchor.present?`, followed by `#expansion-anchor` always.
- `GenerateExpansionJob`: added `ExpansionProcessor::TruncatedRewrite` to the existing rescue clause that forwards `error.message` to `expansion.fail!`.

## Files changed

- `/Users/grillermo/c/serve-html-markdown/app/services/expansion_processor.rb`
- `/Users/grillermo/c/serve-html-markdown/app/jobs/generate_expansion_job.rb`
- `/Users/grillermo/c/serve-html-markdown/test/services/expansion_processor_test.rb`
- `/Users/grillermo/c/serve-html-markdown/test/jobs/generate_expansion_job_test.rb`

## TDD Evidence

### RED

Command:
```
bin/rails test test/services/expansion_processor_test.rb test/jobs/generate_expansion_job_test.rb
```

Output (before implementation):
```
16 runs, 0 failures, 7 errors
```
Sample failures:
```
ExpansionProcessorTest#test_refuses_a_rewrite_shorter_than_half_the_source:
NameError: uninitialized constant ExpansionProcessor::TruncatedRewrite

ExpansionProcessorTest#test_writes_the_rewrite_to_the_next_version_and_leaves_the_source_untouched:
RuntimeError: expand must not be called in edit_in_place mode
    app/services/expansion_processor.rb:23:in 'ExpansionProcessor#process'
    (fell through to the create_new/EXPANDER.expand path, as expected)

GenerateExpansionJobTest#test_reports_a_truncated_rewrite_to_the_reader:
NoMethodError: undefined method 'stub' for class ExpansionProcessor
```

### GREEN

Command:
```
bin/rails test test/services/expansion_processor_test.rb test/jobs/generate_expansion_job_test.rb
```

Output (after implementation), run 3x with different random seeds and file orderings to rule out load-order/global-state flakiness:
```
Run options: --seed 39868
16 runs, 44 assertions, 0 failures, 0 errors, 0 skips

Run options: --seed 28323 (file order reversed)
16 runs, 44 assertions, 0 failures, 0 errors, 0 skips

Run options: --seed 59216
16 runs, 44 assertions, 0 failures, 0 errors, 0 skips
```

Full suite:
```
bin/rails test
251 runs, 622 assertions, 0 failures, 0 errors, 0 skips
```

All pre-existing `create_new` tests pass unmodified, alongside the six new `edit_in_place` tests in `expansion_processor_test.rb` and the one new test in `generate_expansion_job_test.rb`.

## Deviation from the brief's illustrative code (investigated, not guessed)

The brief's exact test code for `generate_expansion_job_test.rb` calls `ExpansionProcessor.stub(:process, ...)`. This app's Gemfile.lock pins **minitest 6.0.6**, which no longer ships `Minitest::Mock` (it was extracted out of core) — so `Object#stub` does not exist by default anywhere in this codebase. The existing `test/services/claude_expand_service_test.rb` (from Task 5) works around this with a local polyfill at the top of the file:

```ruby
unless Object.method_defined?(:stub)
  class Object
    def stub(method_name, callable, &block)
      singleton_class.define_method(method_name) do |*args, &method_block|
        callable.call(*args, &method_block)
      end
      block.call
    ensure
      singleton_class.remove_method(method_name)
    end
  end
end
```

I initially copied this verbatim into `generate_expansion_job_test.rb` (since running only the two files named in the brief's Step 5 command never loads `claude_expand_service_test.rb`, so relying on load order to provide `.stub` would make the specified test command fail non-deterministically depending on which files/order are run). That copy caused a *new*, more serious failure: `ExpansionProcessor.process` is defined directly on `ExpansionProcessor`'s own singleton class (`def self.process(expansion)`), with no ancestor/module to fall back to. The polyfill's `ensure` block unconditionally does `singleton_class.remove_method(method_name)` — for `@service.stub(:run_command, ...)` in the Task 5 test file this is safe (instance-level singleton override, real method lives untouched on the class), but for `ExpansionProcessor.stub(:process, ...)` it **permanently deletes** `ExpansionProcessor.process` for the rest of the test process, breaking every subsequent test that calls it (`with_processor`'s `ExpansionProcessor.method(:process)` — used by the pre-existing `create_new` job tests — then raised `TypeError: wrong argument type NilClass`).

I fixed this by changing the polyfill in `generate_expansion_job_test.rb` only (not touching `claude_expand_service_test.rb`, out of this task's scope) to capture the original method (if any) via `singleton_class.instance_method(method_name)` before overriding, and restore it with `singleton_class.define_method(method_name, original)` in the `ensure`, instead of leaving it removed. Verified stable across 3 runs with random seeds and reversed file order, plus the full 251-test suite.

## Self-review checklist (from the task instructions)

- **Source file byte-identical after edit_in_place?** Yes — `rewrite_in_place` never calls `file_path.write` or reads `latest_source`; only `version_path.write(anchored, ...)` is called. Verified by test: `assert_equal "Alpha beta gamma.", @files_dir.join("notes.md").read` after processing.
- **Lock still taken despite source not being written?** Yes — `with_source_lock(file_path) { ... }` wraps the version-path resolution and write, exactly as instructed, to serialize version-number allocation across concurrent expansions of the same file family.
- **`insert_anchor` keeps first sentinel, drops rest?** Yes — verified by the "keeps only the first anchor sentinel" test: two sentinels in, only the first becomes `<a id="expansion-anchor"></a>`, the second vanishes without a trace (not converted to anything, not left as literal text).
- **Truncation ratio correct (0.5)?** Yes — `MIN_REWRITE_RATIO = 0.5`, check is `rewritten.length < source.length * MIN_REWRITE_RATIO`. Verified by test with a 51-char source and a 6-char rewrite raising `TruncatedRewrite`, and confirmed no version file was written and the source was untouched.

## Concerns

None blocking. The one substantive concern is the polyfill deviation documented above — it's a necessary correctness fix (not a stylistic choice) to make the brief's exact `ExpansionProcessor.stub(:process, ...)` test code actually pass reliably, since the naive copy-paste polyfill silently corrupts global class state in a way that only shows up depending on test run order/composition.

## Fix: load-order-dependent test-infrastructure fragility (reviewer finding)

### What was wrong

Both `test/services/claude_expand_service_test.rb` (Task 5) and `test/jobs/generate_expansion_job_test.rb` (this task) defined `Object#stub`, each gated by `unless Object.method_defined?(:stub)`. The two implementations were not interchangeable:

- `claude_expand_service_test.rb`'s version unconditionally did `singleton_class.remove_method(method_name)` in `ensure` — safe for the instance-level stub it uses on `@service`, but destructive for any class-level singleton method with no ancestor to fall back to.
- `generate_expansion_job_test.rb`'s version (from the deviation above) captured the original method first and restored it in `ensure` — safe for both cases.

Because the guard was `unless Object.method_defined?(:stub)`, whichever file loaded first "won" globally for the rest of the process. This happened to work because `test/jobs/` sorts before `test/services/` alphabetically, so the safe version loaded first — but that's incidental, not structural. Anything that changed load order (file rename, a new test directory sorting earlier and also defining a naive `.stub`, explicitly running `test/services/...` before `test/jobs/...`) could silently reinstate the unsafe polyfill.

Investigating further while fixing this exposed the finding was not hypothetical: `claude_expand_service_test.rb` itself stubs a class-level method three times (`Net::HTTP.stub(:start, ...)`), and `Net::HTTP.start` is defined directly on `Net::HTTP`'s own singleton class with no ancestor fallback — the exact same failure shape as `ExpansionProcessor.process`. Running the full suite with `--seed 36977` reproduced it for real (independent of the job-test race, since this file's own `.stub` was the one active):

```
FilesControllerTest#test_allows_filenames_that_merely_resemble_the_reserved_suffix:
NoMethodError: undefined method 'start' for class Net::HTTP
    app/services/gemini_formatter.rb:35:in 'GeminiFormatter#format'
```

### Fix

1. **`test/jobs/generate_expansion_job_test.rb`**: renamed the helper from `Object#stub` to `Object#stub_class_method` (guard changed to `unless Object.method_defined?(:stub_class_method)`), and updated its one call site (`ExpansionProcessor.stub(:process, ...)` → `ExpansionProcessor.stub_class_method(:process, ...)`). This removes the same-name collision entirely — no load-order dependency remains for this file's helper, and it can no longer be shadowed by, or shadow, any `Object#stub` defined elsewhere.
2. **`test/services/claude_expand_service_test.rb`**: touched despite the instruction to avoid it unless necessary, because the full-suite run above proved it necessary to fully close the race — this file's own naive `.stub` was independently unsafe for its own `Net::HTTP.stub(:start, ...)` call sites, regardless of the job-test rename. Replaced the unconditional `remove_method` with the same capture-and-restore pattern already used in the job test (capture `singleton_class.instance_method(method_name)` if defined, restore it in `ensure` instead of leaving the method deleted). Kept the method named `stub` here since nothing else in the suite defines that name anymore after the rename in (1), so no collision risk remains.

### Why this fully closes the race

After the fix there is exactly one file defining `Object#stub` (`claude_expand_service_test.rb`, now safe for both instance- and class-level targets) and exactly one file defining `Object#stub_class_method` (`generate_expansion_job_test.rb`, distinct name, can't collide). No two files define the same method name with different semantics, so there is no load-order-dependent behavior left to depend on.

### Tests run

```
bin/rails test test/services/expansion_processor_test.rb test/jobs/generate_expansion_job_test.rb
Run options: --seed 11593
16 runs, 44 assertions, 0 failures, 0 errors, 0 skips

bin/rails test test/services/claude_expand_service_test.rb
Run options: --seed 15743
21 runs, 65 assertions, 0 failures, 0 errors, 0 skips

# Re-run with the exact seed that reproduced the Net::HTTP.start bug pre-fix:
bin/rails test --seed 36977
Run options: --seed 36977
251 runs, 622 assertions, 0 failures, 0 errors, 0 skips

# Full suite, fresh random seed:
bin/rails test
Run options: --seed 33273
251 runs, 622 assertions, 0 failures, 0 errors, 0 skips

# Explicit order named in the reviewer finding as a risk scenario:
bin/rails test test/services/claude_expand_service_test.rb test/jobs/generate_expansion_job_test.rb
Run options: --seed 47541
25 runs, 71 assertions, 0 failures, 0 errors, 0 skips
```

All pass. Files changed: `/Users/grillermo/c/serve-html-markdown/test/jobs/generate_expansion_job_test.rb`, `/Users/grillermo/c/serve-html-markdown/test/services/claude_expand_service_test.rb`.
