require "json"

class OpenaiSummarizer
  Error = Class.new(StandardError)
  ConfigurationError = Class.new(Error)

  PROMPT = (
    "You are a professional writer. You are given the transcript of a video, " \
    "taken from its captions. Do not summarize it: write it up as a full blog article about the " \
    "video, covering everything the speaker says, in the speaker's order, keeping " \
    "their arguments, examples, numbers and memorable quotes. Turn the spoken words " \
    "into polished, well-structured prose with an engaging introduction, clear " \
    "sections and a closing paragraph. Ignore filler words, false starts and " \
    "caption errors. Write in the same language as the transcript and " \
    "do not invent facts that are not in it. Return JSON with exactly " \
    "two keys: \"title\" (the article's headline, plain text, no markdown) and " \
    "\"summary_html\" (the article body as a clean HTML fragment, using <p>, <h2>, " \
    "<h3>, <ul>, <li>, <blockquote>, <strong>, <em> as needed, no <html>/<body> " \
    "wrapper and no <h1>). Transcript:\n\n"
  )
  # Structured Outputs: without it a long article sometimes comes back as invalid JSON.
  TEXT_FORMAT = {
    type: "json_schema",
    name: "article",
    strict: true,
    schema: {
      type: "object",
      properties: { title: { type: "string" }, summary_html: { type: "string" } },
      required: %w[title summary_html],
      additionalProperties: false
    }
  }.freeze

  def self.summarize(transcript) = new.summarize(transcript)

  def initialize(api_key: OpenaiResponses.api_key_from_env, connection: Net::HTTP)
    @client = OpenaiResponses.new(api_key:, connection:)
  rescue OpenaiResponses::ConfigurationError => error
    raise ConfigurationError, error.message
  end

  def summarize(transcript)
    text = @client.generate(PROMPT + VttText.call(transcript), text_format: TEXT_FORMAT)
    parse_model_json(text)
  rescue OpenaiResponses::Error
    raise Error, "OpenAI summarization failed."
  end

  private
    def parse_model_json(text)
      json = text.to_s.gsub(/\A```(?:json)?\s*|\s*```\z/, "").strip
      data = JSON.parse(json)
      title = data["title"].to_s.strip
      summary_html = data["summary_html"].to_s.strip
      raise Error, "OpenAI returned incomplete summary." if title.empty? || summary_html.empty?

      { title: title, summary_html: summary_html }
    rescue JSON::ParserError
      raise Error, "OpenAI returned unparseable summary."
    end
end
