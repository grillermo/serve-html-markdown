# app/services/youtube_uploader.rb
require "google/apis/youtube_v3"
require "signet/oauth_2/client"

class YoutubeUploader
  Error = Class.new(StandardError)
  # A dead or missing refresh token. Recoverable by re-running the browser OAuth
  # flow, so callers can offer a link instead of a stack trace.
  AuthorizationExpired = Class.new(Error)

  OAUTH_TOKEN_URL = "https://oauth2.googleapis.com/token".freeze
  SCOPE = "https://www.googleapis.com/auth/youtube.upload".freeze

  # authorizer: is a test seam, like service: below it.
  def initialize(client_id:, client_secret:, refresh_token:, service: nil, authorizer: nil)
    if client_id.blank? || client_secret.blank?
      raise Error, "YouTube OAuth credentials are not configured."
    end
    raise AuthorizationExpired, "No YouTube refresh token is stored." if refresh_token.blank?

    @client_id = client_id
    @client_secret = client_secret
    @refresh_token = refresh_token
    @service = service
    @authorizer = authorizer
  end

  def upload(file_path:, title:, description: "")
    video = Google::Apis::YoutubeV3::Video.new(
      snippet: Google::Apis::YoutubeV3::VideoSnippet.new(title: title, description: description),
      status: Google::Apis::YoutubeV3::VideoStatus.new(privacy_status: "unlisted")
    )
    result = service.insert_video(
      "snippet,status", video,
      upload_source: file_path.to_s, content_type: "video/*"
    )
    result.id
  rescue Google::Apis::Error => error
    raise Error, "YouTube upload failed: #{error.class} status=#{error.status_code} " \
                 "#{error.message} body=#{error.body}"
  end

  private
    def service
      @service ||= build_service
    end

    def build_service
      authorizer = @authorizer || Signet::OAuth2::Client.new(
        token_credential_uri: OAUTH_TOKEN_URL,
        client_id: @client_id,
        client_secret: @client_secret,
        refresh_token: @refresh_token,
        scope: SCOPE
      )
      authorizer.fetch_access_token!
      svc = Google::Apis::YoutubeV3::YouTubeService.new
      svc.authorization = authorizer
      svc
    rescue Signet::AuthorizationError => error
      # googleauth wraps Signet failures in Google::Auth::AuthorizationError, a
      # subclass of this one, so both arrive here. invalid_grant means the refresh
      # token is dead and only a human with a browser can fix it.
      raise AuthorizationExpired, "YouTube authorization expired: #{error.message}"
    end
end
