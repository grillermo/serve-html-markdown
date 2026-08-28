# test/services/youtube_uploader_test.rb
require "test_helper"

class YoutubeUploaderTest < ActiveSupport::TestCase
  test "uploads unlisted and returns the video id" do
    captured = {}
    fake_service = Object.new
    fake_service.define_singleton_method(:insert_video) do |parts, video, upload_source:, content_type:|
      captured[:parts] = parts
      captured[:privacy] = video.status.privacy_status
      captured[:title] = video.snippet.title
      captured[:upload_source] = upload_source
      Struct.new(:id).new("YT123")
    end

    uploader = YoutubeUploader.new(client_id: "c", client_secret: "s",
                                   refresh_token: "r", service: fake_service)
    id = uploader.upload(file_path: "/tmp/x.mp4", title: "Tweet by foo", description: "d")

    assert_equal "YT123", id
    assert_equal "unlisted", captured[:privacy]
    assert_equal "Tweet by foo", captured[:title]
    assert_equal "/tmp/x.mp4", captured[:upload_source]
  end

  test "raises on blank config" do
    assert_raises(YoutubeUploader::Error) do
      YoutubeUploader.new(client_id: "", client_secret: "", refresh_token: "")
    end
  end
end
