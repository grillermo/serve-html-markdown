# Task 3 Parallel-Test Fixture Fix

## Diagnosis

`FilesControllerTest#setup` previously created each served-files directory with
`Dir.mktmpdir("served-files")`. Although the served-files directory was unique,
its parent was the shared system temporary directory. The three traversal and
symlink tests all created `@files_dir.dirname.join("secret.md")` and deleted
that path in `ensure`, so parallel workers could delete another test's target.
That explains the reported symlink test returning 404 instead of 400.

The fix gives each test a unique `files-parent` directory, creates its unique
`served-files` directory inside that parent, and removes the parent in teardown.
Assertions, request paths, and production code are unchanged.

## Evidence and Commands

Working directory: `/Users/grillermo/c/serve-html-markdown`

- `sed -n '1,280p' test/controllers/files_controller_test.rb` confirmed the
  shared-parent `secret.md` fixture and `ensure` cleanup in all three tests.
- `bin/rails test test/controllers/files_controller_test.rb` before the fix:
  `19 tests in a single process`, `19 runs, 63 assertions, 0 failures, 0 errors,
  0 skips`.
- `bin/rails test` before the fix, seed `34159`: `51 tests in parallel using 8
  processes`, `51 runs, 147 assertions, 0 failures, 0 errors, 0 skips`.
- `bin/rails test` before the fix, seed `8508`: `51 tests in parallel using 8
  processes`, `51 runs, 147 assertions, 0 failures, 0 errors, 0 skips`.

The reported failing baseline run is the observed red case; the two local
pre-fix passes are expected for a latent race.

After the fix:

- `git diff --check`: passed.
- `bin/rails test test/controllers/files_controller_test.rb`, seed `58418`:
  `19 runs, 63 assertions, 0 failures, 0 errors, 0 skips`.
- `bin/rails test test/controllers/files_controller_test.rb`, seed `31281`:
  `19 runs, 63 assertions, 0 failures, 0 errors, 0 skips`.
- `bin/rails test`, seed `45244`: `51 tests in parallel using 8 processes`,
  `51 runs, 147 assertions, 0 failures, 0 errors, 0 skips`.
- `bin/rails test`, seed `6132`: `51 tests in parallel using 8 processes`,
  `51 runs, 147 assertions, 0 failures, 0 errors, 0 skips`.

## Files Changed

- `test/controllers/files_controller_test.rb`: isolated the temporary parent
  directory and updated teardown to remove it.
- `.superpowers/sdd/task-3-parallel-fix-report.md`: this report.

## Self-Review

- Scope is limited to the requested test fixture and report.
- The parent is retained in an instance variable so teardown removes the full
  temporary tree without leaving a parent-directory leak.
- Existing test semantics are preserved: `@files_dir` remains the served root,
  and `secret.md` remains its sibling for traversal/symlink coverage.
- No production files, assertions, or other worktree changes were modified.
