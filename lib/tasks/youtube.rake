namespace :youtube do
  desc "Print the URL that re-authorizes YouTube uploads in a browser"
  task refresh_token: :environment do
    puts "Open this, approve access, and the new refresh token is stored for you:"
    puts
    puts YoutubeAuthorization.reauth_url
    puts
    puts "You must be signed in to the app first — the page is behind Devise."
  end
end
