require "test_helper"
require "tmpdir"

class FilesControllerTest < ActionDispatch::IntegrationTest
  setup do
    @files_parent = Pathname.new(Dir.mktmpdir("files-parent"))
    @files_dir = Pathname.new(Dir.mktmpdir("served-files", @files_parent))
    @original_files_dir = FilesController::FILES_DIR if FilesController.const_defined?(:FILES_DIR, false)

    FilesController.send(:remove_const, :FILES_DIR) if FilesController.const_defined?(:FILES_DIR, false)
    FilesController.const_set(:FILES_DIR, @files_dir)
    @user = User.create!(email: "viewer@example.com", password: "s3cretpass")
    sign_in @user
  end

  teardown do
    FileUtils.remove_entry(@files_parent)
    FilesController.send(:remove_const, :FILES_DIR)
    FilesController.const_set(:FILES_DIR, @original_files_dir) if @original_files_dir
  end

  test "health responds to HEAD requests" do
    sign_out @user

    head "/health"

    assert_response :success
    assert_empty response.body
  end

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

  test "favicon requests do not replace the post-sign-in root destination" do
    sign_out @user
    write_file "newest.md", "# Newest"

    get "/"
    assert_redirected_to new_user_session_path

    get "/favicon.ico"
    favicon_status = response.status

    post user_session_path, params: {
      user: { email: @user.email, password: "s3cretpass" }
    }

    assert_equal [204, root_path], [favicon_status, URI(response.location).path]
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

  test "serves HTML files without a Rails layout" do
    write_file "page.html", "<main>Raw HTML</main>"

    get "/page.html"

    assert_response :success
    assert_equal "text/html", response.media_type
    assert response.body.start_with?("<main>Raw HTML</main>")
  end

  test "renders Markdown with the dark layout and Commonmarker options" do
    write_file "notes.md", <<~MARKDOWN
      # “Notes”

      Visit https://example.com.

      <mark>Trusted HTML</mark>
    MARKDOWN

    get "/notes.md"

    assert_response :success
    assert_select "title", text: "notes.md"
    assert_select "link[href*='markdown'][rel='stylesheet']"
    assert_select "h1", text: "“Notes”"
    assert_select "h1 a#notes.anchor[aria-hidden='true']"
    assert_select "a[href='https://example.com']"
    assert_select "mark", text: "Trusted HTML"
  end

  test "redirects to the newest supported file" do
    older = write_file "older.html", "older"
    newer = write_file "newer.markdown", "newer"
    File.utime 2.minutes.ago.to_time, 2.minutes.ago.to_time, older
    File.utime 1.minute.ago.to_time, 1.minute.ago.to_time, newer

    get "/last"

    assert_response :found
    assert_redirected_to "/newer.markdown"
  end

  test "returns JSON not found when no supported files exist" do
    write_file "ignored.txt", "ignored"

    get "/"

    assert_response :not_found
    assert_equal({ "detail" => "No files found." }, response.parsed_body)
  end

  test "rejects unsupported and missing files" do
    write_file "private.txt", "not served"

    get "/private.txt"
    assert_response :not_found
    assert_equal(
      { "detail" => "Only .html, .md, and .markdown files are supported." },
      response.parsed_body
    )

    get "/missing.md"
    assert_response :not_found
    assert_equal({ "detail" => "File not found: missing.md" }, response.parsed_body)
  end

  test "rejects path traversal during path resolution" do
    secret = @files_dir.dirname.join("secret.md")
    secret.write("secret")

    assert_raises ActionController::BadRequest do
      FilesController.new.send(:resolve_file_path, "../secret.md")
    end
  ensure
    secret&.delete if secret&.exist?
  end

  test "does not serve encoded path traversal attempts" do
    secret = @files_dir.dirname.join("secret.md")
    secret.write("secret")

    get "/%2E%2E%2Fsecret.md"

    assert_includes [400, 404], response.status
    assert_not_includes response.body, "secret"
  ensure
    secret&.delete if secret&.exist?
  end

  test "does not follow symlinks outside the files directory" do
    secret = @files_dir.dirname.join("secret.md")
    secret.write("secret")
    @files_dir.join("linked.md").make_symlink(secret)

    get "/linked.md"

    assert_response :bad_request
    assert_not_includes response.body, "secret"
  ensure
    secret&.delete if secret&.exist?
  end

  test "serves files to older browser user agents" do
    write_file "page.html", "<main>Available</main>"

    get "/page.html", headers: {
      "User-Agent" => "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " \
        "AppleWebKit/537.36 (KHTML, like Gecko) Chrome/80.0.3987.149 Safari/537.36"
    }

    assert_response :success
    assert response.body.start_with?("<main>Available</main>")
  end

  test "injects the expand script and csrf token into served HTML" do
    write_file "page.html", "<html><body><main>Raw</main></body></html>"

    get "/page.html"

    assert_response :success
    assert_includes response.body, %(<script src="#{expand_script_path}" defer></script></body>)
    assert_select "meta[name='csrf-token']"
    assert_includes response.body, "<main>Raw</main>"
  end

  test "appends the expand script when HTML has no body tag" do
    write_file "page.html", "<main>Raw HTML</main>"

    get "/page.html"

    assert_response :success
    assert_includes response.body, %(<script src="#{expand_script_path}" defer></script>)
    assert response.body.start_with?("<main>Raw HTML</main>")
  end

  test "includes the expand script and csrf tags in the markdown layout" do
    write_file "notes.md", "# Notes"

    with_forgery_protection do
      get "/notes.md"
    end

    assert_response :success
    assert_select "meta[name='csrf-token']"
    assert_select "script[src='#{expand_script_path}'][defer]"
  end

  test "embeds a saved scroll anchor for markdown and HTML files" do
    @user.scroll_positions.create!(file_name: "notes.md", anchor: "saved-heading")
    @user.scroll_positions.create!(file_name: "page.html", anchor: "saved-section")
    write_file "notes.md", "# Notes"
    write_file "page.html", "<main id=\"saved-section\">Raw HTML</main>"

    get "/notes.md"
    assert_includes response.body, 'window.__scrollAnchor = "saved-heading";'

    get "/page.html"
    assert_includes response.body, 'window.__scrollAnchor = "saved-section";'
  end

  test "omits the scroll anchor script when no position was saved" do
    write_file "notes.md", "# Notes"

    get "/notes.md"

    assert_not_includes response.body, "window.__scrollAnchor"
  end

  test "the expand script is served under a content digest so edits bust the cache" do
    assert_match %r{\A/assets/expand-[0-9a-f]{8,}\.js\z}, expand_script_path
  end

  test "rejects uploads without a configured bearer token" do
    with_env "API_TOKEN", nil do
      post "/file/new", params: { content: "hello", filename: "note" }
    end

    assert_response :unauthorized
    assert_equal({ "detail" => "Unauthorized" }, response.parsed_body)
  end

  test "rejects uploads with the wrong bearer token" do
    with_env "API_TOKEN", "correct-token" do
      post "/file/new",
        params: { content: "hello", filename: "note" },
        headers: { "Authorization" => "Bearer wrong-token" }
    end

    assert_response :unauthorized
    assert_empty @files_dir.children
  end

  test "formats and saves an authenticated upload with a unique safe filename" do
    write_file "note.md", "existing"
    received_content = nil
    formatter = lambda do |content|
      received_content = content
      "formatted markdown"
    end

    with_env "API_TOKEN", "upload-token" do
      with_env "HOST", "reader.example" do
        with_formatter(formatter) do
          post "/file/new",
            params: { content: "source text", filename: "../nested/note.txt" },
            headers: { "Authorization" => "Bearer upload-token" }
        end
      end
    end

    assert_response :success
    assert_equal "source text", received_content
    assert_equal({ "url" => "https://reader.example/note-1.md" }, response.parsed_body)
    assert_equal "formatted markdown", @files_dir.join("note-1.md").read
  end

  test "returns a generic bad gateway response when Gemini fails" do
    formatter = ->(*) { raise GeminiFormatter::Error, "sensitive upstream detail" }

    with_env "API_TOKEN", "upload-token" do
      with_formatter(formatter) do
        post "/file/new",
          params: { content: "source text", filename: "note" },
          headers: { "Authorization" => "Bearer upload-token" }
      end
    end

    assert_response :bad_gateway
    assert_equal({ "detail" => "Gemini formatting failed." }, response.parsed_body)
    assert_empty @files_dir.children
  end

  test "rejects an empty upload filename" do
    with_env "API_TOKEN", "upload-token" do
      post "/file/new",
        params: { content: "source text", filename: "" },
        headers: { "Authorization" => "Bearer upload-token" }
    end

    assert_response :bad_request
    assert_equal({ "detail" => "Invalid filename." }, response.parsed_body)
  end

  private
    def write_file(name, content)
      @files_dir.join(name).tap { |path| path.write(content) }
    end

    def expand_script_path
      ActionController::Base.helpers.asset_path("expand.js")
    end

    def with_env(name, value)
      original = ENV[name]
      value.nil? ? ENV.delete(name) : ENV[name] = value
      yield
    ensure
      original.nil? ? ENV.delete(name) : ENV[name] = original
    end

    def with_formatter(callable)
      had_formatter = FilesController.const_defined?(:FORMATTER, false)
      original = FilesController::FORMATTER if had_formatter
      fake = Object.new
      fake.define_singleton_method(:format, &callable)

      FilesController.send(:remove_const, :FORMATTER) if had_formatter
      FilesController.const_set(:FORMATTER, fake)
      yield
    ensure
      FilesController.send(:remove_const, :FORMATTER) if FilesController.const_defined?(:FORMATTER, false)
      FilesController.const_set(:FORMATTER, original) if had_formatter
    end

    def with_forgery_protection
      original = ActionController::Base.allow_forgery_protection
      ActionController::Base.allow_forgery_protection = true
      yield
    ensure
      ActionController::Base.allow_forgery_protection = original
    end
end
