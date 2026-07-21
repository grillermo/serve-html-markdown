require "test_helper"
require "tmpdir"

class ExpansionsControllerTest < ActionDispatch::IntegrationTest
  include ActiveJob::TestHelper

  setup do
    @files_dir = Pathname.new(Dir.mktmpdir("served-files"))
    @original_files_dir = ExpansionProcessor::FILES_DIR
    ExpansionProcessor.send(:remove_const, :FILES_DIR)
    ExpansionProcessor.const_set(:FILES_DIR, @files_dir)

    @user = User.create!(email: "expander@example.com", password: "s3cretpass")
    sign_in @user
  end

  teardown do
    FileUtils.remove_entry(@files_dir)
    ExpansionProcessor.send(:remove_const, :FILES_DIR)
    ExpansionProcessor.const_set(:FILES_DIR, @original_files_dir)
  end

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

  test "accepts a syntactically valid missing file and fails the job in the background" do
    post "/expansions", params: {
      file_name: "missing.md", selected_text: "beta", occurrence: 0, question: "why?"
    }, as: :json

    assert_response :accepted
    expansion = @user.expansions.find(response.parsed_body.fetch("id"))

    perform_enqueued_jobs

    get "/expansions/#{expansion.id}", as: :json
    assert_equal "failed", response.parsed_body["status"]
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
end
