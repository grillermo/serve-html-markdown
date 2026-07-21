class ExpansionProcessor
  include ResolvesServedFiles

  FILES_DIR = ResolvesServedFiles::FILES_DIR
  ALLOWED_EXTENSIONS = ResolvesServedFiles::ALLOWED_EXTENSIONS
  UnsupportedFile = ResolvesServedFiles::UnsupportedFile
  MissingFile = ResolvesServedFiles::MissingFile
  EXPANDER = ClaudeExpandService

  def self.process(expansion)
    new(expansion).process
  end

  def initialize(expansion)
    @expansion = expansion
  end

  def process
    file_path = resolve_file_path(@expansion.file_name)
    source = file_path.read(encoding: "UTF-8")
    html = EXPANDER.expand(
      file_name: file_path.basename.to_s,
      document: source,
      selection: @expansion.selected_text,
      question: @expansion.question,
      use_openai: @expansion.use_openai
    )

    with_source_lock(file_path) do
      latest_source = file_path.read(encoding: "UTF-8")
      expansion_path = unique_expansion_path(file_path)
      url = "/#{ERB::Util.url_encode(expansion_path.basename.to_s)}"
      rewritten = SelectionLinker.link(
        source: latest_source,
        extension: file_path.extname.downcase,
        selected_text: @expansion.selected_text,
        occurrence: @expansion.occurrence,
        url: url
      )

      expansion_path.write(html, encoding: "UTF-8")
      file_path.write(rewritten, encoding: "UTF-8")
      url
    end
  end

  private

  def with_source_lock(file_path)
    lock_path = self.class::FILES_DIR.join(".#{file_path.basename}.expansion.lock")
    File.open(lock_path, File::RDWR | File::CREAT, 0o600) do |lock_file|
      lock_file.flock(File::LOCK_EX)
      yield
    ensure
      lock_file.flock(File::LOCK_UN)
    end
  end

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
