# app/services/youtube_uploader.rb
require "google/apis/youtube_v3"
require "signet/oauth_2/client"

class YoutubeUploader
  Error = Class.new(StandardError)

  OAUTH_TOKEN_URL = "https://oauth2.googleapis.com/token".freeze
  SCOPE = "https://www.googleapis.com/auth/youtube.upload".freeze

  def initialize(client_id:, client_secret:, refresh_token:, service: nil)
    if client_id.blank? || client_secret.blank? || refresh_token.blank?
      raise Error, "YouTube OAuth credentials are not configured."
    end

    @client_id = client_id
    @client_secret = client_secret
    @refresh_token = refresh_token
    @service = service
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
      authorizer = Signet::OAuth2::Client.new(
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
    end
end
