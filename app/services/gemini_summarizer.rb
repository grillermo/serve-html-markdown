require "json"
require "net/http"

class GeminiSummarizer
  Error = Class.new(StandardError)
  ConfigurationError = Class.new(Error)

  MODEL = "gemini-flash-lite-latest"
  ENDPOINT = URI("https://generativelanguage.googleapis.com/v1beta/models/#{MODEL}:generateContent")
  PROMPT = (
    "You are a professional writer. You are given the transcript of a video, " \
    "taken from its captions. Do not summarize it: write it up as a full blog article about the " \
    "video, covering everything the speaker says, in the speaker's order, keeping " \
    "their arguments, examples, numbers and memorable quotes. Turn the spoken words " \
    "into polished, well-structured prose with an engaging introduction, clear " \
    "sections and a closing paragraph. Ignore filler words, false starts and " \
    "caption errors. Write in the same language as the transcript and " \
    "do not invent facts that are not in it. Return ONLY minified JSON with exactly " \
    "two keys: \"title\" (the article's headline, plain text, no markdown) and " \
    "\"summary_html\" (the article body as a clean HTML fragment, using <p>, <h2>, " \
    "<h3>, <ul>, <li>, <blockquote>, <strong>, <em> as needed, no <html>/<body> " \
    "wrapper and no <h1>). Transcript:\n\n"
  )
  # JSON mode: without it a long article sometimes comes back as invalid JSON.
  GENERATION_CONFIG = {
    responseMimeType: "application/json",
    responseSchema: {
      type: "OBJECT",
      properties: { title: { type: "STRING" }, summary_html: { type: "STRING" } },
      required: %w[title summary_html]
    }
  }.freeze

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
    request.body = {
      contents: [{ parts: [{ text: PROMPT + VttText.call(transcript) }] }],
      generationConfig: GENERATION_CONFIG
    }.to_json

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
