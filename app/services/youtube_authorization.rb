require "signet/oauth_2/client"

# The browser half of YouTube OAuth: build a consent URL, then trade the code Google
# hands back for a refresh token. Deliberately knows nothing about requests or
# controllers, so the URL shape is testable without a browser.
class YoutubeAuthorization
  Error = Class.new(StandardError)
  ConfigurationError = Class.new(Error)

  CONSENT_URI = "https://accounts.google.com/o/oauth2/v2/auth".freeze

  class << self
    attr_writer :build

    # Injection point for controllers and tests, mirroring TwitterVideoIngestJob.
    def build = @build ||= -> { new }
    def reset_build! = (@build = nil)

    # Must match the redirect URI registered on the Google web client byte-for-byte.
    # Derived from HOST, never from the request: the public tunnel forwards plain
    # HTTP, so a request-derived URI would be http:// and Google would reject it.
    def redirect_uri
      ENV["YOUTUBE_REDIRECT_URI"].presence || "https://#{host}/youtube/callback"
    end

    def reauth_url(video_id = nil)
      base = ENV["YOUTUBE_REAUTH_URL"].presence || "https://#{host}/youtube/reauth"
      video_id.present? ? "#{base}?video_id=#{video_id}" : base
    end

    private
      def host = ENV.fetch("HOST", "localhost:8009")
  end

  def initialize(client_id: ENV["YOUTUBE_CLIENT_ID"], client_secret: ENV["YOUTUBE_CLIENT_SECRET"],
                 redirect_uri: self.class.redirect_uri, client: nil)
    raise ConfigurationError, "YOUTUBE_CLIENT_ID is not set." if client_id.blank?
    raise ConfigurationError, "YOUTUBE_CLIENT_SECRET is not set." if client_secret.blank?

    @client_id = client_id
    @client_secret = client_secret
    @redirect_uri = redirect_uri
    @client = client
  end

  def consent_url(state:)
    client.authorization_uri(state: state).to_s
  end

  # Google only returns a refresh token when prompt=consent is requested, which is why
  # that parameter is not optional below.
  def exchange!(code:)
    client.code = code
    client.fetch_access_token!
    client.refresh_token.presence ||
      raise(Error, "Google returned no refresh token for this code.")
  end

  private
    def client
      @client ||= Signet::OAuth2::Client.new(
        authorization_uri: CONSENT_URI,
        token_credential_uri: YoutubeUploader::OAUTH_TOKEN_URL,
        client_id: @client_id,
        client_secret: @client_secret,
        scope: YoutubeUploader::SCOPE,
        redirect_uri: @redirect_uri,
        additional_parameters: { "access_type" => "offline", "prompt" => "consent" }
      )
    end
end
