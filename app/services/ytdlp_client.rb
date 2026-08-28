require "open3"
require "pathname"

class YtdlpClient
  Error = Class.new(StandardError)

  DEFAULT_RUNNER = lambda do |argv|
    stdout, _stderr, status = Open3.capture3(*argv)
    [stdout, status.success?]
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
    stdout, ok = @runner.call(argv)
    raise Error, "yt-dlp download failed" unless ok

    path = parse_prefixed(stdout, "TW2WL_FILE:")
    title = parse_prefixed(stdout, "TW2WL_TITLE:")
    raise Error, "yt-dlp produced no file" if path.nil?

    { path: Pathname.new(path), title: title }
  end

  def fetch_auto_subs(youtube_url, dest_dir)
    outtmpl = File.join(dest_dir.to_s, "%(id)s.%(ext)s")
    argv = [@bin,
            "--write-auto-subs",
            "--sub-langs", "en.*",
            "--sub-format", "vtt",
            "--skip-download",
            "--no-playlist",
            "-o", outtmpl,
            youtube_url]
    _stdout, ok = @runner.call(argv)
    raise Error, "yt-dlp subs fetch failed" unless ok

    Pathname.glob(File.join(dest_dir.to_s, "*.vtt")).min_by { |p| p.to_s }
  end

  private
    def parse_prefixed(output, prefix)
      line = output.each_line.find { |l| l.start_with?(prefix) }
      line&.delete_prefix(prefix)&.strip
    end
end
