require "uri"

class TwitterUrl
  InvalidError = Class.new(StandardError)
  HOSTS = %w[twitter.com x.com mobile.twitter.com mobile.x.com].freeze

  def self.normalize(raw_url)
    parsed = URI.parse(raw_url.to_s)
    host = parsed.host.to_s.downcase.delete_prefix("www.")
    raise InvalidError, "Unsupported URL" unless HOSTS.include?(host)
    raise InvalidError, "Unsupported URL" unless parsed.path.to_s.match?(%r{/status/\d+})

    url = "https://x.com#{parsed.path}"
    url = "#{url}?#{parsed.query}" if parsed.query.present?
    url
  rescue URI::InvalidURIError
    raise InvalidError, "Unsupported URL"
  end
end
