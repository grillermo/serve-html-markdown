require "json"
require "net/http"

# Minimal client for OpenAI's Responses API: posts one prompt, returns the output text.
# Errors never carry the upstream response body.
class OpenaiResponses
  Error = Class.new(StandardError)
  ConfigurationError = Class.new(Error)

  MODEL = "gpt-6-luna"
  ENDPOINT = URI("https://api.openai.com/v1/responses")

  def self.api_key_from_env = ENV["OPENAI_API_KEY"].presence || ENV["EXPANSION_LLM_API_KEY"]

  def initialize(api_key: self.class.api_key_from_env, connection: Net::HTTP)
    raise ConfigurationError, "OPENAI_API_KEY is not configured." if api_key.blank?

    @api_key = api_key
    @connection = connection
  end

  # text_format: optional Responses API `text.format` hash (e.g. a json_schema).
  def generate(input, text_format: nil)
    request = Net::HTTP::Post.new(ENDPOINT)
    request["Content-Type"] = "application/json"
    request["Authorization"] = "Bearer #{@api_key}"
    body = { model: MODEL, input: input }
    body[:text] = { format: text_format } if text_format
    request.body = body.to_json

    response = @connection.start(ENDPOINT.host, ENDPOINT.port, use_ssl: true) do |http|
      http.request(request)
    end
    raise Error, "OpenAI request failed." unless response.code.to_i.between?(200, 299)

    output_text(JSON.parse(response.body))
  rescue JSON::ParserError
    raise Error, "OpenAI returned an unparseable response."
  rescue SystemCallError, Net::OpenTimeout, Net::ReadTimeout
    raise Error, "OpenAI request could not be completed."
  end

  private
    def output_text(parsed)
      Array(parsed["output"])
        .select { |item| item["type"] == "message" }
        .flat_map { |item| Array(item["content"]) }
        .select { |content| content["type"] == "output_text" }
        .map { |content| content["text"] }
        .join
        .presence || raise(Error, "OpenAI returned no output.")
    end
end
