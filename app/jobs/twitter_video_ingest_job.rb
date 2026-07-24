require "tmpdir"
require "fileutils"

class TwitterVideoIngestJob < ApplicationJob
  queue_as :default

  class << self
    attr_writer :ytdlp, :uploader, :slack

    def ytdlp = @ytdlp ||= ->(*) { YtdlpClient.new }
    def uploader = @uploader ||= lambda do |*|
      YoutubeUploader.new(
        client_id: ENV["YOUTUBE_CLIENT_ID"], client_secret: ENV["YOUTUBE_CLIENT_SECRET"],
        refresh_token: ENV["YOUTUBE_REFRESH_TOKEN"]
      )
    end
    def slack = @slack ||= -> { SlackNotifier.from_env }

    def reset_collaborators!
      @ytdlp = @uploader = @slack = nil
    end
  end

  def perform(twitter_video_id)
    video = TwitterVideo.find_by(id: twitter_video_id)
    return unless video

    slack = self.class.slack.call
    Dir.mktmpdir do |dir|
      downloaded = self.class.ytdlp.call.download(video.source_url, dir)
      video.update!(youtube_title: downloaded[:title])
      slack.success("[twitter-video ##{video.id}] downloaded: #{downloaded[:title]}")

      video.update!(status: "uploading")
      youtube_id = self.class.uploader.call.upload(
        file_path: downloaded[:path], title: downloaded[:title].presence || "Twitter video"
      )
      video.update!(status: "awaiting_captions", youtube_id: youtube_id,
                    upload_completed_at: Time.current)
      slack.success("[twitter-video ##{video.id}] uploaded unlisted: #{youtube_id}")
    end

    TwitterVideoCaptionsJob.set(wait: TwitterVideo::CHECKPOINTS.first.minutes)
                           .perform_later(video.id)
  rescue StandardError => error
    Rails.logger.error("[TwitterVideoIngestJob] #{error.class}: #{error.message}")
    video&.fail!(error.message)
    self.class.slack.call.failure("[twitter-video ##{twitter_video_id}] ingest failed: #{error.class}: #{error.message}")
  end
end
