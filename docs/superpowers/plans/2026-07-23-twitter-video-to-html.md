# Twitter Video → HTML Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Accept a Twitter/X video URL, download it, upload it unlisted to YouTube for auto-captions, summarize the transcript with Gemini, save the summary as a served HTML page, and push a link to it into rulinky — reporting every stage to Slack.

**Architecture:** An API endpoint creates a `twitter_videos` state row and enqueues `TwitterVideoIngestJob` (download + upload). That job schedules `TwitterVideoCaptionsJob` on an elapsed-time backoff (+5/+10/+20/+30/+40/+60/+120 min); when auto-captions appear the captions job summarizes, saves HTML, and pushes to rulinky. All I/O lives in injectable service objects so tests never touch the network or shell.

**Tech Stack:** Rails 8.1, Postgres, ActiveJob AsyncAdapter, Minitest, `yt-dlp` (shelled out), `google-apis-youtube_v3` + `signet` (YouTube upload), raw `Net::HTTP` (Gemini, rulinky, Slack).

## Global Constraints

- Ruby 3.4.7, Rails ~> 8.1.3, Postgres (`pg`), Minitest (`ActiveSupport::TestCase` / `ActiveJob::TestCase`).
- All network/shell I/O goes through service objects with an injectable `connection:` / `runner:` / `service:` seam. Tests inject fakes — never hit real yt-dlp, YouTube, Gemini, rulinky, or Slack.
- Services follow the existing `GeminiFormatter` shape: raw `Net::HTTP`, `Error`/`ConfigurationError` subclasses, class-level convenience method delegating to an instance.
- Never leak upstream response bodies into error messages (see `GeminiFormatter` "raises a generic error" test).
- Endpoint auth reuses `API_TOKEN` bearer, matching `FilesController#authenticated?`.
- Served files land in `ResolvesServedFiles::FILES_DIR`; register with `ServedFile.record`.
- Caption checkpoints (minutes, elapsed from `upload_completed_at`): `[5, 10, 20, 30, 40, 60, 120]`. Give up after the last.
- Gemini summarizer model: `gemini-flash-lite-latest`.
- YouTube upload visibility: `unlisted`. YouTube video title: tweet text/author from yt-dlp metadata.
- Slack: two webhooks (`SLACK_SUCCESS_WEBHOOK`, `SLACK_FAILURE_WEBHOOK`). A Slack post failure is logged and swallowed — it must never break the pipeline.

---

## File Structure

**Create:**
- `db/migrate/20260723120000_create_twitter_videos.rb` — schema
- `app/models/twitter_video.rb` — state row + status/checkpoint helpers
- `app/services/twitter_url.rb` — validate/normalize an x.com status URL
- `app/services/slack_notifier.rb` — success/failure webhook posts
- `app/services/ytdlp_client.rb` — download + auto-subs fetch (shellout)
- `app/services/gemini_summarizer.rb` — transcript → `{title, summary_html}`
- `app/services/rulinky_client.rb` — POST /api/links
- `app/services/youtube_uploader.rb` — resumable unlisted upload
- `app/services/served_html_writer.rb` — build/write summary HTML, record
- `app/jobs/twitter_video_ingest_job.rb` — download + upload stages
- `app/jobs/twitter_video_captions_job.rb` — poll/summarize/save/push stages
- `app/controllers/twitter_videos_controller.rb` — endpoints
- `lib/tasks/youtube.rake` — one-time refresh-token helper
- Matching test files under `test/`

**Modify:**
- `config/routes.rb` — new routes
- `Gemfile` — `google-apis-youtube_v3`, `signet`
- `.env.example` — new env vars
- `README.md` — YouTube setup + endpoint docs

---

### Task 1: `twitter_videos` table + model

**Files:**
- Create: `db/migrate/20260723120000_create_twitter_videos.rb`
- Create: `app/models/twitter_video.rb`
- Test: `test/models/twitter_video_test.rb`

**Interfaces:**
- Produces:
  - `TwitterVideo` AR model, columns: `source_url:string`, `status:string`, `youtube_id:string`, `youtube_title:string`, `caption_attempts:integer`, `upload_completed_at:datetime`, `html_filename:string`, `rulinky_link_id:string`, `error_detail:text`, timestamps.
  - `TwitterVideo::STATUSES` = `%w[downloading uploading awaiting_captions summarizing publishing done failed]`
  - `TwitterVideo::CHECKPOINTS` = `[5, 10, 20, 30, 40, 60, 120]` (minutes)
  - `#fail!(detail)` → sets status `"failed"`, `error_detail`
  - `#next_caption_wait(now: Time.current)` → returns `ActiveSupport::Duration`/seconds `Integer` until the next checkpoint (based on `caption_attempts` and `upload_completed_at`), or `nil` when checkpoints are exhausted.

- [ ] **Step 1: Write the failing test**

```ruby
# test/models/twitter_video_test.rb
require "test_helper"

class TwitterVideoTest < ActiveSupport::TestCase
  test "fail! records status and detail" do
    v = TwitterVideo.create!(source_url: "https://x.com/a/status/1", status: "uploading")
    v.fail!("boom")
    assert_equal ["failed", "boom"], v.reload.attributes.values_at("status", "error_detail")
  end

  test "next_caption_wait returns seconds to first checkpoint before any attempt" do
    t0 = Time.utc(2026, 7, 23, 12, 0, 0)
    v = TwitterVideo.create!(source_url: "u", status: "awaiting_captions",
                             caption_attempts: 0, upload_completed_at: t0)
    # 5 min checkpoint, 1 min already elapsed -> 240s
    assert_equal 240, v.next_caption_wait(now: t0 + 60)
  end

  test "next_caption_wait never returns negative" do
    t0 = Time.utc(2026, 7, 23, 12, 0, 0)
    v = TwitterVideo.create!(source_url: "u", status: "awaiting_captions",
                             caption_attempts: 1, upload_completed_at: t0)
    # 2nd checkpoint is 10 min; if 15 min elapsed, clamp to 0
    assert_equal 0, v.next_caption_wait(now: t0 + 15 * 60)
  end

  test "next_caption_wait is nil once checkpoints are exhausted" do
    v = TwitterVideo.create!(source_url: "u", status: "awaiting_captions",
                             caption_attempts: TwitterVideo::CHECKPOINTS.length,
                             upload_completed_at: Time.current)
    assert_nil v.next_caption_wait
  end
end
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bin/rails test test/models/twitter_video_test.rb`
Expected: FAIL — `uninitialized constant TwitterVideo`

- [ ] **Step 3: Write the migration**

```ruby
# db/migrate/20260723120000_create_twitter_videos.rb
class CreateTwitterVideos < ActiveRecord::Migration[8.1]
  def change
    create_table :twitter_videos do |t|
      t.string :source_url, null: false
      t.string :status, null: false, default: "downloading"
      t.string :youtube_id
      t.string :youtube_title
      t.integer :caption_attempts, null: false, default: 0
      t.datetime :upload_completed_at
      t.string :html_filename
      t.string :rulinky_link_id
      t.text :error_detail
      t.timestamps
    end
  end
end
```

- [ ] **Step 4: Write the model**

```ruby
# app/models/twitter_video.rb
class TwitterVideo < ApplicationRecord
  STATUSES = %w[downloading uploading awaiting_captions summarizing publishing done failed].freeze
  CHECKPOINTS = [5, 10, 20, 30, 40, 60, 120].freeze # minutes, elapsed from upload_completed_at

  validates :source_url, presence: true
  validates :status, inclusion: { in: STATUSES }

  def fail!(detail)
    update!(status: "failed", error_detail: detail)
  end

  # Seconds to wait before the next caption checkpoint, or nil when exhausted.
  def next_caption_wait(now: Time.current)
    minutes = CHECKPOINTS[caption_attempts]
    return nil if minutes.nil?

    target = upload_completed_at + minutes.minutes
    [(target - now).to_i, 0].max
  end
end
```

- [ ] **Step 5: Migrate and run the test**

Run: `bin/rails db:migrate && bin/rails test test/models/twitter_video_test.rb`
Expected: PASS (4 runs, 0 failures)

- [ ] **Step 6: Commit**

```bash
git add db/migrate/20260723120000_create_twitter_videos.rb db/schema.rb app/models/twitter_video.rb test/models/twitter_video_test.rb
git commit -m "feat: add twitter_videos state model"
```

---

### Task 2: `TwitterUrl` validation/normalization

**Files:**
- Create: `app/services/twitter_url.rb`
- Test: `test/services/twitter_url_test.rb`

**Interfaces:**
- Produces:
  - `TwitterUrl.normalize(raw_url)` → canonical `String` (`https://x.com/<user>/status/<id>` preserving query), or raises `TwitterUrl::InvalidError`.
  - `TwitterUrl::InvalidError < StandardError`

- [ ] **Step 1: Write the failing test**

```ruby
# test/services/twitter_url_test.rb
require "test_helper"

class TwitterUrlTest < ActiveSupport::TestCase
  test "normalizes twitter.com and mobile hosts to x.com" do
    assert_equal "https://x.com/foo/status/123",
      TwitterUrl.normalize("https://twitter.com/foo/status/123")
    assert_equal "https://x.com/foo/status/123",
      TwitterUrl.normalize("https://mobile.x.com/foo/status/123")
  end

  test "preserves query string" do
    assert_equal "https://x.com/foo/status/123?s=20",
      TwitterUrl.normalize("https://x.com/foo/status/123?s=20")
  end

  test "rejects non-status and non-twitter urls" do
    assert_raises(TwitterUrl::InvalidError) { TwitterUrl.normalize("https://x.com/foo") }
    assert_raises(TwitterUrl::InvalidError) { TwitterUrl.normalize("https://youtube.com/watch?v=x") }
  end
end
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bin/rails test test/services/twitter_url_test.rb`
Expected: FAIL — `uninitialized constant TwitterUrl`

- [ ] **Step 3: Write the service**

```ruby
# app/services/twitter_url.rb
require "uri"

class TwitterUrl
  InvalidError = Class.new(StandardError)
  HOSTS = %w[twitter.com x.com mobile.twitter.com mobile.x.com].freeze

  def self.normalize(raw_url)
    parsed = URI.parse(raw_url.to_s)
    host = parsed.host.to_s.downcase.delete_prefix("www.")
    raise InvalidError, "Unsupported URL" unless HOSTS.include?(host)
    raise InvalidError, "Unsupported URL" unless parsed.path.to_s.match?(%r{/status/\d+})

    url = "https://x.com#{parsed.path}"
    url = "#{url}?#{parsed.query}" if parsed.query.present?
    url
  rescue URI::InvalidURIError
    raise InvalidError, "Unsupported URL"
  end
end
```

- [ ] **Step 4: Run test to verify it passes**

Run: `bin/rails test test/services/twitter_url_test.rb`
Expected: PASS (3 runs, 0 failures)

- [ ] **Step 5: Commit**

```bash
git add app/services/twitter_url.rb test/services/twitter_url_test.rb
git commit -m "feat: add TwitterUrl normalization"
```

---

### Task 3: `SlackNotifier`

**Files:**
- Create: `app/services/slack_notifier.rb`
- Test: `test/services/slack_notifier_test.rb`

**Interfaces:**
- Produces:
  - `SlackNotifier.new(success_url:, failure_url:, connection: Net::HTTP)`
  - `#success(text)` → POSTs `{text:}` to `success_url`; no-op if url blank
  - `#failure(text)` → POSTs `{text:}` to `failure_url`; no-op if url blank
  - Both swallow and log any exception (return `nil`).
  - `SlackNotifier.from_env` → builds from `SLACK_SUCCESS_WEBHOOK` / `SLACK_FAILURE_WEBHOOK`.

- [ ] **Step 1: Write the failing test**

```ruby
# test/services/slack_notifier_test.rb
require "test_helper"
require "net/http"

class SlackNotifierTest < ActiveSupport::TestCase
  test "posts success text to the success webhook" do
    conn = FakeConnection.new
    SlackNotifier.new(success_url: "https://hooks.slack.com/S", failure_url: "https://hooks.slack.com/F",
                      connection: conn).success("stage ok")
    assert_equal "hooks.slack.com", conn.host
    assert conn.use_ssl
    assert_equal "/S", conn.captured_request.path
    assert_equal({ "text" => "stage ok" }, JSON.parse(conn.captured_request.body))
  end

  test "no-op when webhook url is blank" do
    conn = FakeConnection.new
    SlackNotifier.new(success_url: "", failure_url: "", connection: conn).success("x")
    assert_nil conn.captured_request
  end

  test "swallows connection errors" do
    raising = Object.new
    def raising.start(*) = raise IOError, "down"
    assert_nothing_raised do
      SlackNotifier.new(success_url: "https://hooks.slack.com/S", failure_url: "F",
                        connection: raising).success("x")
    end
  end

  private
    class FakeConnection
      attr_reader :host, :port, :use_ssl, :captured_request
      def start(host, port, use_ssl:)
        @host = host; @port = port; @use_ssl = use_ssl
        yield self
      end
      def request(request)
        @captured_request = request
        Struct.new(:code, :body).new("200", "ok")
      end
    end
end
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bin/rails test test/services/slack_notifier_test.rb`
Expected: FAIL — `uninitialized constant SlackNotifier`

- [ ] **Step 3: Write the service**

```ruby
# app/services/slack_notifier.rb
require "json"
require "net/http"

class SlackNotifier
  def self.from_env
    new(success_url: ENV["SLACK_SUCCESS_WEBHOOK"].to_s,
        failure_url: ENV["SLACK_FAILURE_WEBHOOK"].to_s)
  end

  def initialize(success_url:, failure_url:, connection: Net::HTTP)
    @success_url = success_url
    @failure_url = failure_url
    @connection = connection
  end

  def success(text) = post(@success_url, text)
  def failure(text) = post(@failure_url, text)

  private
    def post(url, text)
      return if url.blank?

      uri = URI(url)
      request = Net::HTTP::Post.new(uri)
      request["Content-Type"] = "application/json"
      request.body = { text: text }.to_json
      @connection.start(uri.host, uri.port, use_ssl: uri.scheme == "https") do |http|
        http.request(request)
      end
      nil
    rescue StandardError => error
      Rails.logger.error("[SlackNotifier] post failed: #{error.class}")
      nil
    end
end
```

- [ ] **Step 4: Run test to verify it passes**

Run: `bin/rails test test/services/slack_notifier_test.rb`
Expected: PASS (3 runs, 0 failures)

- [ ] **Step 5: Commit**

```bash
git add app/services/slack_notifier.rb test/services/slack_notifier_test.rb
git commit -m "feat: add SlackNotifier"
```

---

### Task 4: `YtdlpClient` (download + auto-subs)

**Files:**
- Create: `app/services/ytdlp_client.rb`
- Test: `test/services/ytdlp_client_test.rb`

**Interfaces:**
- Produces:
  - `YtdlpClient.new(bin: ENV.fetch("YTDLP_BIN", "/opt/homebrew/bin/yt-dlp"), runner: DEFAULT_RUNNER)`
  - `runner` is a callable `->(argv) { [stdout_string, success_boolean] }`.
  - `#download(url, dest_dir)` → `{ path: Pathname, title: String }`. Raises `YtdlpClient::Error` on non-zero exit.
  - `#fetch_auto_subs(youtube_url, dest_dir)` → `Pathname` of a `.vtt` file, or `nil` if none produced.
  - `YtdlpClient::Error < StandardError`

- [ ] **Step 1: Write the failing test**

```ruby
# test/services/ytdlp_client_test.rb
require "test_helper"
require "tmpdir"

class YtdlpClientTest < ActiveSupport::TestCase
  test "download returns path and parsed title" do
    Dir.mktmpdir do |dir|
      path = File.join(dir, "vid.mp4")
      File.write(path, "x")
      runner = lambda do |argv|
        assert_includes argv, "https://x.com/foo/status/1"
        ["TW2WL_FILE:#{path}\nTW2WL_TITLE:Hello Tweet\n", true]
      end
      result = YtdlpClient.new(bin: "yt-dlp", runner: runner).download("https://x.com/foo/status/1", dir)
      assert_equal Pathname.new(path), result[:path]
      assert_equal "Hello Tweet", result[:title]
    end
  end

  test "download raises on failure" do
    Dir.mktmpdir do |dir|
      runner = ->(_argv) { ["ERROR: unavailable", false] }
      assert_raises(YtdlpClient::Error) do
        YtdlpClient.new(bin: "yt-dlp", runner: runner).download("u", dir)
      end
    end
  end

  test "fetch_auto_subs returns the produced vtt path" do
    Dir.mktmpdir do |dir|
      vtt = File.join(dir, "video.en.vtt")
      runner = lambda do |argv|
        assert_includes argv, "--write-auto-subs"
        assert_includes argv, "--skip-download"
        File.write(vtt, "WEBVTT")
        ["", true]
      end
      result = YtdlpClient.new(bin: "yt-dlp", runner: runner)
                          .fetch_auto_subs("https://youtube.com/watch?v=abc", dir)
      assert_equal Pathname.new(vtt), result
    end
  end

  test "fetch_auto_subs returns nil when no vtt produced" do
    Dir.mktmpdir do |dir|
      runner = ->(_argv) { ["", true] }
      assert_nil YtdlpClient.new(bin: "yt-dlp", runner: runner).fetch_auto_subs("u", dir)
    end
  end
end
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bin/rails test test/services/ytdlp_client_test.rb`
Expected: FAIL — `uninitialized constant YtdlpClient`

- [ ] **Step 3: Write the service**

```ruby
# app/services/ytdlp_client.rb
require "open3"
require "pathname"

class YtdlpClient
  Error = Class.new(StandardError)

  DEFAULT_RUNNER = lambda do |argv|
    stdout, _stderr, status = Open3.capture3(*argv)
    [stdout, status.success?]
  end

  def initialize(bin: ENV.fetch("YTDLP_BIN", "/opt/homebrew/bin/yt-dlp"), runner: DEFAULT_RUNNER)
    @bin = bin
    @runner = runner
  end

  def download(url, dest_dir)
    outtmpl = File.join(dest_dir.to_s, "%(id)s.%(ext)s")
    argv = [@bin,
            "-f", "best[ext=mp4]/best",
            "--no-playlist",
            "-o", outtmpl,
            "--print", "after_move:TW2WL_FILE:%(filepath)s",
            "--print", "after_move:TW2WL_TITLE:%(title)s",
            "--newline", url]
    stdout, ok = @runner.call(argv)
    raise Error, "yt-dlp download failed" unless ok

    path = parse_prefixed(stdout, "TW2WL_FILE:")
    title = parse_prefixed(stdout, "TW2WL_TITLE:")
    raise Error, "yt-dlp produced no file" if path.nil?

    { path: Pathname.new(path), title: title }
  end

  def fetch_auto_subs(youtube_url, dest_dir)
    outtmpl = File.join(dest_dir.to_s, "%(id)s.%(ext)s")
    argv = [@bin,
            "--write-auto-subs",
            "--sub-langs", "en.*",
            "--sub-format", "vtt",
            "--skip-download",
            "--no-playlist",
            "-o", outtmpl,
            youtube_url]
    _stdout, ok = @runner.call(argv)
    raise Error, "yt-dlp subs fetch failed" unless ok

    Pathname.glob(File.join(dest_dir.to_s, "*.vtt")).min_by { |p| p.to_s }
  end

  private
    def parse_prefixed(output, prefix)
      line = output.each_line.find { |l| l.start_with?(prefix) }
      line&.delete_prefix(prefix)&.strip
    end
end
```

- [ ] **Step 4: Run test to verify it passes**

Run: `bin/rails test test/services/ytdlp_client_test.rb`
Expected: PASS (4 runs, 0 failures)

- [ ] **Step 5: Commit**

```bash
git add app/services/ytdlp_client.rb test/services/ytdlp_client_test.rb
git commit -m "feat: add YtdlpClient for download and auto-subs"
```

---

### Task 5: `GeminiSummarizer`

**Files:**
- Create: `app/services/gemini_summarizer.rb`
- Test: `test/services/gemini_summarizer_test.rb`

**Interfaces:**
- Produces:
  - `GeminiSummarizer.new(api_key: ENV["GEMINI_API_KEY"], connection: Net::HTTP)`
  - `#summarize(transcript)` → `{ title: String, summary_html: String }`. Raises `GeminiSummarizer::Error` on non-2xx or unparseable output; `ConfigurationError` on blank key.
  - `GeminiSummarizer::MODEL = "gemini-flash-lite-latest"`

- [ ] **Step 1: Write the failing test**

```ruby
# test/services/gemini_summarizer_test.rb
require "test_helper"
require "net/http"

class GeminiSummarizerTest < ActiveSupport::TestCase
  test "summarizes transcript into title and html" do
    model_json = { title: "The Big Idea", summary_html: "<p>Short summary.</p>" }.to_json
    body = { candidates: [{ content: { parts: [{ text: model_json }] } }] }.to_json
    conn = FakeConnection.new(Struct.new(:body, :code).new(body, "200"))

    result = GeminiSummarizer.new(api_key: "k", connection: conn).summarize("full transcript")

    assert_equal "The Big Idea", result[:title]
    assert_equal "<p>Short summary.</p>", result[:summary_html]
    assert_equal "/v1beta/models/gemini-flash-lite-latest:generateContent",
      conn.captured_request.path
    assert_includes conn.captured_request.body, "full transcript"
  end

  test "raises generic error on upstream failure without leaking body" do
    conn = FakeConnection.new(Struct.new(:body, :code).new("secret upstream", "500"))
    error = assert_raises(GeminiSummarizer::Error) do
      GeminiSummarizer.new(api_key: "k", connection: conn).summarize("t")
    end
    assert_not_includes error.message, "secret upstream"
  end

  test "rejects blank api key" do
    assert_raises(GeminiSummarizer::ConfigurationError) { GeminiSummarizer.new(api_key: "") }
  end

  private
    class FakeConnection
      attr_reader :captured_request
      def initialize(response) = (@response = response)
      def start(_host, _port, use_ssl:)
        @use_ssl = use_ssl
        yield self
      end
      def request(request)
        @captured_request = request
        @response
      end
    end
end
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bin/rails test test/services/gemini_summarizer_test.rb`
Expected: FAIL — `uninitialized constant GeminiSummarizer`

- [ ] **Step 3: Write the service**

```ruby
# app/services/gemini_summarizer.rb
require "json"
require "net/http"

class GeminiSummarizer
  Error = Class.new(StandardError)
  ConfigurationError = Class.new(Error)

  MODEL = "gemini-flash-lite-latest"
  ENDPOINT = URI("https://generativelanguage.googleapis.com/v1beta/models/#{MODEL}:generateContent")
  PROMPT = (
    "You are given the transcript of a short video. Return ONLY minified JSON " \
    "with exactly two keys: \"title\" (a concise plain-text title, no markdown) " \
    "and \"summary_html\" (a clean HTML fragment summarizing the video, using " \
    "<p>, <ul>, <li>, <h2> as needed, no <html>/<body> wrapper). Transcript:\n\n"
  )

  def self.summarize(transcript) = new.summarize(transcript)

  def initialize(api_key: ENV["GEMINI_API_KEY"], connection: Net::HTTP)
    raise ConfigurationError, "GEMINI_API_KEY is not configured." if api_key.blank?

    @api_key = api_key
    @connection = connection
  end

  def summarize(transcript)
    request = Net::HTTP::Post.new(ENDPOINT)
    request["Content-Type"] = "application/json"
    request["x-goog-api-key"] = @api_key
    request.body = { contents: [{ parts: [{ text: PROMPT + transcript.to_s }] }] }.to_json

    response = @connection.start(ENDPOINT.host, ENDPOINT.port, use_ssl: true) do |http|
      http.request(request)
    end
    raise Error, "Gemini summarization failed." unless response.code.to_i.between?(200, 299)

    text = JSON.parse(response.body).dig("candidates", 0, "content", "parts", 0, "text")
    parse_model_json(text)
  end

  private
    def parse_model_json(text)
      json = text.to_s.gsub(/\A```(?:json)?\s*|\s*```\z/, "").strip
      data = JSON.parse(json)
      title = data["title"].to_s.strip
      summary_html = data["summary_html"].to_s.strip
      raise Error, "Gemini returned incomplete summary." if title.empty? || summary_html.empty?

      { title: title, summary_html: summary_html }
    rescue JSON::ParserError
      raise Error, "Gemini returned unparseable summary."
    end
end
```

- [ ] **Step 4: Run test to verify it passes**

Run: `bin/rails test test/services/gemini_summarizer_test.rb`
Expected: PASS (3 runs, 0 failures)

- [ ] **Step 5: Commit**

```bash
git add app/services/gemini_summarizer.rb test/services/gemini_summarizer_test.rb
git commit -m "feat: add GeminiSummarizer"
```

---

### Task 6: `RulinkyClient`

**Files:**
- Create: `app/services/rulinky_client.rb`
- Test: `test/services/rulinky_client_test.rb`

**Interfaces:**
- Produces:
  - `RulinkyClient.new(host: ENV["RULINKY_HOST"], token: ENV["RULINKY_API_TOKEN"], connection: Net::HTTP)`
  - `#create_link(link:, note:)` → returns rulinky link id `String` (from response `"id"`). Raises `RulinkyClient::Error` on non-2xx; `ConfigurationError` on blank host/token.

- [ ] **Step 1: Write the failing test**

```ruby
# test/services/rulinky_client_test.rb
require "test_helper"
require "net/http"

class RulinkyClientTest < ActiveSupport::TestCase
  test "creates a link and returns the id" do
    conn = FakeConnection.new(Struct.new(:body, :code).new({ id: "uuid-1" }.to_json, "201"))
    id = RulinkyClient.new(host: "https://rulinky.test", token: "tok", connection: conn)
                      .create_link(link: "https://h/x.html", note: "My Title")

    assert_equal "uuid-1", id
    assert_equal "rulinky.test", conn.host
    assert_equal "/api/links", conn.captured_request.path
    assert_equal "Bearer tok", conn.captured_request["Authorization"]
    assert_equal({ "link" => "https://h/x.html", "note" => "My Title" },
      JSON.parse(conn.captured_request.body))
  end

  test "raises on non-2xx" do
    conn = FakeConnection.new(Struct.new(:body, :code).new("nope", "401"))
    assert_raises(RulinkyClient::Error) do
      RulinkyClient.new(host: "https://rulinky.test", token: "tok", connection: conn)
                   .create_link(link: "l", note: "n")
    end
  end

  test "rejects blank config" do
    assert_raises(RulinkyClient::ConfigurationError) { RulinkyClient.new(host: "", token: "") }
  end

  private
    class FakeConnection
      attr_reader :host, :captured_request
      def initialize(response) = (@response = response)
      def start(host, _port, use_ssl:)
        @host = host
        yield self
      end
      def request(request)
        @captured_request = request
        @response
      end
    end
end
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bin/rails test test/services/rulinky_client_test.rb`
Expected: FAIL — `uninitialized constant RulinkyClient`

- [ ] **Step 3: Write the service**

```ruby
# app/services/rulinky_client.rb
require "json"
require "net/http"

class RulinkyClient
  Error = Class.new(StandardError)
  ConfigurationError = Class.new(Error)

  def initialize(host: ENV["RULINKY_HOST"], token: ENV["RULINKY_API_TOKEN"], connection: Net::HTTP)
    if host.blank? || token.blank?
      raise ConfigurationError, "RULINKY_HOST and RULINKY_API_TOKEN must be configured."
    end

    @host = host
    @token = token
    @connection = connection
  end

  def create_link(link:, note:)
    uri = URI.join(@host, "/api/links")
    request = Net::HTTP::Post.new(uri)
    request["Content-Type"] = "application/json"
    request["Authorization"] = "Bearer #{@token}"
    request.body = { link: link, note: note }.to_json

    response = @connection.start(uri.host, uri.port, use_ssl: uri.scheme == "https") do |http|
      http.request(request)
    end
    raise Error, "rulinky link creation failed." unless response.code.to_i.between?(200, 299)

    JSON.parse(response.body)["id"]
  end
end
```

- [ ] **Step 4: Run test to verify it passes**

Run: `bin/rails test test/services/rulinky_client_test.rb`
Expected: PASS (3 runs, 0 failures)

- [ ] **Step 5: Commit**

```bash
git add app/services/rulinky_client.rb test/services/rulinky_client_test.rb
git commit -m "feat: add RulinkyClient"
```

---

### Task 7: `ServedHtmlWriter` (build + write summary HTML)

**Files:**
- Create: `app/services/served_html_writer.rb`
- Test: `test/services/served_html_writer_test.rb`

**Interfaces:**
- Consumes: `ResolvesServedFiles::FILES_DIR`, `ServedFile.record`.
- Produces:
  - `ServedHtmlWriter.write(title:, summary_html:, files_dir: ResolvesServedFiles::FILES_DIR)` → returns the written file's basename `String` (e.g. `"the-big-idea.html"`). Slugifies the title, avoids collisions with a numeric suffix, writes a full HTML document, and calls `ServedFile.record`.

- [ ] **Step 1: Write the failing test**

```ruby
# test/services/served_html_writer_test.rb
require "test_helper"
require "tmpdir"

class ServedHtmlWriterTest < ActiveSupport::TestCase
  test "writes slugified html and records it" do
    Dir.mktmpdir do |dir|
      dir = Pathname.new(dir)
      name = ServedHtmlWriter.write(title: "The Big Idea!", summary_html: "<p>Hi</p>", files_dir: dir)

      assert_equal "the-big-idea.html", name
      body = dir.join(name).read
      assert_includes body, "<title>The Big Idea!</title>"
      assert_includes body, "<p>Hi</p>"
      assert ServedFile.exists?(name: name)
    end
  end

  test "avoids collisions with a numeric suffix" do
    Dir.mktmpdir do |dir|
      dir = Pathname.new(dir)
      first = ServedHtmlWriter.write(title: "Dup", summary_html: "<p>1</p>", files_dir: dir)
      second = ServedHtmlWriter.write(title: "Dup", summary_html: "<p>2</p>", files_dir: dir)

      assert_equal "dup.html", first
      assert_equal "dup-1.html", second
    end
  end

  test "falls back to a default slug when the title has no word characters" do
    Dir.mktmpdir do |dir|
      dir = Pathname.new(dir)
      name = ServedHtmlWriter.write(title: "!!!", summary_html: "<p>x</p>", files_dir: dir)
      assert_equal "summary.html", name
    end
  end
end
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bin/rails test test/services/served_html_writer_test.rb`
Expected: FAIL — `uninitialized constant ServedHtmlWriter`

- [ ] **Step 3: Write the service**

```ruby
# app/services/served_html_writer.rb
require "cgi"
require "pathname"

class ServedHtmlWriter
  DEFAULT_SLUG = "summary".freeze

  def self.write(title:, summary_html:, files_dir: ResolvesServedFiles::FILES_DIR)
    files_dir = Pathname.new(files_dir)
    files_dir.mkpath
    path = unique_path(files_dir, slugify(title))
    path.write(document(title, summary_html), encoding: "UTF-8")
    ServedFile.record(path.basename.to_s)
    path.basename.to_s
  end

  def self.slugify(title)
    slug = title.to_s.downcase.gsub(/[^a-z0-9]+/, "-").gsub(/\A-+|-+\z/, "")
    slug.presence || DEFAULT_SLUG
  end

  def self.unique_path(files_dir, stem)
    counter = 0
    loop do
      suffix = counter.zero? ? "" : "-#{counter}"
      candidate = files_dir.join("#{stem}#{suffix}.html")
      return candidate unless candidate.exist?

      counter += 1
    end
  end

  def self.document(title, summary_html)
    <<~HTML
      <!doctype html>
      <html lang="en">
      <head>
      <meta charset="utf-8">
      <meta name="viewport" content="width=device-width, initial-scale=1">
      <title>#{CGI.escapeHTML(title)}</title>
      </head>
      <body>
      <h1>#{CGI.escapeHTML(title)}</h1>
      #{summary_html}
      </body>
      </html>
    HTML
  end
end
```

- [ ] **Step 4: Run test to verify it passes**

Run: `bin/rails test test/services/served_html_writer_test.rb`
Expected: PASS (3 runs, 0 failures)

- [ ] **Step 5: Commit**

```bash
git add app/services/served_html_writer.rb test/services/served_html_writer_test.rb
git commit -m "feat: add ServedHtmlWriter"
```

---

### Task 8: `YoutubeUploader` + gems

**Files:**
- Modify: `Gemfile`
- Create: `app/services/youtube_uploader.rb`
- Test: `test/services/youtube_uploader_test.rb`

**Interfaces:**
- Produces:
  - `YoutubeUploader.new(client_id:, client_secret:, refresh_token:, service: nil)` — when `service:` is nil, builds a real `Google::Apis::YoutubeV3::YouTubeService` authorized via a `Signet::OAuth2::Client` refresh token.
  - `#upload(file_path:, title:, description: "")` → YouTube video id `String`. Uploads with `privacyStatus: "unlisted"`. Raises `YoutubeUploader::Error` on failure or blank config.
- Consumes (real path): `Google::Apis::YoutubeV3`, `Signet::OAuth2::Client`.

- [ ] **Step 1: Add gems**

Add to `Gemfile` (after the `commonmarker` line):

```ruby
gem "google-apis-youtube_v3", "~> 0.60"
gem "signet", "~> 0.19"
```

Run: `bundle install`
Expected: bundle completes, `Gemfile.lock` updated.

- [ ] **Step 2: Write the failing test**

```ruby
# test/services/youtube_uploader_test.rb
require "test_helper"

class YoutubeUploaderTest < ActiveSupport::TestCase
  test "uploads unlisted and returns the video id" do
    captured = {}
    fake_service = Object.new
    fake_service.define_singleton_method(:insert_video) do |parts, video, upload_source:, content_type:|
      captured[:parts] = parts
      captured[:privacy] = video.status.privacy_status
      captured[:title] = video.snippet.title
      captured[:upload_source] = upload_source
      Struct.new(:id).new("YT123")
    end

    uploader = YoutubeUploader.new(client_id: "c", client_secret: "s",
                                   refresh_token: "r", service: fake_service)
    id = uploader.upload(file_path: "/tmp/x.mp4", title: "Tweet by foo", description: "d")

    assert_equal "YT123", id
    assert_equal "unlisted", captured[:privacy]
    assert_equal "Tweet by foo", captured[:title]
    assert_equal "/tmp/x.mp4", captured[:upload_source]
  end

  test "raises on blank config" do
    assert_raises(YoutubeUploader::Error) do
      YoutubeUploader.new(client_id: "", client_secret: "", refresh_token: "")
    end
  end
end
```

- [ ] **Step 3: Run test to verify it fails**

Run: `bin/rails test test/services/youtube_uploader_test.rb`
Expected: FAIL — `uninitialized constant YoutubeUploader`

- [ ] **Step 4: Write the service**

```ruby
# app/services/youtube_uploader.rb
require "google/apis/youtube_v3"
require "signet/oauth_2/client"

class YoutubeUploader
  Error = Class.new(StandardError)

  OAUTH_TOKEN_URL = "https://oauth2.googleapis.com/token".freeze
  SCOPE = "https://www.googleapis.com/auth/youtube.upload".freeze

  def initialize(client_id:, client_secret:, refresh_token:, service: nil)
    if client_id.blank? || client_secret.blank? || refresh_token.blank?
      raise Error, "YouTube OAuth credentials are not configured."
    end

    @client_id = client_id
    @client_secret = client_secret
    @refresh_token = refresh_token
    @service = service
  end

  def upload(file_path:, title:, description: "")
    video = Google::Apis::YoutubeV3::Video.new(
      snippet: Google::Apis::YoutubeV3::VideoSnippet.new(title: title, description: description),
      status: Google::Apis::YoutubeV3::VideoStatus.new(privacy_status: "unlisted")
    )
    result = service.insert_video(
      "snippet,status", video,
      upload_source: file_path.to_s, content_type: "video/*"
    )
    result.id
  rescue Google::Apis::Error => error
    raise Error, "YouTube upload failed: #{error.class}"
  end

  private
    def service
      @service ||= build_service
    end

    def build_service
      authorizer = Signet::OAuth2::Client.new(
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
    end
end
```

- [ ] **Step 5: Run test to verify it passes**

Run: `bin/rails test test/services/youtube_uploader_test.rb`
Expected: PASS (2 runs, 0 failures)

- [ ] **Step 6: Commit**

```bash
git add Gemfile Gemfile.lock app/services/youtube_uploader.rb test/services/youtube_uploader_test.rb
git commit -m "feat: add YoutubeUploader with google-apis-youtube_v3"
```

---

### Task 9: `TwitterVideoIngestJob` (download + upload)

**Files:**
- Create: `app/jobs/twitter_video_ingest_job.rb`
- Test: `test/jobs/twitter_video_ingest_job_test.rb`

**Interfaces:**
- Consumes: `TwitterVideo`, `YtdlpClient#download`, `YoutubeUploader#upload`, `SlackNotifier`, `TwitterVideoCaptionsJob`.
- Produces: `TwitterVideoIngestJob.perform(twitter_video_id)`. On success: sets `youtube_title`, `youtube_id`, `upload_completed_at`, status `awaiting_captions`, and enqueues `TwitterVideoCaptionsJob` with `set(wait: 5.minutes)`. Uses class-level injectable collaborators `ytdlp`, `uploader`, `slack` (default real, overridable in tests via `mattr_accessor`-style setters).

- [ ] **Step 1: Write the failing test**

```ruby
# test/jobs/twitter_video_ingest_job_test.rb
require "test_helper"

class TwitterVideoIngestJobTest < ActiveJob::TestCase
  setup do
    @video = TwitterVideo.create!(source_url: "https://x.com/foo/status/1", status: "downloading")
    @slack = FakeSlack.new
    TwitterVideoIngestJob.ytdlp = ->(*) { FakeYtdlp.new }
    TwitterVideoIngestJob.uploader = ->(*) { FakeUploader.new("YT9") }
    TwitterVideoIngestJob.slack = -> { @slack }
  end

  teardown { TwitterVideoIngestJob.reset_collaborators! }

  test "downloads, uploads unlisted, schedules first caption poll" do
    assert_enqueued_with(job: TwitterVideoCaptionsJob, args: [@video.id]) do
      TwitterVideoIngestJob.perform_now(@video.id)
    end

    @video.reload
    assert_equal "awaiting_captions", @video.status
    assert_equal "YT9", @video.youtube_id
    assert_equal "Tweet Title", @video.youtube_title
    assert_not_nil @video.upload_completed_at
    assert @slack.successes.any?
  end

  test "marks failed and notifies on download error" do
    TwitterVideoIngestJob.ytdlp = ->(*) { raise YtdlpClient::Error, "gone" }
    TwitterVideoIngestJob.perform_now(@video.id)

    assert_equal "failed", @video.reload.status
    assert @slack.failures.any?
  end

  class FakeYtdlp
    def download(_url, _dir) = { path: Pathname.new("/tmp/x.mp4"), title: "Tweet Title" }
  end
  class FakeUploader
    def initialize(id) = (@id = id)
    def upload(**) = @id
  end
  class FakeSlack
    attr_reader :successes, :failures
    def initialize = (@successes = []; @failures = [])
    def success(t) = @successes << t
    def failure(t) = @failures << t
  end
end
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bin/rails test test/jobs/twitter_video_ingest_job_test.rb`
Expected: FAIL — `uninitialized constant TwitterVideoIngestJob`

- [ ] **Step 3: Write the job**

```ruby
# app/jobs/twitter_video_ingest_job.rb
require "tmpdir"
require "fileutils"

class TwitterVideoIngestJob < ApplicationJob
  queue_as :default

  class << self
    attr_writer :ytdlp, :uploader, :slack

    def ytdlp = @ytdlp ||= ->(*) { YtdlpClient.new }
    def uploader = @uploader ||= lambda do |*|
      YoutubeUploader.new(
        client_id: ENV["YOUTUBE_CLIENT_ID"], client_secret: ENV["YOUTUBE_CLIENT_SECRET"],
        refresh_token: ENV["YOUTUBE_REFRESH_TOKEN"]
      )
    end
    def slack = @slack ||= -> { SlackNotifier.from_env }

    def reset_collaborators!
      @ytdlp = @uploader = @slack = nil
    end
  end

  def perform(twitter_video_id)
    video = TwitterVideo.find_by(id: twitter_video_id)
    return unless video

    slack = self.class.slack.call
    Dir.mktmpdir do |dir|
      downloaded = self.class.ytdlp.call.download(video.source_url, dir)
      video.update!(youtube_title: downloaded[:title])
      slack.success("[twitter-video ##{video.id}] downloaded: #{downloaded[:title]}")

      video.update!(status: "uploading")
      youtube_id = self.class.uploader.call.upload(
        file_path: downloaded[:path], title: downloaded[:title].presence || "Twitter video"
      )
      video.update!(status: "awaiting_captions", youtube_id: youtube_id,
                    upload_completed_at: Time.current)
      slack.success("[twitter-video ##{video.id}] uploaded unlisted: #{youtube_id}")
    end

    TwitterVideoCaptionsJob.set(wait: TwitterVideo::CHECKPOINTS.first.minutes)
                           .perform_later(video.id)
  rescue StandardError => error
    Rails.logger.error("[TwitterVideoIngestJob] #{error.class}: #{error.message}")
    video&.fail!(error.message)
    self.class.slack.call.failure("[twitter-video ##{twitter_video_id}] ingest failed: #{error.class}: #{error.message}")
  end
end
```

- [ ] **Step 4: Run test to verify it passes**

Run: `bin/rails test test/jobs/twitter_video_ingest_job_test.rb`
Expected: PASS (2 runs, 0 failures)

- [ ] **Step 5: Commit**

```bash
git add app/jobs/twitter_video_ingest_job.rb test/jobs/twitter_video_ingest_job_test.rb
git commit -m "feat: add TwitterVideoIngestJob"
```

---

### Task 10: `TwitterVideoCaptionsJob` (poll → summarize → save → push)

**Files:**
- Create: `app/jobs/twitter_video_captions_job.rb`
- Test: `test/jobs/twitter_video_captions_job_test.rb`

**Interfaces:**
- Consumes: `TwitterVideo#next_caption_wait`, `YtdlpClient#fetch_auto_subs`, `GeminiSummarizer#summarize`, `ServedHtmlWriter.write`, `RulinkyClient#create_link`, `SlackNotifier`.
- Produces: `TwitterVideoCaptionsJob.perform(twitter_video_id)`. On captions hit: summarizes, writes HTML, pushes to rulinky, status `done`. On miss: increments `caption_attempts`, reschedules with `set(wait: next_caption_wait)`, or fails when exhausted. Injectable class collaborators `ytdlp`, `summarizer`, `rulinky`, `slack`, plus `html_writer` and `youtube_watch_url`.
- The rulinky link is `https://#{ENV["HOST"]}/#{html_filename}`. Transcript passed to the summarizer is the raw VTT text (Gemini tolerates VTT).

- [ ] **Step 1: Write the failing test**

```ruby
# test/jobs/twitter_video_captions_job_test.rb
require "test_helper"

class TwitterVideoCaptionsJobTest < ActiveJob::TestCase
  setup do
    @video = TwitterVideo.create!(source_url: "u", status: "awaiting_captions",
                                  youtube_id: "YT9", caption_attempts: 0,
                                  upload_completed_at: Time.current)
    @slack = FakeSlack.new
    TwitterVideoCaptionsJob.slack = -> { @slack }
    TwitterVideoCaptionsJob.summarizer = -> { FakeSummarizer.new }
    TwitterVideoCaptionsJob.rulinky = -> { FakeRulinky.new("link-1") }
    TwitterVideoCaptionsJob.html_writer = ->(**) { "the-idea.html" }
    ENV["HOST"] = "example.com"
  end

  teardown { TwitterVideoCaptionsJob.reset_collaborators! }

  test "on captions hit: summarizes, writes html, pushes rulinky, done" do
    TwitterVideoCaptionsJob.ytdlp = -> { FakeYtdlp.new(vtt: "WEBVTT\nhello") }
    TwitterVideoCaptionsJob.perform_now(@video.id)

    @video.reload
    assert_equal "done", @video.status
    assert_equal "the-idea.html", @video.html_filename
    assert_equal "link-1", @video.rulinky_link_id
    assert(@slack.successes.any? { |t| t.include?("published") })
  end

  test "on miss with checkpoints remaining: reschedules and bumps attempts" do
    TwitterVideoCaptionsJob.ytdlp = -> { FakeYtdlp.new(vtt: nil) }
    assert_enqueued_with(job: TwitterVideoCaptionsJob, args: [@video.id]) do
      TwitterVideoCaptionsJob.perform_now(@video.id)
    end
    assert_equal 1, @video.reload.caption_attempts
    assert_equal "awaiting_captions", @video.status
  end

  test "on miss at final checkpoint: fails" do
    @video.update!(caption_attempts: TwitterVideo::CHECKPOINTS.length - 1)
    TwitterVideoCaptionsJob.ytdlp = -> { FakeYtdlp.new(vtt: nil) }
    TwitterVideoCaptionsJob.perform_now(@video.id)

    assert_equal "failed", @video.reload.status
    assert @slack.failures.any?
  end

  class FakeYtdlp
    def initialize(vtt:) = (@vtt = vtt)
    def fetch_auto_subs(_url, dir)
      return nil if @vtt.nil?
      path = Pathname.new(dir).join("v.en.vtt")
      path.write(@vtt)
      path
    end
  end
  class FakeSummarizer
    def summarize(_t) = { title: "The Idea", summary_html: "<p>x</p>" }
  end
  class FakeRulinky
    def initialize(id) = (@id = id)
    def create_link(link:, note:) = @id
  end
  class FakeSlack
    attr_reader :successes, :failures
    def initialize = (@successes = []; @failures = [])
    def success(t) = @successes << t
    def failure(t) = @failures << t
  end
end
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bin/rails test test/jobs/twitter_video_captions_job_test.rb`
Expected: FAIL — `uninitialized constant TwitterVideoCaptionsJob`

- [ ] **Step 3: Write the job**

```ruby
# app/jobs/twitter_video_captions_job.rb
require "tmpdir"

class TwitterVideoCaptionsJob < ApplicationJob
  queue_as :default

  WATCH_URL = ->(youtube_id) { "https://www.youtube.com/watch?v=#{youtube_id}" }

  class << self
    attr_writer :ytdlp, :summarizer, :rulinky, :slack, :html_writer

    def ytdlp = @ytdlp ||= -> { YtdlpClient.new }
    def summarizer = @summarizer ||= -> { GeminiSummarizer.new }
    def rulinky = @rulinky ||= -> { RulinkyClient.new }
    def slack = @slack ||= -> { SlackNotifier.from_env }
    def html_writer = @html_writer ||= ->(**kwargs) { ServedHtmlWriter.write(**kwargs) }

    def reset_collaborators!
      @ytdlp = @summarizer = @rulinky = @slack = @html_writer = nil
    end
  end

  def perform(twitter_video_id)
    video = TwitterVideo.find_by(id: twitter_video_id)
    return unless video && video.status == "awaiting_captions"

    vtt = Dir.mktmpdir { |dir| read_captions(video, dir) }
    return reschedule_or_fail(video) if vtt.nil?

    publish(video, vtt)
  rescue StandardError => error
    Rails.logger.error("[TwitterVideoCaptionsJob] #{error.class}: #{error.message}")
    video&.fail!(error.message)
    self.class.slack.call.failure("[twitter-video ##{twitter_video_id}] captions/publish failed: #{error.class}: #{error.message}")
  end

  private
    def read_captions(video, dir)
      path = self.class.ytdlp.call.fetch_auto_subs(WATCH_URL.call(video.youtube_id), dir)
      path&.read
    end

    def reschedule_or_fail(video)
      video.update!(caption_attempts: video.caption_attempts + 1)
      wait = video.next_caption_wait
      if wait.nil?
        video.fail!("Auto-captions never became available.")
        self.class.slack.call.failure("[twitter-video ##{video.id}] captions timed out after #{TwitterVideo::CHECKPOINTS.last} min")
      else
        self.class.slack.call.success("[twitter-video ##{video.id}] captions not ready (attempt #{video.caption_attempts}); retrying")
        TwitterVideoCaptionsJob.set(wait: wait.seconds).perform_later(video.id)
      end
    end

    def publish(video, vtt)
      slack = self.class.slack.call
      video.update!(status: "summarizing")
      summary = self.class.summarizer.call.summarize(vtt)
      slack.success("[twitter-video ##{video.id}] summarized: #{summary[:title]}")

      video.update!(status: "publishing")
      filename = self.class.html_writer.call(title: summary[:title], summary_html: summary[:summary_html])
      link = "https://#{ENV.fetch("HOST", "localhost")}/#{filename}"
      rulinky_id = self.class.rulinky.call.create_link(link: link, note: summary[:title])

      video.update!(status: "done", html_filename: filename, rulinky_link_id: rulinky_id)
      slack.success("[twitter-video ##{video.id}] published: #{link}")
    end
end
```

- [ ] **Step 4: Run test to verify it passes**

Run: `bin/rails test test/jobs/twitter_video_captions_job_test.rb`
Expected: PASS (3 runs, 0 failures)

- [ ] **Step 5: Commit**

```bash
git add app/jobs/twitter_video_captions_job.rb test/jobs/twitter_video_captions_job_test.rb
git commit -m "feat: add TwitterVideoCaptionsJob"
```

---

### Task 11: `TwitterVideosController` + routes

**Files:**
- Create: `app/controllers/twitter_videos_controller.rb`
- Modify: `config/routes.rb`
- Test: `test/controllers/twitter_videos_controller_test.rb`

**Interfaces:**
- Consumes: `TwitterUrl.normalize`, `TwitterVideo`, `TwitterVideoIngestJob`, `API_TOKEN` auth.
- Produces:
  - `POST /twitter-video` — body `{ url: }`, bearer `API_TOKEN`. 202 `{ id:, status: }`. 401 unauthorized, 400 invalid URL.
  - `GET /twitter-video/:id` — 200 `{ id:, status:, error_detail:, html_filename:, youtube_id: }`, 404 if missing.

- [ ] **Step 1: Write the failing test**

```ruby
# test/controllers/twitter_videos_controller_test.rb
require "test_helper"

class TwitterVideosControllerTest < ActionDispatch::IntegrationTest
  setup { ENV["API_TOKEN"] = "secret-token" }

  test "rejects missing bearer token" do
    post "/twitter-video", params: { url: "https://x.com/a/status/1" }, as: :json
    assert_response :unauthorized
  end

  test "creates a row and enqueues ingest job" do
    assert_enqueued_with(job: TwitterVideoIngestJob) do
      post "/twitter-video",
        params: { url: "https://twitter.com/a/status/1" }, as: :json,
        headers: { "Authorization" => "Bearer secret-token" }
    end
    assert_response :accepted
    body = JSON.parse(response.body)
    video = TwitterVideo.find(body["id"])
    assert_equal "https://x.com/a/status/1", video.source_url
    assert_equal "downloading", body["status"]
  end

  test "rejects an invalid url" do
    post "/twitter-video",
      params: { url: "https://youtube.com/watch?v=x" }, as: :json,
      headers: { "Authorization" => "Bearer secret-token" }
    assert_response :bad_request
  end

  test "returns status for an existing row" do
    v = TwitterVideo.create!(source_url: "u", status: "awaiting_captions", youtube_id: "YT1")
    get "/twitter-video/#{v.id}"
    assert_response :success
    assert_equal "awaiting_captions", JSON.parse(response.body)["status"]
  end
end
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bin/rails test test/controllers/twitter_videos_controller_test.rb`
Expected: FAIL — routing error / uninitialized constant.

- [ ] **Step 3: Add routes**

In `config/routes.rb`, add after the `post "/file/new"` line:

```ruby
  post "/twitter-video", to: "twitter_videos#create"
  get "/twitter-video/:id", to: "twitter_videos#show", constraints: { id: /\d+/ }
```

- [ ] **Step 4: Write the controller**

```ruby
# app/controllers/twitter_videos_controller.rb
class TwitterVideosController < ApplicationController
  skip_forgery_protection only: :create
  skip_before_action :authenticate_user!, only: [:create, :show], raise: false

  def create
    return render_unauthorized unless authenticated?

    source_url = TwitterUrl.normalize(params[:url])
    video = TwitterVideo.create!(source_url: source_url, status: "downloading")
    TwitterVideoIngestJob.perform_later(video.id)
    render json: { id: video.id, status: video.status }, status: :accepted
  rescue TwitterUrl::InvalidError => error
    render json: { detail: error.message }, status: :bad_request
  end

  def show
    video = TwitterVideo.find_by(id: params[:id])
    return render json: { detail: "Not found" }, status: :not_found unless video

    render json: video.slice(:id, :status, :error_detail, :html_filename, :youtube_id)
  end

  private
    def authenticated?
      token = ENV["API_TOKEN"].to_s
      authorization = request.authorization.to_s
      expected = "Bearer #{token}"
      token.present? &&
        authorization.bytesize == expected.bytesize &&
        ActiveSupport::SecurityUtils.secure_compare(authorization, expected)
    end

    def render_unauthorized
      render json: { detail: "Unauthorized" }, status: :unauthorized
    end
end
```

- [ ] **Step 5: Run test to verify it passes**

Run: `bin/rails test test/controllers/twitter_videos_controller_test.rb`
Expected: PASS (4 runs, 0 failures)

- [ ] **Step 6: Commit**

```bash
git add app/controllers/twitter_videos_controller.rb config/routes.rb test/controllers/twitter_videos_controller_test.rb
git commit -m "feat: add /twitter-video endpoints"
```

---

### Task 12: Config, docs, and YouTube auth helper

**Files:**
- Modify: `.env.example`
- Modify: `README.md`
- Create: `lib/tasks/youtube.rake`

**Interfaces:**
- Produces: `rake youtube:refresh_token` — prints the consent URL, reads the pasted auth code from stdin, exchanges it, and prints the refresh token. No test (interactive glue).

- [ ] **Step 1: Extend `.env.example`**

Append:

```dotenv
# Twitter-video → HTML pipeline
YTDLP_BIN=/opt/homebrew/bin/yt-dlp
YOUTUBE_CLIENT_ID=
YOUTUBE_CLIENT_SECRET=
YOUTUBE_REFRESH_TOKEN=
RULINKY_HOST=https://rulinky.example.com
RULINKY_API_TOKEN=
SLACK_SUCCESS_WEBHOOK=
SLACK_FAILURE_WEBHOOK=
```

- [ ] **Step 2: Write the rake helper**

```ruby
# lib/tasks/youtube.rake
namespace :youtube do
  desc "Obtain a YouTube upload refresh token via OAuth"
  task refresh_token: :environment do
    require "signet/oauth_2/client"

    client = Signet::OAuth2::Client.new(
      authorization_uri: "https://accounts.google.com/o/oauth2/auth",
      token_credential_uri: "https://oauth2.googleapis.com/token",
      client_id: ENV.fetch("YOUTUBE_CLIENT_ID"),
      client_secret: ENV.fetch("YOUTUBE_CLIENT_SECRET"),
      scope: "https://www.googleapis.com/auth/youtube.upload",
      redirect_uri: "urn:ietf:wg:oauth:2.0:oob",
      additional_parameters: { "access_type" => "offline", "prompt" => "consent" }
    )

    puts "1) Open this URL, approve access, copy the code:\n\n#{client.authorization_uri}\n\n"
    print "2) Paste the authorization code: "
    client.code = $stdin.gets.strip
    client.fetch_access_token!
    puts "\nYOUTUBE_REFRESH_TOKEN=#{client.refresh_token}"
  end
end
```

- [ ] **Step 3: Document in README**

Add a section to `README.md` after the existing env setup:

```markdown
## Twitter video → HTML

`POST /twitter-video` (bearer `API_TOKEN`, body `{ "url": "<x.com status url>" }`)
downloads the tweet's video, uploads it unlisted to YouTube for auto-captions,
summarizes the transcript with Gemini, saves an HTML page, and pushes a link to
[rulinky](../rulinky). Poll `GET /twitter-video/:id` for status.

Requires `yt-dlp` on `PATH` (or set `YTDLP_BIN`).

### YouTube API setup

1. Create a project at <https://console.cloud.google.com/>.
2. Enable the **YouTube Data API v3** (APIs & Services → Library).
3. Configure the **OAuth consent screen** (External; add your Google account as
   a test user).
4. Create an **OAuth client ID** of type **Desktop app**. Copy the client ID and
   secret into `.env` as `YOUTUBE_CLIENT_ID` / `YOUTUBE_CLIENT_SECRET`.
5. Run `rake youtube:refresh_token`, open the printed URL, approve, paste the
   code back. Copy the printed `YOUTUBE_REFRESH_TOKEN` into `.env`.

### rulinky + Slack

Set `RULINKY_HOST` and `RULINKY_API_TOKEN` (a rulinky user's API token), plus
`SLACK_SUCCESS_WEBHOOK` and `SLACK_FAILURE_WEBHOOK` incoming-webhook URLs.
```

- [ ] **Step 4: Verify the full suite passes**

Run: `bin/rails test`
Expected: all green, including the new files.

- [ ] **Step 5: Commit**

```bash
git add .env.example README.md lib/tasks/youtube.rake
git commit -m "docs: document twitter-video pipeline and add youtube auth task"
```

---

## Self-Review Notes

- **Spec coverage:** download (T4/T9), unlisted upload (T8/T9), caption backoff at exact checkpoints (T1/T10), Gemini summarize with `gemini-flash-lite-latest` (T5/T10), HTML save + `ServedFile.record` (T7/T10), rulinky push of the HTML page URL (T6/T10), `twitter_videos` state table + status endpoint (T1/T11), per-stage Slack success/failure (T3, wired in T9/T10), env + README + YouTube setup (T12). All spec sections map to a task.
- **Placeholder scan:** none — every code step is complete.
- **Type consistency:** `YtdlpClient#download` returns `{path:, title:}` (consumed in T9); `#fetch_auto_subs` returns `Pathname|nil` (consumed in T10). `GeminiSummarizer#summarize` returns `{title:, summary_html:}` (consumed in T10). `ServedHtmlWriter.write` returns a basename String (consumed in T10). `RulinkyClient#create_link` returns an id String (consumed in T10). `TwitterVideo#next_caption_wait` returns seconds Integer|nil (consumed in T10). Injectable collaborators use consistent `-> { ... }` / `->(**) { ... }` call shapes across T9/T10.
