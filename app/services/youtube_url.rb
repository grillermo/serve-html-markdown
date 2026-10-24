require "uri"

class YoutubeUrl
  HOSTS = %w[youtube.com m.youtube.com music.youtube.com youtu.be].freeze
  ID = /\A[\w-]{11}\z/

  # The 11-character video id of a YouTube link, or nil when it isn't one.
  def self.video_id(raw_url)
    parsed = URI.parse(raw_url.to_s)
    host = parsed.host.to_s.downcase.delete_prefix("www.")
    return nil unless HOSTS.include?(host)

    id = if host == "youtu.be"
      parsed.path.to_s.delete_prefix("/")
    elsif parsed.path == "/watch"
      URI.decode_www_form(parsed.query.to_s).to_h["v"]
    else
      parsed.path.to_s[%r{\A/(?:shorts|live|embed)/([^/]+)}, 1]
    end
    id if id.to_s.match?(ID)
  rescue URI::InvalidURIError, ArgumentError
    nil
  end

  def self.watch_url(video_id) = "https://www.youtube.com/watch?v=#{video_id}"
end
