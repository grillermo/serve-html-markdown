require "test_helper"

class YoutubeAuthorizationTest < ActiveSupport::TestCase
  teardown { YoutubeAuthorization.reset_build! }

  test "consent url asks for offline access and a fresh consent" do
    url = build_auth.consent_url(state: "st4te")
    params = Rack::Utils.parse_query(URI.parse(url).query)

    assert_equal "https://accounts.google.com/o/oauth2/auth", url.split("?").first
    assert_equal "offline", params["access_type"]
    assert_equal "consent", params["prompt"]
    assert_equal "st4te", params["state"]
    assert_equal "code", params["response_type"]
    assert_equal "cid", params["client_id"]
    assert_equal "https://serve.chiq.me/youtube/callback", params["redirect_uri"]
    assert_equal YoutubeUploader::SCOPE, params["scope"]
  end

  test "exchange! hands google the code and returns the refresh token" do
    fake = FakeClient.new("1//refresh")

    assert_equal "1//refresh", build_auth(client: fake).exchange!(code: "abc")
    assert_equal "abc", fake.code
    assert fake.fetched
  end

  test "exchange! raises when google sends no refresh token" do
    error = assert_raises(YoutubeAuthorization::Error) do
      build_auth(client: FakeClient.new(nil)).exchange!(code: "abc")
    end
    assert_match(/no refresh token/, error.message)
  end

  test "missing client credentials name the absent variable" do
    error = assert_raises(YoutubeAuthorization::ConfigurationError) do
      YoutubeAuthorization.new(client_id: "", client_secret: "sec")
    end
    assert_match(/YOUTUBE_CLIENT_ID/, error.message)
  end

  test "reauth_url appends the video id when given" do
    with_env("YOUTUBE_REAUTH_URL" => "https://serve.chiq.me/youtube/reauth") do
      assert_equal "https://serve.chiq.me/youtube/reauth", YoutubeAuthorization.reauth_url
      assert_equal "https://serve.chiq.me/youtube/reauth?video_id=7", YoutubeAuthorization.reauth_url(7)
    end
  end

  test "build is overridable for tests" do
    sentinel = Object.new
    YoutubeAuthorization.build = -> { sentinel }

    assert_same sentinel, YoutubeAuthorization.build.call
  end

  private
    def build_auth(client: nil)
      YoutubeAuthorization.new(client_id: "cid", client_secret: "sec",
                               redirect_uri: "https://serve.chiq.me/youtube/callback",
                               client: client)
    end

    def with_env(values)
      previous = values.keys.index_with { |key| ENV[key] }
      values.each { |key, value| value.nil? ? ENV.delete(key) : ENV[key] = value }
      yield
    ensure
      previous.each { |key, value| value.nil? ? ENV.delete(key) : ENV[key] = value }
    end

    class FakeClient
      attr_accessor :code
      attr_reader :refresh_token, :fetched

      def initialize(refresh_token)
        @refresh_token = refresh_token
        @fetched = false
      end

      def fetch_access_token!
        @fetched = true
        { "access_token" => "at" }
      end
    end
end
