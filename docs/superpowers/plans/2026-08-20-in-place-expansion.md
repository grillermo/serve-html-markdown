# In-Place Expansion and File Versions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an `edit in place` expansion mode that has the LLM rewrite the whole document into a new versioned file, with bottom-of-page arrows to move between versions.

**Architecture:** A dropdown in the expansion sheet picks a mode, stored per-user. `create_new` keeps today's behavior untouched. `edit_in_place` sends the whole document to the LLM, writes the rewritten result to `foo--v2.md` (never modifying the source), and navigates the reader to that new file at the anchor the model marked. Version families are derived from filenames by a single value object; the arrows are rendered client-side from data the controller injects, so Markdown and HTML pages share one implementation.

**Tech Stack:** Rails 8.1, Postgres, Minitest, Commonmarker, plain ES5-style JS in `app/assets/javascripts/expand.js` (no build step, no JS test harness).

**Spec:** `docs/superpowers/specs/2026-08-20-in-place-expansion-design.md`

## Global Constraints

- Version numbers start at **2**. `foo.md` is v1; there is never a `foo--v1.md` written by this feature.
- Reserved-suffix rejection message, verbatim: `Filenames may not use the reserved --v<number> suffix.`
- Truncation failure message, verbatim: `Rewrite looked truncated.`
- Anchor sentinel, verbatim: `⟦EXPANSION_ANCHOR⟧` (U+27E6 / U+27E7 mathematical white square brackets).
- Anchor element id, verbatim: `expansion-anchor`.
- Minimum rewrite length ratio: `0.5` of the source's character length.
- Mode values, verbatim: `create_new`, `edit_in_place`.
- The source file is **never** modified in `edit_in_place` mode.
- `create_new` behavior must stay byte-identical — existing tests in `test/services/expansion_processor_test.rb` must keep passing unmodified.
- Run tests with `bin/rails test <path>`. Run the whole suite with `bin/rails test`.
- Tests swap `FILES_DIR` constants rather than using the real `files/` directory. Follow the existing `swap_constant`/`restore_constants` idiom in `test/services/expansion_processor_test.rb`.

---

### Task 1: FileVersions value object

**Files:**
- Create: `app/services/file_versions.rb`
- Test: `test/services/file_versions_test.rb`

**Interfaces:**
- Consumes: `ServedFile` (existing model, `name` column).
- Produces:
  - `FileVersions.parse(basename) -> FileVersions` — parses `"foo--v3.md"` into stem/version/extension.
  - `FileVersions::RESERVED_SUFFIX` — `/--v\d+\z/`, matched against a filename **stem** (extension already removed).
  - `#stem -> String`, `#version -> Integer`, `#extension -> String`
  - `#base_name -> String` — the v1 filename, e.g. `"foo.md"`
  - `#name_for(version) -> String` — e.g. `name_for(4) == "foo--v4.md"`
  - `#family_names -> Array<String>` — every recorded name in the family, ordered v1 first
  - `#next_path(files_dir) -> Pathname` — first free version path, starting at 2

- [ ] **Step 1: Write the failing test**

Create `test/services/file_versions_test.rb`:

```ruby
require "test_helper"
require "tmpdir"

class FileVersionsTest < ActiveSupport::TestCase
  test "parses a bare filename as version 1" do
    parsed = FileVersions.parse("foo.md")

    assert_equal "foo", parsed.stem
    assert_equal 1, parsed.version
    assert_equal ".md", parsed.extension
    assert_equal "foo.md", parsed.base_name
  end

  test "parses a versioned filename" do
    parsed = FileVersions.parse("foo--v3.html")

    assert_equal "foo", parsed.stem
    assert_equal 3, parsed.version
    assert_equal ".html", parsed.extension
    assert_equal "foo.html", parsed.base_name
  end

  test "treats --v1 and padded versions as part of the stem" do
    assert_equal 1, FileVersions.parse("foo--v1.md").version
    assert_equal "foo--v1", FileVersions.parse("foo--v1.md").stem
    assert_equal "foo--v02", FileVersions.parse("foo--v02.md").stem
  end

  test "builds a name for a given version" do
    assert_equal "foo--v4.md", FileVersions.parse("foo--v2.md").name_for(4)
    assert_equal "foo.md", FileVersions.parse("foo--v2.md").name_for(1)
  end

  test "lists the family in version order from ServedFile" do
    %w[foo.md foo--v3.md foo--v2.md unrelated.md].each { |name| ServedFile.create!(name: name) }

    assert_equal %w[foo.md foo--v2.md foo--v3.md], FileVersions.parse("foo--v2.md").family_names
  end

  test "excludes family members with a different extension" do
    %w[foo.md foo--v2.html foo--v2.md].each { |name| ServedFile.create!(name: name) }

    assert_equal %w[foo.md foo--v2.md], FileVersions.parse("foo.md").family_names
  end

  test "escapes LIKE wildcards in the stem" do
    %w[a_b.md axb--v2.md a_b--v2.md].each { |name| ServedFile.create!(name: name) }

    assert_equal %w[a_b.md a_b--v2.md], FileVersions.parse("a_b.md").family_names
  end

  test "returns the first free version path starting at 2" do
    Dir.mktmpdir do |dir|
      files_dir = Pathname.new(dir)
      files_dir.join("foo.md").write("v1")
      files_dir.join("foo--v2.md").write("v2")

      assert_equal files_dir.join("foo--v3.md"), FileVersions.parse("foo.md").next_path(files_dir)
    end
  end

  test "next_path from a versioned file stays in the same family" do
    Dir.mktmpdir do |dir|
      files_dir = Pathname.new(dir)
      files_dir.join("foo--v2.md").write("v2")

      assert_equal files_dir.join("foo--v3.md"), FileVersions.parse("foo--v2.md").next_path(files_dir)
    end
  end

  test "RESERVED_SUFFIX matches any --v<digits> stem" do
    assert_match FileVersions::RESERVED_SUFFIX, "foo--v2"
    assert_match FileVersions::RESERVED_SUFFIX, "foo--v1"
    assert_match FileVersions::RESERVED_SUFFIX, "foo--v007"
    assert_no_match FileVersions::RESERVED_SUFFIX, "foo--version"
    assert_no_match FileVersions::RESERVED_SUFFIX, "foo-v2"
  end
end
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bin/rails test test/services/file_versions_test.rb`
Expected: FAIL with `NameError: uninitialized constant FileVersions`

- [ ] **Step 3: Write minimal implementation**

Create `app/services/file_versions.rb`:

```ruby
# Owns the `--v<N>` filename convention that groups a document with its
# rewritten versions. `foo.md` is v1; rewrites are written as `foo--v2.md`,
# `foo--v3.md`. Nothing else in the app should hardcode this suffix.
class FileVersions
  # Strict: only v2 and up are real versions, and no leading zeros. A file
  # literally named `foo--v1.md` is its own document, not a version of `foo.md`.
  VERSION_PATTERN = /\A(?<stem>.+?)--v(?<version>[2-9]|[1-9]\d+)\z/

  # Loose: what user-supplied filenames are forbidden from ending in, so nothing
  # posted can land in a version slot or look like one.
  RESERVED_SUFFIX = /--v\d+\z/

  FIRST_VERSION = 2

  attr_reader :stem, :version, :extension

  def self.parse(basename)
    name = File.basename(basename.to_s)
    extension = File.extname(name)
    full_stem = File.basename(name, extension)

    if (match = VERSION_PATTERN.match(full_stem))
      new(match[:stem], match[:version].to_i, extension)
    else
      new(full_stem, 1, extension)
    end
  end

  def initialize(stem, version, extension)
    @stem = stem
    @version = version
    @extension = extension
  end

  def base_name
    "#{stem}#{extension}"
  end

  def name_for(version)
    version <= 1 ? base_name : "#{stem}--v#{version}#{extension}"
  end

  def family_names
    ServedFile
      .where(name: base_name)
      .or(ServedFile.where("name LIKE ? ESCAPE '\\'", "#{escaped_stem}--v%#{extension}"))
      .pluck(:name)
      .select { |name| self.class.parse(name).stem == stem }
      .sort_by { |name| self.class.parse(name).version }
  end

  def next_path(files_dir)
    candidate_version = FIRST_VERSION
    loop do
      candidate = Pathname.new(files_dir).join(name_for(candidate_version))
      return candidate unless candidate.exist?

      candidate_version += 1
    end
  end

  private
    def escaped_stem
      stem.gsub(/[\\%_]/) { |char| "\\#{char}" }
    end
end
```

- [ ] **Step 4: Run test to verify it passes**

Run: `bin/rails test test/services/file_versions_test.rb`
Expected: PASS, 10 runs, 0 failures

Note: the `select` after `pluck` is what makes the extension and wildcard tests pass — the `LIKE` narrows the query, and re-parsing each name confirms the stem matches exactly.

- [ ] **Step 5: Commit**

```bash
git add app/services/file_versions.rb test/services/file_versions_test.rb
git commit -m "feat: add FileVersions to derive version families from filenames"
```

---

### Task 2: Reject the reserved suffix on posted filenames

**Files:**
- Modify: `app/controllers/files_controller.rb` (`unique_file_path`, around line 139)
- Test: `test/controllers/files_controller_test.rb`

**Interfaces:**
- Consumes: `FileVersions::RESERVED_SUFFIX` from Task 1.
- Produces: nothing new. Both `files#create` and `files#upload` already call `unique_file_path`, so one guard covers both endpoints.

- [ ] **Step 1: Write the failing test**

Add these tests to `test/controllers/files_controller_test.rb`, just before the `private` keyword:

```ruby
  test "rejects a created file whose name uses the reserved version suffix" do
    with_env "API_TOKEN", "create-token" do
      post "/file/new",
        params: { content: "# Hi", filename: "report--v2.md" },
        headers: { "Authorization" => "Bearer create-token" }
    end

    assert_response :bad_request
    assert_equal(
      { "detail" => "Filenames may not use the reserved --v<number> suffix." },
      response.parsed_body
    )
    assert_empty @files_dir.children
  end

  test "rejects an uploaded file whose name uses the reserved version suffix" do
    with_env "API_TOKEN", "upload-token" do
      post "/file/upload",
        params: { file: markdown_upload("# Hi", "report--v10.md") },
        headers: { "Authorization" => "Bearer upload-token" }
    end

    assert_response :bad_request
    assert_equal(
      { "detail" => "Filenames may not use the reserved --v<number> suffix." },
      response.parsed_body
    )
    assert_empty @files_dir.children
  end

  test "allows filenames that merely resemble the reserved suffix" do
    with_env "API_TOKEN", "create-token" do
      post "/file/new",
        params: { content: "# Hi", filename: "report-v2.md" },
        headers: { "Authorization" => "Bearer create-token" }
    end

    assert_response :success
    assert @files_dir.join("report-v2.md").exist?
  end
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bin/rails test test/controllers/files_controller_test.rb -n "/reserved version suffix/"`
Expected: FAIL — the first two tests get `:success` instead of `:bad_request`, because the guard does not exist yet.

- [ ] **Step 3: Write minimal implementation**

In `app/controllers/files_controller.rb`, inside `unique_file_path`, add the guard immediately after the existing blank-stem check:

```ruby
    def unique_file_path(filename, extension: ".md")
      normalized = filename.to_s.tr("\\", "/")
      basename = File.basename(normalized)
      stem = File.basename(basename, File.extname(basename))
      raise ActionController::BadRequest, "Invalid filename." if stem.blank? || %w[. ..].include?(stem)

      if stem.match?(FileVersions::RESERVED_SUFFIX)
        raise ActionController::BadRequest, "Filenames may not use the reserved --v<number> suffix."
      end

      counter = 0
      loop do
        suffix = counter.zero? ? "" : "-#{counter}"
        candidate = FILES_DIR.join("#{stem}#{suffix}#{extension}").expand_path
        root_prefix = "#{FILES_DIR}#{File::SEPARATOR}"
        raise ActionController::BadRequest, "Invalid filename." unless candidate.to_s.start_with?(root_prefix)

        return candidate unless candidate.exist?

        counter += 1
      end
    end
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `bin/rails test test/controllers/files_controller_test.rb`
Expected: PASS, all existing tests still green.

- [ ] **Step 5: Commit**

```bash
git add app/controllers/files_controller.rb test/controllers/files_controller_test.rb
git commit -m "feat: reject the reserved --v<number> suffix on posted filenames"
```

---

### Task 3: Expansion mode and fallback anchor columns

**Files:**
- Create: `db/migrate/20260820120000_add_mode_to_expansions.rb`
- Modify: `app/models/expansion.rb`
- Modify: `app/controllers/expansions_controller.rb`
- Test: `test/models/expansion_test.rb`, `test/controllers/expansions_controller_test.rb`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `Expansion::MODES` — `%w[create_new edit_in_place]`
  - `Expansion#mode -> String`, `Expansion#edit_in_place? -> Boolean`
  - `Expansion#fallback_anchor -> String | nil` — the id of the nearest element preceding the reader's selection, used as a scroll fallback.
  - `ExpansionsController#create` accepts `mode` and `fallback_anchor` params.

- [ ] **Step 1: Write the failing test**

Add to `test/models/expansion_test.rb`:

```ruby
  test "defaults to the create_new mode" do
    expansion = User.create!(email: "mode@example.com", password: "s3cretpass")
      .expansions.create!(file_name: "notes.md", selected_text: "beta", question: "why?")

    assert_equal "create_new", expansion.mode
    assert_not expansion.edit_in_place?
  end

  test "rejects an unknown mode" do
    expansion = User.create!(email: "badmode@example.com", password: "s3cretpass")
      .expansions.build(file_name: "notes.md", selected_text: "beta", question: "why?", mode: "nonsense")

    assert_not expansion.valid?
    assert_includes expansion.errors[:mode], "is not included in the list"
  end
```

Add to `test/controllers/expansions_controller_test.rb`, before the `private` keyword:

```ruby
  test "stores the requested mode and fallback anchor" do
    write_file "notes.md", "Alpha beta gamma."

    post "/expansions", params: {
      file_name: "notes.md", selected_text: "beta", occurrence: 0, question: "why?",
      mode: "edit_in_place", fallback_anchor: "section-two"
    }, as: :json

    assert_response :accepted
    expansion = @user.expansions.find(response.parsed_body.fetch("id"))
    assert_equal "edit_in_place", expansion.mode
    assert_equal "section-two", expansion.fallback_anchor
  end

  test "returns 400 for an unknown mode" do
    write_file "notes.md", "Alpha beta gamma."

    post "/expansions", params: {
      file_name: "notes.md", selected_text: "beta", occurrence: 0, question: "why?", mode: "nonsense"
    }, as: :json

    assert_response :bad_request
    assert_equal({ "detail" => "Unknown mode." }, response.parsed_body)
  end
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `bin/rails test test/models/expansion_test.rb test/controllers/expansions_controller_test.rb`
Expected: FAIL with `NoMethodError: undefined method 'mode'` / unknown attribute errors.

- [ ] **Step 3: Write the migration and run it**

Create `db/migrate/20260820120000_add_mode_to_expansions.rb`:

```ruby
class AddModeToExpansions < ActiveRecord::Migration[8.1]
  def change
    add_column :expansions, :mode, :string, null: false, default: "create_new"
    add_column :expansions, :fallback_anchor, :string
  end
end
```

Run: `bin/rails db:migrate`

- [ ] **Step 4: Write the model and controller changes**

In `app/models/expansion.rb`, add the constant next to `STATUSES` and the validation and predicate:

```ruby
  STATUSES = %w[pending processing completed failed].freeze
  MODES = %w[create_new edit_in_place].freeze
```

```ruby
  validates :status, inclusion: { in: STATUSES }
  validates :mode, inclusion: { in: MODES }

  def edit_in_place?
    mode == "edit_in_place"
  end
```

In `app/controllers/expansions_controller.rb#create`, validate the mode after the existing blank check and pass both new attributes through:

```ruby
    mode = params[:mode].presence || "create_new"
    unless Expansion::MODES.include?(mode)
      render json: { detail: "Unknown mode." }, status: :bad_request
      return
    end

    expansion = current_user.expansions.create!(
      file_name: file_name,
      selected_text: selected_text,
      occurrence: [params[:occurrence].to_i, 0].max,
      question: question,
      mode: mode,
      fallback_anchor: params[:fallback_anchor].presence,
      use_openai: ActiveModel::Type::Boolean.new.cast(params[:use_openai]) || false
    )
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `bin/rails test test/models/expansion_test.rb test/controllers/expansions_controller_test.rb`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add db/migrate/20260820120000_add_mode_to_expansions.rb db/schema.rb app/models/expansion.rb app/controllers/expansions_controller.rb test/models/expansion_test.rb test/controllers/expansions_controller_test.rb
git commit -m "feat: add mode and fallback_anchor to expansions"
```

---

### Task 4: Remember the mode on the user

**Files:**
- Create: `db/migrate/20260820120100_add_expansion_mode_to_users.rb`
- Modify: `app/models/user.rb`
- Modify: `app/controllers/expansions_controller.rb`
- Test: `test/models/user_test.rb`, `test/controllers/expansions_controller_test.rb`

**Interfaces:**
- Consumes: `Expansion::MODES` from Task 3.
- Produces: `User#expansion_mode -> String` (default `"create_new"`), written back on every expansion whose mode differs.

- [ ] **Step 1: Write the failing test**

Add to `test/models/user_test.rb`:

```ruby
  test "defaults the remembered expansion mode to create_new" do
    user = User.create!(email: "prefs@example.com", password: "s3cretpass")

    assert_equal "create_new", user.expansion_mode
  end

  test "rejects an unknown remembered expansion mode" do
    user = User.new(email: "badprefs@example.com", password: "s3cretpass", expansion_mode: "nonsense")

    assert_not user.valid?
  end
```

Add to `test/controllers/expansions_controller_test.rb`, before the `private` keyword:

```ruby
  test "remembers the submitted mode on the user" do
    write_file "notes.md", "Alpha beta gamma."

    post "/expansions", params: {
      file_name: "notes.md", selected_text: "beta", occurrence: 0, question: "why?",
      mode: "edit_in_place"
    }, as: :json

    assert_response :accepted
    assert_equal "edit_in_place", @user.reload.expansion_mode
  end
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `bin/rails test test/models/user_test.rb test/controllers/expansions_controller_test.rb -n "/expansion mode/"`
Expected: FAIL with `NoMethodError: undefined method 'expansion_mode'`

- [ ] **Step 3: Write the migration and run it**

Create `db/migrate/20260820120100_add_expansion_mode_to_users.rb`:

```ruby
class AddExpansionModeToUsers < ActiveRecord::Migration[8.1]
  def change
    add_column :users, :expansion_mode, :string, null: false, default: "create_new"
  end
end
```

Run: `bin/rails db:migrate`

- [ ] **Step 4: Write the model and controller changes**

In `app/models/user.rb`:

```ruby
  validates :expansion_mode, inclusion: { in: Expansion::MODES }
```

In `app/controllers/expansions_controller.rb#create`, after the expansion is created:

```ruby
    current_user.update_column(:expansion_mode, mode) if current_user.expansion_mode != mode
```

`update_column` is deliberate: the preference is incidental to the request and must not run validations or touch `updated_at` on a Devise user record mid-request.

- [ ] **Step 5: Run tests to verify they pass**

Run: `bin/rails test test/models/user_test.rb test/controllers/expansions_controller_test.rb`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add db/migrate/20260820120100_add_expansion_mode_to_users.rb db/schema.rb app/models/user.rb app/controllers/expansions_controller.rb test/models/user_test.rb test/controllers/expansions_controller_test.rb
git commit -m "feat: remember the expansion mode on the user"
```

---

### Task 5: Whole-document rewrite in ClaudeExpandService

**Files:**
- Modify: `app/services/claude_expand_service.rb`
- Test: `test/services/claude_expand_service_test.rb`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `ClaudeExpandService.rewrite(file_name:, document:, selection:, question:, use_openai: false, expansion: nil) -> String` — the complete rewritten document in the source's own format. `.expand` keeps its exact current signature and behavior.

**Why this task exists:** every provider path currently ends in `ensure_html`, which would reject a valid Markdown rewrite. The runner has to take its validator as a parameter.

- [ ] **Step 1: Write the failing test**

Add to `test/services/claude_expand_service_test.rb`:

```ruby
  test "rewrite returns markdown unchanged by the html check" do
    markdown = "# Notes\n\nAlpha beta gamma.\n"
    service = ClaudeExpandService.new
    service.define_singleton_method(:run_claude) { |_prompt| markdown }

    assert_equal markdown, service.rewrite(
      file_name: "notes.md", document: "# Notes\n", selection: "beta", question: "why?"
    )
  end

  test "rewrite still requires html for an html source" do
    service = ClaudeExpandService.new
    service.define_singleton_method(:run_claude) { |_prompt| "not markup" }

    assert_raises(ClaudeExpandService::Error) do
      service.rewrite(file_name: "notes.html", document: "<html></html>", selection: "beta", question: "why?")
    end
  end

  test "rewrite rejects a blank document" do
    service = ClaudeExpandService.new
    service.define_singleton_method(:run_claude) { |_prompt| "   \n" }

    assert_raises(ClaudeExpandService::Error) do
      service.rewrite(file_name: "notes.md", document: "# Notes\n", selection: "beta", question: "why?")
    end
  end

  test "rewrite prompt asks for the anchor sentinel and the original format" do
    captured = nil
    service = ClaudeExpandService.new
    service.define_singleton_method(:run_claude) { |prompt| captured = prompt; "# ok\n" }

    service.rewrite(file_name: "notes.md", document: "# Notes\n", selection: "beta", question: "why?")

    assert_includes captured, "⟦EXPANSION_ANCHOR⟧"
    assert_includes captured, "beta"
    assert_includes captured, "why?"
  end
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `bin/rails test test/services/claude_expand_service_test.rb -n "/rewrite/"`
Expected: FAIL with `NoMethodError: undefined method 'rewrite'`

- [ ] **Step 3: Refactor the runner to take a validator, and add rewrite**

In `app/services/claude_expand_service.rb`, add the rewrite template next to `PROMPT_TEMPLATE`:

```ruby
  ANCHOR_SENTINEL = "⟦EXPANSION_ANCHOR⟧"

  REWRITE_PROMPT_TEMPLATE = <<~PROMPT
    You are given a document, a text selection from it, and a reader's question about that selection.

    Rewrite the document so the selected passage is expanded in light of the question: add the background, context, related concepts, and concrete details the original leaves out, woven into the document's own voice.

    Requirements:
    - Output the COMPLETE document, from its first line to its last. Never truncate, summarize, or elide with "...".
    - Keep the document's original format exactly: Markdown stays Markdown, HTML stays HTML.
    - Preserve every part of the document unrelated to the selection verbatim, including front matter, links, and code blocks.
    - Immediately before the expanded passage, emit the marker #{ANCHOR_SENTINEL} on a line of its own. Emit it exactly once.
    - Output ONLY the document. No markdown fences, no commentary.

    <document filename="%{file_name}">
    %{document}
    </document>

    <selection>
    %{selection}
    </selection>

    <question>
    %{question}
    </question>
  PROMPT
```

Add the class-level entry point next to `def self.expand`:

```ruby
  def self.expand(**kwargs) = new.expand(**kwargs)
  def self.rewrite(**kwargs) = new.rewrite(**kwargs)
```

Replace the body of `expand` with a delegation to a shared `generate`, and add `rewrite` beside it:

```ruby
  def expand(file_name:, document:, selection:, question:, use_openai: false, expansion: nil)
    generate(
      template: PROMPT_TEMPLATE, validator: method(:ensure_html),
      file_name:, document:, selection:, question:, use_openai:, expansion:
    )
  end

  def rewrite(file_name:, document:, selection:, question:, use_openai: false, expansion: nil)
    validator = File.extname(file_name).downcase == ".html" ? method(:ensure_html) : method(:ensure_present)
    generate(
      template: REWRITE_PROMPT_TEMPLATE, validator:,
      file_name:, document:, selection:, question:, use_openai:, expansion:
    )
  end

  private
    def generate(template:, validator:, file_name:, document:, selection:, question:, use_openai:, expansion:)
      @validator = validator
      prompt = format(template, file_name:, document:, selection:, question:)
      Rails.logger.info "[ClaudeExpandService] generating file=#{file_name} selection_bytes=#{selection.bytesize} question_bytes=#{question.bytesize} use_openai=#{use_openai}"
      expansion&.stamp!(:llm_request_start)

      if use_openai
        html = run_openai(prompt)
        record_response(expansion, "openai", html)
        return html
      end

      html = run_claude(prompt)
      record_response(expansion, "claude", html)
      html
    rescue Error => error
      raise error if use_openai

      Rails.logger.warn "[ClaudeExpandService] claude failed, falling back to codex"
      expansion&.stamp!(:llm_first_failure)
      html = run_codex(prompt)
      record_response(expansion, "codex", html)
      html
    end
```

Every `ensure_html(strip_fence(...))` call inside `run_openai`, `run_claude`, and `run_codex` becomes `finish(...)`. There are exactly three. Add:

```ruby
    def finish(text)
      @validator.call(strip_fence(text))
    end

    def ensure_present(text)
      raise Error, "output was empty" if text.strip.empty?

      text
    end
```

So, for example, `run_claude`'s last expression becomes:

```ruby
      finish(parsed["result"].to_s)
```

and `run_openai`'s becomes `finish(text)`, and `run_codex`'s becomes `finish(File.read(output.path))`.

- [ ] **Step 4: Run the full service test file**

Run: `bin/rails test test/services/claude_expand_service_test.rb`
Expected: PASS — both the new `rewrite` tests and every pre-existing `expand` test. If an existing test fails, `expand`'s behavior changed and must be restored; it is a constraint that `create_new` stays byte-identical.

- [ ] **Step 5: Commit**

```bash
git add app/services/claude_expand_service.rb test/services/claude_expand_service_test.rb
git commit -m "feat: add whole-document rewrite mode to ClaudeExpandService"
```

---

### Task 6: The edit_in_place branch in ExpansionProcessor

**Files:**
- Modify: `app/services/expansion_processor.rb`
- Modify: `app/jobs/generate_expansion_job.rb`
- Test: `test/services/expansion_processor_test.rb`

**Interfaces:**
- Consumes: `FileVersions.parse(...).next_path(files_dir)` (Task 1), `Expansion#edit_in_place?` and `#fallback_anchor` (Task 3), `ClaudeExpandService.rewrite` (Task 5).
- Produces:
  - `ExpansionProcessor::TruncatedRewrite < StandardError` — raised when the rewrite is too short; `GenerateExpansionJob` reports its message to the reader.
  - `ExpansionProcessor::ANCHOR_ID` — `"expansion-anchor"`.
  - A completed `edit_in_place` expansion's URL: `/foo--v2.md?fallback=<id>#expansion-anchor` (the `?fallback=` part is omitted when the expansion has no `fallback_anchor`).

- [ ] **Step 1: Write the failing test**

Add to `test/services/expansion_processor_test.rb`, before the `private` keyword. Note the helper `with_rewriter`, added in the private section below:

```ruby
  test "writes the rewrite to the next version and leaves the source untouched" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")
    @expansion.update!(mode: "edit_in_place")
    rewritten = "Alpha ⟦EXPANSION_ANCHOR⟧beta, at length, gamma. Plus much more text here."

    with_rewriter(->(**) { rewritten }) do
      assert_equal "/notes--v2.md#expansion-anchor", ExpansionProcessor.process(@expansion)
    end

    assert_equal "Alpha beta gamma.", @files_dir.join("notes.md").read
    assert_equal(
      %(Alpha <a id="expansion-anchor"></a>beta, at length, gamma. Plus much more text here.),
      @files_dir.join("notes--v2.md").read
    )
    assert ServedFile.exists?(name: "notes--v2.md")
  end

  test "includes the fallback anchor in the completed url" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")
    @expansion.update!(mode: "edit_in_place", fallback_anchor: "section two")

    with_rewriter(->(**) { "Alpha ⟦EXPANSION_ANCHOR⟧beta expanded gamma, and then some more." }) do
      assert_equal "/notes--v2.md?fallback=section%20two#expansion-anchor", ExpansionProcessor.process(@expansion)
    end
  end

  test "rewrites a versioned file into the next version of the same family" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")
    @files_dir.join("notes--v2.md").write("Alpha beta gamma, expanded once already.")
    @expansion.update!(mode: "edit_in_place", file_name: "notes--v2.md")

    with_rewriter(->(**) { "Alpha ⟦EXPANSION_ANCHOR⟧beta expanded twice now, with more words." }) do
      assert_equal "/notes--v3.md#expansion-anchor", ExpansionProcessor.process(@expansion)
    end

    assert @files_dir.join("notes--v3.md").exist?
  end

  test "keeps only the first anchor sentinel" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")
    @expansion.update!(mode: "edit_in_place")

    with_rewriter(->(**) { "A ⟦EXPANSION_ANCHOR⟧B ⟦EXPANSION_ANCHOR⟧C with plenty of extra text." }) do
      ExpansionProcessor.process(@expansion)
    end

    assert_equal(
      %(A <a id="expansion-anchor"></a>B C with plenty of extra text.),
      @files_dir.join("notes--v2.md").read
    )
  end

  test "still writes a version when the model omits the sentinel" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")
    @expansion.update!(mode: "edit_in_place")

    with_rewriter(->(**) { "Alpha beta gamma, expanded without any marker at all." }) do
      assert_equal "/notes--v2.md#expansion-anchor", ExpansionProcessor.process(@expansion)
    end

    assert_equal "Alpha beta gamma, expanded without any marker at all.", @files_dir.join("notes--v2.md").read
  end

  test "refuses a rewrite shorter than half the source" do
    @files_dir.join("notes.md").write("Alpha beta gamma, a reasonably long document body.")
    @expansion.update!(mode: "edit_in_place")

    with_rewriter(->(**) { "Alpha." }) do
      error = assert_raises(ExpansionProcessor::TruncatedRewrite) { ExpansionProcessor.process(@expansion) }
      assert_equal "Rewrite looked truncated.", error.message
    end

    assert_not @files_dir.join("notes--v2.md").exist?
    assert_equal "Alpha beta gamma, a reasonably long document body.", @files_dir.join("notes.md").read
  end
```

And add the helper to the private section of that test file, next to `with_expander`:

```ruby
  def with_rewriter(callable)
    fake = Object.new
    fake.define_singleton_method(:rewrite, &callable)
    fake.define_singleton_method(:expand) { |**| raise "expand must not be called in edit_in_place mode" }
    swap_constant(ExpansionProcessor, :EXPANDER, fake)
    yield
  end
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `bin/rails test test/services/expansion_processor_test.rb`
Expected: FAIL — `NameError: uninitialized constant ExpansionProcessor::TruncatedRewrite`, and the mode tests fall through to the `create_new` path.

- [ ] **Step 3: Write the implementation**

In `app/services/expansion_processor.rb`, add the constants and error near the top of the class:

```ruby
  TruncatedRewrite = Class.new(StandardError)

  ANCHOR_SENTINEL = ClaudeExpandService::ANCHOR_SENTINEL
  ANCHOR_ID = "expansion-anchor"
  MIN_REWRITE_RATIO = 0.5
```

Split `process` so the existing body moves into `link_new_page` untouched:

```ruby
  def process
    file_path = resolve_file_path(@expansion.file_name)
    source = file_path.read(encoding: "UTF-8")
    @expansion.stamp!(:source_read)

    if @expansion.edit_in_place?
      rewrite_in_place(file_path, source)
    else
      link_new_page(file_path, source)
    end
  end
```

`link_new_page(file_path, source)` is the current code from the `html = EXPANDER.expand(...)` line to the end of `process`, moved verbatim into a private method taking those two arguments.

Add the new private methods:

```ruby
    def rewrite_in_place(file_path, source)
      rewritten = EXPANDER.rewrite(
        file_name: file_path.basename.to_s,
        document: source,
        selection: @expansion.selected_text,
        question: @expansion.question,
        use_openai: @expansion.use_openai,
        expansion: @expansion
      )

      if rewritten.length < source.length * MIN_REWRITE_RATIO
        raise TruncatedRewrite, "Rewrite looked truncated."
      end

      anchored = insert_anchor(rewritten)

      with_source_lock(file_path) do
        @expansion.stamp!(:lock_acquired)
        version_path = FileVersions.parse(file_path.basename.to_s).next_path(self.class::FILES_DIR)
        version_path.write(anchored, encoding: "UTF-8")
        @expansion.stamp!(:files_written)
        ServedFile.record(version_path.basename.to_s)
        version_url(version_path)
      end
    end

    # The model is asked for exactly one sentinel; keep the first and drop any
    # extras so the page can never carry a duplicate element id.
    def insert_anchor(document)
      return document unless document.include?(ANCHOR_SENTINEL)

      first, *rest = document.split(ANCHOR_SENTINEL)
      "#{first}<a id=\"#{ANCHOR_ID}\"></a>#{rest.join}"
    end

    # The fallback rides in the URL because the page reloads: the client cannot
    # keep it in memory across the navigation.
    def version_url(version_path)
      url = "/#{ERB::Util.url_encode(version_path.basename.to_s)}"
      if @expansion.fallback_anchor.present?
        url += "?fallback=#{ERB::Util.url_encode(@expansion.fallback_anchor)}"
      end
      "#{url}##{ANCHOR_ID}"
    end
```

The lock is still taken even though the source is never written: it serializes two concurrent expansions on the same file so they cannot claim the same version number.

- [ ] **Step 4: Report the truncation message to the reader**

In `app/jobs/generate_expansion_job.rb`, add the new error to the rescue clause that forwards `error.message`:

```ruby
  rescue SelectionLinker::Error, ActionController::BadRequest, ExpansionProcessor::TruncatedRewrite,
         ResolvesServedFiles::UnsupportedFile, ResolvesServedFiles::MissingFile => error
    expansion&.fail!(error.message)
```

Add to `test/jobs/generate_expansion_job_test.rb`:

```ruby
  test "reports a truncated rewrite to the reader" do
    expansion = @user.expansions.create!(
      file_name: "notes.md", selected_text: "beta", question: "why?", mode: "edit_in_place"
    )
    ExpansionProcessor.stub(:process, ->(*) { raise ExpansionProcessor::TruncatedRewrite, "Rewrite looked truncated." }) do
      GenerateExpansionJob.perform_now(expansion.id)
    end

    assert_equal "failed", expansion.reload.status
    assert_equal "Rewrite looked truncated.", expansion.error_detail
  end
```

If the existing job test file builds its user differently, match its setup rather than introducing a second style.

- [ ] **Step 5: Run tests to verify they pass**

Run: `bin/rails test test/services/expansion_processor_test.rb test/jobs/generate_expansion_job_test.rb`
Expected: PASS — including every pre-existing `create_new` test, unmodified.

- [ ] **Step 6: Commit**

```bash
git add app/services/expansion_processor.rb app/jobs/generate_expansion_job.rb test/services/expansion_processor_test.rb test/jobs/generate_expansion_job_test.rb
git commit -m "feat: rewrite documents into versioned files for in-place expansions"
```

---

### Task 7: One bootstrap for both render paths

**Files:**
- Create: `app/helpers/expand_bootstrap_helper.rb`
- Modify: `app/views/layouts/markdown.html.erb`
- Modify: `app/controllers/files_controller.rb` (`show`, `inject_expand_script`)
- Test: `test/controllers/files_controller_test.rb`

**Interfaces:**
- Consumes: `FileVersions#family_names` and `#version` (Task 1), `User#expansion_mode` (Task 4).
- Produces:
  - `ExpandBootstrapHelper#expand_bootstrap_tags(scroll_anchor:, file_versions:, expansion_mode:) -> ActiveSupport::SafeBuffer` — the csrf meta tag, the `window.__*` bootstrap script, and the deferred `expand.js` tag.
  - `window.__fileVersions` — `[{ "name": "foo.md", "url": "/foo.md", "version": 1, "current": true }, ...]`, ordered v1 first, `[]` when the file has no siblings.
  - `window.__expansionMode` — `"create_new"` or `"edit_in_place"`.

**Why this task exists:** the version bar has to appear on both raw-HTML documents (served by string injection) and Markdown documents (served through a layout). Injecting the data in one shared place lets Task 10 render the bar once instead of twice.

- [ ] **Step 1: Write the failing test**

Add to `test/controllers/files_controller_test.rb`, before the `private` keyword:

```ruby
  test "injects version data into a markdown page with siblings" do
    write_file "notes.md", "# Notes"
    write_file "notes--v2.md", "# Notes, expanded"
    ServedFile.record("notes.md")
    ServedFile.record("notes--v2.md")

    get "/notes.md"

    assert_response :success
    assert_match %r{window\.__fileVersions\s*=}, response.body
    assert_match %r{"name":"notes--v2\.md"}, response.body
    assert_match %r{"current":true}, response.body
    assert_match %r{window\.__expansionMode\s*=\s*"create_new"}, response.body
  end

  test "injects version data into an html page with siblings" do
    write_file "page.html", "<html><body>one</body></html>"
    write_file "page--v2.html", "<html><body>two</body></html>"
    ServedFile.record("page.html")
    ServedFile.record("page--v2.html")

    get "/page--v2.html"

    assert_response :success
    assert_match %r{window\.__fileVersions\s*=}, response.body
    assert_match %r{"name":"page\.html"}, response.body
    assert_match %r{"version":2,"current":true}, response.body
  end

  test "injects an empty version list for a file with no siblings" do
    write_file "solo.md", "# Solo"
    ServedFile.record("solo.md")

    get "/solo.md"

    assert_response :success
    assert_match %r{window\.__fileVersions\s*=\s*\[\]}, response.body
  end

  test "reflects the user's remembered expansion mode" do
    @user.update_column(:expansion_mode, "edit_in_place")
    write_file "notes.md", "# Notes"

    get "/notes.md"

    assert_match %r{window\.__expansionMode\s*=\s*"edit_in_place"}, response.body
  end
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `bin/rails test test/controllers/files_controller_test.rb -n "/version data|remembered expansion mode/"`
Expected: FAIL — `window.__fileVersions` appears nowhere in the response body.

- [ ] **Step 3: Write the helper**

Create `app/helpers/expand_bootstrap_helper.rb`:

```ruby
# The two render paths (a layout for Markdown, string injection for raw HTML)
# both need the same client bootstrap. Keeping it here means expand.js gets
# identical data whichever way the document was served.
module ExpandBootstrapHelper
  def expand_bootstrap_tags(scroll_anchor:, file_versions:, expansion_mode:)
    script = +""
    script << "window.__scrollAnchor = #{scroll_anchor.to_json};" if scroll_anchor.present?
    script << "window.__fileVersions = #{file_versions.to_json};"
    script << "window.__expansionMode = #{expansion_mode.to_json};"

    safe_join([
      tag.meta(name: "csrf-token", content: form_authenticity_token),
      tag.script(raw(script)),
      javascript_include_tag("expand", defer: true)
    ])
  end
end
```

`to_json` handles the escaping for every injected value, so nothing reader-controlled reaches the page unescaped.

- [ ] **Step 4: Build the version list in the controller**

In `app/controllers/files_controller.rb#show`, compute the shared data before branching:

```ruby
  def show
    file_path = resolve_file_path(params[:file_name])
    content = file_path.read(encoding: "UTF-8")
    @file_name = file_path.basename.to_s
    @scroll_position = current_user.scroll_positions.find_by(file_name: @file_name)&.anchor
    @file_versions = file_versions_for(@file_name)

    if file_path.extname.downcase == ".html"
      render html: inject_expand_script(content).html_safe, layout: false
    else
      @rendered = Commonmarker.to_html(content, options: MARKDOWN_OPTIONS)
      render :show, formats: :html, layout: "markdown"
    end
  end
```

And add the private builder:

```ruby
    def file_versions_for(name)
      names = FileVersions.parse(name).family_names
      return [] if names.length < 2

      names.map do |sibling|
        {
          name: sibling,
          url: "/#{ERB::Util.url_encode(sibling)}",
          version: FileVersions.parse(sibling).version,
          current: sibling == name
        }
      end
    end
```

Replace `inject_expand_script` with a call through the helper:

```ruby
    def inject_expand_script(content)
      snippet = helpers.expand_bootstrap_tags(
        scroll_anchor: @scroll_position,
        file_versions: @file_versions,
        expansion_mode: current_user.expansion_mode
      )
      if content =~ %r{</body>}i
        content.sub(%r{</body>}i) { "#{snippet}</body>" }
      else
        content + snippet
      end
    end
```

- [ ] **Step 5: Update the Markdown layout**

Replace the `csrf_meta_tags`, the `__scrollAnchor` script block, and the `javascript_include_tag` in `app/views/layouts/markdown.html.erb` with the single helper call:

```erb
    <%= stylesheet_link_tag "markdown" %>
    <%= expand_bootstrap_tags(
          scroll_anchor: @scroll_position,
          file_versions: @file_versions,
          expansion_mode: current_user.expansion_mode
        ) %>
  </head>
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `bin/rails test test/controllers/files_controller_test.rb`
Expected: PASS — including the pre-existing tests asserting the csrf meta tag, the scroll anchor script, and the `expand.js` script tag on both Markdown and HTML pages. Those existing assertions are the regression guard for this refactor; if one fails, the helper's output does not match what the two paths emitted before.

- [ ] **Step 7: Commit**

```bash
git add app/helpers/expand_bootstrap_helper.rb app/views/layouts/markdown.html.erb app/controllers/files_controller.rb test/controllers/files_controller_test.rb
git commit -m "refactor: share one client bootstrap between the markdown and html render paths"
```

---

### Task 8: Mode dropdown and fallback anchor in the expansion sheet

**Files:**
- Modify: `app/assets/javascripts/expand.js`

**Interfaces:**
- Consumes: `window.__expansionMode` (Task 7), the `mode` and `fallback_anchor` params (Task 3).
- Produces: each entry in the `jobs` map gains a `mode` property, which Task 9 reads to decide whether to navigate.

**No JS test harness exists in this repo.** These steps end in explicit manual verification rather than a test run. Do not skip the manual check.

- [ ] **Step 1: Add the fallback-anchor walker**

Add near `occurrenceIndex`:

```js
  // The reload lands on a rewritten document, so the only durable way back to
  // roughly where the reader was is the nearest id before their selection.
  function anchorBeforeSelection(range) {
    let node = range.startContainer;
    if (node.nodeType === Node.TEXT_NODE) node = node.parentElement;

    while (node && node !== document.body) {
      if (node.id) return node.id;
      let sibling = node.previousElementSibling;
      while (sibling) {
        if (sibling.id) return sibling.id;
        sibling = sibling.previousElementSibling;
      }
      node = node.parentElement;
    }
    return null;
  }
```

- [ ] **Step 2: Record it with the selection**

In `showButton`, extend the `currentSelection` object:

```js
    currentSelection = {
      text: text,
      occurrence: occurrenceIndex(range, text),
      fallbackAnchor: anchorBeforeSelection(range),
      range: range.cloneRange()
    };
```

- [ ] **Step 3: Add the dropdown to the sheet**

In `showPopover`, build the select just before the `actions` element is assembled:

```js
    const modeSelect = document.createElement("select");
    [
      { value: "create_new", label: "Create new page" },
      { value: "edit_in_place", label: "Edit in place" }
    ].forEach((option) => {
      const element = document.createElement("option");
      element.value = option.value;
      element.textContent = option.label;
      modeSelect.appendChild(element);
    });
    modeSelect.value = window.__expansionMode === "edit_in_place" ? "edit_in_place" : "create_new";
    modeSelect.setAttribute("aria-label", "Expansion mode");
    Object.assign(modeSelect.style, {
      minHeight: "44px",
      background: "#111",
      color: "#eee",
      border: "1px solid #444",
      borderRadius: "6px",
      padding: "0 8px",
      font: "inherit",
      fontSize: "16px" // keeps iOS from zooming the page on focus
    });
```

Then include it in the actions row:

```js
    actions.append(modeSelect, openaiLabel, submit);
```

- [ ] **Step 4: Send the new fields**

In the submit handler's `JSON.stringify` body, add two lines:

```js
        body: JSON.stringify({
          file_name: decodeURIComponent(location.pathname.slice(1)),
          selected_text: currentSelection.text,
          occurrence: currentSelection.occurrence,
          question: textarea.value,
          mode: modeSelect.value,
          fallback_anchor: currentSelection.fallbackAnchor,
          use_openai: openaiCheckbox.checked,
          client_clicked_at: Date.now()
        })
```

And carry the mode into the status bar so Task 9 can read it. Change the call site:

```js
          addStatusBar(jobId, currentSelection.text, currentSelection.range, modeSelect.value);
```

and the definition:

```js
  function addStatusBar(jobId, selection, range, mode) {
```

and the record it stores:

```js
    const record = { bar, content, timer: null, range: range, mode: mode };
```

- [ ] **Step 5: Verify manually**

Run `bin/rails server`, open a Markdown file, and select some text.

Confirm: the sheet shows a dropdown reading "Create new page"; switching it to "Edit in place", submitting, and reloading the page shows the dropdown now defaulting to "Edit in place" (the preference round-tripped through the user record). In the browser's network tab, the `POST /expansions` body includes `mode` and a non-null `fallback_anchor` when the selection sits under a heading.

- [ ] **Step 6: Commit**

```bash
git add app/assets/javascripts/expand.js
git commit -m "feat: add the expansion mode dropdown to the selection sheet"
```

---

### Task 9: Navigate to the rewritten version on completion

**Files:**
- Modify: `app/assets/javascripts/expand.js`

**Interfaces:**
- Consumes: the `mode` on each job record (Task 8), the completed URL shape `/foo--v2.md?fallback=<id>#expansion-anchor` (Task 6).
- Produces: no new interface.

- [ ] **Step 1: Navigate instead of linking for in-place jobs**

Change `renderCompleted` so an in-place job takes the reader to the new version rather than offering a link:

```js
  function renderCompleted(record, url) {
    if (record.mode === "edit_in_place") {
      record.content.textContent = "Rewrite ready — opening it";
      location.href = url;
      return;
    }

    const link = document.createElement("a");
    link.href = url;
    link.textContent = "Expansion ready — open it";
    link.style.color = "#bb86fc";
    record.content.replaceChildren(link);

    const anchor = linkOriginalSelection(record.range, url);
    if (anchor) anchor.scrollIntoView({ behavior: "smooth", block: "center" });
  }
```

The `create_new` branch is unchanged — in particular `linkOriginalSelection` must not run for an in-place job, since that path never links the source.

- [ ] **Step 2: Scroll to the anchor, then the fallback, on load**

Add above the existing `DOMContentLoaded` handler:

```js
  // A rewritten document arrives with #expansion-anchor in the URL. If the model
  // dropped the marker, ?fallback= carries the id nearest the reader's original
  // selection. Either beats the saved scroll position, which predates the rewrite.
  function scrollToRequestedTarget() {
    const candidates = [
      location.hash.slice(1),
      new URLSearchParams(location.search).get("fallback")
    ];

    for (const id of candidates) {
      if (!id) continue;
      const target = document.getElementById(id);
      if (target) {
        target.scrollIntoView();
        return true;
      }
    }
    return false;
  }
```

And make the existing handler defer to it:

```js
  document.addEventListener("DOMContentLoaded", () => {
    if (scrollToRequestedTarget()) return;
    if (typeof window.__scrollAnchor !== "string") return;

    const target = document.getElementById(window.__scrollAnchor);
    if (target) target.scrollIntoView();
  });
```

- [ ] **Step 3: Verify manually**

With the server running, select text in a Markdown file, choose "Edit in place", and submit. When the job finishes the browser should navigate to `/<name>--v2.md?...#expansion-anchor` and land on the expanded passage rather than the top of the page.

Then confirm the original file is untouched: `git diff files/` (or `cat files/<name>.md`) shows no change to the source, and `files/<name>--v2.md` exists alongside it.

Finally confirm the fallback: edit `files/<name>--v2.md` by hand to delete the `<a id="expansion-anchor"></a>` element, reload the same URL including its `?fallback=` param, and check the page still scrolls to that heading instead of the top.

- [ ] **Step 4: Commit**

```bash
git add app/assets/javascripts/expand.js
git commit -m "feat: open the rewritten version at the expanded passage"
```

---

### Task 10: Version navigation bar

**Files:**
- Modify: `app/assets/javascripts/expand.js`

**Interfaces:**
- Consumes: `window.__fileVersions` (Task 7) — `[{ name, url, version, current }]`, ordered v1 first, `[]` when there are no siblings.
- Produces: no new interface.

- [ ] **Step 1: Render the bar**

Add near the other UI builders:

```js
  let versionBar = null;

  function renderVersionBar() {
    const versions = Array.isArray(window.__fileVersions) ? window.__fileVersions : [];
    if (versions.length < 2) return;

    const index = versions.findIndex((version) => version.current);
    if (index === -1) return;

    versionBar = document.createElement("nav");
    versionBar.setAttribute("aria-label", "Document versions");
    Object.assign(versionBar.style, {
      position: "fixed", left: "0", right: "0", bottom: "0",
      display: "flex", alignItems: "center", justifyContent: "center", gap: "16px",
      padding: "8px 12px calc(8px + env(safe-area-inset-bottom))",
      background: "#1b1b1b", color: "#eee", borderTop: "1px solid #555",
      font: "14px system-ui, sans-serif", zIndex: "9998"
    });

    const label = document.createElement("span");
    label.textContent = `v${versions[index].version} of ${versions.length}`;

    versionBar.append(
      versionArrow("‹", "Previous version", versions[index - 1]),
      label,
      versionArrow("›", "Next version", versions[index + 1])
    );
    document.body.appendChild(versionBar);
  }

  function versionArrow(glyph, label, target) {
    const arrow = document.createElement("a");
    arrow.textContent = glyph;
    arrow.setAttribute("aria-label", label);
    Object.assign(arrow.style, {
      display: "flex", alignItems: "center", justifyContent: "center",
      minWidth: "44px", minHeight: "44px", textDecoration: "none",
      font: "22px/1 system-ui, sans-serif"
    });

    if (target) {
      arrow.href = target.url;
      arrow.style.color = "#bb86fc";
    } else {
      arrow.setAttribute("aria-disabled", "true");
      arrow.style.color = "#555";
      arrow.style.pointerEvents = "none";
    }
    return arrow;
  }
```

Call it from the existing `DOMContentLoaded` handler, after the scroll logic:

```js
  document.addEventListener("DOMContentLoaded", () => {
    renderVersionBar();

    if (scrollToRequestedTarget()) return;
    if (typeof window.__scrollAnchor !== "string") return;

    const target = document.getElementById(window.__scrollAnchor);
    if (target) target.scrollIntoView();
  });
```

- [ ] **Step 2: Keep it out of the sheet's way**

The expansion sheet is also fixed to the bottom edge. Its `zIndex` is `"9999"` against the bar's `"9998"`, but overlap still looks wrong on a short viewport, so hide the bar outright while the sheet is open.

At the end of `showPopover`, next to `trackKeyboard(popover)`:

```js
    if (versionBar) versionBar.style.display = "none";
```

And in `removeUI`:

```js
  function removeUI() {
    if (button) { button.remove(); button = null; }
    if (popover) { popover.remove(); popover = null; untrackKeyboard(); }
    if (versionBar) versionBar.style.display = "";
  }
```

- [ ] **Step 3: Verify manually**

With the server running:

- Open a file with no siblings — no bar appears.
- Open a file that has a `--v2` sibling — the bar reads `‹ v1 of 2 ›` with the left arrow dimmed and non-clickable, and the right arrow navigates to the v2 URL.
- On the v2 page the bar reads `v2 of 2` with the right arrow dimmed and the left arrow returning to v1.
- Selecting text hides the bar while the sheet is open, and closing the sheet with `×` or Escape brings it back.
- Repeat one of these on a raw `.html` document to confirm both render paths behave identically.

- [ ] **Step 4: Run the full suite**

Run: `bin/rails test`
Expected: PASS, no failures, no errors.

- [ ] **Step 5: Commit**

```bash
git add app/assets/javascripts/expand.js
git commit -m "feat: add version navigation arrows to versioned documents"
```

---

## Verification Checklist

After Task 10, confirm the whole feature end to end before calling it done:

- [ ] `bin/rails test` passes with no failures or errors.
- [ ] `create_new` mode produces the same result it did before this branch: a `--expand-N.html` page and a link spliced into the source.
- [ ] `edit_in_place` mode writes `--v2` and leaves the source byte-identical (`git diff` on the source file shows nothing).
- [ ] A second in-place expansion on the same document produces `--v3`, not a second `--v2`.
- [ ] `POST /file/new` and `POST /file/upload` both reject a `--v2` filename with the exact message from Global Constraints.
- [ ] The version arrows appear on both a Markdown and a raw HTML document.

## Known Consequences

These are accepted design outcomes, recorded in the spec — not bugs to fix:

- `/foo.md` permanently serves v1, so an already-saved link keeps showing pre-rewrite text.
- Each version appears as its own entry in `files#index` and can become `files#last`, rather than folding into its family.
