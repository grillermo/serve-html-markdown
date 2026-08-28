require "json"
require "net/http"

class GeminiSummarizer
  Error = Class.new(StandardError)
  ConfigurationError = Class.new(Error)

  MODEL = "gemini-flash-lite-latest"
  ENDPOINT = URI("https://generativelanguage.googleapis.com/v1beta/models/#{MODEL}:generateContent")
  PROMPT = (
    "You are given the transcript of a short video. Return ONLY minified JSON " \
    "with exactly two keys: \"title\" (a concise plain-text title, no markdown) " \
    "and \"summary_html\" (a clean HTML fragment summarizing the video, using " \
    "<p>, <ul>, <li>, <h2> as needed, no <html>/<body> wrapper). Transcript:\n\n"
  )

  def self.summarize(transcript) = new.summarize(transcript)

  def initialize(api_key: ENV["GEMINI_API_KEY"], connection: Net::HTTP)
    raise ConfigurationError, "GEMINI_API_KEY is not configured." if api_key.blank?

    @api_key = api_key
    @connection = connection
  end

  def summarize(transcript)
    request = Net::HTTP::Post.new(ENDPOINT)
    request["Content-Type"] = "application/json"
    request["x-goog-api-key"] = @api_key
    request.body = { contents: [{ parts: [{ text: PROMPT + transcript.to_s }] }] }.to_json

    response = @connection.start(ENDPOINT.host, ENDPOINT.port, use_ssl: true) do |http|
      http.request(request)
    end
    raise Error, "Gemini summarization failed." unless response.code.to_i.between?(200, 299)

    text = JSON.parse(response.body).dig("candidates", 0, "content", "parts", 0, "text")
    parse_model_json(text)
  end

  private
    def parse_model_json(text)
      json = text.to_s.gsub(/\A```(?:json)?\s*|\s*```\z/, "").strip
      data = JSON.parse(json)
      title = data["title"].to_s.strip
      summary_html = data["summary_html"].to_s.strip
      raise Error, "Gemini returned incomplete summary." if title.empty? || summary_html.empty?

      { title: title, summary_html: summary_html }
    rescue JSON::ParserError
      raise Error, "Gemini returned unparseable summary."
    end
end
