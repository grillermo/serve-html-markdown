# Scroll Position Persistence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remember each signed-in user's scroll position per document on the backend, and restore it automatically the next time they open that document.

**Architecture:** A new `scroll_positions` table keyed by `(user_id, file_name)` stores an **anchor**: the `id` of the topmost element visible in the viewport when the reader last scrolled. `FilesController#show` looks up the saved anchor and embeds it as `window.__scrollAnchor` in the served page (markdown layout or raw injected `.html`). The existing `expand.js` (already loaded on every page) restores scroll on load via `scrollIntoView()` and saves the current topmost anchor via a debounced `PATCH /scroll_position` while scrolling plus a `pagehide` flush. Markdown headings need `header_ids` enabled in `Commonmarker` to have `id`s to anchor to in the first place.

**Tech Stack:** Rails 8.1, PostgreSQL, Devise, Commonmarker (comrak), vanilla JS (no bundler), Minitest.

## Global Constraints

- Position is an **anchor id string**, not a pixel offset or scroll-height fraction.
- Scoped per `(user_id, file_name)`, unique index enforced at the DB level.
- `anchor` must match `/\A[\w\-:.]+\z/` — this is the primary XSS defense, since the value is later embedded server-side into an inline `<script>` tag. Validated at the model layer; the controller responds 422 on violation.
- Endpoint uses the app's default `authenticate_user!` and standard CSRF protection — no `skip_forgery_protection`.
- Reuses the existing single JS asset `app/assets/javascripts/expand.js`. No new JS files.
- The endpoint does not resolve or validate that `file_name` corresponds to an actual file — it's an opaque per-key store, unlike `ResolvesServedFiles`.
- Restore uses `Element.scrollIntoView()` (default instant, not smooth).
- Save is debounced ~500ms after scrolling stops, plus flushed on `pagehide`.
- Save is a no-op when no anchor currently qualifies as "topmost visible" (e.g. document has no `id`-bearing elements, or reader is scrolled above the first one).
- Reference spec: `docs/superpowers/specs/2026-07-20-scroll-position-persistence-design.md`.

---

### Task 1: `ScrollPosition` model + migration

**Files:**
- Create: `db/migrate/20260720120000_create_scroll_positions.rb`
- Create: `app/models/scroll_position.rb`
- Modify: `app/models/user.rb`
- Test: `test/models/scroll_position_test.rb`

**Interfaces:**
- Consumes: `User` model (existing, `app/models/user.rb`).
- Produces: `ScrollPosition` — `belongs_to :user`, attributes `file_name` (string), `anchor` (string, format `/\A[\w\-:.]+\z/`), validated unique per `[user_id, file_name]`. `User#scroll_positions` association.

- [ ] **Step 1: Write the failing test**

```ruby
# test/models/scroll_position_test.rb
require "test_helper"

class ScrollPositionTest < ActiveSupport::TestCase
  setup do
    @user = User.create!(email: "reader@example.com", password: "s3cretpass")
  end

  test "valid with a user, file_name, and a slug-shaped anchor" do
    scroll_position = ScrollPosition.new(user: @user, file_name: "notes.md", anchor: "sub-heading-1")

    assert scroll_position.valid?
  end

  test "requires file_name" do
    scroll_position = ScrollPosition.new(user: @user, anchor: "intro")

    assert_not scroll_position.valid?
    assert_includes scroll_position.errors[:file_name], "can't be blank"
  end

  test "requires anchor" do
    scroll_position = ScrollPosition.new(user: @user, file_name: "notes.md")

    assert_not scroll_position.valid?
    assert_includes scroll_position.errors[:anchor], "can't be blank"
  end

  test "rejects an anchor containing characters outside the safe charset" do
    ["<script>", "\"; alert(1)", "has space", "a/b", "a\\b"].each do |unsafe|
      scroll_position = ScrollPosition.new(user: @user, file_name: "notes.md", anchor: unsafe)

      assert_not scroll_position.valid?, "expected #{unsafe.inspect} to be invalid"
    end
  end

  test "accepts anchors with word characters, hyphens, colons, and periods" do
    scroll_position = ScrollPosition.new(user: @user, file_name: "notes.md", anchor: "Section_2.1:intro-part")

    assert scroll_position.valid?
  end

  test "enforces one position per user and file_name" do
    ScrollPosition.create!(user: @user, file_name: "notes.md", anchor: "intro")
    duplicate = ScrollPosition.new(user: @user, file_name: "notes.md", anchor: "outro")

    assert_not duplicate.valid?
    assert_includes duplicate.errors[:file_name], "has already been taken"
  end

  test "allows the same file_name for different users" do
    other_user = User.create!(email: "other@example.com", password: "s3cretpass")
    ScrollPosition.create!(user: @user, file_name: "notes.md", anchor: "intro")
    other_position = ScrollPosition.new(user: other_user, file_name: "notes.md", anchor: "outro")

    assert other_position.valid?
  end

  test "belongs to a user" do
    scroll_position = ScrollPosition.create!(user: @user, file_name: "notes.md", anchor: "intro")

    assert_equal @user, scroll_position.user
    assert_includes @user.scroll_positions, scroll_position
  end
end
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bin/rails test test/models/scroll_position_test.rb`
Expected: FAIL with `NameError: uninitialized constant ScrollPositionTest::ScrollPosition`

- [ ] **Step 3: Create the migration and run it**

```ruby
# db/migrate/20260720120000_create_scroll_positions.rb
class CreateScrollPositions < ActiveRecord::Migration[8.1]
  def change
    create_table :scroll_positions do |t|
      t.references :user, null: false, foreign_key: true
      t.string :file_name, null: false
      t.string :anchor, null: false

      t.timestamps
    end

    add_index :scroll_positions, [:user_id, :file_name], unique: true
  end
end
```

Run: `bin/rails db:migrate`
Expected: `== CreateScrollPositions: migrated` and `db/schema.rb` updated with the new table.

- [ ] **Step 4: Write the model and the User association**

```ruby
# app/models/scroll_position.rb
class ScrollPosition < ApplicationRecord
  ANCHOR_FORMAT = /\A[\w\-:.]+\z/

  belongs_to :user

  validates :file_name, presence: true
  validates :file_name, uniqueness: { scope: :user_id }
  validates :anchor, presence: true, format: { with: ANCHOR_FORMAT }
end
```

Modify `app/models/user.rb`:

```ruby
class User < ApplicationRecord
  devise :database_authenticatable, :rememberable, :validatable

  has_many :scroll_positions, dependent: :destroy
end
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `bin/rails test test/models/scroll_position_test.rb`
Expected: PASS (7 runs, 0 failures)

- [ ] **Step 6: Commit**

```bash
git add db/migrate/20260720120000_create_scroll_positions.rb db/schema.rb \
  app/models/scroll_position.rb app/models/user.rb test/models/scroll_position_test.rb
git commit -m "feat: add ScrollPosition model"
```

---

### Task 2: `PATCH /scroll_position` endpoint

**Files:**
- Create: `app/controllers/scroll_positions_controller.rb`
- Modify: `config/routes.rb`
- Test: `test/controllers/scroll_positions_controller_test.rb`

**Interfaces:**
- Consumes: `ScrollPosition` model, `ScrollPosition::ANCHOR_FORMAT`, `current_user.scroll_positions` (Task 1).
- Produces: route `/scroll_position` (verbs `PATCH`, `POST`) → `ScrollPositionsController#update`. Upserts `current_user.scroll_positions.find_or_initialize_by(file_name:)`, sets `anchor`, responds `204 No Content` on success, `400 { detail: ... }` on missing params, `422 { detail: ... }` on an anchor that fails model validation.

- [ ] **Step 1: Write the failing test**

```ruby
# test/controllers/scroll_positions_controller_test.rb
require "test_helper"

class ScrollPositionsControllerTest < ActionDispatch::IntegrationTest
  setup do
    @user = User.create!(email: "scroller@example.com", password: "s3cretpass")
    sign_in @user
  end

  test "creates a scroll position on first save" do
    patch "/scroll_position", params: { file_name: "notes.md", anchor: "intro" }, as: :json

    assert_response :no_content
    record = @user.scroll_positions.find_by(file_name: "notes.md")
    assert_equal "intro", record.anchor
  end

  test "upserts on repeat saves for the same file" do
    patch "/scroll_position", params: { file_name: "notes.md", anchor: "intro" }, as: :json
    patch "/scroll_position", params: { file_name: "notes.md", anchor: "outro" }, as: :json

    assert_response :no_content
    assert_equal 1, @user.scroll_positions.where(file_name: "notes.md").count
    assert_equal "outro", @user.scroll_positions.find_by(file_name: "notes.md").anchor
  end

  test "rejects a missing file_name" do
    patch "/scroll_position", params: { anchor: "intro" }, as: :json

    assert_response :bad_request
    assert_equal({ "detail" => "Missing file_name." }, response.parsed_body)
  end

  test "rejects a missing anchor" do
    patch "/scroll_position", params: { file_name: "notes.md" }, as: :json

    assert_response :bad_request
    assert_equal({ "detail" => "Missing anchor." }, response.parsed_body)
  end

  test "rejects an anchor outside the safe charset" do
    patch "/scroll_position", params: { file_name: "notes.md", anchor: "<script>alert(1)</script>" }, as: :json

    assert_response :unprocessable_content
    assert_nil @user.scroll_positions.find_by(file_name: "notes.md")
  end

  test "accepts the save via POST for sendBeacon compatibility" do
    post "/scroll_position", params: { file_name: "notes.md", anchor: "intro" }, as: :json

    assert_response :no_content
    assert_equal "intro", @user.scroll_positions.find_by(file_name: "notes.md").anchor
  end

  test "rejects unauthenticated requests" do
    sign_out @user

    patch "/scroll_position", params: { file_name: "notes.md", anchor: "intro" }, as: :json

    assert_response :unauthorized
  end

  test "scopes positions to the signed-in user" do
    other_user = User.create!(email: "other@example.com", password: "s3cretpass")
    other_user.scroll_positions.create!(file_name: "notes.md", anchor: "other-intro")

    patch "/scroll_position", params: { file_name: "notes.md", anchor: "my-intro" }, as: :json

    assert_response :no_content
    assert_equal "my-intro", @user.scroll_positions.find_by(file_name: "notes.md").anchor
    assert_equal "other-intro", other_user.scroll_positions.find_by(file_name: "notes.md").anchor
  end
end
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bin/rails test test/controllers/scroll_positions_controller_test.rb`
Expected: FAIL with a routing error (`No route matches [PATCH] "/scroll_position"`)

- [ ] **Step 3: Add the route**

Modify `config/routes.rb`, adding a line next to the other action routes:

```ruby
Rails.application.routes.draw do
  devise_for :users
  match "/health", to: proc { [200, {}, [""]] }, via: :head
  post "/file/new", to: "files#create"
  post "/expansions", to: "expansions#create"
  match "/scroll_position", to: "scroll_positions#update", via: [:patch, :post]
  get "/favicon.ico", to: proc { [204, {}, []] }
  root "files#last"
  get "/last", to: "files#last"
  get "/:file_name", to: "files#show", constraints: { file_name: /[^\/]+/ }, defaults: { format: :html }
end
```

(`via: [:patch, :post]` because `navigator.sendBeacon`, used later for the
`pagehide` save, can only issue `POST` — both verbs hit the same upsert
action.)

- [ ] **Step 4: Write the controller**

```ruby
# app/controllers/scroll_positions_controller.rb
class ScrollPositionsController < ApplicationController
  rescue_from ActionController::BadRequest do |error|
    render json: { detail: error.message }, status: :bad_request
  end
  rescue_from ActiveRecord::RecordInvalid do |error|
    render json: { detail: error.message }, status: :unprocessable_content
  end

  def update
    file_name = params[:file_name].to_s
    anchor = params[:anchor].to_s
    raise ActionController::BadRequest, "Missing file_name." if file_name.blank?
    raise ActionController::BadRequest, "Missing anchor." if anchor.blank?

    record = current_user.scroll_positions.find_or_initialize_by(file_name: file_name)
    record.update!(anchor: anchor)

    head :no_content
  end
end
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `bin/rails test test/controllers/scroll_positions_controller_test.rb`
Expected: PASS (8 runs, 0 failures)

- [ ] **Step 6: Commit**

```bash
git add app/controllers/scroll_positions_controller.rb config/routes.rb \
  test/controllers/scroll_positions_controller_test.rb
git commit -m "feat: add PATCH /scroll_position endpoint"
```

---

### Task 3: Heading IDs + embed saved anchor in `FilesController#show`

**Files:**
- Modify: `app/controllers/files_controller.rb`
- Modify: `app/views/layouts/markdown.html.erb`
- Test: `test/controllers/files_controller_test.rb`

**Interfaces:**
- Consumes: `current_user.scroll_positions` (Task 1).
- Produces: markdown headings now render with `id` attributes (comrak `header_ids` extension). Every served page (markdown-rendered or raw `.html`) includes
  `<script>window.__scrollAnchor = "<anchor>";</script>` next to the CSRF
  meta tag when a saved position exists for `current_user` + that
  `file_name`; omitted otherwise. `inject_expand_script` gains a
  `scroll_anchor:` keyword argument.

- [ ] **Step 1: Write the failing tests**

Add to `test/controllers/files_controller_test.rb` (inside the test class,
alongside the other `.html`/`.md` tests):

```ruby
  test "renders markdown headings with ids" do
    write_file "notes.md", "# Notes\n\n## Sub Heading!"

    get "/notes.md"

    assert_response :success
    assert_select "h1 a#notes"
    assert_select "h2 a#sub-heading"
  end

  test "embeds the saved scroll anchor in the markdown layout" do
    write_file "notes.md", "# Notes\n\n## Sub Heading!"
    ScrollPosition.create!(user: @user, file_name: "notes.md", anchor: "sub-heading")

    get "/notes.md"

    assert_response :success
    assert_includes response.body, %(window.__scrollAnchor = "sub-heading";)
  end

  test "omits the scroll anchor script when the markdown file has none saved" do
    write_file "notes.md", "# Notes"

    get "/notes.md"

    assert_response :success
    assert_not_includes response.body, "__scrollAnchor"
  end

  test "embeds the saved scroll anchor in injected HTML" do
    write_file "page.html", "<html><body><h1 id='top'>Raw</h1></body></html>"
    ScrollPosition.create!(user: @user, file_name: "page.html", anchor: "top")

    get "/page.html"

    assert_response :success
    assert_includes response.body, %(window.__scrollAnchor = "top";)
  end

  test "omits the scroll anchor script when the HTML file has none saved" do
    write_file "page.html", "<html><body><main>Raw</main></body></html>"

    get "/page.html"

    assert_response :success
    assert_not_includes response.body, "__scrollAnchor"
  end

  test "does not leak another user's scroll anchor" do
    write_file "notes.md", "# Notes"
    other_user = User.create!(email: "other@example.com", password: "s3cretpass")
    other_user.scroll_positions.create!(file_name: "notes.md", anchor: "notes")

    get "/notes.md"

    assert_response :success
    assert_not_includes response.body, "__scrollAnchor"
  end
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `bin/rails test test/controllers/files_controller_test.rb`
Expected: the 6 new tests FAIL (no `id` attributes on headings yet,
`__scrollAnchor` never appears in the response body); pre-existing tests
still PASS.

- [ ] **Step 3: Enable heading ids and modify `FilesController`**

```ruby
# app/controllers/files_controller.rb
  MARKDOWN_OPTIONS = {
    render: { unsafe: true },
    extension: { autolink: true, header_ids: "" },
    parse: { smart: true }
  }.freeze
```

```ruby
  def show
    file_path = resolve_file_path(params[:file_name])
    content = file_path.read(encoding: "UTF-8")
    scroll_anchor = current_user.scroll_positions.find_by(file_name: file_path.basename.to_s)&.anchor

    if file_path.extname.downcase == ".html"
      render html: inject_expand_script(content, scroll_anchor: scroll_anchor).html_safe, layout: false
    else
      @file_name = file_path.basename.to_s
      @scroll_anchor = scroll_anchor
      @rendered = Commonmarker.to_html(content, options: MARKDOWN_OPTIONS)
      render :show, formats: :html, layout: "markdown"
    end
  end
```

```ruby
  private
    def inject_expand_script(content, scroll_anchor: nil)
      anchor_snippet = scroll_anchor ? "<script>window.__scrollAnchor = #{scroll_anchor.to_json};</script>" : ""
      snippet = %(<meta name="csrf-token" content="#{form_authenticity_token}">#{anchor_snippet}<script src="#{helpers.asset_path("expand.js")}" defer></script>)
      if content =~ %r{</body>}i
        content.sub(%r{</body>}i) { "#{snippet}</body>" }
      else
        content + snippet
      end
    end
```

(Only `MARKDOWN_OPTIONS`, `show`, and `inject_expand_script` change; `last`,
`create`, and the other private methods are untouched.)

- [ ] **Step 4: Modify the markdown layout**

```erb
<!-- app/views/layouts/markdown.html.erb -->
<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title><%= @file_name %></title>
    <link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Bookerly:ital,wght@0,400;0,700;1,400;1,700&display=swap">
    <%= stylesheet_link_tag "markdown" %>
    <%= csrf_meta_tags %>
    <% if @scroll_anchor %>
      <script>window.__scrollAnchor = <%= raw @scroll_anchor.to_json %>;</script>
    <% end %>
    <%= javascript_include_tag "expand", defer: true %>
  </head>
  <body>
    <%= yield %>
  </body>
</html>
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `bin/rails test test/controllers/files_controller_test.rb`
Expected: PASS, all tests in the file green.

- [ ] **Step 6: Commit**

```bash
git add app/controllers/files_controller.rb app/views/layouts/markdown.html.erb \
  test/controllers/files_controller_test.rb
git commit -m "feat: embed saved scroll anchor when serving a document"
```

---

### Task 4: Restore and save scroll anchor in the browser

**Files:**
- Modify: `app/assets/javascripts/expand.js`

**Interfaces:**
- Consumes: `window.__scrollAnchor` (Task 3, may be `undefined`),
  `PATCH`/`POST /scroll_position` (Task 2), existing `CSRF()` helper already
  defined in this file.
- Produces: on load, scrolls the saved anchor's element into view if
  present; while scrolling, saves the current topmost-visible anchor
  debounced ~500ms; on `pagehide`, flushes an immediate save. No test
  coverage — matches this repo's existing precedent of leaving `expand.js`
  untested (no JS test infra).

- [ ] **Step 1: Add anchor-scanning, restore, and save logic**

Insert the following block into `app/assets/javascripts/expand.js`, just
before the file's closing `})();` (after the existing `keydown` listener for
`Escape`):

```js
  function topmostVisibleAnchor() {
    const anchored = document.querySelectorAll("[id]");
    let current = null;
    for (const el of anchored) {
      if (el.getBoundingClientRect().top <= 1) {
        current = el.id;
      } else {
        break;
      }
    }
    return current;
  }

  function restoreScrollPosition() {
    if (typeof window.__scrollAnchor !== "string") return;
    const target = document.getElementById(window.__scrollAnchor);
    if (target) target.scrollIntoView();
  }

  function saveScrollPosition() {
    const anchor = topmostVisibleAnchor();
    if (!anchor) return;

    const body = JSON.stringify({
      file_name: decodeURIComponent(location.pathname.slice(1)),
      anchor: anchor,
      authenticity_token: CSRF()
    });

    if (navigator.sendBeacon) {
      navigator.sendBeacon("/scroll_position", new Blob([body], { type: "application/json" }));
    } else {
      fetch("/scroll_position", {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: body,
        keepalive: true
      });
    }
  }

  let scrollSaveTimer = null;
  document.addEventListener("scroll", () => {
    clearTimeout(scrollSaveTimer);
    scrollSaveTimer = setTimeout(saveScrollPosition, 500);
  });

  document.addEventListener("pagehide", () => {
    clearTimeout(scrollSaveTimer);
    saveScrollPosition();
  });

  document.addEventListener("DOMContentLoaded", restoreScrollPosition);
```

The `authenticity_token` travels inside the JSON body rather than an
`X-CSRF-Token` header because `navigator.sendBeacon` cannot set custom
headers; Rails reads `params[:authenticity_token]` from a parsed JSON body
just as readily as the header, so one body shape covers both the
`sendBeacon` and `fetch` send paths.

- [ ] **Step 2: Manually verify in the running app**

Run: `bin/rails server -p 8009` (per README), then in a browser:

1. Sign in, open a served `.md` file with several headings and enough
   content to scroll.
2. Scroll so a later heading is at the top of the viewport, wait ~1s
   (debounce), reload the page.
   Expected: page opens already scrolled to that heading.
3. Repeat for a served `.html` file that has an element with an `id`.
   Expected: same behavior.
4. Scroll to the very top (above the first heading) and reload.
   Expected: page opens at the top — no anchor was saved for that state.
5. Open a document with no headings and nothing else with an `id`.
   Expected: no errors in the console; nothing saved for that file;
   scrolling and reloading does nothing (page opens at the top every time).

- [ ] **Step 3: Commit**

```bash
git add app/assets/javascripts/expand.js
git commit -m "feat: restore and persist scroll anchor in the browser"
```

---

## Self-Review Notes

- **Spec coverage:** heading-id prerequisite (Task 3's `header_ids: ""`),
  data model with charset-restricted anchor (Task 1), endpoint with 400/422
  distinction (Task 2), restore embedding for both markdown and raw HTML
  paths (Task 3), frontend topmost-visible-anchor scan + debounce +
  `pagehide` + `sendBeacon` fallback (Task 4) — all covered. The spec's "no
  cleanup job for stale rows" and "no file-name validation" trade-offs are
  reflected in Task 2's endpoint (no `ResolvesServedFiles` dependency) and
  require no task of their own.
- **Placeholder scan:** none found — every step has literal code, not a
  description of code.
- **Type consistency:** `ScrollPosition#anchor` (string) flows unchanged
  from Task 1's model → Task 2's `record.update!(anchor:)` →
  `current_user.scroll_positions.find_by(file_name:)&.anchor` in Task 3 →
  `window.__scrollAnchor` (JS string) in Task 4. `file_name` (string) is
  consistent across all four tasks. `ScrollPosition::ANCHOR_FORMAT` is
  defined once in Task 1 and not redefined elsewhere.
