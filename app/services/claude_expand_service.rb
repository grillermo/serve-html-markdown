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

  ANCHOR_SENTINEL = "⟦EXPANSION_ANCHOR⟧"

  REWRITE_PROMPT_TEMPLATE = <<~PROMPT
    You are given a document, a text selection from it, and a reader's question about that selection.

    Rewrite the document so the selected passage is expanded in light of the question: add the background, context, related concepts, and concrete details the original leaves out, woven into the document's own voice.

    Requirements:
    - Output the COMPLETE document, from its first line to its last. Never truncate, summarize, or elide with "...".
    - Keep the document's original format exactly: Markdown stays Markdown, HTML stays HTML.
    - Preserve every part of the document unrelated to the selection verbatim, including front matter, links, and code blocks.
    - Immediately before the expanded passage, emit the marker #{ANCHOR_SENTINEL} on a line of its own. Emit it exactly once.
    - Output ONLY the document. No markdown fences, no commentary.

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
  def self.rewrite(**kwargs) = new.rewrite(**kwargs)

  def expand(file_name:, document:, selection:, question:, use_openai: false, expansion: nil)
    generate(
      template: PROMPT_TEMPLATE, validator: method(:ensure_html),
      file_name:, document:, selection:, question:, use_openai:, expansion:
    )
  end

  def rewrite(file_name:, document:, selection:, question:, use_openai: false, expansion: nil)
    validator = File.extname(file_name).downcase == ".html" ? method(:ensure_html) : method(:ensure_present)
    generate(
      template: REWRITE_PROMPT_TEMPLATE, validator:, preserve_whitespace: true,
      file_name:, document:, selection:, question:, use_openai:, expansion:
    )
  end

  private
    def generate(template:, validator:, file_name:, document:, selection:, question:, use_openai:, expansion:, preserve_whitespace: false)
      @validator = validator
      @preserve_whitespace = preserve_whitespace
      prompt = format(template, file_name:, document:, selection:, question:)
      Rails.logger.info "[ClaudeExpandService] generating file=#{file_name} selection_bytes=#{selection.bytesize} question_bytes=#{question.bytesize} use_openai=#{use_openai}"
      expansion&.stamp!(:llm_request_start)

      if use_openai
        html = finish(run_openai(prompt))
        record_response(expansion, "openai", html)
        return html
      end

      html = finish(run_claude(prompt))
      record_response(expansion, "claude", html)
      html
    rescue Error => error
      raise error if use_openai

      Rails.logger.warn "[ClaudeExpandService] claude failed, falling back to codex"
      expansion&.stamp!(:llm_first_failure)
      html = finish(run_codex(prompt))
      record_response(expansion, "codex", html)
      html
    end

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

      text
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

      parsed["result"].to_s
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

        File.read(output.path)
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
      return stripped.sub(/\A```[a-z]*\n/i, "").sub(/\n```\z/, "") if stripped.start_with?("```")
      return text if @preserve_whitespace

      stripped
    end

    def ensure_html(text)
      raise Error, "output does not look like HTML" unless text.match?(/<html/i)

      text
    end

    def finish(text)
      @validator.call(strip_fence(text))
    end

    def ensure_present(text)
      raise Error, "output was empty" if text.strip.empty?

      text
    end
end
