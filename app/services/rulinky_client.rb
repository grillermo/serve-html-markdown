require "json"
require "net/http"

class RulinkyClient
  Error = Class.new(StandardError)
  ConfigurationError = Class.new(Error)

  def initialize(host: ENV["RULINKY_HOST"], token: ENV["RULINKY_API_TOKEN"], connection: Net::HTTP)
    if host.blank? || token.blank?
      raise ConfigurationError, "RULINKY_HOST and RULINKY_API_TOKEN must be configured."
    end

    @host = host
    @token = token
    @connection = connection
  end

  def create_link(link:, note:)
    uri = URI.join(@host, "/api/links")
    request = Net::HTTP::Post.new(uri)
    request["Content-Type"] = "application/json"
    request["Authorization"] = "Bearer #{@token}"
    request.body = { link: link, note: note }.to_json

    response = @connection.start(uri.host, uri.port, use_ssl: uri.scheme == "https") do |http|
      http.request(request)
    end
    raise Error, "rulinky link creation failed." unless response.code.to_i.between?(200, 299)

    JSON.parse(response.body)["id"]
  end
end
