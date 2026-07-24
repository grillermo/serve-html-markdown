require "test_helper"

class TwitterUrlTest < ActiveSupport::TestCase
  test "normalizes twitter.com and mobile hosts to x.com" do
    assert_equal "https://x.com/foo/status/123",
      TwitterUrl.normalize("https://twitter.com/foo/status/123")
    assert_equal "https://x.com/foo/status/123",
      TwitterUrl.normalize("https://mobile.x.com/foo/status/123")
  end

  test "preserves query string" do
    assert_equal "https://x.com/foo/status/123?s=20",
      TwitterUrl.normalize("https://x.com/foo/status/123?s=20")
  end

  test "rejects non-status and non-twitter urls" do
    assert_raises(TwitterUrl::InvalidError) { TwitterUrl.normalize("https://x.com/foo") }
    assert_raises(TwitterUrl::InvalidError) { TwitterUrl.normalize("https://youtube.com/watch?v=x") }
  end
end
