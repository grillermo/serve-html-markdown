require "cgi"
require "pathname"

class ServedHtmlWriter
  DEFAULT_SLUG = "summary".freeze

  def self.write(title:, summary_html:, files_dir: ResolvesServedFiles::FILES_DIR)
    files_dir = Pathname.new(files_dir)
    files_dir.mkpath
    path = unique_path(files_dir, slugify(title))
    path.write(document(title, summary_html), encoding: "UTF-8")
    ServedFile.record(path.basename.to_s)
    path.basename.to_s
  end

  def self.slugify(title)
    slug = title.to_s.downcase.gsub(/[^a-z0-9]+/, "-").gsub(/\A-+|-+\z/, "")
    slug.presence || DEFAULT_SLUG
  end

  def self.unique_path(files_dir, stem)
    counter = 0
    loop do
      suffix = counter.zero? ? "" : "-#{counter}"
      candidate = files_dir.join("#{stem}#{suffix}.html")
      return candidate unless candidate.exist?

      counter += 1
    end
  end

  def self.document(title, summary_html)
    <<~HTML
      <!doctype html>
      <html lang="en">
      <head>
      <meta charset="utf-8">
      <meta name="viewport" content="width=device-width, initial-scale=1">
      <title>#{CGI.escapeHTML(title)}</title>
      </head>
      <body>
      <h1>#{CGI.escapeHTML(title)}</h1>
      #{summary_html}
      </body>
      </html>
    HTML
  end
end
