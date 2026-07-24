class TwitterVideosController < ApplicationController
  skip_forgery_protection only: :create
  skip_before_action :authenticate_user!, only: [:create, :show], raise: false

  def create
    return render_unauthorized unless authenticated?

    source_url = TwitterUrl.normalize(params[:url])
    video = TwitterVideo.create!(source_url: source_url, status: "downloading")
    TwitterVideoIngestJob.perform_later(video.id)
    render json: { id: video.id, status: video.status }, status: :accepted
  rescue TwitterUrl::InvalidError => error
    render json: { detail: error.message }, status: :bad_request
  end

  def show
    video = TwitterVideo.find_by(id: params[:id])
    return render json: { detail: "Not found" }, status: :not_found unless video

    render json: video.slice(:id, :status, :error_detail, :html_filename, :youtube_id)
  end

  private
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
