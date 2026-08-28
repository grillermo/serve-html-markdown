require "tmpdir"

class TwitterVideoCaptionsJob < ApplicationJob
  queue_as :default

  WATCH_URL = ->(youtube_id) { "https://www.youtube.com/watch?v=#{youtube_id}" }

  class << self
    attr_writer :ytdlp, :summarizer, :rulinky, :slack, :html_writer

    def ytdlp = @ytdlp ||= -> { YtdlpClient.new }
    def summarizer = @summarizer ||= -> { GeminiSummarizer.new }
    def rulinky = @rulinky ||= -> { RulinkyClient.new }
    def slack = @slack ||= -> { SlackNotifier.from_env }
    def html_writer = @html_writer ||= ->(**kwargs) { ServedHtmlWriter.write(**kwargs) }

    def reset_collaborators!
      @ytdlp = @summarizer = @rulinky = @slack = @html_writer = nil
    end
  end

  def perform(twitter_video_id)
    video = TwitterVideo.find_by(id: twitter_video_id)
    unless video && video.status == "awaiting_captions"
      Rails.logger.info("[TwitterVideoCaptionsJob] ##{twitter_video_id} skipping (missing or not awaiting_captions)")
      return
    end

    Rails.logger.info("[TwitterVideoCaptionsJob] ##{video.id} checking for auto-captions (attempt #{video.caption_attempts + 1})")
    vtt = Dir.mktmpdir { |dir| read_captions(video, dir) }
    return reschedule_or_fail(video) if vtt.nil?

    Rails.logger.info("[TwitterVideoCaptionsJob] ##{video.id} captions found (#{vtt.bytesize} bytes)")
    publish(video, vtt)
  rescue StandardError => error
    Rails.logger.error("[TwitterVideoCaptionsJob] ##{twitter_video_id} #{error.class}: #{error.message}")
    video&.fail!(error.message)
    self.class.slack.call.failure("[twitter-video ##{twitter_video_id}] captions/publish failed: #{error.class}: #{error.message}")
  end

  private
    def read_captions(video, dir)
      path = self.class.ytdlp.call.fetch_auto_subs(WATCH_URL.call(video.youtube_id), dir)
      path&.read
    end

    def reschedule_or_fail(video)
      video.update!(caption_attempts: video.caption_attempts + 1)
      wait = video.next_caption_wait
      if wait.nil?
        Rails.logger.warn("[TwitterVideoCaptionsJob] ##{video.id} captions never became available, giving up")
        video.fail!("Auto-captions never became available.")
        self.class.slack.call.failure("[twitter-video ##{video.id}] captions timed out after #{TwitterVideo::CHECKPOINTS.last.minutes.inspect}")
      else
        Rails.logger.info("[TwitterVideoCaptionsJob] ##{video.id} not ready, retrying in #{wait}s (attempt #{video.caption_attempts})")
        self.class.slack.call.success("[twitter-video ##{video.id}] captions not ready (attempt #{video.caption_attempts}); retrying")
        TwitterVideoCaptionsJob.set(wait: wait.seconds).perform_later(video.id)
      end
    end

    def publish(video, vtt)
      slack = self.class.slack.call

      video.update!(status: "summarizing")
      Rails.logger.info("[TwitterVideoCaptionsJob] ##{video.id} summarizing transcript with Gemini")
      summary = self.class.summarizer.call.summarize(vtt)
      Rails.logger.info("[TwitterVideoCaptionsJob] ##{video.id} summarized: #{summary[:title]}")
      slack.success("[twitter-video ##{video.id}] summarized: #{summary[:title]}")

      video.update!(status: "publishing")
      Rails.logger.info("[TwitterVideoCaptionsJob] ##{video.id} writing summary HTML")
      filename = self.class.html_writer.call(title: summary[:title], summary_html: summary[:summary_html])
      link = "https://#{ENV.fetch("HOST", "localhost")}/#{filename}"
      Rails.logger.info("[TwitterVideoCaptionsJob] ##{video.id} wrote #{filename}, pushing link to rulinky")
      rulinky_id = self.class.rulinky.call.create_link(link: link, note: summary[:title])

      video.update!(status: "done", html_filename: filename, rulinky_link_id: rulinky_id)
      Rails.logger.info("[TwitterVideoCaptionsJob] ##{video.id} done: #{link} (rulinky_id=#{rulinky_id})")
      slack.success("[twitter-video ##{video.id}] published: #{link}")
    end
end
