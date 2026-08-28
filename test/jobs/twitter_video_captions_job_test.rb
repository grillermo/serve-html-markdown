require "test_helper"

class TwitterVideoCaptionsJobTest < ActiveJob::TestCase
  setup do
    @video = TwitterVideo.create!(source_url: "u", status: "awaiting_captions",
                                  youtube_id: "YT9", caption_attempts: 0,
                                  upload_completed_at: Time.current)
    @slack = FakeSlack.new
    TwitterVideoCaptionsJob.slack = -> { @slack }
    TwitterVideoCaptionsJob.summarizer = -> { FakeSummarizer.new }
    TwitterVideoCaptionsJob.rulinky = -> { FakeRulinky.new("link-1") }
    TwitterVideoCaptionsJob.html_writer = ->(**) { "the-idea.html" }
    ENV["HOST"] = "example.com"
  end

  teardown { TwitterVideoCaptionsJob.reset_collaborators! }

  test "on captions hit: summarizes, writes html, pushes rulinky, done" do
    TwitterVideoCaptionsJob.ytdlp = -> { FakeYtdlp.new(vtt: "WEBVTT\nhello") }
    TwitterVideoCaptionsJob.perform_now(@video.id)

    @video.reload
    assert_equal "done", @video.status
    assert_equal "the-idea.html", @video.html_filename
    assert_equal "link-1", @video.rulinky_link_id
    assert(@slack.successes.any? { |t| t.include?("published") })
  end

  test "on miss with checkpoints remaining: reschedules and bumps attempts" do
    TwitterVideoCaptionsJob.ytdlp = -> { FakeYtdlp.new(vtt: nil) }
    assert_enqueued_with(job: TwitterVideoCaptionsJob, args: [@video.id]) do
      TwitterVideoCaptionsJob.perform_now(@video.id)
    end
    assert_equal 1, @video.reload.caption_attempts
    assert_equal "awaiting_captions", @video.status
  end

  test "on miss at final checkpoint: fails" do
    @video.update!(caption_attempts: TwitterVideo::CHECKPOINTS.length - 1)
    TwitterVideoCaptionsJob.ytdlp = -> { FakeYtdlp.new(vtt: nil) }
    TwitterVideoCaptionsJob.perform_now(@video.id)

    assert_equal "failed", @video.reload.status
    assert @slack.failures.any?
  end

  class FakeYtdlp
    def initialize(vtt:) = (@vtt = vtt)
    def fetch_auto_subs(_url, dir)
      return nil if @vtt.nil?
      path = Pathname.new(dir).join("v.en.vtt")
      path.write(@vtt)
      path
    end
  end
  class FakeSummarizer
    def summarize(_t) = { title: "The Idea", summary_html: "<p>x</p>" }
  end
  class FakeRulinky
    def initialize(id) = (@id = id)
    def create_link(link:, note:) = @id
  end
  class FakeSlack
    attr_reader :successes, :failures
    def initialize = (@successes = []; @failures = [])
    def success(t) = @successes << t
    def failure(t) = @failures << t
  end
end
