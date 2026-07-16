# Task 4 report: ExpansionsController + route

## Status

Complete. The endpoint is authenticated through `ApplicationController` and retains Rails CSRF protection.

## RED evidence

The integration test was added before the route or controller. It includes the binding amendment requiring a non-blank `file_name` and checks both blank and omitted values.

```text
$ bin/rails test test/controllers/expansions_controller_test.rb
Running 9 tests in a single process (parallelization threshold is 50)
Run options: --seed 45313

# Running:

E

Error:
ExpansionsControllerTest#test_returns_400_when_selected_text_or_question_is_missing:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:9:in 'block in <class:ExpansionsControllerTest>'

Error:
ExpansionsControllerTest#test_returns_400_when_selected_text_or_question_is_missing:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:19:in 'block in <class:ExpansionsControllerTest>'

bin/rails test test/controllers/expansions_controller_test.rb:88

E

Error:
ExpansionsControllerTest#test_returns_422_when_the_selection_is_not_in_the_source:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:9:in 'block in <class:ExpansionsControllerTest>'

Error:
ExpansionsControllerTest#test_returns_422_when_the_selection_is_not_in_the_source:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:19:in 'block in <class:ExpansionsControllerTest>'

bin/rails test test/controllers/expansions_controller_test.rb:73

E

Error:
ExpansionsControllerTest#test_requires_authentication:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:9:in 'block in <class:ExpansionsControllerTest>'

Error:
ExpansionsControllerTest#test_requires_authentication:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:19:in 'block in <class:ExpansionsControllerTest>'

bin/rails test test/controllers/expansions_controller_test.rb:129

E

Error:
ExpansionsControllerTest#test_returns_400_when_file_name_is_blank_or_missing:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:9:in 'block in <class:ExpansionsControllerTest>'

Error:
ExpansionsControllerTest#test_returns_400_when_file_name_is_blank_or_missing:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:19:in 'block in <class:ExpansionsControllerTest>'

bin/rails test test/controllers/expansions_controller_test.rb:96

E

Error:
ExpansionsControllerTest#test_increments_the_expansion_suffix:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:9:in 'block in <class:ExpansionsControllerTest>'

Error:
ExpansionsControllerTest#test_increments_the_expansion_suffix:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:19:in 'block in <class:ExpansionsControllerTest>'

bin/rails test test/controllers/expansions_controller_test.rb:60

E

Error:
ExpansionsControllerTest#test_returns_404_for_a_missing_file:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:9:in 'block in <class:ExpansionsControllerTest>'

Error:
ExpansionsControllerTest#test_returns_404_for_a_missing_file:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:19:in 'block in <class:ExpansionsControllerTest>'

bin/rails test test/controllers/expansions_controller_test.rb:106

E

Error:
ExpansionsControllerTest#test_rewrites_an_html_source_with_an_anchor:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:9:in 'block in <class:ExpansionsControllerTest>'

Error:
ExpansionsControllerTest#test_rewrites_an_html_source_with_an_anchor:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:19:in 'block in <class:ExpansionsControllerTest>'

bin/rails test test/controllers/expansions_controller_test.rb:47

E

Error:
ExpansionsControllerTest#test_generates_a_page_and_rewrites_a_markdown_source:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:9:in 'block in <class:ExpansionsControllerTest>'

Error:
ExpansionsControllerTest#test_generates_a_page_and_rewrites_a_markdown_source:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:19:in 'block in <class:ExpansionsControllerTest>'

bin/rails test test/controllers/expansions_controller_test.rb:23

E

Error:
ExpansionsControllerTest#test_returns_502_and_writes_nothing_when_generation_fails:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:9:in 'block in <class:ExpansionsControllerTest>'

Error:
ExpansionsControllerTest#test_returns_502_and_writes_nothing_when_generation_fails:
NameError: uninitialized constant ExpansionsControllerTest::ExpansionsController
    test/controllers/expansions_controller_test.rb:19:in 'block in <class:ExpansionsControllerTest>'

bin/rails test test/controllers/expansions_controller_test.rb:114



Finished in 0.023453s, 383.7462 runs/s, 0.0000 assertions/s.
9 runs, 0 assertions, 0 failures, 9 errors, 0 skips
```

## GREEN evidence

```text
$ bin/rails test test/controllers/expansions_controller_test.rb
Running 9 tests in a single process (parallelization threshold is 50)
Run options: --seed 63380

# Running:

.........

Finished in 0.114505s, 78.5992 runs/s, 200.8646 assertions/s.
9 runs, 23 assertions, 0 failures, 0 errors, 0 skips
```

## Full-suite evidence

```text
$ bin/rails test
Running 63 tests in parallel using 8 processes
Run options: --seed 45586

# Running:

...............................................................

Finished in 0.437974s, 143.8442 runs/s, 410.9833 assertions/s.
63 runs, 180 assertions, 0 failures, 0 errors, 0 skips
```

## File-name amendment

`params[:file_name].to_s` is validated as blank together with `selected_text` and `question`. A focused integration test verifies HTTP 400 for both an empty string and an omitted `file_name`.

## Files changed

- `app/controllers/expansions_controller.rb`: authenticated expansion orchestration and error responses.
- `config/routes.rb`: `POST /expansions` route immediately after `POST /file/new`.
- `test/controllers/expansions_controller_test.rb`: end-to-end coverage, including the file-name amendment.
- `.superpowers/sdd/task-4-report.md`: this report.

## Self-review

- `SelectionLinker.link` precedes `EXPANDER.expand`; an unlinkable selection produces 422 without calling the expander.
- The expansion HTML and rewritten source are only written after linking and generation both succeed; the generation-failure test verifies no writes.
- The route inherits Devise authentication and Rails CSRF protection from `ApplicationController`; neither is skipped.
- File resolution, extension restrictions, and symlink safety remain delegated to `ResolvesServedFiles`.
- Unique `--expand-N.html` suffixes and Markdown/HTML link rewriting are covered by integration tests.
- `git diff --check` passed before commit.

## Concerns

No implementation concerns. The controller writes the generated page before the rewritten source, so an operating-system write failure during the second write could leave the generated page present; this is outside the stated requirement, which requires no writes only when linking or generation fails.
