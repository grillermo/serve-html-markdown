require "test_helper"

class TwitterVideoIngestJobTest < ActiveJob::TestCase
  setup do
    @video = TwitterVideo.create!(source_url: "https://x.com/foo/status/1", status: "downloading")
    @slack = FakeSlack.new
    TwitterVideoIngestJob.ytdlp = ->(*) { FakeYtdlp.new }
    TwitterVideoIngestJob.uploader = ->(*) { FakeUploader.new("YT9") }
    TwitterVideoIngestJob.slack = -> { @slack }
  end

  teardown { TwitterVideoIngestJob.reset_collaborators! }

  test "downloads, uploads unlisted, schedules first caption poll" do
    assert_enqueued_with(job: TwitterVideoCaptionsJob, args: [@video.id]) do
      TwitterVideoIngestJob.perform_now(@video.id)
    end

    @video.reload
    assert_equal "awaiting_captions", @video.status
    assert_equal "YT9", @video.youtube_id
    assert_equal "Tweet Title", @video.youtube_title
    assert_not_nil @video.upload_completed_at
    assert @slack.successes.any?
  end

  test "marks failed and notifies on download error" do
    TwitterVideoIngestJob.ytdlp = ->(*) { raise YtdlpClient::Error, "gone" }
    TwitterVideoIngestJob.perform_now(@video.id)

    assert_equal "failed", @video.reload.status
    assert @slack.failures.any?
  end

  class FakeYtdlp
    def download(_url, _dir) = { path: Pathname.new("/tmp/x.mp4"), title: "Tweet Title" }
  end
  class FakeUploader
    def initialize(id) = (@id = id)
    def upload(**) = @id
  end
  class FakeSlack
    attr_reader :successes, :failures
    def initialize = (@successes = []; @failures = [])
    def success(t) = @successes << t
    def failure(t) = @failures << t
  end
end
