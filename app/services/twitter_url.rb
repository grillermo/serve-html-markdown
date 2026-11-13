require "uri"

class TwitterUrl
  InvalidError = Class.new(StandardError)
  # x.com and twitter.com, their mobile hosts, and the embed-fixer and mirror hosts
  # people share links through. All of them point at the same tweet ids.
  HOSTS = %w[
    x.com twitter.com mobile.x.com mobile.twitter.com
    fxtwitter.com vxtwitter.com fixupx.com fixvx.com twittpr.com
    xcancel.com nitter.net
  ].freeze
  # /<user>/status/<id>, /i/status/<id>, /i/web/status/<id>, with any trailing
  # /video/1 or /photo/1 and "statuses" in older links.
  STATUS_PATH = %r{\A/(?:i/web|([^/]+))/status(?:es)?/(\d+)}

  # The canonical https://x.com/<user>/status/<id> for any tweet link, without its
  # tracking query string.
  def self.normalize(raw_url)
    parsed = URI.parse(YoutubeUrl.with_scheme(raw_url.to_s.strip))
    host = parsed.host.to_s.downcase.delete_prefix("www.")
    raise InvalidError, "Unsupported URL" unless HOSTS.include?(host)

    match = parsed.path.to_s.match(STATUS_PATH)
    raise InvalidError, "Unsupported URL" unless match

    "https://x.com/#{match[1] || "i"}/status/#{match[2]}"
  rescue URI::InvalidURIError
    raise InvalidError, "Unsupported URL"
  end
end
