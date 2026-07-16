require "json"
require "open3"
require "tempfile"

class ClaudeExpandService
  Error = Class.new(StandardError)

  CLAUDE_MODEL = "sonnet"
  CODEX_MODEL = "earth"
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

  def expand(file_name:, document:, selection:, question:)
    prompt = format(PROMPT_TEMPLATE, file_name:, document:, selection:, question:)
    Rails.logger.info "[ClaudeExpandService] expanding file=#{file_name} selection_bytes=#{selection.bytesize} question_bytes=#{question.bytesize}"

    html = run_claude(prompt)
    Rails.logger.info "[ClaudeExpandService] claude succeeded bytes=#{html.bytesize}"
    html
  rescue Error => error
    Rails.logger.warn "[ClaudeExpandService] claude failed (#{error.message}), falling back to codex"
    html = run_codex(prompt)
    Rails.logger.info "[ClaudeExpandService] codex succeeded bytes=#{html.bytesize}"
    html
  end

  private
    def run_claude(prompt)
      stdout, stderr, status = run_command([
        "claude", "-p", prompt,
        "--model", CLAUDE_MODEL,
        "--output-format", "json",
        "--tools", ""
      ])
      unless status.success?
        raise Error, "claude CLI failed (#{status.exitstatus}): #{stderr.strip[0, 500]}"
      end

      parsed = JSON.parse(stdout)
      raise Error, "claude returned error: #{parsed["result"].to_s[0, 500]}" if parsed["is_error"]

      ensure_html(strip_fence(parsed["result"].to_s))
    rescue JSON::ParserError => error
      raise Error, "claude output was not JSON: #{error.message}"
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
          raise Error, "codex CLI failed (#{status.exitstatus}): #{stderr.strip[0, 500]}"
        end

        ensure_html(strip_fence(File.read(output.path)))
      end
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
