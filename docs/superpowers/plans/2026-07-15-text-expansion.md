# Text Expansion Feature Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Select text on a served page, ask a question, get an AI-generated HTML answer page, and turn the selection into a link to it.

**Architecture:** A vanilla-JS selection UI posts to a new `ExpansionsController`, which uses `SelectionLinker` (pure text rewriting) and `ClaudeExpandService` (shells out to `claude` CLI, falls back to `codex` CLI) and writes the generated page into `files/`. Path-safety logic is extracted from `FilesController` into a shared concern.

**Tech Stack:** Rails 8.1, Minitest, Devise, vanilla JS (no bundler), `claude` CLI (sonnet), `codex` CLI (earth, fallback).

**Spec:** `docs/superpowers/specs/2026-07-15-text-expansion-design.md`

**Conventions for this repo:**
- Run tests with `bin/rails test <path>`.
- Tests swap class constants (e.g. `FilesController::FILES_DIR`) to point at tmp dirs — follow that pattern.
- Commits fail if the 1Password signing agent is locked; if `git commit` errors with "1Password: agent returned an error", retry once, then use `git commit --no-gpg-sign`.

---

## Chunk 1: Backend units

### Task 1: Extract `ResolvesServedFiles` concern

Pure refactor — move path-resolution logic out of `FilesController` so `ExpansionsController` can reuse it. No behavior change; existing tests must stay green.

**Files:**
- Create: `app/controllers/concerns/resolves_served_files.rb`
- Modify: `app/controllers/files_controller.rb`
- Test: existing `test/controllers/files_controller_test.rb` (unchanged)

**Constant-lookup gotcha:** existing tests shadow `FilesController::FILES_DIR` with `const_set`. Methods defined inside the concern module resolve bare constants lexically (to the module), which would ignore the test override. Therefore the concern must reference constants via `self.class::FILES_DIR` / `self.class::ALLOWED_EXTENSIONS`, never bare.

- [ ] **Step 1: Run the existing suite to establish a green baseline**

Run: `bin/rails test`
Expected: all tests pass, 0 failures.

- [ ] **Step 2: Create the concern**

Create `app/controllers/concerns/resolves_served_files.rb`:

```ruby
module ResolvesServedFiles
  extend ActiveSupport::Concern

  FILES_DIR = Rails.root.join("files").expand_path
  ALLOWED_EXTENSIONS = %w[.html .md .markdown].freeze

  UnsupportedFile = Class.new(StandardError)
  MissingFile = Class.new(StandardError)

  FILES_DIR.mkpath

  private
    def resolve_file_path(file_name)
      files_dir = self.class::FILES_DIR
      file_path = files_dir.join(file_name).expand_path
      root_prefix = "#{files_dir}#{File::SEPARATOR}"

      unless file_path.to_s.start_with?(root_prefix)
        raise ActionController::BadRequest, "Invalid file path."
      end

      unless self.class::ALLOWED_EXTENSIONS.include?(file_path.extname.downcase)
        raise self.class::UnsupportedFile, "Only .html, .md, and .markdown files are supported."
      end

      raise self.class::MissingFile, "File not found: #{file_name}" unless file_path.file?

      resolved_path = file_path.realpath
      resolved_root_prefix = "#{files_dir.realpath}#{File::SEPARATOR}"
      unless resolved_path.to_s.start_with?(resolved_root_prefix)
        raise ActionController::BadRequest, "Invalid file path."
      end

      resolved_path
    end
end
```

- [ ] **Step 3: Slim `FilesController` to use the concern**

In `app/controllers/files_controller.rb`:

1. Add `include ResolvesServedFiles` as the first line inside the class.
2. Delete these class-body definitions (now provided by the concern): `FILES_DIR = ...`, `ALLOWED_EXTENSIONS = ...`, `UnsupportedFile = ...`, `MissingFile = ...`, and the `FILES_DIR.mkpath` line.
3. Delete the private `resolve_file_path` method.
4. Everything else (`MARKDOWN_OPTIONS`, `FORMATTER`, actions, `rescue_from` blocks, `authenticated?`, `unique_file_path`) stays as-is. Bare references to `FILES_DIR`, `UnsupportedFile`, `MissingFile` inside `FilesController` still resolve — first to a class-level const if a test shadows one, otherwise through the included module.

The resulting top of the class:

```ruby
class FilesController < ApplicationController
  include ResolvesServedFiles

  MARKDOWN_OPTIONS = {
    render: { unsafe: true },
    extension: { autolink: true },
    parse: { smart: true }
  }.freeze
  FORMATTER = GeminiFormatter
```

- [ ] **Step 4: Fix the one test that calls the private method with a shadowed const**

`test/controllers/files_controller_test.rb` test "rejects path traversal during path resolution" calls `FilesController.new.send(:resolve_file_path, "../secret.md")`. Because `resolve_file_path` now reads `self.class::FILES_DIR`, and the test setup shadows `FilesController::FILES_DIR`, this still works unchanged. Run it to confirm; only edit if it fails.

- [ ] **Step 5: Run the full suite**

Run: `bin/rails test`
Expected: all tests pass, 0 failures (identical count to Step 1).

- [ ] **Step 6: Commit**

```bash
git add app/controllers/concerns/resolves_served_files.rb app/controllers/files_controller.rb
git commit -m "refactor: extract ResolvesServedFiles concern"
```

---

### Task 2: `SelectionLinker` — rewrite source text into a link

Pure, stateless text transformation. No I/O. All the fiddly edge cases live here so the controller stays thin.

**Interface:** `SelectionLinker.link(source:, extension:, selected_text:, occurrence:, url:) → String` (rewritten source). Raises `SelectionLinker::NotFound` when the text isn't in the source, `SelectionLinker::UnsafeMatch` when linking would corrupt markup or nest links.

**Files:**
- Create: `app/services/selection_linker.rb`
- Test: `test/services/selection_linker_test.rb`

- [ ] **Step 1: Write the failing tests**

Create `test/services/selection_linker_test.rb`:

```ruby
require "test_helper"

class SelectionLinkerTest < ActiveSupport::TestCase
  test "wraps a markdown selection in a link" do
    result = SelectionLinker.link(
      source: "Alpha beta gamma.",
      extension: ".md",
      selected_text: "beta",
      occurrence: 0,
      url: "/x.html"
    )

    assert_equal "Alpha [beta](/x.html) gamma.", result
  end

  test "wraps an html selection in an anchor" do
    result = SelectionLinker.link(
      source: "<p>Alpha beta gamma.</p>",
      extension: ".html",
      selected_text: "beta",
      occurrence: 0,
      url: "/x.html"
    )

    assert_equal %(<p>Alpha <a href="/x.html">beta</a> gamma.</p>), result
  end

  test "picks the requested occurrence" do
    result = SelectionLinker.link(
      source: "cat dog cat bird cat",
      extension: ".md",
      selected_text: "cat",
      occurrence: 2,
      url: "/x.html"
    )

    assert_equal "cat dog cat bird [cat](/x.html)", result
  end

  test "falls back to the first occurrence when index is out of range" do
    result = SelectionLinker.link(
      source: "cat dog cat",
      extension: ".md",
      selected_text: "cat",
      occurrence: 9,
      url: "/x.html"
    )

    assert_equal "[cat](/x.html) dog cat", result
  end

  test "raises NotFound when the text is absent" do
    assert_raises SelectionLinker::NotFound do
      SelectionLinker.link(
        source: "Alpha beta.",
        extension: ".md",
        selected_text: "missing",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "escapes closing brackets in the markdown label" do
    result = SelectionLinker.link(
      source: "a b] c",
      extension: ".md",
      selected_text: "b]",
      occurrence: 0,
      url: "/x.html"
    )

    assert_equal "a [b\\]](/x.html) c", result
  end

  test "rejects a match inside an existing markdown link label" do
    assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: "see [beta gamma](/old.html) end",
        extension: ".md",
        selected_text: "beta",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "rejects a match inside an existing markdown link url" do
    assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: "see [label](/beta.html) end",
        extension: ".md",
        selected_text: "beta",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "rejects an html match inside a tag" do
    assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: %(<p class="beta">x</p>),
        extension: ".html",
        selected_text: "beta",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "rejects an html match inside a script block" do
    assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: "<script>var beta = 1;</script><p>x</p>",
        extension: ".html",
        selected_text: "beta",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "rejects an html match inside an existing anchor" do
    assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: %(<a href="/old.html">beta</a>),
        extension: ".html",
        selected_text: "beta",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "skips unsafe occurrences is not attempted; requested occurrence is judged as-is" do
    # occurrence 0 is inside an anchor -> unsafe, even though occurrence 1 is fine
    assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: %(<a href="/old.html">beta</a> and beta again),
        extension: ".html",
        selected_text: "beta",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end
end
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `bin/rails test test/services/selection_linker_test.rb`
Expected: FAIL — `NameError: uninitialized constant SelectionLinker`.

- [ ] **Step 3: Implement `SelectionLinker`**

Create `app/services/selection_linker.rb`:

```ruby
class SelectionLinker
  Error = Class.new(StandardError)
  NotFound = Class.new(Error)
  UnsafeMatch = Class.new(Error)

  def self.link(source:, extension:, selected_text:, occurrence:, url:)
    new(source, extension, selected_text, occurrence, url).link
  end

  def initialize(source, extension, selected_text, occurrence, url)
    @source = source
    @extension = extension
    @selected_text = selected_text
    @occurrence = [occurrence.to_i, 0].max
    @url = url
  end

  def link
    index = match_index
    prefix = @source[0, index]
    suffix = @source[(index + @selected_text.length)..]

    if @extension == ".html"
      ensure_safe_html!(prefix)
      "#{prefix}<a href=\"#{@url}\">#{@selected_text}</a>#{suffix}"
    else
      ensure_safe_markdown!(prefix)
      label = @selected_text.gsub("]", "\\]")
      "#{prefix}[#{label}](#{@url})#{suffix}"
    end
  end

  private
    def match_index
      indices = []
      position = 0
      while (found = @source.index(@selected_text, position))
        indices << found
        position = found + 1
      end
      if indices.empty?
        raise NotFound, "Selection not found in source — select a plainer run of text."
      end

      indices.fetch(@occurrence, indices.first)
    end

    def ensure_safe_html!(prefix)
      last_lt = prefix.rindex("<")
      last_gt = prefix.rindex(">")
      if last_lt && (last_gt.nil? || last_lt > last_gt)
        raise UnsafeMatch, "Selection falls inside an HTML tag."
      end
      %w[script style a].each do |tag|
        opens = prefix.scan(/<#{tag}\b/i).length
        closes = prefix.scan(%r{</#{tag}\s*>}i).length
        raise UnsafeMatch, "Selection falls inside a <#{tag}> element." if opens > closes
      end
    end

    def ensure_safe_markdown!(prefix)
      last_open = prefix.rindex("[")
      last_close = prefix.rindex("]")
      if last_open && (last_close.nil? || last_open > last_close)
        raise UnsafeMatch, "Selection falls inside a markdown link label."
      end

      last_link_open = prefix.rindex("](")
      last_paren_close = prefix.rindex(")")
      if last_link_open && (last_paren_close.nil? || last_link_open > last_paren_close)
        raise UnsafeMatch, "Selection falls inside a markdown link URL."
      end
    end
end
```

Note: an unclosed `<` in the prefix (last `<` after last `>`) means the match sits inside a tag. These are heuristics; the spec accepts them for v1 (trusted content).

- [ ] **Step 4: Run tests to verify they pass**

Run: `bin/rails test test/services/selection_linker_test.rb`
Expected: PASS, 12 runs, 0 failures.

- [ ] **Step 5: Commit**

```bash
git add app/services/selection_linker.rb test/services/selection_linker_test.rb
git commit -m "feat: add SelectionLinker for rewriting selections into links"
```

---

### Task 3: `ClaudeExpandService` — generate the answer page

Shells out to `claude` (sonnet, JSON output, **no system prompt** — this is deliberate, per spec), falls back to `codex exec` (model `earth`) on any failure. Same `Open3` pattern as yosubee's `claude_search_word_service.rb`.

**Interface:** `ClaudeExpandService.expand(file_name:, document:, selection:, question:) → String` (full HTML). Raises `ClaudeExpandService::Error` when both CLIs fail or output isn't HTML.

**Files:**
- Create: `app/services/claude_expand_service.rb`
- Test: `test/services/claude_expand_service_test.rb`

**Testing approach:** the private `run_command(cmd)` method is the only thing that touches the OS. Tests stub it per-instance with Minitest's `Object#stub`, capturing the `cmd` array to assert on flags. (Intentional deviation from the spec's "stub `Open3.capture3`" — a single seam covers both the claude and codex paths and the timeout wrapper.)

- [ ] **Step 1: Write the failing tests**

Create `test/services/claude_expand_service_test.rb`:

```ruby
require "test_helper"

class ClaudeExpandServiceTest < ActiveSupport::TestCase
  HTML = "<!DOCTYPE html><html><body>answer</body></html>"

  setup do
    @service = ClaudeExpandService.new
    @args = { file_name: "notes.md", document: "Alpha beta.", selection: "beta", question: "why?" }
  end

  test "returns claude result on success" do
    commands = []
    runner = lambda do |cmd|
      commands << cmd
      [{ "is_error" => false, "result" => HTML }.to_json, "", fake_status(true)]
    end

    result = @service.stub(:run_command, runner) { @service.expand(**@args) }

    assert_equal HTML, result
    assert_equal 1, commands.length
    claude_cmd = commands.first
    assert_equal "claude", claude_cmd.first
    assert_includes claude_cmd, "--model"
    assert_includes claude_cmd, "sonnet"
    assert_not_includes claude_cmd, "--system-prompt"
    prompt = claude_cmd[claude_cmd.index("-p") + 1]
    assert_includes prompt, "Alpha beta."
    assert_includes prompt, "<selection>\nbeta\n</selection>"
    assert_includes prompt, "<question>\nwhy?\n</question>"
  end

  test "strips a wrapping markdown code fence" do
    fenced = "```html\n#{HTML}\n```"
    runner = ->(cmd) { [{ "is_error" => false, "result" => fenced }.to_json, "", fake_status(true)] }

    result = @service.stub(:run_command, runner) { @service.expand(**@args) }

    assert_equal HTML, result
  end

  test "falls back to codex when claude fails" do
    commands = []
    runner = lambda do |cmd|
      commands << cmd
      if cmd.first == "claude"
        ["", "boom", fake_status(false)]
      else
        output_file = cmd[cmd.index("-o") + 1]
        File.write(output_file, HTML)
        ["", "", fake_status(true)]
      end
    end

    result = @service.stub(:run_command, runner) { @service.expand(**@args) }

    assert_equal HTML, result
    assert_equal %w[claude codex], commands.map(&:first)
    codex_cmd = commands.last
    assert_equal %w[codex exec], codex_cmd.first(2)
    assert_includes codex_cmd, "earth"
    assert_includes codex_cmd, "read-only"
    assert_includes codex_cmd, "--skip-git-repo-check"
  end

  test "falls back to codex when claude reports is_error" do
    runner = lambda do |cmd|
      if cmd.first == "claude"
        [{ "is_error" => true, "result" => "refused" }.to_json, "", fake_status(true)]
      else
        File.write(cmd[cmd.index("-o") + 1], HTML)
        ["", "", fake_status(true)]
      end
    end

    result = @service.stub(:run_command, runner) { @service.expand(**@args) }

    assert_equal HTML, result
  end

  test "raises Error when both CLIs fail" do
    runner = ->(cmd) { ["", "boom", fake_status(false)] }

    assert_raises ClaudeExpandService::Error do
      @service.stub(:run_command, runner) { @service.expand(**@args) }
    end
  end

  test "raises Error when output is not HTML" do
    runner = lambda do |cmd|
      if cmd.first == "claude"
        [{ "is_error" => false, "result" => "sorry, cannot help" }.to_json, "", fake_status(true)]
      else
        File.write(cmd[cmd.index("-o") + 1], "plain text")
        ["", "", fake_status(true)]
      end
    end

    assert_raises ClaudeExpandService::Error do
      @service.stub(:run_command, runner) { @service.expand(**@args) }
    end
  end

  private
    def fake_status(success)
      status = Object.new
      status.define_singleton_method(:success?) { success }
      status.define_singleton_method(:exitstatus) { success ? 0 : 1 }
      status
    end
end
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `bin/rails test test/services/claude_expand_service_test.rb`
Expected: FAIL — `NameError: uninitialized constant ClaudeExpandService`.

- [ ] **Step 3: Implement `ClaudeExpandService`**

Create `app/services/claude_expand_service.rb`:

```ruby
require "json"
require "open3"
require "tempfile"

class ClaudeExpandService
  Error = Class.new(StandardError)

  CLAUDE_MODEL = "sonnet"
  CODEX_MODEL = "earth"
  TIMEOUT_SECONDS = 120

  PROMPT_TEMPLATE = <<~PROMPT
    You are given a document, a text selection from it, and a reader's question about that selection.

    Write a complete standalone HTML page that answers the question and expands on the selected text with additional depth: background, context, related concepts, and concrete details the original document leaves out.

    Requirements:
    - Output ONLY the HTML document, starting with <!DOCTYPE html>. No markdown fences, no commentary.
    - Dark theme, readable typography (max-width ~70ch, generous line-height), semantic HTML.
    - Title the page after the selection.
    - Ground the answer in the document's context, but bring in outside knowledge freely.

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

  def self.expand(**kwargs) = new.expand(**kwargs)

  def expand(file_name:, document:, selection:, question:)
    prompt = format(PROMPT_TEMPLATE, file_name:, document:, selection:, question:)
    Rails.logger.info "[ClaudeExpandService] expanding file=#{file_name} selection_bytes=#{selection.bytesize} question_bytes=#{question.bytesize}"

    html = run_claude(prompt)
    Rails.logger.info "[ClaudeExpandService] claude succeeded bytes=#{html.bytesize}"
    html
  rescue Error => error
    Rails.logger.warn "[ClaudeExpandService] claude failed (#{error.message}), falling back to codex"
    html = run_codex(prompt)
    Rails.logger.info "[ClaudeExpandService] codex succeeded bytes=#{html.bytesize}"
    html
  end

  private
    def run_claude(prompt)
      stdout, stderr, status = run_command([
        "claude", "-p", prompt,
        "--model", CLAUDE_MODEL,
        "--output-format", "json"
      ])
      unless status.success?
        raise Error, "claude CLI failed (#{status.exitstatus}): #{stderr.strip[0, 500]}"
      end

      parsed = JSON.parse(stdout)
      raise Error, "claude returned error: #{parsed["result"].to_s[0, 500]}" if parsed["is_error"]

      ensure_html(strip_fence(parsed["result"].to_s))
    rescue JSON::ParserError => error
      raise Error, "claude output was not JSON: #{error.message}"
    end

    def run_codex(prompt)
      Tempfile.create(["expansion", ".html"]) do |output|
        _stdout, stderr, status = run_command([
          "codex", "exec",
          "-m", CODEX_MODEL,
          "-s", "read-only",
          "--skip-git-repo-check",
          "--color", "never",
          "-o", output.path,
          prompt
        ])
        unless status.success?
          raise Error, "codex CLI failed (#{status.exitstatus}): #{stderr.strip[0, 500]}"
        end

        ensure_html(strip_fence(File.read(output.path)))
      end
    end

    def run_command(cmd)
      Open3.popen3(*cmd) do |stdin, stdout, stderr, wait_thr|
        stdin.close
        out_reader = Thread.new { stdout.read }
        err_reader = Thread.new { stderr.read }

        unless wait_thr.join(TIMEOUT_SECONDS)
          Process.kill("KILL", wait_thr.pid) rescue nil
          raise Error, "#{cmd.first} timed out after #{TIMEOUT_SECONDS}s"
        end

        [out_reader.value, err_reader.value, wait_thr.value]
      end
    end

    def strip_fence(text)
      stripped = text.strip
      if stripped.start_with?("```")
        stripped = stripped.sub(/\A```[a-z]*\n/i, "").sub(/\n```\z/, "")
      end
      stripped
    end

    def ensure_html(text)
      raise Error, "output does not look like HTML" unless text.match?(/<html/i)

      text
    end
end
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `bin/rails test test/services/claude_expand_service_test.rb`
Expected: PASS, 6 runs, 0 failures.

- [ ] **Step 5: Run the full suite**

Run: `bin/rails test`
Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add app/services/claude_expand_service.rb test/services/claude_expand_service_test.rb
git commit -m "feat: add ClaudeExpandService with codex fallback"
```

---

## Chunk 2: Endpoint, frontend, docs

### Task 4: `ExpansionsController` + route

Orchestrates: resolve file → validate params → rewrite via `SelectionLinker` (cheap, fails fast **before** the expensive CLI call) → generate HTML → write both files.

**Files:**
- Create: `app/controllers/expansions_controller.rb`
- Modify: `config/routes.rb`
- Test: `test/controllers/expansions_controller_test.rb`

- [ ] **Step 1: Write the failing tests**

Create `test/controllers/expansions_controller_test.rb`:

```ruby
require "test_helper"
require "tmpdir"

class ExpansionsControllerTest < ActionDispatch::IntegrationTest
  HTML = "<!DOCTYPE html><html><body>answer</body></html>"

  setup do
    @files_dir = Pathname.new(Dir.mktmpdir("served-files"))
    @original_files_dir = ExpansionsController::FILES_DIR if ExpansionsController.const_defined?(:FILES_DIR, false)
    ExpansionsController.send(:remove_const, :FILES_DIR) if ExpansionsController.const_defined?(:FILES_DIR, false)
    ExpansionsController.const_set(:FILES_DIR, @files_dir)

    @user = User.create!(email: "expander@example.com", password: "s3cretpass")
    sign_in @user
  end

  teardown do
    FileUtils.remove_entry(@files_dir)
    ExpansionsController.send(:remove_const, :FILES_DIR)
    ExpansionsController.const_set(:FILES_DIR, @original_files_dir) if @original_files_dir
  end

  test "generates a page and rewrites a markdown source" do
    write_file "notes.md", "Alpha beta gamma."
    received = nil
    expander = lambda do |**kwargs|
      received = kwargs
      HTML
    end

    with_expander(expander) do
      post "/expansions", params: {
        file_name: "notes.md", selected_text: "beta", occurrence: 0, question: "why?"
      }, as: :json
    end

    assert_response :success
    assert_equal({ "url" => "/notes--expand-1.html" }, response.parsed_body)
    assert_equal HTML, @files_dir.join("notes--expand-1.html").read
    assert_equal "Alpha [beta](/notes--expand-1.html) gamma.", @files_dir.join("notes.md").read
    assert_equal "notes.md", received[:file_name]
    assert_equal "Alpha beta gamma.", received[:document]
    assert_equal "beta", received[:selection]
    assert_equal "why?", received[:question]
  end

  test "rewrites an html source with an anchor" do
    write_file "page.html", "<p>Alpha beta gamma.</p>"

    with_expander(->(**) { HTML }) do
      post "/expansions", params: {
        file_name: "page.html", selected_text: "beta", occurrence: 0, question: "why?"
      }, as: :json
    end

    assert_response :success
    assert_equal %(<p>Alpha <a href="/page--expand-1.html">beta</a> gamma.</p>), @files_dir.join("page.html").read
  end

  test "increments the expansion suffix" do
    write_file "notes.md", "Alpha beta gamma."
    write_file "notes--expand-1.html", "taken"

    with_expander(->(**) { HTML }) do
      post "/expansions", params: {
        file_name: "notes.md", selected_text: "beta", occurrence: 0, question: "why?"
      }, as: :json
    end

    assert_equal({ "url" => "/notes--expand-2.html" }, response.parsed_body)
  end

  test "returns 422 when the selection is not in the source" do
    write_file "notes.md", "Alpha **be**ta gamma."
    called = false

    with_expander(->(**) { called = true; HTML }) do
      post "/expansions", params: {
        file_name: "notes.md", selected_text: "beta", occurrence: 0, question: "why?"
      }, as: :json
    end

    assert_response :unprocessable_entity
    assert_not called, "expander must not run when the selection cannot be linked"
    assert_equal "Alpha **be**ta gamma.", @files_dir.join("notes.md").read
  end

  test "returns 400 when params are missing" do
    write_file "notes.md", "Alpha beta."

    post "/expansions", params: { file_name: "notes.md", selected_text: "", question: "why?" }, as: :json

    assert_response :bad_request
  end

  test "returns 404 for a missing file" do
    post "/expansions", params: {
      file_name: "missing.md", selected_text: "beta", occurrence: 0, question: "why?"
    }, as: :json

    assert_response :not_found
  end

  test "returns 502 and writes nothing when generation fails" do
    write_file "notes.md", "Alpha beta gamma."

    with_expander(->(**) { raise ClaudeExpandService::Error, "both CLIs failed" }) do
      post "/expansions", params: {
        file_name: "notes.md", selected_text: "beta", occurrence: 0, question: "why?"
      }, as: :json
    end

    assert_response :bad_gateway
    assert_equal({ "detail" => "Generation failed." }, response.parsed_body)
    assert_equal "Alpha beta gamma.", @files_dir.join("notes.md").read
    assert_equal ["notes.md"], @files_dir.children.map { |c| c.basename.to_s }
  end

  test "requires authentication" do
    sign_out @user
    write_file "notes.md", "Alpha beta."

    post "/expansions", params: {
      file_name: "notes.md", selected_text: "beta", occurrence: 0, question: "why?"
    }, as: :json

    assert_response :unauthorized
  end

  private
    def write_file(name, content)
      @files_dir.join(name).tap { |path| path.write(content) }
    end

    def with_expander(callable)
      had = ExpansionsController.const_defined?(:EXPANDER, false)
      original = ExpansionsController::EXPANDER if had
      fake = Object.new
      fake.define_singleton_method(:expand, &callable)

      ExpansionsController.send(:remove_const, :EXPANDER) if had
      ExpansionsController.const_set(:EXPANDER, fake)
      yield
    ensure
      ExpansionsController.send(:remove_const, :EXPANDER) if ExpansionsController.const_defined?(:EXPANDER, false)
      ExpansionsController.const_set(:EXPANDER, original) if had
    end
end
```

Note: Devise returns 401 for JSON requests (no redirect), hence `assert_response :unauthorized`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `bin/rails test test/controllers/expansions_controller_test.rb`
Expected: FAIL — `NameError: uninitialized constant ExpansionsController`.

- [ ] **Step 3: Add the route**

In `config/routes.rb`, add directly after the `post "/file/new"` line:

```ruby
post "/expansions", to: "expansions#create"
```

- [ ] **Step 4: Implement the controller**

Create `app/controllers/expansions_controller.rb`:

```ruby
class ExpansionsController < ApplicationController
  include ResolvesServedFiles

  EXPANDER = ClaudeExpandService

  rescue_from ActionController::BadRequest do |error|
    render json: { detail: error.message }, status: :bad_request
  end
  rescue_from UnsupportedFile, MissingFile do |error|
    render json: { detail: error.message }, status: :not_found
  end
  rescue_from SelectionLinker::Error do |error|
    render json: { detail: error.message }, status: :unprocessable_entity
  end
  rescue_from ClaudeExpandService::Error do |error|
    Rails.logger.error("Expansion generation failed (#{error.class}): #{error.message}")
    render json: { detail: "Generation failed." }, status: :bad_gateway
  end

  def create
    selected_text = params[:selected_text].to_s
    question = params[:question].to_s
    if selected_text.blank? || question.blank?
      raise ActionController::BadRequest, "Missing selected_text or question."
    end

    file_path = resolve_file_path(params[:file_name].to_s)
    source = file_path.read(encoding: "UTF-8")
    expansion_path = unique_expansion_path(file_path)
    url = "/#{ERB::Util.url_encode(expansion_path.basename.to_s)}"

    rewritten = SelectionLinker.link(
      source: source,
      extension: file_path.extname.downcase,
      selected_text: selected_text,
      occurrence: params[:occurrence].to_i,
      url: url
    )

    html = EXPANDER.expand(
      file_name: file_path.basename.to_s,
      document: source,
      selection: selected_text,
      question: question
    )

    expansion_path.write(html, encoding: "UTF-8")
    file_path.write(rewritten, encoding: "UTF-8")

    render json: { url: url }
  end

  private
    def unique_expansion_path(file_path)
      stem = file_path.basename(file_path.extname).to_s
      counter = 1
      loop do
        candidate = self.class::FILES_DIR.join("#{stem}--expand-#{counter}.html")
        return candidate unless candidate.exist?

        counter += 1
      end
    end
end
```

Ordering matters: `SelectionLinker.link` runs before `EXPANDER.expand`, so unlinkable selections 422 without paying for a CLI call, and nothing is written unless both succeed.

- [ ] **Step 5: Run tests to verify they pass**

Run: `bin/rails test test/controllers/expansions_controller_test.rb`
Expected: PASS, 8 runs, 0 failures.

- [ ] **Step 6: Run the full suite and commit**

Run: `bin/rails test`
Expected: all green.

```bash
git add app/controllers/expansions_controller.rb config/routes.rb test/controllers/expansions_controller_test.rb
git commit -m "feat: add POST /expansions endpoint"
```

---

### Task 5: Frontend — `expand.js`, layout wiring, HTML injection

**Files:**
- Create: `public/expand.js`
- Modify: `app/views/layouts/markdown.html.erb`
- Modify: `app/controllers/files_controller.rb` (`show` action)
- Test: `test/controllers/files_controller_test.rb` (add cases)

`expand.js` lives in `public/` (not Propshaft assets) so the hardcoded `/expand.js` path in injected HTML works without digest resolution.

- [ ] **Step 1: Write the failing controller tests**

Add to `test/controllers/files_controller_test.rb` (before `private`):

```ruby
  test "injects the expand script and csrf token into served HTML" do
    write_file "page.html", "<html><body><main>Raw</main></body></html>"

    get "/page.html"

    assert_response :success
    assert_includes response.body, %(<script src="/expand.js" defer></script></body>)
    assert_select "meta[name='csrf-token']"
    assert_includes response.body, "<main>Raw</main>"
  end

  test "appends the expand script when HTML has no body tag" do
    write_file "page.html", "<main>Raw HTML</main>"

    get "/page.html"

    assert_response :success
    assert_includes response.body, %(<script src="/expand.js" defer></script>)
    assert response.body.start_with?("<main>Raw HTML</main>")
  end

  test "includes the expand script and csrf tags in the markdown layout" do
    write_file "notes.md", "# Notes"

    get "/notes.md"

    assert_response :success
    assert_select "meta[name='csrf-token']"
    assert_select "script[src='/expand.js'][defer]"
  end
```

Also update the existing test "serves HTML files without a Rails layout": change its final assertion from `assert_equal "<main>Raw HTML</main>", response.body` to `assert response.body.start_with?("<main>Raw HTML</main>")`, and likewise in "serves files to older browser user agents".

- [ ] **Step 2: Run tests to verify the new ones fail**

Run: `bin/rails test test/controllers/files_controller_test.rb`
Expected: the three new tests FAIL (no script tag in response); the two updated ones PASS.

- [ ] **Step 3: Inject the script when serving HTML**

In `app/controllers/files_controller.rb`, change `show`'s HTML branch:

```ruby
    if file_path.extname.downcase == ".html"
      render html: inject_expand_script(content).html_safe, layout: false
```

and add this private method:

```ruby
    def inject_expand_script(content)
      snippet = %(<meta name="csrf-token" content="#{form_authenticity_token}"><script src="/expand.js" defer></script>)
      if content =~ %r{</body>}i
        content.sub(%r{</body>}i) { "#{snippet}</body>" }
      else
        content + snippet
      end
    end
```

(`form_authenticity_token` requires a session; served pages are already behind Devise auth, so a session exists.)

- [ ] **Step 4: Wire the markdown layout**

In `app/views/layouts/markdown.html.erb`, inside `<head>` after the stylesheet tag, add:

```erb
    <%= csrf_meta_tags %>
    <script src="/expand.js" defer></script>
```

- [ ] **Step 5: Run controller tests**

Run: `bin/rails test test/controllers/files_controller_test.rb`
Expected: PASS, all tests.

- [ ] **Step 6: Write `public/expand.js`**

Create `public/expand.js`:

```javascript
(function () {
  "use strict";

  let button = null;
  let popover = null;
  let currentSelection = null;

  const CSRF = () => {
    const meta = document.querySelector("meta[name='csrf-token']");
    return meta ? meta.content : "";
  };

  function removeUI() {
    if (button) { button.remove(); button = null; }
    if (popover) { popover.remove(); popover = null; }
  }

  function occurrenceIndex(range, text) {
    const pre = range.cloneRange();
    pre.selectNodeContents(document.body);
    pre.setEnd(range.startContainer, range.startOffset);
    const before = pre.toString();
    let count = 0;
    let idx = -1;
    while ((idx = before.indexOf(text, idx + 1)) !== -1) count += 1;
    return count;
  }

  function showButton(range, text) {
    removeUI();
    const rect = range.getBoundingClientRect();
    currentSelection = {
      text: text,
      occurrence: occurrenceIndex(range, text)
    };

    button = document.createElement("button");
    button.type = "button";
    button.textContent = "⤢"; // ⤢ expand icon
    button.setAttribute("aria-label", "Expand selection");
    Object.assign(button.style, {
      position: "absolute",
      left: `${window.scrollX + rect.left + rect.width / 2 - 16}px`,
      top: `${window.scrollY + rect.top - 40}px`,
      width: "32px",
      height: "32px",
      borderRadius: "6px",
      border: "1px solid #555",
      background: "#222",
      color: "#eee",
      fontSize: "18px",
      cursor: "pointer",
      zIndex: "9999"
    });
    button.addEventListener("mousedown", (event) => event.preventDefault());
    button.addEventListener("click", () => showPopover(rect));
    document.body.appendChild(button);
  }

  function showPopover(rect) {
    if (button) { button.remove(); button = null; }

    popover = document.createElement("form");
    Object.assign(popover.style, {
      position: "absolute",
      left: `${window.scrollX + rect.left}px`,
      top: `${window.scrollY + rect.bottom + 8}px`,
      width: "320px",
      padding: "12px",
      borderRadius: "8px",
      border: "1px solid #555",
      background: "#1b1b1b",
      color: "#eee",
      zIndex: "9999",
      display: "flex",
      flexDirection: "column",
      gap: "8px",
      font: "14px system-ui, sans-serif"
    });

    const textarea = document.createElement("textarea");
    textarea.placeholder = "Ask about this selection…";
    textarea.required = true;
    textarea.rows = 3;
    Object.assign(textarea.style, {
      resize: "vertical",
      background: "#111",
      color: "#eee",
      border: "1px solid #444",
      borderRadius: "4px",
      padding: "6px",
      font: "inherit"
    });

    const submit = document.createElement("button");
    submit.type = "submit";
    submit.textContent = "Expand";
    Object.assign(submit.style, {
      padding: "6px 12px",
      background: "#2d5a88",
      color: "#fff",
      border: "none",
      borderRadius: "4px",
      cursor: "pointer",
      font: "inherit"
    });

    const message = document.createElement("div");
    message.style.color = "#e08080";
    message.style.minHeight = "1em";

    popover.append(textarea, submit, message);
    popover.addEventListener("submit", (event) => {
      event.preventDefault();
      submit.disabled = true;
      submit.textContent = "Expanding… (may take a minute)";
      message.textContent = "";

      fetch("/expansions", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": CSRF()
        },
        body: JSON.stringify({
          file_name: decodeURIComponent(location.pathname.slice(1)),
          selected_text: currentSelection.text,
          occurrence: currentSelection.occurrence,
          question: textarea.value
        })
      })
        .then(async (response) => {
          const data = await response.json().catch(() => ({}));
          if (!response.ok) {
            throw new Error(data.detail || `Request failed (${response.status})`);
          }
          location.reload();
        })
        .catch((error) => {
          message.textContent = error.message;
          submit.disabled = false;
          submit.textContent = "Expand";
        });
    });

    document.body.appendChild(popover);
    textarea.focus();
  }

  document.addEventListener("mouseup", (event) => {
    if (popover && popover.contains(event.target)) return;
    if (button && button.contains(event.target)) return;

    setTimeout(() => {
      const selection = window.getSelection();
      const text = selection ? selection.toString().trim() : "";
      if (!text || selection.rangeCount === 0) {
        if (!popover) removeUI();
        return;
      }
      showButton(selection.getRangeAt(0), text);
    }, 0);
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") removeUI();
  });
})();
```

Intentional deviation from spec: the popover is dismissed by Escape or a new selection, but not by outside clicks — protects a typed question from a stray click. Do not "fix" this.

- [ ] **Step 7: Run the full suite**

Run: `bin/rails test`
Expected: all green.

- [ ] **Step 8: Commit**

```bash
git add public/expand.js app/views/layouts/markdown.html.erb app/controllers/files_controller.rb test/controllers/files_controller_test.rb
git commit -m "feat: selection expand UI with script injection"
```

---

### Task 6: Documentation

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Document the feature and update the trust boundary**

In `README.md`:

1. Add to the feature list near the top: `- Select text on any page to ask a question and generate a linked AI answer page (requires the \`claude\` CLI; falls back to \`codex\`).`
2. In the "Trust boundary" section, append: `Served .html responses have a small script tag injected before </body> to enable the text-expansion feature, so they are no longer byte-for-byte verbatim.`
3. Amend the first feature bullet from "serves HTML as-is" to "serves HTML with a small expansion script injected".

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: document text expansion feature"
```

---

### Task 7: Manual verification

- [ ] **Step 1: End-to-end check**

1. Ensure `claude` CLI is on PATH: `which claude`.
2. Start the app: `./serve` (listens on http://localhost:8009).
3. Drop a test file: `echo "The mitochondria is the powerhouse of the cell." > files/manual-test.md`.
4. Sign in, open `http://localhost:8009/manual-test.md`.
5. Select "mitochondria" → expand button appears → click → ask "what does it actually do?" → submit.
6. Wait (spinner text shows); page reloads with "mitochondria" as a link.
7. Click the link → generated dark-theme HTML page renders at `/manual-test--expand-1.html`.
8. On the generated page, select some text → the expand button appears there too (injection works recursively).
9. Clean up: `rm files/manual-test*.{md,html}` (adjust for actual names).

Expected: all steps work; failures at step 6 show the error message inside the popover, not a blank hang.
