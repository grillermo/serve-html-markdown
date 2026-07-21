class ExpansionsController < ApplicationController
  rescue_from ActiveRecord::RecordNotFound do
    render json: { detail: "Expansion not found." }, status: :not_found
  end

  def create
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
    GenerateExpansionJob.perform_later(expansion.id)

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
