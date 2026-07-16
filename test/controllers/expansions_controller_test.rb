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
    assert_equal false, received[:use_openai]
  end

  test "passes use_openai through to the expander" do
    write_file "notes.md", "Alpha beta gamma."
    received = nil
    expander = lambda do |**kwargs|
      received = kwargs
      HTML
    end

    with_expander(expander) do
      post "/expansions", params: {
        file_name: "notes.md", selected_text: "beta", occurrence: 0, question: "why?", use_openai: true
      }, as: :json
    end

    assert_response :success
    assert_equal true, received[:use_openai]
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

  test "returns 400 when selected_text or question is missing" do
    write_file "notes.md", "Alpha beta."

    post "/expansions", params: { file_name: "notes.md", selected_text: "", question: "why?" }, as: :json

    assert_response :bad_request
  end

  test "returns 400 when file_name is blank or missing" do
    post "/expansions", params: {
      file_name: "", selected_text: "beta", occurrence: 0, question: "why?"
    }, as: :json
    assert_response :bad_request

    post "/expansions", params: { selected_text: "beta", occurrence: 0, question: "why?" }, as: :json
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
