namespace :youtube do
  desc "Obtain a YouTube upload refresh token via OAuth"
  task refresh_token: :environment do
    require "signet/oauth_2/client"

    client = Signet::OAuth2::Client.new(
      authorization_uri: "https://accounts.google.com/o/oauth2/auth",
      token_credential_uri: "https://oauth2.googleapis.com/token",
      client_id: ENV.fetch("YOUTUBE_CLIENT_ID"),
      client_secret: ENV.fetch("YOUTUBE_CLIENT_SECRET"),
      scope: "https://www.googleapis.com/auth/youtube.upload",
      redirect_uri: "urn:ietf:wg:oauth:2.0:oob",
      additional_parameters: { "access_type" => "offline", "prompt" => "consent" }
    )

    puts "1) Open this URL, approve access, copy the code:\n\n#{client.authorization_uri}\n\n"
    print "2) Paste the authorization code: "
    client.code = $stdin.gets.strip
    client.fetch_access_token!
    puts "\nYOUTUBE_REFRESH_TOKEN=#{client.refresh_token}"
  end
end
