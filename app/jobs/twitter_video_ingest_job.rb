require "fileutils"
require "pathname"

class TwitterVideoIngestJob < ApplicationJob
  queue_as :default

  # Permanent store, so a failed ingest can be retried without re-downloading.
  VIDEOS_DIR = Pathname.new(ENV.fetch("VIDEOS_DIR") { Rails.root.join("videos").to_s }).expand_path

  # Uploads land private while the OAuth app is unverified; flip them by hand here.
  STUDIO_EDIT_URL = "https://studio.youtube.com/video/%s/edit".freeze

  class << self
    attr_writer :ytdlp, :uploader, :slack

    def ytdlp = @ytdlp ||= ->(*) { YtdlpClient.new }
    def uploader = @uploader ||= lambda do |*|
      YoutubeUploader.new(
        client_id: ENV["YOUTUBE_CLIENT_ID"], client_secret: ENV["YOUTUBE_CLIENT_SECRET"],
        refresh_token: YoutubeCredential.refresh_token
      )
    end
    def slack = @slack ||= -> { SlackNotifier.from_env }

    def reset_collaborators!
      @ytdlp = @uploader = @slack = nil
    end
  end

  def perform(twitter_video_id)
    video = TwitterVideo.find_by(id: twitter_video_id)
    unless video
      Rails.logger.warn("[TwitterVideoIngestJob] ##{twitter_video_id} not found, skipping")
      return
    end

    Rails.logger.info("[TwitterVideoIngestJob] ##{video.id} starting: #{video.source_url}")
    slack = self.class.slack.call
    downloaded = reuse_existing(video) || download(video, slack)

    video.update!(status: "uploading")
    Rails.logger.info("[TwitterVideoIngestJob] ##{video.id} uploading to YouTube (unlisted)")
    youtube_id = self.class.uploader.call.upload(
      file_path: downloaded[:path], title: downloaded[:title].presence || "Twitter video"
    )
    video.update!(status: "awaiting_captions", youtube_id: youtube_id,
                  upload_completed_at: Time.current)
    Rails.logger.info("[TwitterVideoIngestJob] ##{video.id} uploaded: youtube_id=#{youtube_id}")
    slack.success("[twitter-video ##{video.id}] uploaded: #{video.youtube_title}\n" \
                  "set it to unlisted: #{STUDIO_EDIT_URL % youtube_id}")

    wait = TwitterVideo::CHECKPOINTS.first.minutes
    Rails.logger.info("[TwitterVideoIngestJob] ##{video.id} scheduling first caption check in #{wait.inspect}")
    TwitterVideoCaptionsJob.set(wait: wait).perform_later(video.id)
  rescue StandardError => error
    Rails.logger.error("[TwitterVideoIngestJob] ##{twitter_video_id} #{error.class}: #{error.message}")
    video&.fail!(error.message)
    self.class.slack.call.failure(failure_message(twitter_video_id, error))
  end

  private
    # A dead OAuth grant is the one failure a human can fix from a phone, so it gets a
    # link instead of a stack trace. The full Google message still reaches the log and
    # twitter_videos.error_detail.
    def failure_message(video_id, error)
      unless error.is_a?(YoutubeUploader::AuthorizationExpired)
        return "[twitter-video ##{video_id}] ingest failed: #{error.class}: #{error.message}"
      end

      "[twitter-video ##{video_id}] ingest failed: YouTube authorization expired.\n" \
        "re-authorize: #{YoutubeAuthorization.reauth_url(video_id)}\n" \
        "the video is already downloaded — the upload retries by itself once you're done."
    end

    def download(video, slack)
      FileUtils.mkdir_p(VIDEOS_DIR)
      Rails.logger.info("[TwitterVideoIngestJob] ##{video.id} downloading via yt-dlp into #{VIDEOS_DIR}")
      downloaded = self.class.ytdlp.call.download(video.source_url, VIDEOS_DIR)
      video.update!(youtube_title: downloaded[:title], video_path: downloaded[:path].to_s)
      Rails.logger.info("[TwitterVideoIngestJob] ##{video.id} downloaded: #{downloaded[:title]} (#{downloaded[:path]})")
      slack.success("[twitter-video ##{video.id}] downloaded: #{downloaded[:title]}")
      downloaded
    end

    # Any prior record for the same tweet (query string ignored) whose file is still on disk.
    def reuse_existing(video)
      candidate = downloaded_siblings(video).find { |other| File.exist?(other.video_path.to_s) }
      return nil unless candidate

      video.update!(video_path: candidate.video_path,
                    youtube_title: video.youtube_title.presence || candidate.youtube_title)
      Rails.logger.info("[TwitterVideoIngestJob] ##{video.id} reusing #{candidate.video_path}")
      { path: Pathname.new(candidate.video_path), title: video.youtube_title }
    end

    def downloaded_siblings(video)
      TwitterVideo.for_source_url(video.source_url)
                  .where.not(video_path: nil)
                  .order(Arel.sql("CASE WHEN id = #{video.id.to_i} THEN 0 ELSE 1 END"), id: :desc)
    end
end
