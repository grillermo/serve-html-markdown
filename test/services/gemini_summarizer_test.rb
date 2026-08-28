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
