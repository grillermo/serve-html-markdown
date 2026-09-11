require "test_helper"

class YoutubeAuthorizationsControllerTest < ActionDispatch::IntegrationTest
  setup do
    @user = User.create!(email: "youtube-oauth@example.com", password: "s3cretpass")
    @video = TwitterVideo.create!(source_url: "https://x.com/foo/status/9", status: "failed")
    @auth = FakeAuthorization.new("1//new-token")
    YoutubeAuthorization.build = -> { @auth }
  end

  teardown { YoutubeAuthorization.reset_build! }

  test "requires a signed-in user" do
    get "/youtube/reauth"

    assert_redirected_to new_user_session_path
  end

  test "redirects to google with a state parameter" do
    sign_in @user

    get "/youtube/reauth", params: { video_id: @video.id }

    assert_redirected_to "https://accounts.google.com/o/oauth2/auth?state=#{state}"
    assert_equal @video.id.to_s, session[YoutubeAuthorizationsController::VIDEO_KEY]
    assert_equal state, @auth.state
  end

  test "stores the token and re-queues the stalled video" do
    sign_in @user
    get "/youtube/reauth", params: { video_id: @video.id }

    assert_enqueued_with(job: TwitterVideoIngestJob, args: [@video.id]) do
      get "/youtube/callback", params: { code: "auth-code", state: state }
    end

    assert_response :success
    assert_equal "auth-code", @auth.exchanged_code
    assert_equal "1//new-token", YoutubeCredential.refresh_token
    assert_nil session[YoutubeAuthorizationsController::STATE_KEY]
  end

  test "rejects a mismatched state without storing anything" do
    sign_in @user
    get "/youtube/reauth", params: { video_id: @video.id }

    get "/youtube/callback", params: { code: "auth-code", state: "forged" }

    assert_response :bad_request
    assert_nil YoutubeCredential.current
    assert_no_enqueued_jobs only: TwitterVideoIngestJob
  end

  test "reports a denied consent without storing anything" do
    sign_in @user
    get "/youtube/reauth", params: { video_id: @video.id }

    get "/youtube/callback", params: { error: "access_denied", state: state }

    assert_response :bad_request
    assert_nil YoutubeCredential.current
  end

  test "stores the token even when no video was waiting" do
    sign_in @user
    get "/youtube/reauth"

    assert_no_enqueued_jobs only: TwitterVideoIngestJob do
      get "/youtube/callback", params: { code: "auth-code", state: state }
    end

    assert_response :success
    assert_equal "1//new-token", YoutubeCredential.refresh_token
  end

  test "reports a configuration error during exchange as an internal server error, not a bad gateway" do
    sign_in @user
    get "/youtube/reauth", params: { video_id: @video.id }
    @auth = FakeMisconfiguredAuthorization.new
    YoutubeAuthorization.build = -> { @auth }

    get "/youtube/callback", params: { code: "auth-code", state: state }

    assert_response :internal_server_error
    assert_nil YoutubeCredential.current
  end

  private
    def state = session[YoutubeAuthorizationsController::STATE_KEY]

    class FakeAuthorization
      attr_reader :state, :exchanged_code

      def initialize(token) = (@token = token)

      def consent_url(state:)
        @state = state
        "https://accounts.google.com/o/oauth2/auth?state=#{state}"
      end

      def exchange!(code:)
        @exchanged_code = code
        @token
      end
    end

    class FakeMisconfiguredAuthorization
      def consent_url(state:) = "https://accounts.google.com/o/oauth2/auth?state=#{state}"

      def exchange!(code:)
        raise YoutubeAuthorization::ConfigurationError, "YOUTUBE_CLIENT_SECRET is not set."
      end
    end
end
