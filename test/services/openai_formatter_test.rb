require "test_helper"
require "net/http"

class OpenaiFormatterTest < ActiveSupport::TestCase
  test "formats content through the configured OpenAI model" do
    response = Struct.new(:body, :code).new(
      { output: [
        { type: "reasoning", summary: [] },
        { type: "message", content: [{ type: "output_text", text: "formatted markdown" }] }
      ] }.to_json,
      "200"
    )
    connection = FakeConnection.new(response)

    result = OpenaiFormatter.new(api_key: "openai-key", connection: connection).format("source text")

    assert_equal "formatted markdown", result
    assert_equal "api.openai.com", connection.host
    assert_equal 443, connection.port
    assert connection.use_ssl
    assert_instance_of Net::HTTP::Post, connection.captured_request
    assert_equal "/v1/responses", connection.captured_request.path
    assert_equal "Bearer openai-key", connection.captured_request["Authorization"]
    assert_equal(
      { "model" => "gpt-6-luna", "input" => OpenaiFormatter::FORMAT_PROMPT + "source text" },
      JSON.parse(connection.captured_request.body)
    )
  end

  test "rejects an empty API key before making a request" do
    error = assert_raises OpenaiFormatter::ConfigurationError do
      OpenaiFormatter.new(api_key: "")
    end

    assert_equal "OPENAI_API_KEY is not configured.", error.message
  end

  test "raises a generic error when OpenAI rejects the request" do
    response = Struct.new(:body, :code).new("sensitive upstream details", "500")
    connection = FakeConnection.new(response)

    error = assert_raises OpenaiFormatter::Error do
      OpenaiFormatter.new(api_key: "openai-key", connection: connection).format("source text")
    end

    assert_equal "OpenAI formatting failed.", error.message
    assert_not_includes error.message, response.body
  end

  test "raises a generic error when OpenAI returns a malformed body" do
    response = Struct.new(:body, :code).new("not json sensitive", "200")

    error = assert_raises OpenaiFormatter::Error do
      OpenaiFormatter.new(api_key: "k", connection: FakeConnection.new(response)).format("x")
    end

    assert_equal "OpenAI formatting failed.", error.message
  end

  private
    class FakeConnection
      attr_reader :host, :port, :use_ssl, :captured_request

      def initialize(response)
        @response = response
      end

      def start(host, port, use_ssl:)
        @host = host
        @port = port
        @use_ssl = use_ssl
        yield self
      end

      def request(request)
        @captured_request = request
        @response
      end
    end
end
