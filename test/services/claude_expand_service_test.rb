require "test_helper"
require "stringio"

unless Object.method_defined?(:stub)
  class Object
    def stub(method_name, callable, &block)
      singleton_class.define_method(method_name) do |*args, &method_block|
        callable.call(*args, &method_block)
      end
      block.call
    ensure
      singleton_class.remove_method(method_name)
    end
  end
end

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
    tools_index = claude_cmd.index("--tools")
    assert tools_index, "Expected Claude command to disable tools"
    assert_equal "", claude_cmd[tools_index + 1]
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

  test "falls back to codex when claude command cannot launch" do
    commands = []
    runner = lambda do |cmd|
      commands << cmd
      if cmd.first == "claude"
        raise Errno::ENOENT, "claude"
      else
        File.write(cmd[cmd.index("-o") + 1], HTML)
        ["", "", fake_status(true)]
      end
    end

    result = @service.stub(:run_command, runner) { @service.expand(**@args) }

    assert_equal HTML, result
    assert_equal %w[claude codex], commands.map(&:first)
  end

  test "raises service Error without launch exception content when both commands cannot launch" do
    secret = "TOP-SECRET-LAUNCH-DETAIL"
    runner = ->(cmd) { raise Errno::ENOENT, secret }

    error = assert_raises ClaudeExpandService::Error do
      @service.stub(:run_command, runner) { @service.expand(**@args) }
    end

    assert_not_includes error.message, secret
  end

  test "does not log Claude result content when falling back" do
    secret = "TOP-SECRET-CLAUDE-RESULT"
    runner = lambda do |cmd|
      if cmd.first == "claude"
        [{ "is_error" => true, "result" => secret }.to_json, "", fake_status(true)]
      else
        File.write(cmd[cmd.index("-o") + 1], HTML)
        ["", "", fake_status(true)]
      end
    end

    logs = with_captured_logs do |output|
      result = @service.stub(:run_command, runner) { @service.expand(**@args) }
      assert_equal HTML, result
      output.string
    end

    assert_not_includes logs, secret
  end

  test "raises Error when both CLIs fail" do
    secret = "TOP-SECRET-STDERR"
    runner = ->(cmd) { ["", secret, fake_status(false)] }

    error = assert_raises ClaudeExpandService::Error do
      @service.stub(:run_command, runner) { @service.expand(**@args) }
    end

    assert_not_includes error.message, secret
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

  test "calls openai when use_openai is true" do
    ENV["EXPANSION_LLM_API_KEY"] = "test-key"
    response = fake_http_response(200, {
      "output" => [
        { "type" => "message", "content" => [{ "type" => "output_text", "text" => HTML }] }
      ]
    }.to_json)
    request_body = nil
    request_headers = nil

    Net::HTTP.stub(:start, ->(*_args, **_opts, &blk) {
      fake_http = Object.new
      fake_http.define_singleton_method(:request) do |req|
        request_body = req.body
        request_headers = req.to_hash
        response
      end
      blk.call(fake_http)
    }) do
      result = @service.expand(**@args, use_openai: true)
      assert_equal HTML, result
    end

    parsed_body = JSON.parse(request_body)
    assert_equal "gpt-5.6-terra", parsed_body["model"]
    assert_equal "medium", parsed_body["reasoning"]["effort"]
    assert_includes parsed_body["input"], "Alpha beta."
    assert_equal ["Bearer test-key"], request_headers["authorization"]
  ensure
    ENV.delete("EXPANSION_LLM_API_KEY")
  end

  test "raises without falling back to codex when openai fails" do
    ENV["EXPANSION_LLM_API_KEY"] = "test-key"
    response = fake_http_response(500, "boom")

    Net::HTTP.stub(:start, ->(*_args, **_opts, &blk) {
      fake_http = Object.new
      fake_http.define_singleton_method(:request) { |_req| response }
      blk.call(fake_http)
    }) do
      assert_raises ClaudeExpandService::Error do
        @service.expand(**@args, use_openai: true)
      end
    end
  ensure
    ENV.delete("EXPANSION_LLM_API_KEY")
  end

  test "raises when EXPANSION_LLM_API_KEY is not configured" do
    ENV.delete("EXPANSION_LLM_API_KEY")

    assert_raises ClaudeExpandService::Error do
      @service.expand(**@args, use_openai: true)
    end
  end

  private
    def fake_http_response(code, body)
      response = Object.new
      response.define_singleton_method(:code) { code.to_s }
      response.define_singleton_method(:body) { body }
      response
    end

    def with_captured_logs
      output = StringIO.new
      original_logger = Rails.logger
      Rails.logger = ActiveSupport::Logger.new(output)
      yield output
    ensure
      Rails.logger = original_logger
    end

    def fake_status(success)
      status = Object.new
      status.define_singleton_method(:success?) { success }
      status.define_singleton_method(:exitstatus) { success ? 0 : 1 }
      status
    end
end
