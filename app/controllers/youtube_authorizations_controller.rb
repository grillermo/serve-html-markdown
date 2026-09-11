# Runs the two-leg YouTube OAuth flow a human reaches from the Slack failure link.
# Inherits ApplicationController on purpose: these routes are publicly reachable at
# serve.chiq.me, so Devise's authenticate_user! guards them.
class YoutubeAuthorizationsController < ApplicationController
  STATE_KEY = "youtube_oauth_state".freeze
  VIDEO_KEY = "youtube_oauth_video_id".freeze

  def new
    state = SecureRandom.hex(16)
    session[STATE_KEY] = state
    session[VIDEO_KEY] = params[:video_id]
    redirect_to authorization.consent_url(state: state), allow_other_host: true
  rescue YoutubeAuthorization::ConfigurationError => error
    render_problem(error.message, :internal_server_error)
  end

  def create
    return render_problem("Google denied the request: #{params[:error]}", :bad_request) if params[:error].present?
    return render_problem("Authorization state did not match. Start again from the Slack link.", :bad_request) unless valid_state?

    YoutubeCredential.store!(authorization.exchange!(code: params[:code].to_s))
    @video_id = retry_stalled_video
    clear_oauth_session
    render :create
  rescue YoutubeAuthorization::ConfigurationError => error
    render_problem(error.message, :internal_server_error)
  rescue YoutubeAuthorization::Error => error
    render_problem(error.message, :bad_gateway)
  end

  private
    def authorization = @authorization ||= YoutubeAuthorization.build.call

    def valid_state?
      expected = session[STATE_KEY].to_s
      given = params[:state].to_s
      expected.present? && given.bytesize == expected.bytesize &&
        ActiveSupport::SecurityUtils.secure_compare(given, expected)
    end

    # The mp4 is still on disk, so the retry skips yt-dlp and goes straight to the upload.
    def retry_stalled_video
      id = session[VIDEO_KEY]
      return nil if id.blank? || !TwitterVideo.exists?(id: id)

      TwitterVideoIngestJob.perform_later(id.to_i)
      id.to_i
    end

    def clear_oauth_session
      session.delete(STATE_KEY)
      session.delete(VIDEO_KEY)
    end

    def render_problem(message, status)
      @message = message
      render :problem, status: status
    end
end
