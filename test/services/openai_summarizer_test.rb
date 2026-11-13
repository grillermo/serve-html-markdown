require "test_helper"
require "net/http"

class OpenaiSummarizerTest < ActiveSupport::TestCase
  test "summarizes transcript into title and html" do
    model_json = { title: "The Big Idea", summary_html: "<p>Short summary.</p>" }.to_json
    body = { output: [{ type: "message", content: [{ type: "output_text", text: model_json }] }] }.to_json
    conn = FakeConnection.new(Struct.new(:body, :code).new(body, "200"))

    result = OpenaiSummarizer.new(api_key: "k", connection: conn).summarize("full transcript")

    assert_equal "The Big Idea", result[:title]
    assert_equal "<p>Short summary.</p>", result[:summary_html]
    assert_equal "/v1/responses", conn.captured_request.path
    sent = JSON.parse(conn.captured_request.body)
    assert_equal "gpt-6-luna", sent["model"]
    assert_includes sent["input"], "full transcript"
    assert_equal "json_schema", sent.dig("text", "format", "type")
    assert_equal true, sent.dig("text", "format", "strict")
  end

  test "raises generic error on upstream failure without leaking body" do
    conn = FakeConnection.new(Struct.new(:body, :code).new("secret upstream", "500"))
    error = assert_raises(OpenaiSummarizer::Error) do
      OpenaiSummarizer.new(api_key: "k", connection: conn).summarize("t")
    end
    assert_not_includes error.message, "secret upstream"
  end

  test "raises generic error on malformed envelope without leaking body" do
    conn = FakeConnection.new(Struct.new(:body, :code).new("{secret upstream", "200"))
    error = assert_raises(OpenaiSummarizer::Error) do
      OpenaiSummarizer.new(api_key: "k", connection: conn).summarize("t")
    end
    assert_not_includes error.message, "secret upstream"
  end

  test "rejects blank api key" do
    assert_raises(OpenaiSummarizer::ConfigurationError) { OpenaiSummarizer.new(api_key: "") }
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
