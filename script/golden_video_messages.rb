# Captures what the real Ruby TwitterVideoIngestJob and TwitterVideoCaptionsJob say and
# store in a fixed set of scenarios (Slack texts, error_detail, status, the YouTube title
# passed to the uploader, the delay of the next caption check), so the Go port
# (internal/jobs) can be compared with them without Ruby.
#
#   bin/rails runner -e test script/golden_video_messages.rb
#
# Writes testdata/golden/video/messages.json. Everything runs in a transaction that is
# rolled back; the collaborators are fakes and the queue is ActiveJob's test adapter, so
# nothing reaches Slack, YouTube or the queue database. Record ids are replaced by "{id}".
require "json"
require "tmpdir"

ActiveJob::Base.queue_adapter = :test
ENV["HOST"] = "example.com"
ENV["YOUTUBE_REAUTH_URL"] = "https://serve.chiq.me/youtube/reauth"

class FakeSlack
  attr_reader :successes, :failures
  def initialize = (@successes = []; @failures = [])
  def success(t) = @successes << t
  def failure(t) = @failures << t
end

class FakeIngestYtdlp
  def initialize(title: "Tweet Title", error: nil) = (@title, @error = title, error)
  def download(_url, _dir)
    raise @error if @error
    { path: Pathname.new(Dir.mktmpdir).join("x.mp4"), title: @title }
  end
end

class FakeUploader
  attr_reader :title
  def initialize(error: nil) = (@error = error)
  def upload(file_path:, title:, description: "")
    @title = title
    raise @error if @error
    "YT9"
  end
end

class FakeCaptionsYtdlp
  def initialize(vtt: nil, error: nil) = (@vtt, @error = vtt, error)
  def fetch_auto_subs(_url, dir, cookies: nil)
    raise @error if @error
    return nil if @vtt.nil?
    Pathname.new(dir).join("v.en.vtt").tap { |p| p.write(@vtt) }
  end
end

class FakeCookies
  def export(path) = (File.write(path, "x"); path)
end

class FakeSummarizer
  def initialize(error: nil) = (@error = error)
  def summarize(_t) = @error ? raise(@error) : { title: "The Idea", summary_html: "<p>x</p>" }
end

class FakeRulinky
  def initialize(error: nil) = (@error = error)
  def create_link(link:, note:) = @error ? raise(@error) : "link-1"
end

def normalize(text, id)
  text.to_s.gsub("##{id}]", "#" + "{id}]").gsub("video_id=#{id}", "video_id=" + "{id}")
end

def minutes_until(enqueued, before)
  job = enqueued.last
  job && ((job[:at] - before) / 60.0).round
end

def snapshot(video, slack, enqueued, before, extra = {})
  video.reload
  {
    "status" => video.status,
    "error_detail" => video.error_detail,
    "caption_attempts" => video.caption_attempts,
    "html_filename" => video.html_filename,
    "rulinky_link_id" => video.rulinky_link_id,
    "slack_success" => slack.successes.map { |t| normalize(t, video.id) },
    "slack_failure" => slack.failures.map { |t| normalize(t, video.id) },
    "next_check_in_minutes" => minutes_until(enqueued, before)
  }.merge(extra)
end

def reset_queue = ActiveJob::Base.queue_adapter.enqueued_jobs.clear

def with_ingest(error_ytdlp: nil, title: "Tweet Title", upload_error: nil)
  video = TwitterVideo.create!(source_url: "https://x.com/golden/status/1", status: "downloading")
  slack = FakeSlack.new
  uploader = FakeUploader.new(error: upload_error)
  TwitterVideoIngestJob.ytdlp = ->(*) { FakeIngestYtdlp.new(title: title, error: error_ytdlp) }
  TwitterVideoIngestJob.uploader = ->(*) { uploader }
  TwitterVideoIngestJob.slack = -> { slack }
  reset_queue
  before = Time.now.to_f
  TwitterVideoIngestJob.perform_now(video.id)
  snapshot(video, slack, ActiveJob::Base.queue_adapter.enqueued_jobs, before, "upload_title" => uploader.title)
ensure
  TwitterVideoIngestJob.reset_collaborators!
end

def with_captions(source: "https://x.com/golden/status/1", attempts: 0, vtt: "WEBVTT\nhello", fetch_error: nil,
                  summarizer_error: nil, rulinky_error: nil)
  video = TwitterVideo.create!(source_url: source, status: "awaiting_captions", youtube_id: "YT9",
                               caption_attempts: attempts, upload_completed_at: Time.current)
  slack = FakeSlack.new
  TwitterVideoCaptionsJob.slack = -> { slack }
  TwitterVideoCaptionsJob.cookies = -> { FakeCookies.new }
  TwitterVideoCaptionsJob.ytdlp = -> { FakeCaptionsYtdlp.new(vtt: vtt, error: fetch_error) }
  TwitterVideoCaptionsJob.summarizer = -> { FakeSummarizer.new(error: summarizer_error) }
  TwitterVideoCaptionsJob.rulinky = -> { FakeRulinky.new(error: rulinky_error) }
  TwitterVideoCaptionsJob.html_writer = ->(**) { "the-idea.html" }
  reset_queue
  before = Time.now.to_f
  TwitterVideoCaptionsJob.perform_now(video.id)
  snapshot(video, slack, ActiveJob::Base.queue_adapter.enqueued_jobs, before)
ensure
  TwitterVideoCaptionsJob.reset_collaborators!
end

out = {}
ActiveRecord::Base.transaction do
  out["ingest_happy"] = with_ingest
  out["ingest_no_title"] = with_ingest(title: "")
  out["ingest_download_error"] = with_ingest(error_ytdlp: YtdlpClient::Error.new("gone"))
  out["ingest_reauth"] = with_ingest(upload_error: YoutubeUploader::AuthorizationExpired.new("invalid_grant"))
  out["ingest_upload_error"] = with_ingest(upload_error: YoutubeUploader::Error.new("YouTube upload failed: boom"))

  out["captions_hit"] = with_captions
  out["captions_miss"] = with_captions(vtt: nil)
  out["captions_ytdlp_error"] = with_captions(fetch_error: YtdlpClient::Error.new("HTTP 429"))
  out["captions_exhausted_twitter"] = with_captions(vtt: nil, attempts: TwitterVideo::CHECKPOINTS.length - 1)
  out["captions_exhausted_youtube"] = with_captions(vtt: nil, source: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
                                                    attempts: TwitterVideo::YOUTUBE_LINK_CHECKPOINTS.length - 1)
  out["captions_summarizer_error"] = with_captions(summarizer_error: OpenaiSummarizer::Error.new("OpenAI summarization failed."))
  out["captions_rulinky_error"] = with_captions(rulinky_error: RulinkyClient::Error.new("rulinky link creation failed."))
  raise ActiveRecord::Rollback
end

path = Rails.root.join("testdata/golden/video/messages.json")
FileUtils.mkdir_p(path.dirname)
path.write(JSON.pretty_generate(out) + "\n")
puts "wrote #{path} (#{out.size} scenarios)"
