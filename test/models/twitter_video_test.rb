require "test_helper"

class TwitterVideoTest < ActiveSupport::TestCase
  test "fail! records status and detail" do
    v = TwitterVideo.create!(source_url: "https://x.com/a/status/1", status: "uploading")
    v.fail!("boom")
    assert_equal ["failed", "boom"], v.reload.attributes.values_at("status", "error_detail")
  end

  test "next_caption_wait returns seconds to first checkpoint before any attempt" do
    t0 = Time.utc(2026, 7, 23, 12, 0, 0)
    v = TwitterVideo.create!(source_url: "u", status: "awaiting_captions",
                             caption_attempts: 0, upload_completed_at: t0)
    # 5 min checkpoint, 1 min already elapsed -> 240s
    assert_equal 240, v.next_caption_wait(now: t0 + 60)
  end

  test "next_caption_wait never returns negative" do
    t0 = Time.utc(2026, 7, 23, 12, 0, 0)
    v = TwitterVideo.create!(source_url: "u", status: "awaiting_captions",
                             caption_attempts: 1, upload_completed_at: t0)
    # 2nd checkpoint is 10 min; if 15 min elapsed, clamp to 0
    assert_equal 0, v.next_caption_wait(now: t0 + 15 * 60)
  end

  test "next_caption_wait is nil once checkpoints are exhausted" do
    v = TwitterVideo.create!(source_url: "u", status: "awaiting_captions",
                             caption_attempts: TwitterVideo::CHECKPOINTS.length,
                             upload_completed_at: Time.current)
    assert_nil v.next_caption_wait
  end
end
