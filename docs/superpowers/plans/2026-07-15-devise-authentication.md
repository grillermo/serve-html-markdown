# Devise Authentication Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Password-based sign-in (Devise, database_authenticatable + rememberable) protecting all file-viewing routes, with a seeded admin user and no registration.

**Architecture:** Enable ActiveRecord with PostgreSQL (credentials from `.env`). Add Devise with only `database_authenticatable`, `rememberable`, and `validatable` modules — no registerable/recoverable, so Devise generates only session routes. `ApplicationController` requires authentication globally; `FilesController#create` opts out and keeps its existing `API_TOKEN` bearer auth. `/health` is a rack proc and bypasses controllers entirely. A seed script creates/updates the single user from `ADMIN_EMAIL`/`ADMIN_PASSWORD` env vars.

**Tech Stack:** Rails 8.1, PostgreSQL (`pg` gem), Devise ~> 4.9, dotenv-rails (already present), Minitest integration tests with `Devise::Test::IntegrationHelpers`.

**Context notes for the implementer:**
- This app was generated WITHOUT ActiveRecord: `config/application.rb` has `require "active_record/railtie"` commented out, and there is no `db/` directory or `database.yml`. Task 1 fixes that.
- There is no `app/models/application_record.rb` — it must be created.
- dotenv-rails loads `.env` automatically in development and test. In production, env vars come from the environment (the `serve` script).
- Tests run in parallel with processes; Rails auto-creates per-worker Postgres test databases (`serve_html_markdown_test-0`, …). The Postgres user must have CREATEDB privileges.
- `bin/rails test` auto-maintains the test schema from `db/schema.rb` (Rails default), so no manual test-db migration steps are needed after `db:migrate` has produced a schema.
- Run all commands from the repo root. `.env` must contain valid `DATABASE_*` values before any `db:*` command works.

---

## Chunk 1: All tasks

### Task 1: Enable ActiveRecord + PostgreSQL

**Files:**
- Modify: `Gemfile`
- Modify: `config/application.rb`
- Create: `config/database.yml`
- Modify: `.env.example`
- Modify: `bin/setup`

- [ ] **Step 1: Add pg gem**

In `Gemfile`, after `gem "puma", ">= 5.0"`:

```ruby
# PostgreSQL database for authentication
gem "pg", "~> 1.5"
```

- [ ] **Step 2: Enable the ActiveRecord railtie**

In `config/application.rb`, change:

```ruby
# require "active_record/railtie"
```

to:

```ruby
require "active_record/railtie"
```

- [ ] **Step 3: Create `config/database.yml`**

```yaml
default: &default
  adapter: postgresql
  encoding: unicode
  pool: <%= ENV.fetch("RAILS_MAX_THREADS") { 5 } %>
  host: <%= ENV.fetch("DATABASE_HOST", "localhost") %>
  port: <%= ENV.fetch("DATABASE_PORT", 5432) %>
  username: <%= ENV["DATABASE_USER"] %>
  password: <%= ENV["DATABASE_PASSWORD"] %>

development:
  <<: *default
  database: serve_html_markdown_development

test:
  <<: *default
  database: serve_html_markdown_test

production:
  <<: *default
  database: serve_html_markdown_production
```

- [ ] **Step 4: Update `.env.example`**

Replace the file contents with:

```
API_TOKEN=
GEMINI_API_KEY=
HOST=localhost
DATABASE_HOST=localhost
DATABASE_PORT=5432
DATABASE_USER=
DATABASE_PASSWORD=
ADMIN_EMAIL=
ADMIN_PASSWORD=
```

- [ ] **Step 5: Add db:prepare to `bin/setup`**

In `bin/setup`, after the "Installing dependencies" block, add:

```ruby
  puts "\n== Preparing database =="
  system! "bin/rails db:prepare"
```

- [ ] **Step 6: Install and create databases**

Run: `bundle install`
Expected: `pg` installed, `Gemfile.lock` updated.

Ensure the local `.env` has real `DATABASE_*` values, then run: `bin/rails db:prepare`
Expected: `Created database 'serve_html_markdown_development'` (and test db). It also dumps an empty `db/schema.rb` — verify it exists (`ls db/schema.rb`); parallel test workers rebuild their per-worker databases from it. If `db:prepare` fails with a connection error, fix `.env` before continuing.

- [ ] **Step 7: Verify existing tests still pass**

Run: `bin/rails test`
Expected: all existing tests PASS (ActiveRecord now boots but nothing else changed). If per-worker database setup fails here, confirm `db/schema.rb` exists and the Postgres user has CREATEDB privileges.

- [ ] **Step 8: Commit**

```bash
git add Gemfile Gemfile.lock config/application.rb config/database.yml .env.example bin/setup db/schema.rb
git commit -m "feat: enable ActiveRecord with PostgreSQL"
```

### Task 2: Add Devise and the User model

**Files:**
- Modify: `Gemfile`
- Create: `config/initializers/devise.rb` (generated)
- Create: `config/locales/devise.en.yml` (generated)
- Create: `app/models/application_record.rb`
- Create: `app/models/user.rb`
- Create: `db/migrate/<timestamp>_devise_create_users.rb`
- Modify: `config/routes.rb`
- Test: `test/models/user_test.rb`

- [ ] **Step 1: Add devise gem**

In `Gemfile`, after the `pg` line:

```ruby
# Authentication
gem "devise", "~> 4.9"
```

Run: `bundle install`
Expected: devise installed.

- [ ] **Step 2: Run the Devise install generator**

Run: `bin/rails generate devise:install`
Expected: creates `config/initializers/devise.rb` and `config/locales/devise.en.yml`. Ignore the printed manual-setup instructions (no mailer needed — recoverable/confirmable are not used).

- [ ] **Step 3: Create `app/models/application_record.rb`**

```ruby
class ApplicationRecord < ActiveRecord::Base
  primary_abstract_class
end
```

- [ ] **Step 4: Write the failing model test**

Replace/create `test/models/user_test.rb`:

```ruby
require "test_helper"

class UserTest < ActiveSupport::TestCase
  test "authenticates with a valid password" do
    user = User.create!(email: "admin@example.com", password: "s3cretpass")

    assert user.valid_password?("s3cretpass")
    assert_not user.valid_password?("wrong")
  end

  test "requires an email" do
    user = User.new(password: "s3cretpass")

    assert_not user.valid?
  end
end
```

- [ ] **Step 5: Run test to verify it fails**

Run: `bin/rails test test/models/user_test.rb`
Expected: FAIL with `NameError: uninitialized constant UserTest::User`.

- [ ] **Step 6: Create the migration**

Create `db/migrate/<timestamp>_devise_create_users.rb` — generate the timestamp with `bin/rails generate migration DeviseCreateUsers` and replace the generated file's contents, or name the file with the current UTC time as `YYYYMMDDHHMMSS_devise_create_users.rb`:

```ruby
class DeviseCreateUsers < ActiveRecord::Migration[8.1]
  def change
    create_table :users do |t|
      ## Database authenticatable
      t.string :email, null: false, default: ""
      t.string :encrypted_password, null: false, default: ""

      ## Rememberable
      t.datetime :remember_created_at

      t.timestamps null: false
    end

    add_index :users, :email, unique: true
  end
end
```

- [ ] **Step 7: Create `app/models/user.rb`**

```ruby
class User < ApplicationRecord
  devise :database_authenticatable, :rememberable, :validatable
end
```

- [ ] **Step 8: Add Devise routes**

In `config/routes.rb`, add as the first line inside the draw block:

```ruby
  devise_for :users
```

(Only session routes are generated because registerable/recoverable are not enabled. Verify with `bin/rails routes -g users` — expect exactly `new_user_session` GET, `user_session` POST, and `destroy_user_session` DELETE.)

- [ ] **Step 9: Migrate**

Run: `bin/rails db:migrate`
Expected: `DeviseCreateUsers: migrated`, `db/schema.rb` updated with the users table.

- [ ] **Step 10: Run test to verify it passes**

Run: `bin/rails test test/models/user_test.rb`
Expected: 2 runs, 0 failures.

- [ ] **Step 11: Commit**

```bash
git add Gemfile Gemfile.lock config/initializers/devise.rb config/locales/devise.en.yml app/models db/migrate db/schema.rb config/routes.rb test/models/user_test.rb
git commit -m "feat: add Devise User model with database_authenticatable and rememberable"
```

### Task 3: Require sign-in for file viewing

**Files:**
- Modify: `test/test_helper.rb`
- Modify: `test/controllers/files_controller_test.rb`
- Modify: `app/controllers/application_controller.rb`
- Modify: `app/controllers/files_controller.rb`

- [ ] **Step 1: Add Devise integration helpers to `test/test_helper.rb`**

Append at the bottom of the file:

```ruby
class ActionDispatch::IntegrationTest
  include Devise::Test::IntegrationHelpers
end
```

- [ ] **Step 2: Update `test/controllers/files_controller_test.rb` — sign in existing tests, add new ones**

In the `setup` block, after the FILES_DIR swap, add:

```ruby
    @user = User.create!(email: "viewer@example.com", password: "s3cretpass")
    sign_in @user
```

Add these tests:

```ruby
  test "redirects unauthenticated viewers to sign in" do
    sign_out @user
    write_file "notes.md", "# Notes"

    get "/notes.md"

    assert_redirected_to new_user_session_path
  end

  test "redirects unauthenticated root requests to sign in" do
    sign_out @user

    get "/"

    assert_redirected_to new_user_session_path
  end

  test "creates files with a bearer token and no session" do
    sign_out @user

    with_env("API_TOKEN", "token-123") do
      with_formatter(->(*) { "formatted" }) do
        post "/file/new",
          params: { content: "# Hi", filename: "hi.md" },
          headers: { "Authorization" => "Bearer token-123" }
      end
    end

    assert_response :success
  end
```

Notes for the implementer:
- The file already has `with_env` and `with_formatter` private helpers (around lines 215-235) — reuse them exactly as the existing upload tests do. `with_formatter` is REQUIRED: without it `FilesController::FORMATTER` (`GeminiFormatter`) either raises `ConfigurationError` (502) when `GEMINI_API_KEY` is blank or makes a real network call to Gemini when it is set in `.env`.
- Read the whole existing test file first; do not remove existing tests. The existing `/health` test must keep passing WITHOUT a session — move the `sign_in` line out of `setup` into a private `sign_in_viewer` helper called by each viewing test if `/health` breaks (it should not: `/health` is a rack proc that never hits controllers).

- [ ] **Step 3: Run tests to verify the new ones fail**

Run: `bin/rails test test/controllers/files_controller_test.rb`
Expected: new "redirects unauthenticated" tests FAIL (responses succeed instead of redirecting); existing tests PASS.

- [ ] **Step 4: Require authentication in `app/controllers/application_controller.rb`**

```ruby
class ApplicationController < ActionController::Base
  before_action :authenticate_user!
end
```

- [ ] **Step 5: Skip Devise auth for API create in `app/controllers/files_controller.rb`**

After the `skip_forgery_protection only: :create` line, add:

```ruby
  skip_before_action :authenticate_user!, only: :create
```

(`#create` keeps its existing `authenticated?` bearer-token check.)

- [ ] **Step 6: Run the full suite to verify everything passes**

Run: `bin/rails test`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add test/test_helper.rb test/controllers/files_controller_test.rb app/controllers/application_controller.rb app/controllers/files_controller.rb
git commit -m "feat: require sign-in for file viewing routes"
```

### Task 4: Sign-in page

**Files:**
- Create: `app/views/devise/sessions/new.html.erb`
- Modify: `config/initializers/devise.rb`
- Test: `test/controllers/sessions_test.rb`

- [ ] **Step 1: Write the failing integration test**

Create `test/controllers/sessions_test.rb`:

```ruby
require "test_helper"

class SessionsTest < ActionDispatch::IntegrationTest
  setup do
    @user = User.create!(email: "viewer@example.com", password: "s3cretpass")
  end

  test "renders the sign-in form" do
    get new_user_session_path

    assert_response :success
    assert_select "form[action=?]", user_session_path do
      assert_select "input[name='user[email]']"
      assert_select "input[name='user[password]']"
      assert_select "input[name='user[remember_me]'][type=checkbox]"
    end
  end

  test "signs in with valid credentials and remembers the user" do
    post user_session_path, params: {
      user: { email: "viewer@example.com", password: "s3cretpass", remember_me: "1" }
    }

    assert_redirected_to root_path
    assert cookies[:remember_user_token].present?
  end

  test "rejects invalid credentials" do
    post user_session_path, params: {
      user: { email: "viewer@example.com", password: "wrong" }
    }

    assert_response :unprocessable_entity
  end
end
```

- [ ] **Step 2: Run test to verify current state**

Run: `bin/rails test test/controllers/sessions_test.rb`
Expected: "rejects invalid credentials" FAILS with 200 instead of 422 (Devise 4.9 defaults `error_status` to `:ok` for backward compatibility). "renders the sign-in form" may also fail depending on Devise's bundled view markup. "signs in with valid credentials" should PASS; if not, debug before proceeding.

- [ ] **Step 3: Set modern responder statuses in `config/initializers/devise.rb`**

Uncomment (or add) these lines in the generated initializer:

```ruby
  config.responder.error_status = :unprocessable_entity
  config.responder.redirect_status = :see_other
```

Run: `bin/rails test test/controllers/sessions_test.rb`
Expected: "rejects invalid credentials" now PASSES.

- [ ] **Step 4: Create the custom sign-in view**

Create `app/views/devise/sessions/new.html.erb`:

```erb
<% content_for :title, "Sign in" %>

<main class="signin">
  <h1>Sign in</h1>

  <% if alert %>
    <p class="signin-alert"><%= alert %></p>
  <% end %>

  <%= form_for(resource, as: resource_name, url: session_path(resource_name)) do |f| %>
    <div class="field">
      <%= f.label :email %>
      <%= f.email_field :email, autofocus: true, autocomplete: "email", required: true %>
    </div>

    <div class="field">
      <%= f.label :password %>
      <%= f.password_field :password, autocomplete: "current-password", required: true %>
    </div>

    <div class="field field--checkbox">
      <%= f.check_box :remember_me %>
      <%= f.label :remember_me %>
    </div>

    <%= f.submit "Sign in" %>
  <% end %>
</main>
```

Style minimally in `app/assets/stylesheets/application.css`, matching the existing file's conventions (read it first). Keep it small: center the form, space the fields, make `.signin-alert` visibly red.

- [ ] **Step 5: Run tests to verify they pass**

Run: `bin/rails test test/controllers/sessions_test.rb`
Expected: 3 runs, 0 failures.

- [ ] **Step 6: Commit**

```bash
git add app/views/devise app/assets/stylesheets/application.css config/initializers/devise.rb test/controllers/sessions_test.rb
git commit -m "feat: add sign-in page with remember me"
```

### Task 5: Seed the admin user

**Files:**
- Create: `db/seeds.rb`

- [ ] **Step 1: Create `db/seeds.rb`**

```ruby
email = ENV.fetch("ADMIN_EMAIL")
password = ENV.fetch("ADMIN_PASSWORD")

user = User.find_or_initialize_by(email: email)
user.password = password
user.save!

puts "Seeded user #{email}"
```

(Idempotent: re-running updates the password rather than failing. `fetch` without defaults makes missing env vars fail loudly.)

- [ ] **Step 2: Verify the seed runs**

Ensure `.env` has `ADMIN_EMAIL` and `ADMIN_PASSWORD` set, then:

Run: `bin/rails db:seed`
Expected: `Seeded user <email>`. Run it twice to confirm idempotency (no error on second run).

- [ ] **Step 3: Commit**

```bash
git add db/seeds.rb
git commit -m "feat: seed admin user from ENV"
```

### Task 6: Final verification

- [ ] **Step 1: Full test suite**

Run: `bin/rails test`
Expected: all PASS, 0 failures, 0 errors.

- [ ] **Step 2: Manual smoke test**

Run `bin/dev`, then in a browser:
1. Visit `http://localhost:3000/` → redirected to `/users/sign_in`.
2. Sign in with the seeded `ADMIN_EMAIL`/`ADMIN_PASSWORD`, check "Remember me" → redirected to root (which redirects to the newest file, or shows "No files found" JSON if none).
3. `curl -X POST -H "Authorization: Bearer $API_TOKEN" -d 'content=# Hello' -d 'filename=hello.md' http://localhost:3000/file/new` → returns a JSON URL without any session.
4. `curl -I http://localhost:3000/health` → 200 without auth.

- [ ] **Step 3: Update `README.md`**

Add a short "Authentication" section: sign-in required for viewing, user is seeded from `ADMIN_EMAIL`/`ADMIN_PASSWORD` via `bin/rails db:seed`, database configured via `DATABASE_*` env vars, API uploads still use `API_TOKEN`.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: document authentication setup"
```
