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
