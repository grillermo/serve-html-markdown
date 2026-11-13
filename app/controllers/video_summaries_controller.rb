class VideoSummariesController < ApplicationController
  skip_forgery_protection only: :create
  skip_before_action :authenticate_user!, only: [:create, :show], raise: false
  # The first link in a shared blob of text ("Watch this! https://youtu.be/…").
  EMBEDDED_URL = %r{https?://\S+}i

  def create
    return render_unauthorized unless authenticated?

    raw_url = submitted_url
    if raw_url.blank?
      Rails.logger.warn("[VideoSummariesController] rejected request with no url")
      return render json: { detail: "Missing url" }, status: :bad_request
    end

    youtube_id = YoutubeUrl.video_id(raw_url)
    return create_from_youtube(youtube_id) if youtube_id

    source_url = TwitterUrl.normalize(raw_url)
    existing = TwitterVideo.for_source_url(source_url).order(id: :desc).first
    video = existing || TwitterVideo.create!(source_url: source_url, status: "downloading")
    Rails.logger.info("[VideoSummariesController] ##{video.id} #{existing ? 'retrying' : 'enqueued'} for #{source_url}")
    TwitterVideoIngestJob.perform_later(video.id)
    render json: { id: video.id, status: video.status }, status: :accepted
  rescue TwitterUrl::InvalidError => error
    Rails.logger.warn("[VideoSummariesController] rejected invalid url: #{params[:url].inspect}")
    render json: { detail: "#{error.message}: send an x.com/twitter.com post or a YouTube video link" },
      status: :bad_request
  end

  def show
    video = TwitterVideo.find_by(id: params[:id])
    return render json: { detail: "Not found" }, status: :not_found unless video

    render json: video.slice(:id, :status, :error_detail, :html_filename, :youtube_id)
  end

  private
    def submitted_url
      text = params[:url].to_s.strip
      text[EMBEDDED_URL] || text
    end

    # A YouTube video already has auto-captions, so it skips the download and upload
    # and goes straight to the captions job.
    def create_from_youtube(youtube_id)
      source_url = YoutubeUrl.watch_url(youtube_id)
      video = TwitterVideo.where(source_url: source_url).order(id: :desc).first ||
        TwitterVideo.new(source_url: source_url)
      retrying = video.persisted?
      video.update!(status: "awaiting_captions", youtube_id: youtube_id, caption_attempts: 0,
                    upload_completed_at: Time.current, error_detail: nil)
      Rails.logger.info("[VideoSummariesController] ##{video.id} #{retrying ? 'retrying' : 'enqueued'} for #{source_url}")
      TwitterVideoCaptionsJob.perform_later(video.id)
      render json: { id: video.id, status: video.status }, status: :accepted
    end

    def authenticated?
      token = ENV["API_TOKEN"].to_s
      authorization = request.authorization.to_s
      expected = "Bearer #{token}"
      token.present? &&
        authorization.bytesize == expected.bytesize &&
        ActiveSupport::SecurityUtils.secure_compare(authorization, expected)
    end

    def render_unauthorized
      render json: { detail: "Unauthorized" }, status: :unauthorized
    end
end
