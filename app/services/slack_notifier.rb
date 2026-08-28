require "json"
require "net/http"

class SlackNotifier
  def self.from_env
    new(success_url: ENV["SLACK_SUCCESS_WEBHOOK"].to_s,
        failure_url: ENV["SLACK_FAILURE_WEBHOOK"].to_s)
  end

  def initialize(success_url:, failure_url:, connection: Net::HTTP)
    @success_url = success_url
    @failure_url = failure_url
    @connection = connection
  end

  def success(text) = post(@success_url, text)
  def failure(text) = post(@failure_url, text)

  private
    def post(url, text)
      return if url.blank?

      uri = URI(url)
      request = Net::HTTP::Post.new(uri)
      request["Content-Type"] = "application/json"
      request.body = { text: text }.to_json
      @connection.start(uri.host, uri.port, use_ssl: uri.scheme == "https") do |http|
        http.request(request)
      end
      nil
    rescue StandardError => error
      Rails.logger.error("[SlackNotifier] post failed: #{error.class}")
      nil
    end
end
