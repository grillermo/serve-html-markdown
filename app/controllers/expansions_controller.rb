class ExpansionsController < ApplicationController
  rescue_from ActiveRecord::RecordNotFound do
    render json: { detail: "Expansion not found." }, status: :not_found
  end

  def create
    request_received_ms = Expansion.now_ms
    file_name = params[:file_name].to_s
    selected_text = params[:selected_text].to_s
    question = params[:question].to_s
    if file_name.blank? || selected_text.blank? || question.blank?
      render json: { detail: "Missing file_name, selected_text, or question." }, status: :bad_request
      return
    end

    expansion = current_user.expansions.create!(
      file_name: file_name,
      selected_text: selected_text,
      occurrence: [params[:occurrence].to_i, 0].max,
      question: question,
      use_openai: ActiveModel::Type::Boolean.new.cast(params[:use_openai]) || false
    )

    client_clicked_ms = params[:client_clicked_at].presence&.to_i
    expansion.stamp!(:client_clicked, client_clicked_ms) if client_clicked_ms
    expansion.stamp!(:request_received, request_received_ms)

    GenerateExpansionJob.perform_later(expansion.id)
    expansion.stamp!(:job_enqueued)

    render json: status_payload(expansion), status: :accepted
  end

  def show
    render json: status_payload(current_user.expansions.find(params[:id]))
  end

  private

  def status_payload(expansion)
    { id: expansion.id, status: expansion.status }.tap do |payload|
      payload[:url] = expansion.url if expansion.status == "completed"
      payload[:detail] = expansion.error_detail if expansion.status == "failed"
    end
  end
end
