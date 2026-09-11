# The live YouTube refresh token. Stored in the database rather than .env so the
# browser re-auth flow can replace it without an edit-and-restart.
class YoutubeCredential < ApplicationRecord
  validates :refresh_token, presence: true

  class << self
    # ENV is the bootstrap path: a checkout that has never run the browser flow.
    def refresh_token
      current&.refresh_token.presence || ENV["YOUTUBE_REFRESH_TOKEN"].presence
    end

    def store!(token)
      record = current || new
      record.update!(refresh_token: token, obtained_at: Time.current)
      record
    end

    def current
      order(:id).last
    end
  end
end
