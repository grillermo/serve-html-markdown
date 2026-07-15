class FilesController < ApplicationController
  FILES_DIR = Rails.root.join("files").expand_path
  ALLOWED_EXTENSIONS = %w[.html .md .markdown].freeze
  MARKDOWN_OPTIONS = {
    render: { unsafe: true },
    extension: { autolink: true },
    parse: { smart: true }
  }.freeze
  FORMATTER = GeminiFormatter

  UnsupportedFile = Class.new(StandardError)
  MissingFile = Class.new(StandardError)

  FILES_DIR.mkpath

  skip_forgery_protection only: :create
  skip_before_action :authenticate_user!, only: :create

  rescue_from ActionController::BadRequest do |error|
    render json: { detail: error.message }, status: :bad_request
  end
  rescue_from UnsupportedFile do |error|
    render json: { detail: error.message }, status: :not_found
  end
  rescue_from MissingFile do |error|
    render json: { detail: error.message }, status: :not_found
  end
  rescue_from GeminiFormatter::Error do |error|
    Rails.logger.error("Gemini formatting failed (#{error.class})")
    render json: { detail: "Gemini formatting failed." }, status: :bad_gateway
  end

  def show
    file_path = resolve_file_path(params[:file_name])
    content = file_path.read(encoding: "UTF-8")

    if file_path.extname.downcase == ".html"
      render html: content.html_safe, layout: false
    else
      @file_name = file_path.basename.to_s
      @rendered = Commonmarker.to_html(content, options: MARKDOWN_OPTIONS)
      render :show, formats: :html, layout: "markdown"
    end
  end

  def last
    latest = FILES_DIR.children
      .select { |path| path.file? && ALLOWED_EXTENSIONS.include?(path.extname.downcase) }
      .max_by(&:mtime)

    if latest
      redirect_to "/#{ERB::Util.url_encode(latest.basename.to_s)}", status: :found
    else
      render json: { detail: "No files found." }, status: :not_found
    end
  end

  def create
    unless authenticated?
      return render json: { detail: "Unauthorized" }, status: :unauthorized
    end

    content = params[:content]
    raise ActionController::BadRequest, "Missing content." if content.nil?

    file_path = unique_file_path(params[:filename])
    formatted = FORMATTER.format(content)
    file_path.write(formatted, encoding: "UTF-8")

    host = ENV.fetch("HOST", "localhost")
    render json: { url: "https://#{host}/#{file_path.basename}" }
  end

  private
    def resolve_file_path(file_name)
      file_path = FILES_DIR.join(file_name).expand_path
      root_prefix = "#{FILES_DIR}#{File::SEPARATOR}"

      unless file_path.to_s.start_with?(root_prefix)
        raise ActionController::BadRequest, "Invalid file path."
      end

      unless ALLOWED_EXTENSIONS.include?(file_path.extname.downcase)
        raise UnsupportedFile, "Only .html, .md, and .markdown files are supported."
      end

      raise MissingFile, "File not found: #{file_name}" unless file_path.file?

      resolved_path = file_path.realpath
      resolved_root_prefix = "#{FILES_DIR.realpath}#{File::SEPARATOR}"
      unless resolved_path.to_s.start_with?(resolved_root_prefix)
        raise ActionController::BadRequest, "Invalid file path."
      end

      resolved_path
    end

    def authenticated?
      token = ENV["API_TOKEN"].to_s
      authorization = request.authorization.to_s
      expected = "Bearer #{token}"

      token.present? &&
        authorization.bytesize == expected.bytesize &&
        ActiveSupport::SecurityUtils.secure_compare(authorization, expected)
    end

    def unique_file_path(filename)
      normalized = filename.to_s.tr("\\", "/")
      basename = File.basename(normalized)
      stem = File.basename(basename, File.extname(basename))
      raise ActionController::BadRequest, "Invalid filename." if stem.blank? || %w[. ..].include?(stem)

      counter = 0
      loop do
        suffix = counter.zero? ? "" : "-#{counter}"
        candidate = FILES_DIR.join("#{stem}#{suffix}.md").expand_path
        root_prefix = "#{FILES_DIR}#{File::SEPARATOR}"
        raise ActionController::BadRequest, "Invalid filename." unless candidate.to_s.start_with?(root_prefix)

        return candidate unless candidate.exist?

        counter += 1
      end
    end
end
