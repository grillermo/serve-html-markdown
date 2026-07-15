require "test_helper"
require "net/http"

class GeminiFormatterTest < ActiveSupport::TestCase
  test "formats content through the configured Gemini model" do
    assert defined?(GeminiFormatter), "Expected GeminiFormatter to be defined"

    response = Struct.new(:body, :code).new(
      { candidates: [{ content: { parts: [{ text: "formatted markdown" }] } }] }.to_json,
      "200"
    )
    connection = FakeConnection.new(response)

    result = GeminiFormatter.new(api_key: "gemini-key", connection: connection).format("source text")

    assert_equal "formatted markdown", result
    assert_equal "generativelanguage.googleapis.com", connection.host
    assert_equal 443, connection.port
    assert connection.use_ssl
    assert_instance_of Net::HTTP::Post, connection.captured_request
    assert_equal(
      "/v1beta/models/gemini-3-flash-preview:generateContent",
      connection.captured_request.path
    )
    assert_equal "gemini-key", connection.captured_request["x-goog-api-key"]
    assert_equal(
      {
        "contents" => [
          {
            "parts" => [
              {
                "text" => GeminiFormatter::FORMAT_PROMPT + "source text"
              }
            ]
          }
        ]
      },
      JSON.parse(connection.captured_request.body)
    )
  end

  test "rejects an empty API key before making a request" do
    assert defined?(GeminiFormatter::ConfigurationError),
      "Expected GeminiFormatter::ConfigurationError to be defined"

    error = assert_raises GeminiFormatter::ConfigurationError do
      GeminiFormatter.new(api_key: "")
    end

    assert_equal "GEMINI_API_KEY is not configured.", error.message
  end

  test "raises a generic error when Gemini rejects the request" do
    assert defined?(GeminiFormatter::Error), "Expected GeminiFormatter::Error to be defined"

    response = Struct.new(:body, :code).new("sensitive upstream details", "500")
    connection = FakeConnection.new(response)

    error = assert_raises GeminiFormatter::Error do
      GeminiFormatter.new(api_key: "gemini-key", connection: connection).format("source text")
    end

    assert_equal "Gemini formatting failed.", error.message
    assert_not_includes error.message, response.body
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
