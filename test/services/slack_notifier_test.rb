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
