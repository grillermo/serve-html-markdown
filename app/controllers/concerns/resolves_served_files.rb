module ResolvesServedFiles
  extend ActiveSupport::Concern

  FILES_DIR = Rails.root.join("files").expand_path
  ALLOWED_EXTENSIONS = %w[.html .md .markdown].freeze

  UnsupportedFile = Class.new(StandardError)
  MissingFile = Class.new(StandardError)

  FILES_DIR.mkpath

  private
    def resolve_file_path(file_name)
      files_dir = self.class::FILES_DIR
      file_path = files_dir.join(file_name).expand_path
      root_prefix = "#{files_dir}#{File::SEPARATOR}"

      unless file_path.to_s.start_with?(root_prefix)
        raise ActionController::BadRequest, "Invalid file path."
      end

      unless self.class::ALLOWED_EXTENSIONS.include?(file_path.extname.downcase)
        raise self.class::UnsupportedFile, "Only .html, .md, and .markdown files are supported."
      end

      raise self.class::MissingFile, "File not found: #{file_name}" unless file_path.file?

      resolved_path = file_path.realpath
      resolved_root_prefix = "#{files_dir.realpath}#{File::SEPARATOR}"
      unless resolved_path.to_s.start_with?(resolved_root_prefix)
        raise ActionController::BadRequest, "Invalid file path."
      end

      resolved_path
    end
end
