class FilesController < ApplicationController
  include ResolvesServedFiles

  MARKDOWN_OPTIONS = {
    render: { unsafe: true },
    extension: { autolink: true, header_ids: "" },
    parse: { smart: true }
  }.freeze
  FORMATTER = GeminiFormatter
  UPLOAD_EXTENSIONS = {
    ".html" => ".html",
    ".htm" => ".html",
    ".md" => ".md",
    ".markdown" => ".md"
  }.freeze

  skip_forgery_protection only: [:create, :upload]
  skip_before_action :authenticate_user!, only: [:create, :upload]

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
    @scroll_position = current_user.scroll_positions.find_by(file_name: file_path.basename.to_s)&.anchor

    if file_path.extname.downcase == ".html"
      render html: inject_expand_script(content).html_safe, layout: false
    else
      @file_name = file_path.basename.to_s
      @rendered = Commonmarker.to_html(content, options: MARKDOWN_OPTIONS)
      render :show, formats: :html, layout: "markdown"
    end
  end

  def index
    @served_files = ServedFile.order(updated_at: :desc)
  end

  def last
    latest = ServedFile.newest

    if latest
      redirect_to "/#{ERB::Util.url_encode(latest.name)}", status: :found
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
    ServedFile.record(file_path.basename.to_s)

    render json: { url: public_url(file_path) }
  end

  def upload
    unless authenticated?
      return render json: { detail: "Unauthorized" }, status: :unauthorized
    end

    file = params[:file]
    raise ActionController::BadRequest, "Missing file." unless file.respond_to?(:read)

    filename = params[:filename].presence || file.try(:original_filename)
    file_path = unique_file_path(filename, extension: upload_extension(filename))
    file_path.write(utf8_contents(file), encoding: "UTF-8")
    ServedFile.record(file_path.basename.to_s)

    render json: { url: public_url(file_path) }
  end

  private
    def public_url(file_path)
      host = ENV.fetch("HOST", "localhost")
      "https://#{host}/#{file_path.basename}"
    end

    def upload_extension(filename)
      extension = File.extname(File.basename(filename.to_s.tr("\\", "/"))).downcase
      UPLOAD_EXTENSIONS.fetch(extension) do
        raise ActionController::BadRequest, "Only .html, .md, and .markdown files are supported."
      end
    end

    def utf8_contents(file)
      contents = file.read.to_s.dup.force_encoding(Encoding::UTF_8)
      raise ActionController::BadRequest, "File must be UTF-8 text." unless contents.valid_encoding?

      contents
    end

    def inject_expand_script(content)
      scroll_position_script = if @scroll_position
        %(<script>window.__scrollAnchor = #{@scroll_position.to_json};</script>)
      else
        ""
      end
      snippet = %(<meta name="csrf-token" content="#{form_authenticity_token}">#{scroll_position_script}<script src="#{helpers.asset_path("expand.js")}" defer></script>)
      if content =~ %r{</body>}i
        content.sub(%r{</body>}i) { "#{snippet}</body>" }
      else
        content + snippet
      end
    end

    def authenticated?
      token = ENV["API_TOKEN"].to_s
      authorization = request.authorization.to_s
      expected = "Bearer #{token}"

      token.present? &&
        authorization.bytesize == expected.bytesize &&
        ActiveSupport::SecurityUtils.secure_compare(authorization, expected)
    end

    def unique_file_path(filename, extension: ".md")
      normalized = filename.to_s.tr("\\", "/")
      basename = File.basename(normalized)
      stem = File.basename(basename, File.extname(basename))
      raise ActionController::BadRequest, "Invalid filename." if stem.blank? || %w[. ..].include?(stem)

      counter = 0
      loop do
        suffix = counter.zero? ? "" : "-#{counter}"
        candidate = FILES_DIR.join("#{stem}#{suffix}#{extension}").expand_path
        root_prefix = "#{FILES_DIR}#{File::SEPARATOR}"
        raise ActionController::BadRequest, "Invalid filename." unless candidate.to_s.start_with?(root_prefix)

        return candidate unless candidate.exist?

        counter += 1
      end
    end
end
