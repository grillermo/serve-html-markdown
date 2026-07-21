require "json"
require "net/http"
require "open3"
require "tempfile"

class ClaudeExpandService
  Error = Class.new(StandardError)

  CLAUDE_MODEL = -> { ENV.fetch("EXPANSION_CLAUDE_MODEL", "sonnet") }
  CODEX_MODEL = "earth"
  OPENAI_MODEL = "gpt-5.6-terra"
  OPENAI_REASONING_EFFORT = "medium"
  OPENAI_ENDPOINT = URI("https://api.openai.com/v1/responses")
  TIMEOUT_SECONDS = 120

  PROMPT_TEMPLATE = <<~PROMPT
    You are given a document, a text selection from it, and a reader's question about that selection.

    Write a complete standalone HTML page that answers the question and expands on the selected text with additional depth: background, context, related concepts, and concrete details the original document leaves out.

    Requirements:
    - Output ONLY the HTML document, starting with <!DOCTYPE html>. No markdown fences, no commentary.
    - Dark theme, readable typography (max-width ~70ch, generous line-height), semantic HTML.
    - Title the page after the selection.
    - Ground the answer in the document's context, but bring in outside knowledge freely.

    <document filename="%{file_name}">
    %{document}
    </document>

    <selection>
    %{selection}
    </selection>

    <question>
    %{question}
    </question>
  PROMPT

  def self.expand(**kwargs) = new.expand(**kwargs)

  def expand(file_name:, document:, selection:, question:, use_openai: false, expansion: nil)
    prompt = format(PROMPT_TEMPLATE, file_name:, document:, selection:, question:)
    Rails.logger.info "[ClaudeExpandService] expanding file=#{file_name} selection_bytes=#{selection.bytesize} question_bytes=#{question.bytesize} use_openai=#{use_openai}"
    expansion&.stamp!(:llm_request_start)

    if use_openai
      html = run_openai(prompt)
      record_response(expansion, "openai", html)
      return html
    end

    html = run_claude(prompt)
    record_response(expansion, "claude", html)
    html
  rescue Error => error
    raise error if use_openai

    Rails.logger.warn "[ClaudeExpandService] claude failed, falling back to codex"
    expansion&.stamp!(:llm_first_failure)
    html = run_codex(prompt)
    record_response(expansion, "codex", html)
    html
  end

  private
    def record_response(expansion, provider, html)
      Rails.logger.info "[ClaudeExpandService] #{provider} succeeded bytes=#{html.bytesize}"
      expansion&.stamp!(:llm_response)
      expansion&.update_columns(provider_used: provider, html_bytes: html.bytesize)
    end

    def run_openai(prompt)
      api_key = ENV["EXPANSION_LLM_API_KEY"]
      raise Error, "EXPANSION_LLM_API_KEY is not configured." if api_key.blank?

      request = Net::HTTP::Post.new(OPENAI_ENDPOINT)
      request["Content-Type"] = "application/json"
      request["Authorization"] = "Bearer #{api_key}"
      request.body = {
        model: OPENAI_MODEL,
        input: prompt,
        reasoning: { effort: OPENAI_REASONING_EFFORT }
      }.to_json

      response = Net::HTTP.start(OPENAI_ENDPOINT.host, OPENAI_ENDPOINT.port, use_ssl: true) do |http|
        http.request(request)
      end

      unless response.code.to_i.between?(200, 299)
        raise Error, "openai CLI failed"
      end

      parsed = JSON.parse(response.body)
      text = parsed["output"]
        &.find { |item| item["type"] == "message" }
        &.dig("content")
        &.find { |content| content["type"] == "output_text" }
        &.fetch("text", nil)

      raise Error, "openai returned no output" if text.blank?

      ensure_html(strip_fence(text))
    rescue JSON::ParserError
      raise Error, "openai output was not JSON"
    rescue SystemCallError
      raise Error, "openai request could not be started"
    end

    def run_claude(prompt)
      stdout, stderr, status = run_command([
        "claude", "-p", prompt,
        "--model", CLAUDE_MODEL.call,
        "--output-format", "json",
        "--tools", ""
      ])
      unless status.success?
        raise Error, "claude CLI failed"
      end

      parsed = JSON.parse(stdout)
      raise Error, "claude returned error" if parsed["is_error"]

      ensure_html(strip_fence(parsed["result"].to_s))
    rescue JSON::ParserError
      raise Error, "claude output was not JSON"
    rescue SystemCallError
      raise Error, "claude CLI could not be started"
    end

    def run_codex(prompt)
      Tempfile.create(["expansion", ".html"]) do |output|
        _stdout, stderr, status = run_command([
          "codex", "exec",
          "-m", CODEX_MODEL,
          "-s", "read-only",
          "--skip-git-repo-check",
          "--color", "never",
          "-o", output.path,
          prompt
        ])
        unless status.success?
          raise Error, "codex CLI failed"
        end

        ensure_html(strip_fence(File.read(output.path)))
      end
    rescue SystemCallError
      raise Error, "codex CLI could not be started"
    end

    def run_command(cmd)
      Open3.popen3(*cmd) do |stdin, stdout, stderr, wait_thr|
        stdin.close
        out_reader = Thread.new { stdout.read }
        err_reader = Thread.new { stderr.read }

        unless wait_thr.join(TIMEOUT_SECONDS)
          Process.kill("KILL", wait_thr.pid) rescue nil
          raise Error, "#{cmd.first} timed out after #{TIMEOUT_SECONDS}s"
        end

        [out_reader.value, err_reader.value, wait_thr.value]
      end
    end

    def strip_fence(text)
      stripped = text.strip
      if stripped.start_with?("```")
        stripped = stripped.sub(/\A```[a-z]*\n/i, "").sub(/\n```\z/, "")
      end
      stripped
    end

    def ensure_html(text)
      raise Error, "output does not look like HTML" unless text.match?(/<html/i)

      text
    end
end
