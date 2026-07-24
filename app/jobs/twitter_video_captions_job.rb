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
    return unless video && video.status == "awaiting_captions"

    vtt = Dir.mktmpdir { |dir| read_captions(video, dir) }
    return reschedule_or_fail(video) if vtt.nil?

    publish(video, vtt)
  rescue StandardError => error
    Rails.logger.error("[TwitterVideoCaptionsJob] #{error.class}: #{error.message}")
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
        video.fail!("Auto-captions never became available.")
        self.class.slack.call.failure("[twitter-video ##{video.id}] captions timed out after #{TwitterVideo::CHECKPOINTS.last} min")
      else
        self.class.slack.call.success("[twitter-video ##{video.id}] captions not ready (attempt #{video.caption_attempts}); retrying")
        TwitterVideoCaptionsJob.set(wait: wait.seconds).perform_later(video.id)
      end
    end

    def publish(video, vtt)
      slack = self.class.slack.call
      video.update!(status: "summarizing")
      summary = self.class.summarizer.call.summarize(vtt)
      slack.success("[twitter-video ##{video.id}] summarized: #{summary[:title]}")

      video.update!(status: "publishing")
      filename = self.class.html_writer.call(title: summary[:title], summary_html: summary[:summary_html])
      link = "https://#{ENV.fetch("HOST", "localhost")}/#{filename}"
      rulinky_id = self.class.rulinky.call.create_link(link: link, note: summary[:title])

      video.update!(status: "done", html_filename: filename, rulinky_link_id: rulinky_id)
      slack.success("[twitter-video ##{video.id}] published: #{link}")
    end
end
