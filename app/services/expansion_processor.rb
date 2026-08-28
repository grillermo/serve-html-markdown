class ExpansionProcessor
  include ResolvesServedFiles

  FILES_DIR = ResolvesServedFiles::FILES_DIR
  ALLOWED_EXTENSIONS = ResolvesServedFiles::ALLOWED_EXTENSIONS
  UnsupportedFile = ResolvesServedFiles::UnsupportedFile
  MissingFile = ResolvesServedFiles::MissingFile
  EXPANDER = ClaudeExpandService

  TruncatedRewrite = Class.new(StandardError)

  ANCHOR_SENTINEL = ClaudeExpandService::ANCHOR_SENTINEL
  ANCHOR_ID = "expansion-anchor"
  MIN_REWRITE_RATIO = 0.5

  def self.process(expansion)
    new(expansion).process
  end

  def initialize(expansion)
    @expansion = expansion
  end

  def process
    file_path = resolve_file_path(@expansion.file_name)
    source = file_path.read(encoding: "UTF-8")
    @expansion.stamp!(:source_read)

    if @expansion.edit_in_place?
      rewrite_in_place(file_path, source)
    else
      link_new_page(file_path, source)
    end
  end

  private

  def link_new_page(file_path, source)
    html = EXPANDER.expand(
      file_name: file_path.basename.to_s,
      document: source,
      selection: @expansion.selected_text,
      question: @expansion.question,
      use_openai: @expansion.use_openai,
      expansion: @expansion
    )

    with_source_lock(file_path) do
      @expansion.stamp!(:lock_acquired)
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
      @expansion.stamp!(:link_rewritten)

      expansion_path.write(html, encoding: "UTF-8")
      file_path.write(rewritten, encoding: "UTF-8")
      @expansion.stamp!(:files_written)
      ServedFile.record(expansion_path.basename.to_s)
      ServedFile.record_modification(file_path.basename.to_s)
      url
    end
  end

  def rewrite_in_place(file_path, source)
    rewritten = EXPANDER.rewrite(
      file_name: file_path.basename.to_s,
      document: source,
      selection: @expansion.selected_text,
      question: @expansion.question,
      use_openai: @expansion.use_openai,
      expansion: @expansion
    )

    if rewritten.length < source.length * MIN_REWRITE_RATIO
      raise TruncatedRewrite, "Rewrite looked truncated."
    end

    anchored = insert_anchor(rewritten)

    # Lock on the version family's base name (not the requested file_path's own
    # basename) so concurrent rewrites of foo.md, foo--v2.md, foo--v3.md, etc.
    # all serialize against the same lock file when allocating the next version.
    family_path = self.class::FILES_DIR.join(FileVersions.parse(file_path.basename.to_s).base_name)

    with_source_lock(family_path) do
      @expansion.stamp!(:lock_acquired)
      version_path = FileVersions.parse(file_path.basename.to_s).next_path(self.class::FILES_DIR)
      version_path.write(anchored, encoding: "UTF-8")
      @expansion.stamp!(:files_written)
      ServedFile.record(version_path.basename.to_s)
      version_url(version_path)
    end
  end

  # The model is asked for exactly one sentinel; keep the first and drop any
  # extras so the page can never carry a duplicate element id.
  def insert_anchor(document)
    return document unless document.include?(ANCHOR_SENTINEL)

    first, *rest = document.split(ANCHOR_SENTINEL)
    "#{first}<a id=\"#{ANCHOR_ID}\"></a>#{rest.join}"
  end

  # The fallback rides in the URL because the page reloads: the client cannot
  # keep it in memory across the navigation.
  def version_url(version_path)
    url = "/#{ERB::Util.url_encode(version_path.basename.to_s)}"
    if @expansion.fallback_anchor.present?
      url += "?fallback=#{ERB::Util.url_encode(@expansion.fallback_anchor)}"
    end
    "#{url}##{ANCHOR_ID}"
  end

  # Takes an exclusive lock keyed on lock_path's basename. Callers decide what
  # that key means: link_new_page locks on the actual source file_path (writes
  # to that one file must be serialized), while rewrite_in_place locks on the
  # version family's base path (version-number allocation must be serialized
  # across every member of the family, not just the requested filename).
  def with_source_lock(lock_path)
    lock_path = self.class::FILES_DIR.join(".#{lock_path.basename}.expansion.lock")
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
