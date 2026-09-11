# YouTube Re-authorization Link Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a twitter-video ingest fails on a dead YouTube OAuth grant, Slack posts a link that re-authorizes in the browser, stores the new refresh token, and re-queues the stalled video.

**Architecture:** The uploader translates Signet/googleauth authorization failures into a typed `YoutubeUploader::AuthorizationExpired`. `TwitterVideoIngestJob` branches on that type to post a re-auth link instead of a stack trace. A new `YoutubeAuthorization` service owns the OAuth URL building and code exchange; a Devise-guarded `YoutubeAuthorizationsController` runs the two-leg browser flow and persists the token in a single-row `youtube_credentials` table, which the job now reads instead of `ENV["YOUTUBE_REFRESH_TOKEN"]`.

**Tech Stack:** Rails 8.1 (edge, `rails/rails@main`), PostgreSQL, minitest, Devise 4.9, `signet` 0.22, `googleauth` 1.17, `google-apis-youtube_v3` 0.67, Solid Queue, dotenv-rails.

**Spec:** `docs/superpowers/specs/2026-09-11-youtube-reauth-link-design.md`

## Global Constraints

- **Run tests with:** `bin/rails test` (whole suite, ~1s) or `bin/rails test path/to/file.rb -n test_name`. The suite is serial by default (`PARALLEL_WORKERS=1` in `test/test_helper.rb`).
- **Migration class version:** `ActiveRecord::Migration[8.1]`, matching `db/migrate/20260820120100_add_expansion_mode_to_users.rb`.
- **Migration filenames:** hand-written timestamps, e.g. `20260911120000_create_youtube_credentials.rb`.
- **`dotenv-rails` loads `.env` in the test environment too.** Never `ENV.delete` a `YOUTUBE_*` key in a test teardown — save the previous value and restore it, or later tests in the same process lose it.
- **Redirect URI must match Google's registered value byte-for-byte:** `https://serve.chiq.me/youtube/callback`. Derive it from `HOST`, never from `request.base_url` (the tunnel forwards plain HTTP, so a request-derived URI would be `http://…` and Google would reject it).
- **Env var defaults follow the existing convention** (`app/controllers/files_controller.rb:97`, `app/jobs/twitter_video_captions_job.rb:73`): `ENV.fetch("HOST", "localhost:8009")` — no new variable is required for the test environment.
- **minitest is 6.0.6, which ships NO `minitest/mock`.** `Object#stub`, `Minitest::Mock` and `stub_any_instance` do not exist here. Test collaborators only through constructor keywords or the class-level injection idiom below — never reach for a stubbing helper.
- **Scope string is `YoutubeUploader::SCOPE`** (`https://www.googleapis.com/auth/youtube.upload`). Do not re-declare it.
- **Collaborator-injection idiom:** class-level `attr_writer` + a lambda reader + a `reset_*!` method, as in `TwitterVideoIngestJob` (`app/jobs/twitter_video_ingest_job.rb:14-26`). Tests set the writer in `setup` and reset in `teardown`.
- **Commit after each task**, using the repo's `type: subject` style and ending every commit message with:

  ```
  Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_012SvGHdfHcPtYvbwHjKMzJZ
  ```

**One deliberate deviation from the spec:** the spec's sample Slack text says `YouTube authorization expired (invalid_grant)`. `AuthorizationExpired` also covers "no token stored at all", where `invalid_grant` would be a lie, so the implemented text omits it. The full Google message still reaches `Rails.logger` and `twitter_videos.error_detail` unchanged.

## File Structure

| File | Responsibility |
| --- | --- |
| `db/migrate/20260911120000_create_youtube_credentials.rb` | Create the single-row token table |
| `app/models/youtube_credential.rb` | Read/write the stored refresh token, fall back to ENV |
| `app/services/youtube_uploader.rb` (modify) | Add `AuthorizationExpired`; translate Signet failures |
| `app/services/youtube_authorization.rb` | Consent URL, code exchange, and the two public URLs (`redirect_uri`, `reauth_url`) |
| `app/jobs/twitter_video_ingest_job.rb` (modify) | Read the token from the DB; post the re-auth link |
| `app/controllers/youtube_authorizations_controller.rb` | The two-leg browser flow, `state` check, retry enqueue |
| `app/views/youtube_authorizations/create.html.erb` | Success page |
| `app/views/youtube_authorizations/problem.html.erb` | Denied / bad-state / exchange-failure page |
| `config/routes.rb` (modify) | `GET /youtube/reauth`, `GET /youtube/callback` |
| `lib/tasks/youtube.rake` (modify) | Replace the dead OOB flow with a pointer to the browser flow |
| `docs/configure-twitter-video-to-html.md`, `.env.example` (modify) | Document the browser flow and the new optional env vars |

---

### Task 1: `youtube_credentials` table and model

**Files:**
- Create: `db/migrate/20260911120000_create_youtube_credentials.rb`
- Create: `app/models/youtube_credential.rb`
- Test: `test/models/youtube_credential_test.rb`

**Interfaces:**
- Consumes: nothing.
- Produces: `YoutubeCredential.refresh_token → String | nil`, `YoutubeCredential.store!(String) → YoutubeCredential`, `YoutubeCredential.current → YoutubeCredential | nil`.

- [ ] **Step 1: Write the failing test**

Create `test/models/youtube_credential_test.rb`:

```ruby
require "test_helper"

class YoutubeCredentialTest < ActiveSupport::TestCase
  test "prefers the stored row over the env var" do
    YoutubeCredential.create!(refresh_token: "from-db", obtained_at: Time.current)

    with_env("YOUTUBE_REFRESH_TOKEN" => "from-env") do
      assert_equal "from-db", YoutubeCredential.refresh_token
    end
  end

  test "falls back to the env var when nothing is stored" do
    with_env("YOUTUBE_REFRESH_TOKEN" => "from-env") do
      assert_equal "from-env", YoutubeCredential.refresh_token
    end
  end

  test "returns nil when neither a row nor the env var exists" do
    with_env("YOUTUBE_REFRESH_TOKEN" => nil) do
      assert_nil YoutubeCredential.refresh_token
    end
  end

  test "store! keeps a single row and overwrites the token" do
    YoutubeCredential.store!("first")
    YoutubeCredential.store!("second")

    assert_equal 1, YoutubeCredential.count
    assert_equal "second", YoutubeCredential.current.refresh_token
    assert_not_nil YoutubeCredential.current.obtained_at
  end

  private
    # dotenv-rails loads the real .env in the test env, so restore whatever was there.
    def with_env(values)
      previous = values.keys.index_with { |key| ENV[key] }
      values.each { |key, value| value.nil? ? ENV.delete(key) : ENV[key] = value }
      yield
    ensure
      previous.each { |key, value| value.nil? ? ENV.delete(key) : ENV[key] = value }
    end
end
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bin/rails test test/models/youtube_credential_test.rb`
Expected: FAIL — `NameError: uninitialized constant YoutubeCredential`.

- [ ] **Step 3: Write the migration**

Create `db/migrate/20260911120000_create_youtube_credentials.rb`:

```ruby
class CreateYoutubeCredentials < ActiveRecord::Migration[8.1]
  def change
    create_table :youtube_credentials do |t|
      t.text :refresh_token, null: false
      t.datetime :obtained_at, null: false
      t.timestamps
    end
  end
end
```

- [ ] **Step 4: Run the migration**

Run: `bin/rails db:migrate`
Expected: `create_table(:youtube_credentials)` and a new `youtube_credentials` block in `db/schema.rb`.

- [ ] **Step 5: Write the model**

Create `app/models/youtube_credential.rb`:

```ruby
# The live YouTube refresh token. Stored in the database rather than .env so the
# browser re-auth flow can replace it without an edit-and-restart.
class YoutubeCredential < ApplicationRecord
  validates :refresh_token, presence: true

  class << self
    # ENV is the bootstrap path: a checkout that has never run the browser flow.
    def refresh_token
      current&.refresh_token.presence || ENV["YOUTUBE_REFRESH_TOKEN"].presence
    end

    def store!(token)
      record = current || new
      record.update!(refresh_token: token, obtained_at: Time.current)
      record
    end

    def current
      order(:id).last
    end
  end
end
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `bin/rails test test/models/youtube_credential_test.rb`
Expected: 4 runs, 0 failures.

- [ ] **Step 7: Commit**

```bash
git add db/migrate/20260911120000_create_youtube_credentials.rb db/schema.rb \
        app/models/youtube_credential.rb test/models/youtube_credential_test.rb
git commit -m "$(cat <<'EOF'
feat: store the youtube refresh token in the database

The browser re-auth flow needs somewhere to put a new token that takes
effect without editing .env and restarting. ENV stays as the bootstrap
fallback for a checkout that has never re-authorized.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012SvGHdfHcPtYvbwHjKMzJZ
EOF
)"
```

---

### Task 2: `YoutubeUploader::AuthorizationExpired`

**Files:**
- Modify: `app/services/youtube_uploader.rb:6-19` (error classes and constructor), `:38-52` (`build_service`)
- Test: `test/services/youtube_uploader_test.rb`

**Interfaces:**
- Consumes: nothing.
- Produces: `YoutubeUploader::AuthorizationExpired < YoutubeUploader::Error`, raised by the constructor when `refresh_token` is blank and by `build_service` when Signet refuses the grant. Also a new `authorizer:` constructor keyword (default `nil`), the test seam that replaces the internally built `Signet::OAuth2::Client` — symmetric with the existing `service:` keyword.

**Why one rescue is enough:** googleauth's Signet wrapper (`googleauth/signet.rb:127`) converts `Signet::AuthorizationError` into `Google::Auth::AuthorizationError`, which subclasses it (`googleauth/errors.rb:91`). Rescuing the parent catches both.

- [ ] **Step 1: Write the failing tests**

Append to `test/services/youtube_uploader_test.rb` (inside the class, after the existing tests), and add `require "googleauth"` under the existing `require "test_helper"` at the top of the file so `Google::Auth::AuthorizationError` resolves explicitly:

```ruby
  test "translates a dead refresh token into AuthorizationExpired" do
    uploader = YoutubeUploader.new(
      client_id: "c", client_secret: "s", refresh_token: "dead",
      authorizer: DeadAuthorizer.new(Google::Auth::AuthorizationError,
                                     'Authorization failed. Server message: {"error": "invalid_grant"}')
    )

    error = assert_raises(YoutubeUploader::AuthorizationExpired) do
      uploader.upload(file_path: "/tmp/x.mp4", title: "t")
    end
    assert_match(/invalid_grant/, error.message)
  end

  test "translates a bare Signet authorization error too" do
    uploader = YoutubeUploader.new(
      client_id: "c", client_secret: "s", refresh_token: "dead",
      authorizer: DeadAuthorizer.new(Signet::AuthorizationError, "nope")
    )

    assert_raises(YoutubeUploader::AuthorizationExpired) do
      uploader.upload(file_path: "/tmp/x.mp4", title: "t")
    end
  end

  test "a blank refresh token is an expired authorization, not a config error" do
    assert_raises(YoutubeUploader::AuthorizationExpired) do
      YoutubeUploader.new(client_id: "c", client_secret: "s", refresh_token: "")
    end
  end

  test "a blank client id stays a plain configuration error" do
    error = assert_raises(YoutubeUploader::Error) do
      YoutubeUploader.new(client_id: "", client_secret: "s", refresh_token: "r")
    end
    assert_not error.is_a?(YoutubeUploader::AuthorizationExpired)
  end

  class DeadAuthorizer
    def initialize(error_class, message)
      @error_class = error_class
      @message = message
    end

    def fetch_access_token!
      raise @error_class, @message
    end
  end
```

No stubbing helper is used: minitest 6 has none, so the authorizer arrives through a constructor keyword instead.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `bin/rails test test/services/youtube_uploader_test.rb`
Expected: FAIL — `NameError: uninitialized constant YoutubeUploader::AuthorizationExpired`.

- [ ] **Step 3: Add the error class and split the constructor guard**

In `app/services/youtube_uploader.rb`, replace:

```ruby
  Error = Class.new(StandardError)
```

with:

```ruby
  Error = Class.new(StandardError)
  # A dead or missing refresh token. Recoverable by re-running the browser OAuth
  # flow, so callers can offer a link instead of a stack trace.
  AuthorizationExpired = Class.new(Error)
```

and replace the constructor's signature and guard:

```ruby
  def initialize(client_id:, client_secret:, refresh_token:, service: nil)
    if client_id.blank? || client_secret.blank? || refresh_token.blank?
      raise Error, "YouTube OAuth credentials are not configured."
    end
```

with:

```ruby
  # authorizer: is a test seam, like service: below it.
  def initialize(client_id:, client_secret:, refresh_token:, service: nil, authorizer: nil)
    if client_id.blank? || client_secret.blank?
      raise Error, "YouTube OAuth credentials are not configured."
    end
    raise AuthorizationExpired, "No YouTube refresh token is stored." if refresh_token.blank?
```

and add `@authorizer = authorizer` next to the existing `@service = service` assignment.

- [ ] **Step 4: Translate the Signet failure in `build_service`**

In the same file, add a rescue to `build_service` so it reads:

```ruby
    def build_service
      authorizer = @authorizer || Signet::OAuth2::Client.new(
        token_credential_uri: OAUTH_TOKEN_URL,
        client_id: @client_id,
        client_secret: @client_secret,
        refresh_token: @refresh_token,
        scope: SCOPE
      )
      authorizer.fetch_access_token!
      svc = Google::Apis::YoutubeV3::YouTubeService.new
      svc.authorization = authorizer
      svc
    rescue Signet::AuthorizationError => error
      # googleauth wraps Signet failures in Google::Auth::AuthorizationError, a
      # subclass of this one, so both arrive here. invalid_grant means the refresh
      # token is dead and only a human with a browser can fix it.
      raise AuthorizationExpired, "YouTube authorization expired: #{error.message}"
    end
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `bin/rails test test/services/youtube_uploader_test.rb`
Expected: 6 runs, 0 failures (the two pre-existing tests still pass — `AuthorizationExpired` is an `Error`, so the original `assert_raises(YoutubeUploader::Error)` test is unaffected).

- [ ] **Step 6: Commit**

```bash
git add app/services/youtube_uploader.rb test/services/youtube_uploader_test.rb
git commit -m "$(cat <<'EOF'
feat: type dead youtube grants as AuthorizationExpired

Signet's failure escaped as a raw Google::Auth::AuthorizationError, so
callers could not tell "the human must re-authorize" apart from any other
upload failure. A blank refresh token now lands in the same bucket.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012SvGHdfHcPtYvbwHjKMzJZ
EOF
)"
```

---

### Task 3: `YoutubeAuthorization` service

**Files:**
- Create: `app/services/youtube_authorization.rb`
- Test: `test/services/youtube_authorization_test.rb`

**Interfaces:**
- Consumes: `YoutubeUploader::SCOPE`, `YoutubeUploader::OAUTH_TOKEN_URL`.
- Produces:
  - `YoutubeAuthorization.redirect_uri → String`
  - `YoutubeAuthorization.reauth_url(video_id = nil) → String`
  - `YoutubeAuthorization.build → lambda returning an instance` (+ `build=`, `reset_build!`) — the injection point the controller uses and tests override
  - `YoutubeAuthorization.new(client_id:, client_secret:, redirect_uri:, client: nil)`
  - `#consent_url(state:) → String`
  - `#exchange!(code:) → String` (the refresh token)
  - `YoutubeAuthorization::Error`, `YoutubeAuthorization::ConfigurationError < Error`

**Signet detail that matters:** `Signet::OAuth2::Client#authorization_uri` merges `additional_parameters` into the query string (`signet/oauth_2/client.rb`, `options.merge!(additional_parameters...)`), which is how `access_type=offline` and `prompt=consent` get into the URL. Without `prompt=consent`, Google omits the refresh token on a re-consent.

- [ ] **Step 1: Write the failing test**

Create `test/services/youtube_authorization_test.rb`:

```ruby
require "test_helper"

class YoutubeAuthorizationTest < ActiveSupport::TestCase
  teardown { YoutubeAuthorization.reset_build! }

  test "consent url asks for offline access and a fresh consent" do
    url = build_auth.consent_url(state: "st4te")
    params = Rack::Utils.parse_query(URI.parse(url).query)

    assert_equal "https://accounts.google.com/o/oauth2/auth", url.split("?").first
    assert_equal "offline", params["access_type"]
    assert_equal "consent", params["prompt"]
    assert_equal "st4te", params["state"]
    assert_equal "code", params["response_type"]
    assert_equal "cid", params["client_id"]
    assert_equal "https://serve.chiq.me/youtube/callback", params["redirect_uri"]
    assert_equal YoutubeUploader::SCOPE, params["scope"]
  end

  test "exchange! hands google the code and returns the refresh token" do
    fake = FakeClient.new("1//refresh")

    assert_equal "1//refresh", build_auth(client: fake).exchange!(code: "abc")
    assert_equal "abc", fake.code
    assert fake.fetched
  end

  test "exchange! raises when google sends no refresh token" do
    error = assert_raises(YoutubeAuthorization::Error) do
      build_auth(client: FakeClient.new(nil)).exchange!(code: "abc")
    end
    assert_match(/no refresh token/, error.message)
  end

  test "missing client credentials name the absent variable" do
    error = assert_raises(YoutubeAuthorization::ConfigurationError) do
      YoutubeAuthorization.new(client_id: "", client_secret: "sec")
    end
    assert_match(/YOUTUBE_CLIENT_ID/, error.message)
  end

  test "reauth_url appends the video id when given" do
    with_env("YOUTUBE_REAUTH_URL" => "https://serve.chiq.me/youtube/reauth") do
      assert_equal "https://serve.chiq.me/youtube/reauth", YoutubeAuthorization.reauth_url
      assert_equal "https://serve.chiq.me/youtube/reauth?video_id=7", YoutubeAuthorization.reauth_url(7)
    end
  end

  test "build is overridable for tests" do
    sentinel = Object.new
    YoutubeAuthorization.build = -> { sentinel }

    assert_same sentinel, YoutubeAuthorization.build.call
  end

  private
    def build_auth(client: nil)
      YoutubeAuthorization.new(client_id: "cid", client_secret: "sec",
                               redirect_uri: "https://serve.chiq.me/youtube/callback",
                               client: client)
    end

    def with_env(values)
      previous = values.keys.index_with { |key| ENV[key] }
      values.each { |key, value| value.nil? ? ENV.delete(key) : ENV[key] = value }
      yield
    ensure
      previous.each { |key, value| value.nil? ? ENV.delete(key) : ENV[key] = value }
    end

    class FakeClient
      attr_accessor :code
      attr_reader :refresh_token, :fetched

      def initialize(refresh_token)
        @refresh_token = refresh_token
        @fetched = false
      end

      def fetch_access_token!
        @fetched = true
        { "access_token" => "at" }
      end
    end
end
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bin/rails test test/services/youtube_authorization_test.rb`
Expected: FAIL — `NameError: uninitialized constant YoutubeAuthorization`.

- [ ] **Step 3: Write the service**

Create `app/services/youtube_authorization.rb`:

```ruby
require "signet/oauth_2/client"

# The browser half of YouTube OAuth: build a consent URL, then trade the code Google
# hands back for a refresh token. Deliberately knows nothing about requests or
# controllers, so the URL shape is testable without a browser.
class YoutubeAuthorization
  Error = Class.new(StandardError)
  ConfigurationError = Class.new(Error)

  CONSENT_URI = "https://accounts.google.com/o/oauth2/auth".freeze

  class << self
    attr_writer :build

    # Injection point for controllers and tests, mirroring TwitterVideoIngestJob.
    def build = @build ||= -> { new }
    def reset_build! = (@build = nil)

    # Must match the redirect URI registered on the Google web client byte-for-byte.
    # Derived from HOST, never from the request: the public tunnel forwards plain
    # HTTP, so a request-derived URI would be http:// and Google would reject it.
    def redirect_uri
      ENV.fetch("YOUTUBE_REDIRECT_URI") { "https://#{host}/youtube/callback" }
    end

    def reauth_url(video_id = nil)
      base = ENV.fetch("YOUTUBE_REAUTH_URL") { "https://#{host}/youtube/reauth" }
      video_id.present? ? "#{base}?video_id=#{video_id}" : base
    end

    private
      def host = ENV.fetch("HOST", "localhost:8009")
  end

  def initialize(client_id: ENV["YOUTUBE_CLIENT_ID"], client_secret: ENV["YOUTUBE_CLIENT_SECRET"],
                 redirect_uri: self.class.redirect_uri, client: nil)
    raise ConfigurationError, "YOUTUBE_CLIENT_ID is not set." if client_id.blank?
    raise ConfigurationError, "YOUTUBE_CLIENT_SECRET is not set." if client_secret.blank?

    @client_id = client_id
    @client_secret = client_secret
    @redirect_uri = redirect_uri
    @client = client
  end

  def consent_url(state:)
    client.authorization_uri(state: state).to_s
  end

  # Google only returns a refresh token when prompt=consent is requested, which is why
  # that parameter is not optional below.
  def exchange!(code:)
    client.code = code
    client.fetch_access_token!
    client.refresh_token.presence ||
      raise(Error, "Google returned no refresh token for this code.")
  end

  private
    def client
      @client ||= Signet::OAuth2::Client.new(
        authorization_uri: CONSENT_URI,
        token_credential_uri: YoutubeUploader::OAUTH_TOKEN_URL,
        client_id: @client_id,
        client_secret: @client_secret,
        scope: YoutubeUploader::SCOPE,
        redirect_uri: @redirect_uri,
        additional_parameters: { "access_type" => "offline", "prompt" => "consent" }
      )
    end
end
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `bin/rails test test/services/youtube_authorization_test.rb`
Expected: 6 runs, 0 failures.

- [ ] **Step 5: Commit**

```bash
git add app/services/youtube_authorization.rb test/services/youtube_authorization_test.rb
git commit -m "$(cat <<'EOF'
feat: add YoutubeAuthorization for the browser oauth flow

Owns the consent URL and the code exchange, plus the two public URLs the
Slack message and the callback need. No request knowledge, so the URL
shape is unit-testable.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012SvGHdfHcPtYvbwHjKMzJZ
EOF
)"
```

---

### Task 4: the Slack re-authorization link

**Files:**
- Modify: `app/jobs/twitter_video_ingest_job.rb:17-22` (uploader lambda), `:53-58` (rescue)
- Note: `YoutubeCredential.store!` in the job test needs no fixtures — the row is created inline and rolled back with the test transaction.
- Test: `test/jobs/twitter_video_ingest_job_test.rb`

**Interfaces:**
- Consumes: `YoutubeCredential.refresh_token` (Task 1), `YoutubeUploader::AuthorizationExpired` (Task 2), `YoutubeAuthorization.reauth_url(video_id)` (Task 3).
- Produces: nothing new for later tasks.

- [ ] **Step 1: Write the failing tests**

Append to `test/jobs/twitter_video_ingest_job_test.rb`, inside the class before the `FakeYtdlp` class definition:

```ruby
  test "posts a re-authorization link when the youtube grant is dead" do
    TwitterVideoIngestJob.uploader = ->(*) { raise YoutubeUploader::AuthorizationExpired, "invalid_grant" }

    with_env("YOUTUBE_REAUTH_URL" => "https://serve.chiq.me/youtube/reauth") do
      TwitterVideoIngestJob.perform_now(@video.id)
    end

    assert_equal "failed", @video.reload.status
    message = @slack.failures.last
    assert_includes message, "YouTube authorization expired"
    assert_includes message, "https://serve.chiq.me/youtube/reauth?video_id=#{@video.id}"
    assert_includes message, "already downloaded"
  end

  test "keeps the raw error text for failures a link cannot fix" do
    TwitterVideoIngestJob.ytdlp = ->(*) { raise YtdlpClient::Error, "gone" }

    TwitterVideoIngestJob.perform_now(@video.id)

    message = @slack.failures.last
    assert_includes message, "YtdlpClient::Error: gone"
    assert_not_includes message, "youtube/reauth"
  end

  test "the default uploader takes its refresh token from the database" do
    # setup replaced the uploader with a fake; this test exercises the real default.
    TwitterVideoIngestJob.reset_collaborators!

    with_env("YOUTUBE_CLIENT_ID" => "cid", "YOUTUBE_CLIENT_SECRET" => "sec",
             "YOUTUBE_REFRESH_TOKEN" => nil) do
      # No row and no env var: the constructor guard from Task 2 fires.
      assert_raises(YoutubeUploader::AuthorizationExpired) { TwitterVideoIngestJob.uploader.call }

      YoutubeCredential.store!("from-db")

      assert_nothing_raised { TwitterVideoIngestJob.uploader.call }
    end
  end
```

and add this private helper at the end of the class, after the fake classes:

```ruby
  private
    def with_env(key, value)
      previous = ENV[key]
      ENV[key] = value
      yield
    ensure
      previous.nil? ? ENV.delete(key) : ENV[key] = previous
    end
```

The third test needs no stubbing (minitest 6 has none): `reset_collaborators!` restores the real default lambda, and the Task 2 guard — blank refresh token raises `AuthorizationExpired` — is what proves which source the lambda read. `YoutubeUploader.new` builds no network client, so constructing it is safe.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `bin/rails test test/jobs/twitter_video_ingest_job_test.rb`
Expected: FAIL — the first test's message is the old `ingest failed: YoutubeUploader::AuthorizationExpired: invalid_grant` with no link; the third raises `AuthorizationExpired` even after the row is stored, because the lambda still reads `ENV["YOUTUBE_REFRESH_TOKEN"]`.

- [ ] **Step 3: Read the token from the database**

In `app/jobs/twitter_video_ingest_job.rb`, change the `uploader` default so `refresh_token:` comes from the model:

```ruby
    def uploader = @uploader ||= lambda do |*|
      YoutubeUploader.new(
        client_id: ENV["YOUTUBE_CLIENT_ID"], client_secret: ENV["YOUTUBE_CLIENT_SECRET"],
        refresh_token: YoutubeCredential.refresh_token
      )
    end
```

- [ ] **Step 4: Branch the failure message**

Replace the job's `rescue` block's Slack call so the method reads:

```ruby
  rescue StandardError => error
    Rails.logger.error("[TwitterVideoIngestJob] ##{twitter_video_id} #{error.class}: #{error.message}")
    video&.fail!(error.message)
    self.class.slack.call.failure(failure_message(twitter_video_id, error))
  end
```

and add this as the first private method:

```ruby
    # A dead OAuth grant is the one failure a human can fix from a phone, so it gets a
    # link instead of a stack trace. The full Google message still reaches the log and
    # twitter_videos.error_detail.
    def failure_message(video_id, error)
      unless error.is_a?(YoutubeUploader::AuthorizationExpired)
        return "[twitter-video ##{video_id}] ingest failed: #{error.class}: #{error.message}"
      end

      "[twitter-video ##{video_id}] ingest failed: YouTube authorization expired.\n" \
        "re-authorize: #{YoutubeAuthorization.reauth_url(video_id)}\n" \
        "the video is already downloaded — the upload retries by itself once you're done."
    end
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `bin/rails test test/jobs/twitter_video_ingest_job_test.rb`
Expected: 9 runs, 0 failures.

- [ ] **Step 6: Commit**

```bash
git add app/jobs/twitter_video_ingest_job.rb test/jobs/twitter_video_ingest_job_test.rb
git commit -m "$(cat <<'EOF'
feat: slack posts a re-auth link when the youtube grant dies

The old message dumped Google's invalid_grant JSON with no path forward.
Authorization failures now carry the /youtube/reauth link with the stalled
video id; every other failure keeps its raw text.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012SvGHdfHcPtYvbwHjKMzJZ
EOF
)"
```

---

### Task 5: the browser flow — routes, controller, views

**Files:**
- Create: `app/controllers/youtube_authorizations_controller.rb`
- Create: `app/views/youtube_authorizations/create.html.erb`
- Create: `app/views/youtube_authorizations/problem.html.erb`
- Modify: `config/routes.rb:7-8` (after the twitter-video routes, well above the `/:file_name` catch-all)
- Test: `test/controllers/youtube_authorizations_controller_test.rb`

**Interfaces:**
- Consumes: `YoutubeAuthorization.build`, `#consent_url(state:)`, `#exchange!(code:)`, `YoutubeCredential.store!`, `TwitterVideoIngestJob.perform_later`.
- Produces: `YoutubeAuthorizationsController::STATE_KEY` and `::VIDEO_KEY` (session key names the tests read).

- [ ] **Step 1: Write the failing test**

Create `test/controllers/youtube_authorizations_controller_test.rb`:

```ruby
require "test_helper"

class YoutubeAuthorizationsControllerTest < ActionDispatch::IntegrationTest
  setup do
    @user = User.create!(email: "youtube-oauth@example.com", password: "s3cretpass")
    @video = TwitterVideo.create!(source_url: "https://x.com/foo/status/9", status: "failed")
    @auth = FakeAuthorization.new("1//new-token")
    YoutubeAuthorization.build = -> { @auth }
  end

  teardown { YoutubeAuthorization.reset_build! }

  test "requires a signed-in user" do
    get "/youtube/reauth"

    assert_redirected_to new_user_session_path
  end

  test "redirects to google with a state parameter" do
    sign_in @user

    get "/youtube/reauth", params: { video_id: @video.id }

    assert_redirected_to "https://accounts.google.com/o/oauth2/auth?state=#{state}"
    assert_equal @video.id.to_s, session[YoutubeAuthorizationsController::VIDEO_KEY]
    assert_equal state, @auth.state
  end

  test "stores the token and re-queues the stalled video" do
    sign_in @user
    get "/youtube/reauth", params: { video_id: @video.id }

    assert_enqueued_with(job: TwitterVideoIngestJob, args: [@video.id]) do
      get "/youtube/callback", params: { code: "auth-code", state: state }
    end

    assert_response :success
    assert_equal "auth-code", @auth.exchanged_code
    assert_equal "1//new-token", YoutubeCredential.refresh_token
    assert_nil session[YoutubeAuthorizationsController::STATE_KEY]
  end

  test "rejects a mismatched state without storing anything" do
    sign_in @user
    get "/youtube/reauth", params: { video_id: @video.id }

    get "/youtube/callback", params: { code: "auth-code", state: "forged" }

    assert_response :bad_request
    assert_nil YoutubeCredential.current
    assert_no_enqueued_jobs only: TwitterVideoIngestJob
  end

  test "reports a denied consent without storing anything" do
    sign_in @user
    get "/youtube/reauth", params: { video_id: @video.id }

    get "/youtube/callback", params: { error: "access_denied", state: state }

    assert_response :bad_request
    assert_nil YoutubeCredential.current
  end

  test "stores the token even when no video was waiting" do
    sign_in @user
    get "/youtube/reauth"

    assert_no_enqueued_jobs only: TwitterVideoIngestJob do
      get "/youtube/callback", params: { code: "auth-code", state: state }
    end

    assert_response :success
    assert_equal "1//new-token", YoutubeCredential.refresh_token
  end

  private
    def state = session[YoutubeAuthorizationsController::STATE_KEY]

    class FakeAuthorization
      attr_reader :state, :exchanged_code

      def initialize(token) = (@token = token)

      def consent_url(state:)
        @state = state
        "https://accounts.google.com/o/oauth2/auth?state=#{state}"
      end

      def exchange!(code:)
        @exchanged_code = code
        @token
      end
    end
end
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bin/rails test test/controllers/youtube_authorizations_controller_test.rb`
Expected: FAIL — `ActionController::RoutingError: No route matches [GET] "/youtube/reauth"`.

- [ ] **Step 3: Add the routes**

In `config/routes.rb`, directly below the `get "/twitter-video/:id"` line:

```ruby
  get "/youtube/reauth", to: "youtube_authorizations#new"
  get "/youtube/callback", to: "youtube_authorizations#create"
```

- [ ] **Step 4: Write the controller**

Create `app/controllers/youtube_authorizations_controller.rb`:

```ruby
# Runs the two-leg YouTube OAuth flow a human reaches from the Slack failure link.
# Inherits ApplicationController on purpose: these routes are publicly reachable at
# serve.chiq.me, so Devise's authenticate_user! guards them.
class YoutubeAuthorizationsController < ApplicationController
  STATE_KEY = "youtube_oauth_state".freeze
  VIDEO_KEY = "youtube_oauth_video_id".freeze

  def new
    state = SecureRandom.hex(16)
    session[STATE_KEY] = state
    session[VIDEO_KEY] = params[:video_id]
    redirect_to authorization.consent_url(state: state), allow_other_host: true
  rescue YoutubeAuthorization::ConfigurationError => error
    render_problem(error.message, :internal_server_error)
  end

  def create
    return render_problem("Google denied the request: #{params[:error]}", :bad_request) if params[:error].present?
    return render_problem("Authorization state did not match. Start again from the Slack link.", :bad_request) unless valid_state?

    YoutubeCredential.store!(authorization.exchange!(code: params[:code].to_s))
    @video_id = retry_stalled_video
    clear_oauth_session
    render :create
  rescue YoutubeAuthorization::Error => error
    render_problem(error.message, :bad_gateway)
  end

  private
    def authorization = @authorization ||= YoutubeAuthorization.build.call

    def valid_state?
      expected = session[STATE_KEY].to_s
      given = params[:state].to_s
      expected.present? && given.bytesize == expected.bytesize &&
        ActiveSupport::SecurityUtils.secure_compare(given, expected)
    end

    # The mp4 is still on disk, so the retry skips yt-dlp and goes straight to the upload.
    def retry_stalled_video
      id = session[VIDEO_KEY]
      return nil if id.blank? || !TwitterVideo.exists?(id: id)

      TwitterVideoIngestJob.perform_later(id.to_i)
      id.to_i
    end

    def clear_oauth_session
      session.delete(STATE_KEY)
      session.delete(VIDEO_KEY)
    end

    def render_problem(message, status)
      @message = message
      render :problem, status: status
    end
end
```

- [ ] **Step 5: Write the two views**

Create `app/views/youtube_authorizations/create.html.erb` (reusing the `signin` class, the only single-column page style in `app/assets/stylesheets`):

```erb
<% content_for :title, "YouTube re-authorized" %>

<main class="signin">
  <h1>YouTube re-authorized</h1>

  <p>The new refresh token is stored. Nothing to copy, nothing to restart.</p>

  <% if @video_id %>
    <p>Re-queued twitter video #<%= @video_id %> — the upload resumes from the file already on disk.</p>
  <% else %>
    <p>No video was waiting on this, so nothing was re-queued.</p>
  <% end %>
</main>
```

Create `app/views/youtube_authorizations/problem.html.erb`:

```erb
<% content_for :title, "YouTube authorization failed" %>

<main class="signin">
  <h1>YouTube authorization failed</h1>

  <p><%= @message %></p>

  <p><%= link_to "Try again", "/youtube/reauth" %></p>
</main>
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `bin/rails test test/controllers/youtube_authorizations_controller_test.rb`
Expected: 6 runs, 0 failures.

- [ ] **Step 7: Run the whole suite**

Run: `bin/rails test`
Expected: 0 failures, 0 errors.

- [ ] **Step 8: Commit**

```bash
git add config/routes.rb app/controllers/youtube_authorizations_controller.rb \
        app/views/youtube_authorizations test/controllers/youtube_authorizations_controller_test.rb
git commit -m "$(cat <<'EOF'
feat: add the /youtube/reauth browser oauth round trip

Devise-guarded, state-checked, and it re-queues the video that stalled on
the dead token — so one tap on the Slack link both fixes auth and finishes
the ingest.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012SvGHdfHcPtYvbwHjKMzJZ
EOF
)"
```

---

### Task 6: retire the OOB rake task and document the flow

**Files:**
- Modify: `lib/tasks/youtube.rake` (replace the whole file)
- Modify: `docs/configure-twitter-video-to-html.md:52-105` (steps 5 onward)
- Modify: `.env.example`

**Interfaces:**
- Consumes: `YoutubeAuthorization.reauth_url`.
- Produces: nothing.

**Why:** the task's `urn:ietf:wg:oauth:2.0:oob` redirect was blocked by Google in 2022; the browser flow replaces it. The task name stays so existing muscle memory and the docs still land somewhere useful.

- [ ] **Step 1: Replace the rake task**

Overwrite `lib/tasks/youtube.rake`:

```ruby
namespace :youtube do
  desc "Print the URL that re-authorizes YouTube uploads in a browser"
  task refresh_token: :environment do
    puts "Open this, approve access, and the new refresh token is stored for you:"
    puts
    puts YoutubeAuthorization.reauth_url
    puts
    puts "You must be signed in to the app first — the page is behind Devise."
  end
end
```

- [ ] **Step 2: Run it to verify it prints the URL**

Run: `bin/rails youtube:refresh_token`
Expected: prints `https://serve.chiq.me/youtube/reauth` (or `https://localhost:8009/youtube/reauth` when `HOST` is unset), and exits 0 without prompting for input.

- [ ] **Step 3: Document the browser flow**

In `docs/configure-twitter-video-to-html.md`, replace step 4's "Application type: **Desktop app**" with **Web application**, add the authorized redirect URIs, and replace the whole of "## 5. Obtain a refresh token" with:

```markdown
## 4. Create OAuth client credentials

1. **APIs & Services → Credentials → Create Credentials → OAuth client ID**, or
   go straight to <https://console.cloud.google.com/auth/clients>.
2. Application type: **Web application**.
3. Under **Authorized redirect URIs** add both:
   - `https://serve.chiq.me/youtube/callback` — the real one.
   - `http://127.0.0.1:8009/youtube/callback` — loopback is exempt from
     Google's HTTPS rule, so the flow still works with the tunnel down.
4. Copy the **Client ID** and **Client secret** into `.env`:

   ```dotenv
   YOUTUBE_CLIENT_ID=your-client-id.apps.googleusercontent.com
   YOUTUBE_CLIENT_SECRET=your-client-secret
   ```

The redirect URI must match what the app sends byte-for-byte — no trailing
slash. The app builds it from `HOST` (override with `YOUTUBE_REDIRECT_URI`).

## 5. Publish the project so tokens stop expiring

An OAuth project whose publishing status is **Testing** issues refresh tokens
that die after 7 days. At <https://console.cloud.google.com/auth/audience>,
press **Publish app** so the status reads **In production**. Don't submit for
verification: unverified-in-production costs only the "Google hasn't verified
this app" screen (click **Advanced → Go to … (unsafe)**) and a 100-user cap,
neither of which matters for a single account.

## 6. Authorize

Open <https://serve.chiq.me/youtube/reauth> (sign in to the app first), approve
access, and the refresh token is stored in the `youtube_credentials` table.
There is nothing to copy into `.env` — `YOUTUBE_REFRESH_TOKEN` is now only a
bootstrap fallback for a checkout that has never authorized.

`rake youtube:refresh_token` just prints that URL.

When a token does die, the ingest job posts the link to Slack with the stalled
video's id, and finishing the flow re-queues that video automatically.
```

Then update the Troubleshooting section: delete the "Refresh token stops working after ~7 days" bullet's advice to re-run the rake task periodically, replacing it with a pointer to step 5, and delete the "`invalid_grant` when running the rake task" bullet (there is no code to paste any more).

- [ ] **Step 4: Document the new env vars**

In `.env.example`, below the existing `YOUTUBE_REFRESH_TOKEN` line:

```dotenv
# Optional. Both default to https://$HOST/youtube/{callback,reauth}. YOUTUBE_REDIRECT_URI
# must match the redirect URI registered on the Google web client byte-for-byte.
YOUTUBE_REDIRECT_URI=
YOUTUBE_REAUTH_URL=
```

- [ ] **Step 5: Run the whole suite one last time**

Run: `bin/rails test`
Expected: 0 failures, 0 errors.

- [ ] **Step 6: Commit**

```bash
git add lib/tasks/youtube.rake docs/configure-twitter-video-to-html.md .env.example
git commit -m "$(cat <<'EOF'
docs: replace the dead oob rake flow with the browser flow

Google blocked urn:ietf:wg:oauth:2.0:oob in 2022, so the task could no
longer produce a token. It now prints the /youtube/reauth URL. Records the
web client, its two redirect URIs, and why the project must be published.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012SvGHdfHcPtYvbwHjKMzJZ
EOF
)"
```

---

## Manual verification after Task 6

The automated tests never talk to Google, so finish with one real round trip:

1. `./serve`, then open `https://serve.chiq.me/youtube/reauth` and approve. Expect the success page saying nothing was re-queued.
2. `bin/rails runner 'puts YoutubeCredential.current.inspect'` — a row with a `1//`-prefixed token and a fresh `obtained_at`.
3. POST a tweet URL to `/twitter-video` and confirm the upload succeeds using the stored token.
4. To exercise the failure path end to end: `bin/rails runner 'YoutubeCredential.store!("broken")'`, POST a tweet, and confirm Slack shows the re-auth link with that video's id. Click it, approve, and confirm the video finishes without re-downloading (the log should say `reusing …`).

## Self-Review

**Spec coverage:** every spec section maps to a task — `youtube_credentials` + model → Task 1; typed error → Task 2; `YoutubeAuthorization` (incl. `REDIRECT_URI` from `HOST`, `prompt=consent`, `ConfigurationError`) → Task 3; job wiring, `REAUTH_URL`, branched message → Task 4; controller, routes, state check, retry, error-handling table → Task 5; rake task and docs → Task 6. The spec's error-handling table is covered by Task 5's tests except "response has no refresh token", which is tested at the service level in Task 3.

**Naming consistency:** `YoutubeCredential.refresh_token` / `.store!` / `.current`; `YoutubeAuthorization.build` / `.reset_build!` / `.redirect_uri` / `.reauth_url` / `#consent_url(state:)` / `#exchange!(code:)`; `YoutubeUploader::AuthorizationExpired`; `STATE_KEY` / `VIDEO_KEY`. Each is defined in the task that introduces it and used with the same spelling everywhere after.
