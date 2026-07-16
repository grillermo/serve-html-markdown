class ExpansionsController < ApplicationController
  include ResolvesServedFiles

  EXPANDER = ClaudeExpandService

  rescue_from ActionController::BadRequest do |error|
    render json: { detail: error.message }, status: :bad_request
  end
  rescue_from UnsupportedFile, MissingFile do |error|
    render json: { detail: error.message }, status: :not_found
  end
  rescue_from SelectionLinker::Error do |error|
    render json: { detail: error.message }, status: :unprocessable_entity
  end
  rescue_from ClaudeExpandService::Error do |error|
    Rails.logger.error("Expansion generation failed (#{error.class}): #{error.message}")
    render json: { detail: "Generation failed." }, status: :bad_gateway
  end

  def create
    file_name = params[:file_name].to_s
    selected_text = params[:selected_text].to_s
    question = params[:question].to_s
    if file_name.blank? || selected_text.blank? || question.blank?
      raise ActionController::BadRequest, "Missing file_name, selected_text, or question."
    end

    file_path = resolve_file_path(file_name)
    source = file_path.read(encoding: "UTF-8")
    expansion_path = unique_expansion_path(file_path)
    url = "/#{ERB::Util.url_encode(expansion_path.basename.to_s)}"

    rewritten = SelectionLinker.link(
      source: source,
      extension: file_path.extname.downcase,
      selected_text: selected_text,
      occurrence: params[:occurrence].to_i,
      url: url
    )

    html = EXPANDER.expand(
      file_name: file_path.basename.to_s,
      document: source,
      selection: selected_text,
      question: question,
      use_openai: ActiveModel::Type::Boolean.new.cast(params[:use_openai]) || false
    )

    expansion_path.write(html, encoding: "UTF-8")
    file_path.write(rewritten, encoding: "UTF-8")

    render json: { url: url }
  end

  private
    def unique_expansion_path(file_path)
      stem = file_path.basename(file_path.extname).to_s
      counter = 1
      loop do
        candidate = self.class::FILES_DIR.join("#{stem}--expand-#{counter}.html")
        return candidate unless candidate.exist?

        counter += 1
      end
    end
end
