require "uri"

class YoutubeUrl
  HOSTS = %w[
    youtube.com m.youtube.com music.youtube.com gaming.youtube.com
    youtube-nocookie.com youtu.be
  ].freeze
  ID = /\A[\w-]{11}\z/
  # Paths that carry the id as their second segment: /shorts/ID, /live/ID, /embed/ID,
  # /v/ID, /e/ID and /watch/ID.
  PATH_ID = %r{\A/(?:shorts|live|embed|v|e|watch)/([^/?#]+)}

  # The 11-character video id of a YouTube link, or nil when it isn't one. Accepts
  # links without a scheme ("youtube.com/watch?v=…") and attribution links.
  def self.video_id(raw_url)
    parsed = URI.parse(with_scheme(raw_url.to_s.strip))
    host = parsed.host.to_s.downcase.delete_prefix("www.")
    return nil unless HOSTS.include?(host)

    query = URI.decode_www_form(parsed.query.to_s).to_h
    id = if host == "youtu.be"
      parsed.path.to_s.delete_prefix("/").split("/").first
    elsif parsed.path == "/attribution_link"
      return video_id("https://www.youtube.com#{query["u"]}")
    elsif parsed.path.to_s.chomp("/") == "/watch"
      query["v"]
    else
      parsed.path.to_s[PATH_ID, 1]
    end
    id if id.to_s.match?(ID)
  rescue URI::InvalidURIError, ArgumentError
    nil
  end

  def self.watch_url(video_id) = "https://www.youtube.com/watch?v=#{video_id}"

  def self.with_scheme(url) = url.match?(%r{\A[a-z][a-z0-9+.-]*://}i) ? url : "https://#{url}"
end
