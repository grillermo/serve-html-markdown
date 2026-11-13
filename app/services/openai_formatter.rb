class OpenaiFormatter
  Error = Class.new(StandardError)
  ConfigurationError = Class.new(Error)

  FORMAT_PROMPT = (
    "format the text for markdown, leave the content intact just add line breaks " \
    "and spaces when needed, your response should be the formatted markdown content " \
    "and nothing else, no remarks or preambules, no adding of titles or subtitles that weren't there originally \n\n"
  )

  def self.format(content) = new.format(content)

  def initialize(api_key: OpenaiResponses.api_key_from_env, connection: Net::HTTP)
    @client = OpenaiResponses.new(api_key:, connection:)
  rescue OpenaiResponses::ConfigurationError => error
    raise ConfigurationError, error.message
  end

  def format(content)
    @client.generate(FORMAT_PROMPT + content)
  rescue OpenaiResponses::Error
    raise Error, "OpenAI formatting failed."
  end
end
