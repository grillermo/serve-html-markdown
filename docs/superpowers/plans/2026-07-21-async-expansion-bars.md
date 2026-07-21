# Async Expansion Bars Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let readers submit several text expansions without reloading, then track and open each result from dismissible, fixed top-of-screen bars.

**Architecture:** Enable Rails Active Job with a two-worker in-process async adapter. `POST /expansions` persists an `Expansion` owned by the signed-in user and enqueues `GenerateExpansionJob`; `GET /expansions/:id` exposes only that user's current job state. `ExpansionProcessor` owns the file/AI workflow and serializes writes per source file. Vanilla `expand.js` creates one fixed-width bar per returned job and polls each independently.

**Tech Stack:** Rails 8.1, Active Job async/test adapters, Active Record/PostgreSQL, Minitest, Propshaft, vanilla JavaScript.

## Global Constraints

- Keep bars only in page-local JavaScript state; never restore them after refresh or navigation.
- The status container is fixed at `top: 0`, `left: 0`, and `width: 100%`; bars stack vertically and remain dismissible in every state.
- Pending selection text is clipped entirely with CSS `overflow: hidden`, `text-overflow: ellipsis`, and `white-space: nowrap`; do not pre-truncate it in JavaScript.
- Create responses must return immediately with `202 Accepted`; the AI CLI must never run in a controller request.
- Status records are always scoped through `current_user.expansions`.
- A terminal failure remains visible until the user dismisses it. Do not add retry UI, WebSockets, SSE, or cross-page persistence.
- Preserve the existing `use_openai` option and the existing safe public failure text `Generation failed.`.
- Preserve existing unrelated scroll-position changes in the worktree; do not stage them in feature commits.

---

## File structure

- `config/application.rb` enables the Active Job railtie.
- `config/environments/{development,production,test}.rb` selects the async adapter for the running app and the test adapter for deterministic tests.
- `config/database.yml` allocates enough PostgreSQL connections for Puma request threads plus the async job worker threads.
- `db/migrate/20260721120000_create_expansions.rb` creates persisted, user-owned job state; `db/schema.rb` reflects it.
- `app/models/expansion.rb` validates state and implements atomic pending-to-processing claim plus terminal transitions.
- `app/services/expansion_processor.rb` resolves files, invokes the expander, locks source-file writes, and returns the generated URL.
- `app/jobs/generate_expansion_job.rb` performs an expansion exactly once and records a safe completion/failure result.
- `app/controllers/expansions_controller.rb` creates jobs and renders user-scoped status JSON only.
- `config/routes.rb` adds the polling route before the catch-all file route.
- `app/models/user.rb` exposes the owned jobs association.
- `app/assets/javascripts/expand.js` replaces reload-on-success with fixed bar creation, polling, completion links, errors, and dismissal.
- `test/models/expansion_test.rb`, `test/services/expansion_processor_test.rb`, `test/jobs/generate_expansion_job_test.rb`, and `test/controllers/expansions_controller_test.rb` provide backend coverage. There is no JavaScript test runner, so the interaction is manually verified.

### Task 1: Enable and configure background execution

**Files:**
- Modify: `config/application.rb`
- Modify: `config/environments/development.rb`
- Modify: `config/environments/production.rb`
- Modify: `config/environments/test.rb`
- Modify: `config/database.yml`

**Interfaces:**
- Produces: `GenerateExpansionJob.perform_later(id)` runs outside the request in development/production; `assert_enqueued_with` can observe it in tests.

- [ ] **Step 1: Add a configuration test that exposes the missing railtie**

Create `test/jobs/generate_expansion_job_test.rb` with the minimal load check:

```ruby
require "test_helper"

class GenerateExpansionJobTest < ActiveJob::TestCase
  test "uses the test queue adapter" do
    assert_equal :test, ActiveJob::Base.queue_adapter_name
  end
end
```

- [ ] **Step 2: Run the test to verify the current application does not load Active Job**

Run: `rtk test bin/rails test test/jobs/generate_expansion_job_test.rb`

Expected: FAIL with `NameError` for `ActiveJob` because `config/application.rb` comments out `require "active_job/railtie"`.

- [ ] **Step 3: Enable the railtie and select adapters explicitly**

In `config/application.rb`, replace the commented Active Job line with:

```ruby
require "active_job/railtie"
```

Inside each environment configuration block, add exactly one adapter setting:

```ruby
# config/environments/development.rb and config/environments/production.rb
config.active_job.queue_adapter = ActiveJob::QueueAdapters::AsyncAdapter.new(
  min_threads: 1, max_threads: 2, max_queue: 100
)
```

```ruby
# config/environments/test.rb
config.active_job.queue_adapter = :test
```

In `config/database.yml`, replace the current default `pool:` line with the
following so the three Puma threads and up to two async job workers do not
compete for only three database connections:

```yaml
  pool: <%= ENV.fetch("DATABASE_POOL") { ENV.fetch("RAILS_MAX_THREADS", 3).to_i + 2 } %>
```

- [ ] **Step 4: Run the load test again**

Run: `rtk test bin/rails test test/jobs/generate_expansion_job_test.rb`

Expected: PASS, 1 run, 0 failures.

- [ ] **Step 5: Commit the runnable job configuration**

```sh
rtk git add config/application.rb config/environments/development.rb config/environments/production.rb config/environments/test.rb config/database.yml test/jobs/generate_expansion_job_test.rb
rtk git commit -m "config: enable async expansion jobs"
```

### Task 2: Persist expansion state and provide atomic state transitions

**Files:**
- Create: `db/migrate/20260721120000_create_expansions.rb`
- Create: `app/models/expansion.rb`
- Modify: `app/models/user.rb`
- Modify: `db/schema.rb`
- Create: `test/models/expansion_test.rb`

**Interfaces:**
- Consumes: `User`.
- Produces: `user.expansions`, `Expansion#claim! -> Boolean`, `Expansion#complete!(url)`, and `Expansion#fail!(detail)`.

- [ ] **Step 1: Write the failing model tests**

Create `test/models/expansion_test.rb`:

```ruby
require "test_helper"

class ExpansionTest < ActiveSupport::TestCase
  setup do
    @user = User.create!(email: "expansion-model@example.com", password: "s3cretpass")
    @expansion = @user.expansions.create!(
      file_name: "notes.md", selected_text: "beta", occurrence: 0,
      question: "Why?", use_openai: false
    )
  end

  test "starts pending and requires all submitted fields" do
    assert_equal "pending", @expansion.status
    invalid = @user.expansions.build(file_name: "", selected_text: "", question: "")
    assert_not invalid.valid?
  end

  test "claims a pending job only once" do
    assert @expansion.claim!
    assert_equal "processing", @expansion.reload.status
    assert_not @expansion.claim!
  end

  test "records completed and failed terminal states" do
    @expansion.claim!
    @expansion.complete!("/notes--expand-1.html")
    assert_equal ["completed", "/notes--expand-1.html", nil],
      @expansion.reload.attributes.values_at("status", "url", "error_detail")

    failed = @user.expansions.create!(file_name: "notes.md", selected_text: "gamma", occurrence: 0, question: "Why?")
    failed.claim!
    failed.fail!("Generation failed.")
    assert_equal ["failed", nil, "Generation failed."],
      failed.reload.attributes.values_at("status", "url", "error_detail")
  end
end
```

- [ ] **Step 2: Run it to verify it fails**

Run: `rtk test bin/rails test test/models/expansion_test.rb`

Expected: FAIL with `NoMethodError` for `user.expansions`.

- [ ] **Step 3: Create the migration and apply it**

Create `db/migrate/20260721120000_create_expansions.rb`:

```ruby
class CreateExpansions < ActiveRecord::Migration[8.1]
  def change
    create_table :expansions do |t|
      t.references :user, null: false, foreign_key: true
      t.string :file_name, null: false
      t.text :selected_text, null: false
      t.integer :occurrence, null: false, default: 0
      t.text :question, null: false
      t.boolean :use_openai, null: false, default: false
      t.string :status, null: false, default: "pending"
      t.string :url
      t.string :error_detail
      t.timestamps
    end

    add_index :expansions, [:user_id, :status]
  end
end
```

Run: `rtk proxy bin/rails db:migrate RAILS_ENV=test`

Expected: the `expansions` table is added to `db/schema.rb` with its user
foreign key and `index_expansions_on_user_id_and_status`.

- [ ] **Step 4: Implement the association and state model**

Add this association to `app/models/user.rb`:

```ruby
has_many :expansions, dependent: :destroy
```

Create `app/models/expansion.rb`:

```ruby
class Expansion < ApplicationRecord
  STATUSES = %w[pending processing completed failed].freeze

  belongs_to :user

  validates :file_name, :selected_text, :question, presence: true
  validates :occurrence, numericality: { only_integer: true, greater_than_or_equal_to: 0 }
  validates :status, inclusion: { in: STATUSES }

  def claim!
    self.class.where(id: id, status: "pending")
      .update_all(status: "processing", updated_at: Time.current) == 1
  end

  def complete!(generated_url)
    update!(status: "completed", url: generated_url, error_detail: nil)
  end

  def fail!(detail)
    update!(status: "failed", url: nil, error_detail: detail)
  end
end
```

`claim!` is a conditional update rather than an in-memory status check, so a
duplicate queue delivery cannot run the same job twice.

- [ ] **Step 5: Run the model test and schema load check**

Run: `rtk test bin/rails test test/models/expansion_test.rb && rtk test bin/rails db:test:prepare`

Expected: model test PASS, 3 runs, 0 failures; database preparation exits 0.

- [ ] **Step 6: Commit the durable job state**

```sh
rtk git add app/models/user.rb app/models/expansion.rb db/migrate/20260721120000_create_expansions.rb db/schema.rb test/models/expansion_test.rb
rtk git commit -m "feat: persist expansion job states"
```

### Task 3: Isolate the expansion workflow in a lock-safe processor

**Files:**
- Create: `app/services/expansion_processor.rb`
- Create: `test/services/expansion_processor_test.rb`

**Interfaces:**
- Consumes: `Expansion`, `ResolvesServedFiles`, `SelectionLinker`, and `ClaudeExpandService`.
- Produces: `ExpansionProcessor.process(expansion) -> String` generated URL; raises the existing safe resolver/linker/expander errors for the job to record.

- [ ] **Step 1: Write failing processor tests**

Create `test/services/expansion_processor_test.rb`:

```ruby
require "test_helper"
require "tmpdir"

class ExpansionProcessorTest < ActiveSupport::TestCase
  HTML = "<!DOCTYPE html><html><body>answer</body></html>"

  setup do
    @files_dir = Pathname.new(Dir.mktmpdir("expansion-processor"))
    @user = User.create!(email: "processor@example.com", password: "s3cretpass")
    @expansion = @user.expansions.create!(file_name: "notes.md", selected_text: "beta", occurrence: 0, question: "Why?")
    swap_constant(ExpansionProcessor, :FILES_DIR, @files_dir)
  end

  teardown do
    FileUtils.remove_entry(@files_dir)
    restore_constants
  end

  test "generates an expansion and links the latest source while holding its lock" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")

    with_expander(->(**) { HTML }) do
      assert_equal "/notes--expand-1.html", ExpansionProcessor.process(@expansion)
    end

    assert_equal HTML, @files_dir.join("notes--expand-1.html").read
    assert_equal "Alpha [beta](/notes--expand-1.html) gamma.", @files_dir.join("notes.md").read
  end

  test "does not write either file when the expander fails" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")

    with_expander(->(**) { raise ClaudeExpandService::Error, "cli unavailable" }) do
      assert_raises(ClaudeExpandService::Error) { ExpansionProcessor.process(@expansion) }
    end

    assert_equal "Alpha beta gamma.", @files_dir.join("notes.md").read
    assert_not @files_dir.join("notes--expand-1.html").exist?
  end

  private

  def with_expander(callable)
    swap_constant(ExpansionProcessor, :EXPANDER, Object.new.tap { |fake| fake.define_singleton_method(:expand, &callable) })
    yield
  end

  def swap_constant(owner, name, value)
    @constants ||= {}
    @constants[[owner, name]] ||= owner.const_get(name) if owner.const_defined?(name, false)
    owner.send(:remove_const, name) if owner.const_defined?(name, false)
    owner.const_set(name, value)
  end

  def restore_constants
    @constants&.each do |(owner, name), value|
      owner.send(:remove_const, name) if owner.const_defined?(name, false)
      owner.const_set(name, value)
    end
  end
end
```

- [ ] **Step 2: Run it to verify it fails**

Run: `rtk test bin/rails test test/services/expansion_processor_test.rb`

Expected: FAIL with `NameError: uninitialized constant ExpansionProcessor`.

- [ ] **Step 3: Implement the processor**

Create `app/services/expansion_processor.rb`:

```ruby
class ExpansionProcessor
  include ResolvesServedFiles

  FILES_DIR = ResolvesServedFiles::FILES_DIR
  ALLOWED_EXTENSIONS = ResolvesServedFiles::ALLOWED_EXTENSIONS
  UnsupportedFile = ResolvesServedFiles::UnsupportedFile
  MissingFile = ResolvesServedFiles::MissingFile
  EXPANDER = ClaudeExpandService

  def self.process(expansion)
    new(expansion).process
  end

  def initialize(expansion)
    @expansion = expansion
  end

  def process
    file_path = resolve_file_path(@expansion.file_name)
    source = file_path.read(encoding: "UTF-8")
    html = EXPANDER.expand(
      file_name: file_path.basename.to_s,
      document: source,
      selection: @expansion.selected_text,
      question: @expansion.question,
      use_openai: @expansion.use_openai
    )

    with_source_lock(file_path) do
      latest_source = file_path.read(encoding: "UTF-8")
      expansion_path = unique_expansion_path(file_path)
      url = "/#{ERB::Util.url_encode(expansion_path.basename.to_s)}"
      rewritten = SelectionLinker.link(
        source: latest_source,
        extension: file_path.extname.downcase,
        selected_text: @expansion.selected_text,
        occurrence: @expansion.occurrence,
        url: url
      )

      expansion_path.write(html, encoding: "UTF-8")
      file_path.write(rewritten, encoding: "UTF-8")
      url
    end
  end

  private

  def with_source_lock(file_path)
    lock_path = self.class::FILES_DIR.join(".#{file_path.basename}.expansion.lock")
    File.open(lock_path, File::RDWR | File::CREAT, 0o600) do |lock_file|
      lock_file.flock(File::LOCK_EX)
      yield
    ensure
      lock_file.flock(File::LOCK_UN)
    end
  end

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

The expensive AI process runs before the lock. The lock encloses the current
source read, link insertion, suffix selection, and both writes so two jobs
cannot choose the same URL or overwrite each other's linked source changes.

- [ ] **Step 4: Extend coverage for safe link failures and suffixes**

Add these tests before the `private` section in
`test/services/expansion_processor_test.rb`:

```ruby
test "uses the next suffix and leaves source unchanged when latest source cannot be linked" do
  @files_dir.join("notes.md").write("Alpha **be**ta gamma.")
  @files_dir.join("notes--expand-1.html").write("taken")

  with_expander(->(**) { HTML }) do
    assert_raises(SelectionLinker::NotFound) { ExpansionProcessor.process(@expansion) }
  end

  assert_equal "Alpha **be**ta gamma.", @files_dir.join("notes.md").read
  assert_equal "taken", @files_dir.join("notes--expand-1.html").read
  assert_not @files_dir.join("notes--expand-2.html").exist?
end
```

- [ ] **Step 5: Run the service test**

Run: `rtk test bin/rails test test/services/expansion_processor_test.rb`

Expected: PASS, 3 runs, 0 failures.

- [ ] **Step 6: Commit the isolated generation workflow**

```sh
rtk git add app/services/expansion_processor.rb test/services/expansion_processor_test.rb
rtk git commit -m "feat: process expansion jobs asynchronously"
```

### Task 4: Execute jobs and expose creation/status endpoints

**Files:**
- Create: `app/jobs/generate_expansion_job.rb`
- Modify: `app/controllers/expansions_controller.rb`
- Modify: `config/routes.rb`
- Modify: `test/jobs/generate_expansion_job_test.rb`
- Modify: `test/controllers/expansions_controller_test.rb`

**Interfaces:**
- Consumes: `Expansion#claim!`, `#complete!`, `#fail!`, and `ExpansionProcessor.process(expansion)`.
- Produces: `POST /expansions -> 202 { id, status: "pending" }` and `GET /expansions/:id -> 200 { id, status, url?, detail? }`.

- [ ] **Step 1: Replace the current synchronous controller success test with async endpoint tests**

In `test/controllers/expansions_controller_test.rb`, include `ActiveJob::TestHelper` and replace the current tests that assert an immediate URL/file rewrite with:

```ruby
test "creates a pending job and enqueues it without running the expander" do
  write_file "notes.md", "Alpha beta gamma."

  assert_enqueued_with(job: GenerateExpansionJob) do
    post "/expansions", params: {
      file_name: "notes.md", selected_text: "beta", occurrence: 0,
      question: "why?", use_openai: true
    }, as: :json
  end

  assert_response :accepted
  expansion = @user.expansions.find(response.parsed_body.fetch("id"))
  assert_equal({ "id" => expansion.id, "status" => "pending" }, response.parsed_body)
  assert_equal ["notes.md", "beta", 0, "why?", true],
    expansion.attributes.values_at("file_name", "selected_text", "occurrence", "question", "use_openai")
end

test "returns the current user's status and only terminal fields" do
  expansion = @user.expansions.create!(file_name: "notes.md", selected_text: "beta", occurrence: 0, question: "why?")

  get "/expansions/#{expansion.id}", as: :json
  assert_response :success
  assert_equal({ "id" => expansion.id, "status" => "pending" }, response.parsed_body)

  expansion.complete!("/notes--expand-1.html")
  get "/expansions/#{expansion.id}", as: :json
  assert_equal({ "id" => expansion.id, "status" => "completed", "url" => "/notes--expand-1.html" }, response.parsed_body)
end

test "does not reveal another user's job or missing jobs" do
  other = User.create!(email: "other-expander@example.com", password: "s3cretpass")
  expansion = other.expansions.create!(file_name: "notes.md", selected_text: "beta", occurrence: 0, question: "why?")

  get "/expansions/#{expansion.id}", as: :json
  assert_response :not_found

  get "/expansions/999999", as: :json
  assert_response :not_found
end
```

Retain the existing tests for blank inputs and authentication, but update their
expectation: a syntactically valid missing file now returns `202` and later
becomes a failed job, because all file resolution is background work.

- [ ] **Step 2: Add job tests for exactly-once completion and failures**

Replace the load-only body of `test/jobs/generate_expansion_job_test.rb` with:

```ruby
require "test_helper"

class GenerateExpansionJobTest < ActiveJob::TestCase
  setup do
    @user = User.create!(email: "job@example.com", password: "s3cretpass")
    @expansion = @user.expansions.create!(file_name: "notes.md", selected_text: "beta", occurrence: 0, question: "why?")
  end

  test "completes a pending expansion once" do
    with_processor(->(_) { "/notes--expand-1.html" }) do
      GenerateExpansionJob.perform_now(@expansion.id)
      GenerateExpansionJob.perform_now(@expansion.id)
    end

    assert_equal ["completed", "/notes--expand-1.html"], @expansion.reload.attributes.values_at("status", "url")
  end

  test "stores safe details for known and unexpected failures" do
    with_processor(->(_) { raise ClaudeExpandService::Error, "token leaked" }) do
      GenerateExpansionJob.perform_now(@expansion.id)
    end
    assert_equal ["failed", "Generation failed."], @expansion.reload.attributes.values_at("status", "error_detail")

    failed = @user.expansions.create!(file_name: "notes.md", selected_text: "gamma", occurrence: 0, question: "why?")
    with_processor(->(_) { raise SelectionLinker::NotFound, "Selection not found in source — select a plainer run of text." }) do
      GenerateExpansionJob.perform_now(failed.id)
    end
    assert_equal ["failed", "Selection not found in source — select a plainer run of text."], failed.reload.attributes.values_at("status", "error_detail")
  end

  private

  def with_processor(callable)
    original = ExpansionProcessor.method(:process)
    ExpansionProcessor.define_singleton_method(:process, &callable)
    yield
  ensure
    ExpansionProcessor.define_singleton_method(:process, original)
  end
end
```

- [ ] **Step 3: Implement the job**

Create `app/jobs/generate_expansion_job.rb`:

```ruby
class GenerateExpansionJob < ApplicationJob
  queue_as :default

  def perform(expansion_id)
    expansion = Expansion.find_by(id: expansion_id)
    return unless expansion&.claim!

    expansion.complete!(ExpansionProcessor.process(expansion))
  rescue ClaudeExpandService::Error
    Rails.logger.error("Expansion generation failed for job #{expansion_id}")
    expansion&.fail!("Generation failed.")
  rescue SelectionLinker::Error, ActionController::BadRequest,
         ResolvesServedFiles::UnsupportedFile, ResolvesServedFiles::MissingFile => error
    expansion&.fail!(error.message)
  rescue StandardError => error
    Rails.logger.error("Expansion job #{expansion_id} failed: #{error.class}")
    expansion&.fail!("Expansion failed.")
  end
end
```

`claim!` makes a duplicate delivery a no-op. Each rescue records a safe,
terminal state rather than leaving the browser polling a dead job.

- [ ] **Step 4: Implement the thin controller and routes**

Replace `app/controllers/expansions_controller.rb` with:

```ruby
class ExpansionsController < ApplicationController
  rescue_from ActiveRecord::RecordNotFound do
    render json: { detail: "Expansion not found." }, status: :not_found
  end

  def create
    file_name = params[:file_name].to_s
    selected_text = params[:selected_text].to_s
    question = params[:question].to_s
    if file_name.blank? || selected_text.blank? || question.blank?
      render json: { detail: "Missing file_name, selected_text, or question." }, status: :bad_request
      return
    end

    expansion = current_user.expansions.create!(
      file_name: file_name,
      selected_text: selected_text,
      occurrence: [params[:occurrence].to_i, 0].max,
      question: question,
      use_openai: ActiveModel::Type::Boolean.new.cast(params[:use_openai])
    )
    GenerateExpansionJob.perform_later(expansion.id)

    render json: status_payload(expansion), status: :accepted
  end

  def show
    render json: status_payload(current_user.expansions.find(params[:id]))
  end

  private

  def status_payload(expansion)
    { id: expansion.id, status: expansion.status }.tap do |payload|
      payload[:url] = expansion.url if expansion.status == "completed"
      payload[:detail] = expansion.error_detail if expansion.status == "failed"
    end
  end
end
```

In `config/routes.rb`, add the status route directly after the existing post:

```ruby
get "/expansions/:id", to: "expansions#show", constraints: { id: /\d+/ }
```

The `get` route must remain above `get "/:file_name"` so job polling cannot be
captured as a served-file request.

- [ ] **Step 5: Run the focused backend tests**

Run: `rtk test bin/rails test test/controllers/expansions_controller_test.rb test/jobs/generate_expansion_job_test.rb`

Expected: PASS with no synchronous response/reload assertions remaining.

- [ ] **Step 6: Commit the async API**

```sh
rtk git add app/jobs/generate_expansion_job.rb app/controllers/expansions_controller.rb config/routes.rb test/jobs/generate_expansion_job_test.rb test/controllers/expansions_controller_test.rb
rtk git commit -m "feat: queue and poll text expansions"
```

### Task 5: Replace reload-on-success with fixed, independent status bars

**Files:**
- Modify: `app/assets/javascripts/expand.js`

**Interfaces:**
- Consumes: `POST /expansions` payload `{ id, status }`; `GET /expansions/:id` payloads `{ id, status }`, `{ id, status: "completed", url }`, and `{ id, status: "failed", detail }`.
- Produces: page-local full-width pending, completed-link, and error bars; no JavaScript state is persisted.

- [ ] **Step 1: Add the fixed-bar helpers before `showPopover`**

Insert the following functions after `showButton` in
`app/assets/javascripts/expand.js`:

```javascript
  const POLL_INTERVAL_MS = 1500;
  const jobs = new Map();

  function statusContainer() {
    let container = document.getElementById("expansion-statuses");
    if (container) return container;

    container = document.createElement("div");
    container.id = "expansion-statuses";
    Object.assign(container.style, {
      position: "fixed", top: "0", left: "0", width: "100%",
      boxSizing: "border-box", zIndex: "10000", display: "flex",
      flexDirection: "column", font: "14px system-ui, sans-serif"
    });
    document.body.appendChild(container);
    return container;
  }

  function addStatusBar(jobId, selection) {
    const bar = document.createElement("div");
    const content = document.createElement("span");
    const close = document.createElement("button");
    Object.assign(bar.style, {
      width: "100%", boxSizing: "border-box", minWidth: "0", padding: "10px 12px",
      background: "#1b1b1b", color: "#eee", borderBottom: "1px solid #555",
      display: "flex", alignItems: "center", gap: "12px"
    });
    Object.assign(content.style, {
      minWidth: "0", flex: "1", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap"
    });
    close.type = "button";
    close.textContent = "×";
    close.setAttribute("aria-label", "Dismiss expansion");
    Object.assign(close.style, {
      flex: "0 0 auto", border: "0", background: "transparent", color: "inherit",
      font: "24px/1 system-ui, sans-serif", cursor: "pointer", padding: "0 2px"
    });
    content.textContent = `Expanding text “${selection}”`;
    bar.append(content, close);
    statusContainer().appendChild(bar);

    const record = { bar, content, timer: null };
    jobs.set(jobId, record);
    close.addEventListener("click", () => dismissJob(jobId));
    return record;
  }

  function dismissJob(jobId) {
    const record = jobs.get(jobId);
    if (!record) return;
    clearTimeout(record.timer);
    record.bar.remove();
    jobs.delete(jobId);
    const container = document.getElementById("expansion-statuses");
    if (container && !container.children.length) container.remove();
  }

  function schedulePoll(jobId) {
    const record = jobs.get(jobId);
    if (record) record.timer = setTimeout(() => pollJob(jobId), POLL_INTERVAL_MS);
  }

  function renderCompleted(record, url) {
    const link = document.createElement("a");
    link.href = url;
    link.textContent = "Expansion ready — open it";
    link.style.color = "#bb86fc";
    record.content.replaceChildren(link);
  }

  function renderFailure(record, detail) {
    record.content.textContent = detail || "Expansion failed.";
    record.bar.style.color = "#ffaaaa";
  }

  function pollJob(jobId) {
    const record = jobs.get(jobId);
    if (!record) return;

    fetch(`/expansions/${encodeURIComponent(jobId)}`, { headers: { Accept: "application/json" } })
      .then(async (response) => {
        const data = await response.json().catch(() => ({}));
        if (!response.ok) throw new Error(data.detail || "Unable to check expansion status.");
        return data;
      })
      .then((data) => {
        const current = jobs.get(jobId);
        if (!current) return;
        if (data.status === "completed") renderCompleted(current, data.url);
        else if (data.status === "failed") renderFailure(current, data.detail);
        else schedulePoll(jobId);
      })
      .catch((error) => {
        const current = jobs.get(jobId);
        if (current) renderFailure(current, error.message);
      });
  }
```

`content` is the only flex-shrinking child, so the CSS ellipsis applies to the
full original selection while the `×` always remains visible.

- [ ] **Step 2: Replace the form submit success branch**

Within the existing `popover.addEventListener("submit", ...)` handler, keep
the request body and client-side failure handling, but replace the current
`location.reload()` success callback with:

```javascript
          const jobId = data.id;
          if (!jobId) throw new Error("Expansion was not queued.");
          addStatusBar(jobId, currentSelection.text);
          removeUI();
          pollJob(jobId);
```

Also change the enabled-submit label from `"Expanding… (may take a minute)"`
to `"Queue expansion"`; this button is now only disabled while the short
create request is in flight, not while generation happens.

- [ ] **Step 3: Clear page-local timers on navigation**

Add this handler near the existing `pagehide` listener:

```javascript
  window.addEventListener("pagehide", () => {
    jobs.forEach((record) => clearTimeout(record.timer));
    jobs.clear();
  });
```

Do not write these jobs to local storage, session storage, URL state, or the
server-side page layout; refresh/navigation must make the bars disappear.

- [ ] **Step 4: Perform focused manual browser verification**

Run: `rtk proxy ./serve`

In another terminal, create `files/async-bar-manual.md` with two distinct
expandable phrases, sign in, and open `http://localhost:8009/async-bar-manual.md`.

Verify, in order:

1. Submit the first phrase; the popover closes immediately and a fixed,
   full-width bar appears at the viewport top while the document can scroll.
2. Submit a second phrase before the first completes; a second independent bar
   stacks below the first.
3. Narrow the browser window; each pending selection is visibly ellipsized by
   CSS and its `×` remains available.
4. Wait for success; only that bar turns into a clickable generated-page link,
   with no page reload.
5. Cause one generator failure; its row displays the safe error until `×` is
   clicked, while a separate job can still complete.
6. Reload the source page; no previous rows are restored.

- [ ] **Step 5: Commit the client behavior**

```sh
rtk git add app/assets/javascripts/expand.js
rtk git commit -m "feat: show async expansion status bars"
```

### Task 6: Run the full regression suite and document operation

**Files:**
- Modify: `README.md`

**Interfaces:**
- Documents: `./serve` runs the in-process async queue and expansions are visible only until the page reloads/navigates.

- [ ] **Step 1: Add the asynchronous behavior to the text-expansion feature bullet**

Replace the existing expansion bullet in `README.md` with:

```markdown
- Select text on any page to queue a linked AI answer page. Multiple expansions run in the background; fixed status bars remain only while the current page is open (requires the `claude` CLI; falls back to `codex`).
```

- [ ] **Step 2: Run the entire automated suite**

Run: `rtk test bin/rails test`

Expected: PASS with 0 failures and 0 errors. If it fails, diagnose the actual
failure before changing code; do not weaken an existing test to force green.

- [ ] **Step 3: Check migrations and asset compilation**

Run: `rtk proxy bin/rails db:migrate && rtk test bin/rails assets:precompile && rtk git diff --check`

Expected: all commands exit 0; `git diff --check` reports no whitespace errors.

- [ ] **Step 4: Commit the documentation and verification result**

```sh
rtk git add README.md db/schema.rb
rtk git commit -m "docs: explain async expansion status"
```

## Plan self-review

- Spec coverage: Tasks 1–4 make POST asynchronous, persist and user-scope jobs, expose polling state, preserve safe errors, and prevent duplicate execution/source-write races. Task 5 covers fixed full-width stacked bars, CSS-only ellipsis, links, errors, dismissal, multi-job polling, and page-lifetime-only state. Task 6 covers verification and operation.
- Placeholder scan: no unresolved work markers or deferred implementation references remain; all code changes include concrete snippets and commands.
- Type consistency: controller creates `Expansion` records with the fields validated by the model; job calls `ExpansionProcessor.process(expansion)`; status JSON and JavaScript property names are `id`, `status`, `url`, and `detail` throughout.
