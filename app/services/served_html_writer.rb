require "cgi"
require "pathname"

class ServedHtmlWriter
  DEFAULT_SLUG = "summary".freeze
  # The dark reader theme from ../ipad-send (public/reader.css, html.dark + .md accents).
  STYLESHEET = <<~CSS.freeze
    html, body { margin: 0; padding: 0; }
    body { background: #111; color: #ddd; font-family: Georgia, "Times New Roman", serif; line-height: 1.6;
      -webkit-text-size-adjust: 100%; }
    main { max-width: 44em; margin: 0 auto; padding: 24px 24px 104px; font-size: 20px; }
    a { color: #8ab4f8; }
    img { max-width: 100%; height: auto; }
    h1, h2, h3, h4 { font-family: "Helvetica Neue", Helvetica, Arial, sans-serif; line-height: 1.25; color: #fff; }
    h1 { font-size: 1.6em; margin: 1.6em 0 0.6em; padding-bottom: 0.2em; border-bottom: 3px solid #f0b35a; }
    h2 { font-size: 1.35em; margin: 2.2em 0 0.8em; padding: 0.35em 0 0.35em 0.6em;
      border-left: 6px solid #f0b35a; background: rgba(128, 128, 128, 0.12); }
    h3 { font-size: 1.12em; margin: 1.8em 0 0.5em; padding-bottom: 0.15em; color: #8fd3c7;
      border-bottom: 1px dotted rgba(143, 211, 199, 0.5); }
    h4 { font-size: 0.8em; margin: 1.6em 0 0.4em; text-transform: uppercase; letter-spacing: 0.08em; color: #c8c8c8; }
    p, ul, ol, pre, table { margin: 0 0 1em; }
    li { margin-bottom: 0.35em; }
    strong { color: #fff; }
    em { color: #e8d6b0; }
    code { font-family: "Courier New", Courier, monospace; font-size: 0.9em; padding: 0.1em 0.3em; border-radius: 3px;
      background: rgba(128, 128, 128, 0.18); color: #f2c48d; }
    blockquote { margin: 0 0 1em; padding: 0.6em 1em; font-style: italic;
      border-left: 4px solid rgba(128, 128, 128, 0.5); background: rgba(128, 128, 128, 0.08); }
    blockquote p:last-child { margin-bottom: 0; }
    hr { height: 0; margin: 2.2em 25%; border: 0; border-top: 3px double rgba(128, 128, 128, 0.5); }
  CSS

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
      <style>
      #{STYLESHEET}</style>
      </head>
      <body>
      <main>
      <h1>#{CGI.escapeHTML(title)}</h1>
      #{summary_html}
      </main>
      </body>
      </html>
    HTML
  end
end
