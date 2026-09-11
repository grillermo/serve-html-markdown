require "test_helper"
require "tempfile"

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

  test "posts the studio edit link to slack after upload" do
    TwitterVideoIngestJob.perform_now(@video.id)

    assert_includes @slack.successes.last, "https://studio.youtube.com/video/YT9/edit"
    assert_includes @slack.successes.last, "unlisted"
  end

  test "downloads into the permanent videos dir and records the path" do
    TwitterVideoIngestJob.perform_now(@video.id)

    @video.reload
    assert_equal TwitterVideoIngestJob::VIDEOS_DIR.join("x.mp4").to_s, @video.video_path
  end

  test "reuses an already downloaded file for the same source url" do
    existing = Tempfile.create(["reused", ".mp4"])
    TwitterVideo.create!(source_url: "https://x.com/foo/status/1?s=20", status: "done",
                         video_path: existing.path, youtube_title: "Cached Title")
    TwitterVideoIngestJob.ytdlp = ->(*) { raise "must not download again" }

    TwitterVideoIngestJob.perform_now(@video.id)

    @video.reload
    assert_equal "awaiting_captions", @video.status
    assert_equal existing.path, @video.video_path
    assert_equal "Cached Title", @video.youtube_title
  end

  test "re-downloads when the recorded file is gone" do
    @video.update!(video_path: "/nonexistent/gone.mp4")

    TwitterVideoIngestJob.perform_now(@video.id)

    assert_equal TwitterVideoIngestJob::VIDEOS_DIR.join("x.mp4").to_s, @video.reload.video_path
  end

  test "marks failed and notifies on download error" do
    TwitterVideoIngestJob.ytdlp = ->(*) { raise YtdlpClient::Error, "gone" }
    TwitterVideoIngestJob.perform_now(@video.id)

    assert_equal "failed", @video.reload.status
    assert @slack.failures.any?
  end

  test "posts a re-authorization link when the youtube grant is dead" do
    TwitterVideoIngestJob.uploader = ->(*) { raise YoutubeUploader::AuthorizationExpired, "invalid_grant" }

    with_env("YOUTUBE_REAUTH_URL" => "https://serve.chiq.me/youtube/reauth") do
      TwitterVideoIngestJob.perform_now(@video.id)
    end

    assert_equal "failed", @video.reload.status
    message = @slack.failures.last
    assert_includes message, "YouTube authorization expired"
    assert_includes message, "https://serve.chiq.me/youtube/reauth?video_id=#{@video.id}"
    assert_includes message, "already downloaded"
  end

  test "keeps the raw error text for failures a link cannot fix" do
    TwitterVideoIngestJob.ytdlp = ->(*) { raise YtdlpClient::Error, "gone" }

    TwitterVideoIngestJob.perform_now(@video.id)

    message = @slack.failures.last
    assert_includes message, "YtdlpClient::Error: gone"
    assert_not_includes message, "youtube/reauth"
  end

  test "the default uploader takes its refresh token from the database" do
    # setup replaced the uploader with a fake; this test exercises the real default.
    TwitterVideoIngestJob.reset_collaborators!

    with_env("YOUTUBE_CLIENT_ID" => "cid", "YOUTUBE_CLIENT_SECRET" => "sec",
             "YOUTUBE_REFRESH_TOKEN" => nil) do
      # No row and no env var: the constructor guard from Task 2 fires.
      assert_raises(YoutubeUploader::AuthorizationExpired) { TwitterVideoIngestJob.uploader.call }

      YoutubeCredential.store!("from-db")

      assert_nothing_raised { TwitterVideoIngestJob.uploader.call }
    end
  end

  class FakeYtdlp
    def download(_url, dir)
      path = Pathname.new(dir).join("x.mp4")
      FileUtils.mkdir_p(dir)
      FileUtils.touch(path)
      { path: path, title: "Tweet Title" }
    end
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

  private
    def with_env(hash)
      previous = {}
      hash.each do |key, value|
        previous[key] = ENV[key]
        ENV[key] = value
      end
      yield
    ensure
      previous.each do |key, prev_value|
        prev_value.nil? ? ENV.delete(key) : ENV[key] = prev_value
      end
    end
end
