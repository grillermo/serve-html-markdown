# Owns the `--v<N>` filename convention that groups a document with its
# rewritten versions. `foo.md` is v1; rewrites are written as `foo--v2.md`,
# `foo--v3.md`. Nothing else in the app should hardcode this suffix.
class FileVersions
  # Strict: only v2 and up are real versions, and no leading zeros. A file
  # literally named `foo--v1.md` is its own document, not a version of `foo.md`.
  VERSION_PATTERN = /\A(?<stem>.+?)--v(?<version>[2-9]|[1-9]\d+)\z/

  # Loose: what user-supplied filenames are forbidden from ending in, so nothing
  # posted can land in a version slot or look like one.
  RESERVED_SUFFIX = /--v\d+\z/

  FIRST_VERSION = 2

  attr_reader :stem, :version, :extension

  def self.parse(basename)
    name = File.basename(basename.to_s)
    extension = File.extname(name)
    full_stem = File.basename(name, extension)

    if (match = VERSION_PATTERN.match(full_stem))
      new(match[:stem], match[:version].to_i, extension)
    else
      new(full_stem, 1, extension)
    end
  end

  def initialize(stem, version, extension)
    @stem = stem
    @version = version
    @extension = extension
  end

  def base_name
    "#{stem}#{extension}"
  end

  def name_for(version)
    version <= 1 ? base_name : "#{stem}--v#{version}#{extension}"
  end

  def family_names
    ServedFile
      .where(name: base_name)
      .or(ServedFile.where("name LIKE ? ESCAPE '\\'", "#{escaped_stem}--v%#{extension}"))
      .pluck(:name)
      .select { |name| self.class.parse(name).stem == stem }
      .sort_by { |name| self.class.parse(name).version }
  end

  def next_path(files_dir)
    candidate_version = FIRST_VERSION
    loop do
      candidate = Pathname.new(files_dir).join(name_for(candidate_version))
      return candidate unless candidate.exist?

      candidate_version += 1
    end
  end

  private
    def escaped_stem
      stem.gsub(/[\\%_]/) { |char| "\\#{char}" }
    end
end
