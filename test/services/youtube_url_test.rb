require "test_helper"

class YoutubeUrlTest < ActiveSupport::TestCase
  test "extracts the id from short, watch, shorts and mobile links" do
    assert_equal "KpILHX2D-AA", YoutubeUrl.video_id("https://youtu.be/KpILHX2D-AA?si=3zdm0UjTBIYF-k0c")
    assert_equal "KpILHX2D-AA", YoutubeUrl.video_id("https://www.youtube.com/watch?v=KpILHX2D-AA&t=42")
    assert_equal "KpILHX2D-AA", YoutubeUrl.video_id("https://youtube.com/shorts/KpILHX2D-AA?feature=share")
    assert_equal "KpILHX2D-AA", YoutubeUrl.video_id("https://m.youtube.com/watch?v=KpILHX2D-AA")
  end

  test "returns nil for non-youtube or id-less links" do
    assert_nil YoutubeUrl.video_id("https://x.com/a/status/1")
    assert_nil YoutubeUrl.video_id("https://www.youtube.com/watch?v=x")
    assert_nil YoutubeUrl.video_id("https://www.youtube.com/@channel")
    assert_nil YoutubeUrl.video_id("not a url")
  end
end
