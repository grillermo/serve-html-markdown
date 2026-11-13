require "test_helper"

class TwitterUrlTest < ActiveSupport::TestCase
  test "normalizes every tweet link variant to x.com without tracking query" do
    {
      "https://x.com/foo/status/123" => "https://x.com/foo/status/123",
      "https://x.com/foo/status/123?s=20&t=abc" => "https://x.com/foo/status/123",
      "https://twitter.com/foo/status/123" => "https://x.com/foo/status/123",
      "https://www.twitter.com/foo/status/123" => "https://x.com/foo/status/123",
      "https://mobile.twitter.com/foo/status/123" => "https://x.com/foo/status/123",
      "https://mobile.x.com/foo/status/123" => "https://x.com/foo/status/123",
      "https://x.com/foo/status/123/video/1" => "https://x.com/foo/status/123",
      "https://x.com/foo/status/123/photo/2" => "https://x.com/foo/status/123",
      "https://twitter.com/foo/statuses/123" => "https://x.com/foo/status/123",
      "https://x.com/i/status/123" => "https://x.com/i/status/123",
      "https://twitter.com/i/web/status/123" => "https://x.com/i/status/123",
      "https://fxtwitter.com/foo/status/123" => "https://x.com/foo/status/123",
      "https://vxtwitter.com/foo/status/123" => "https://x.com/foo/status/123",
      "https://fixupx.com/foo/status/123" => "https://x.com/foo/status/123",
      "https://fixvx.com/foo/status/123" => "https://x.com/foo/status/123",
      "https://xcancel.com/foo/status/123" => "https://x.com/foo/status/123",
      "http://x.com/foo/status/123" => "https://x.com/foo/status/123",
      "x.com/foo/status/123" => "https://x.com/foo/status/123",
      " https://X.com/foo/status/123 \n" => "https://x.com/foo/status/123"
    }.each do |input, expected|
      assert_equal expected, TwitterUrl.normalize(input), input
    end
  end

  test "rejects non-status and non-twitter urls" do
    assert_raises(TwitterUrl::InvalidError) { TwitterUrl.normalize("https://x.com/foo") }
    assert_raises(TwitterUrl::InvalidError) { TwitterUrl.normalize("https://x.com/foo/status/abc") }
    assert_raises(TwitterUrl::InvalidError) { TwitterUrl.normalize("https://youtube.com/watch?v=x") }
    assert_raises(TwitterUrl::InvalidError) { TwitterUrl.normalize("https://notx.com/foo/status/1") }
    assert_raises(TwitterUrl::InvalidError) { TwitterUrl.normalize("") }
  end
end
