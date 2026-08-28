class ScrollPositionsController < ApplicationController
  rescue_from ActionController::BadRequest do |error|
    render json: { detail: error.message }, status: :bad_request
  end
  rescue_from ActiveRecord::RecordInvalid do |error|
    render json: { detail: error.record.errors.full_messages.to_sentence }, status: :unprocessable_entity
  end

  def update
    file_name = params[:file_name].to_s
    anchor = params[:anchor].to_s
    raise ActionController::BadRequest, "Missing file_name." if file_name.blank?
    raise ActionController::BadRequest, "Missing anchor." if anchor.blank?

    record = current_user.scroll_positions.find_or_initialize_by(file_name: file_name)
    record.update!(anchor: anchor)
    head :no_content
  end
end
