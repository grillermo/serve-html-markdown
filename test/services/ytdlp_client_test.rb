require "test_helper"
require "tmpdir"

class YtdlpClientTest < ActiveSupport::TestCase
  test "download returns path and parsed title" do
    Dir.mktmpdir do |dir|
      path = File.join(dir, "vid.mp4")
      File.write(path, "x")
      runner = lambda do |argv|
        assert_includes argv, "https://x.com/foo/status/1"
        ["TW2WL_FILE:#{path}\nTW2WL_TITLE:Hello Tweet\n", true]
      end
      result = YtdlpClient.new(bin: "yt-dlp", runner: runner).download("https://x.com/foo/status/1", dir)
      assert_equal Pathname.new(path), result[:path]
      assert_equal "Hello Tweet", result[:title]
    end
  end

  test "download raises on failure" do
    Dir.mktmpdir do |dir|
      runner = ->(_argv) { ["ERROR: unavailable", false] }
      assert_raises(YtdlpClient::Error) do
        YtdlpClient.new(bin: "yt-dlp", runner: runner).download("u", dir)
      end
    end
  end

  test "fetch_auto_subs returns the produced vtt path" do
    Dir.mktmpdir do |dir|
      vtt = File.join(dir, "video.en.vtt")
      runner = lambda do |argv|
        assert_includes argv, "--write-auto-subs"
        assert_includes argv, "--skip-download"
        File.write(vtt, "WEBVTT")
        ["", true]
      end
      result = YtdlpClient.new(bin: "yt-dlp", runner: runner)
                          .fetch_auto_subs("https://youtube.com/watch?v=abc", dir)
      assert_equal Pathname.new(vtt), result
    end
  end

  test "fetch_auto_subs returns nil when no vtt produced" do
    Dir.mktmpdir do |dir|
      runner = ->(_argv) { ["", true] }
      assert_nil YtdlpClient.new(bin: "yt-dlp", runner: runner).fetch_auto_subs("u", dir)
    end
  end
end
