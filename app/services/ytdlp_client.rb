require "open3"
require "pathname"

class YtdlpClient
  Error = Class.new(StandardError)

  DEFAULT_RUNNER = lambda do |argv|
    stdout, stderr, status = Open3.capture3(*argv)
    [stdout, status.success?, stderr]
  end

  def initialize(bin: ENV.fetch("YTDLP_BIN", "/opt/homebrew/bin/yt-dlp"), runner: DEFAULT_RUNNER)
    @bin = bin
    @runner = runner
  end

  def download(url, dest_dir)
    outtmpl = File.join(dest_dir.to_s, "%(id)s.%(ext)s")
    argv = [@bin,
            "-f", "best[ext=mp4]/best",
            "--no-playlist",
            "-o", outtmpl,
            "--print", "after_move:TW2WL_FILE:%(filepath)s",
            "--print", "after_move:TW2WL_TITLE:%(title)s",
            "--newline", url]
    stdout, ok, stderr = @runner.call(argv)
    raise Error, failure_message("yt-dlp download failed", stderr) unless ok

    path = parse_prefixed(stdout, "TW2WL_FILE:")
    title = parse_prefixed(stdout, "TW2WL_TITLE:")
    raise Error, "yt-dlp produced no file" if path.nil?

    { path: Pathname.new(path), title: title }
  end

  def fetch_auto_subs(youtube_url, dest_dir, cookies: nil)
    outtmpl = File.join(dest_dir.to_s, "%(id)s.%(ext)s")
    # Uploaded captions win over auto-captions when a video has both.
    argv = [@bin,
            "--write-subs",
            "--write-auto-subs",
            "--sub-langs", "en.*",
            "--sub-format", "vtt",
            "--skip-download",
            # Signed in, YouTube can offer no playable format; that must not stop the subs.
            "--ignore-no-formats-error",
            "--no-playlist",
            "-o", outtmpl,
            *(["--cookies", cookies.to_s] if cookies),
            youtube_url]
    _stdout, ok, stderr = @runner.call(argv)
    raise Error, failure_message("yt-dlp subs fetch failed", stderr) unless ok

    Pathname.glob(File.join(dest_dir.to_s, "*.vtt")).min_by { |p| p.to_s }
  end

  private
    # yt-dlp explains failures on stderr ("ERROR: ..."); carry those lines into the
    # error so the logs and Slack say why instead of only that it failed.
    def failure_message(summary, stderr)
      lines = stderr.to_s.lines.map(&:strip).reject(&:empty?)
      reason = lines.grep(/\AERROR:/).presence || lines.last(3)
      reason.empty? ? summary : "#{summary}: #{reason.join(" | ")}"
    end

    def parse_prefixed(output, prefix)
      line = output.each_line.find { |l| l.start_with?(prefix) }
      line&.delete_prefix(prefix)&.strip
    end
end
