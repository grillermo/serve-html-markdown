require "test_helper"

class YoutubeUrlTest < ActiveSupport::TestCase
  ID = "KpILHX2D-AA"

  test "extracts the id from every link variant" do
    [
      "https://www.youtube.com/watch?v=#{ID}",
      "https://youtube.com/watch?v=#{ID}&t=42",
      "https://www.youtube.com/watch?feature=share&v=#{ID}",
      "https://www.youtube.com/watch/?v=#{ID}",
      "https://m.youtube.com/watch?v=#{ID}&list=PL123&index=2",
      "https://music.youtube.com/watch?v=#{ID}&si=abc",
      "https://gaming.youtube.com/watch?v=#{ID}",
      "https://youtu.be/#{ID}",
      "https://youtu.be/#{ID}?si=3zdm0UjTBIYF-k0c&t=10",
      "https://www.youtube.com/shorts/#{ID}?feature=share",
      "https://youtube.com/live/#{ID}?si=x",
      "https://www.youtube.com/embed/#{ID}?start=5",
      "https://www.youtube-nocookie.com/embed/#{ID}",
      "https://www.youtube.com/v/#{ID}",
      "https://www.youtube.com/e/#{ID}",
      "https://www.youtube.com/watch/#{ID}",
      "https://www.youtube.com/attribution_link?a=xyz&u=%2Fwatch%3Fv%3D#{ID}%26feature%3Dshare",
      "http://www.youtube.com/watch?v=#{ID}",
      "HTTPS://WWW.YOUTUBE.COM/watch?v=#{ID}",
      "www.youtube.com/watch?v=#{ID}",
      "youtu.be/#{ID}",
      "  https://youtu.be/#{ID}\n"
    ].each do |url|
      assert_equal ID, YoutubeUrl.video_id(url), url
    end
  end

  test "handles the reported watch link" do
    assert_equal "hWe-j78vNpc", YoutubeUrl.video_id("https://www.youtube.com/watch?v=hWe-j78vNpc")
  end

  test "returns nil for non-youtube or id-less links" do
    assert_nil YoutubeUrl.video_id("https://x.com/a/status/1")
    assert_nil YoutubeUrl.video_id("https://www.youtube.com/watch?v=x")
    assert_nil YoutubeUrl.video_id("https://www.youtube.com/@channel")
    assert_nil YoutubeUrl.video_id("https://www.youtube.com/playlist?list=PL123")
    assert_nil YoutubeUrl.video_id("https://notyoutube.com/watch?v=#{ID}")
    assert_nil YoutubeUrl.video_id("not a url")
    assert_nil YoutubeUrl.video_id("")
  end
end
