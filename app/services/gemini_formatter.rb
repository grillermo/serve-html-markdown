require "json"
require "net/http"

class GeminiFormatter
  Error = Class.new(StandardError)
  ConfigurationError = Class.new(Error)

  MODEL = "gemini-3-flash-preview"
  ENDPOINT = URI("https://generativelanguage.googleapis.com/v1beta/models/#{MODEL}:generateContent")
  FORMAT_PROMPT = (
    "format the text for markdown, leave the content intact just add line breaks " \
    "and spaces when needed, your response should be the formatted markdown content " \
    "and nothing else, no remarks or preambules, no adding of titles or subtitles that weren't there originally \n\n"
  )

  def self.format(content) = new.format(content)

  def initialize(api_key: ENV["GEMINI_API_KEY"], connection: Net::HTTP)
    if api_key.blank?
      raise ConfigurationError, "GEMINI_API_KEY is not configured."
    end

    @api_key = api_key
    @connection = connection
  end

  def format(content)
    request = Net::HTTP::Post.new(ENDPOINT)
    request["Content-Type"] = "application/json"
    request["x-goog-api-key"] = @api_key
    request.body = {
      contents: [{ parts: [{ text: FORMAT_PROMPT + content }] }]
    }.to_json

    response = @connection.start(ENDPOINT.host, ENDPOINT.port, use_ssl: true) do |http|
      http.request(request)
    end

    unless response.code.to_i.between?(200, 299)
      raise Error, "Gemini formatting failed."
    end

    JSON.parse(response.body).dig("candidates", 0, "content", "parts", 0, "text")
  end
end
