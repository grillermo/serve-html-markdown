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

  # Reports what THIS server process computes, so a redirect_uri_mismatch can be
  # compared byte-for-byte against the URI registered on the Google client. Values
  # are inspected rather than printed, so stray whitespace is visible.
  def debug
    render plain: debug_report, content_type: "text/plain"
  end

  private
    def authorization = @authorization ||= YoutubeAuthorization.build.call

    def debug_report
      consent_url = authorization.consent_url(state: "debug-state")
      sent_redirect_uri = Rack::Utils.parse_query(URI.parse(consent_url).query)["redirect_uri"]

      {
        "pid" => Process.pid,
        "Rails.env" => Rails.env,
        "Rails.root" => Rails.root.to_s,
        "ENV['HOST']" => ENV["HOST"].inspect,
        "ENV['YOUTUBE_REDIRECT_URI']" => ENV["YOUTUBE_REDIRECT_URI"].inspect,
        "ENV['YOUTUBE_REAUTH_URL']" => ENV["YOUTUBE_REAUTH_URL"].inspect,
        "ENV['YOUTUBE_CLIENT_ID']" => ENV["YOUTUBE_CLIENT_ID"].inspect,
        "ENV['YOUTUBE_CLIENT_SECRET'] length" => ENV["YOUTUBE_CLIENT_SECRET"].to_s.length,
        "YoutubeAuthorization.redirect_uri" => YoutubeAuthorization.redirect_uri.inspect,
        "YoutubeAuthorization.reauth_url" => YoutubeAuthorization.reauth_url.inspect,
        "redirect_uri Google receives" => sent_redirect_uri.inspect,
        "consent_url" => consent_url,
        "request.base_url" => request.base_url,
        "X-Forwarded-Proto" => request.headers["X-Forwarded-Proto"].inspect
      }.map { |key, value| "#{key}: #{value}" }.join("\n")
    end

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
