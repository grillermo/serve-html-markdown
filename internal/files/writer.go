package files

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"

	"serve-html-markdown/internal/store"
)

// DefaultSlug is the file name stem of a title with no ASCII letters or digits.
const DefaultSlug = "summary"

// maxSlugBytes caps the stem of a summary file name. Ruby let a longer title
// fail with Errno::ENAMETOOLONG at 255 bytes; this keeps room for ".html" and
// a "-9999" suffix instead.
const maxSlugBytes = 240

// SummaryStylesheet is the dark reader theme from ../ipad-send (public/reader.css,
// html.dark + .md accents), byte for byte STYLESHEET of
// app/services/served_html_writer.rb (see writer_test.go).
const SummaryStylesheet = `html, body { margin: 0; padding: 0; }
body { background: #111; color: #ddd; font-family: Georgia, "Times New Roman", serif; line-height: 1.6;
  -webkit-text-size-adjust: 100%; }
main { max-width: 88em; margin: 0 auto; padding: 24px 24px 104px; font-size: 20px; }
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
`

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify turns a title into a file name stem: lower-cased, every run of
// characters outside a-z0-9 (accents and other scripts included) becomes one
// "-", leading and trailing "-" are dropped, and an empty result is
// DefaultSlug. Like Ruby's downcase, the capital dotted I lower-cases to "i"
// plus a combining dot, which becomes a "-".
func Slugify(title string) string {
	lower := strings.ToLower(strings.ReplaceAll(title, "İ", "i̇"))
	slug := strings.Trim(nonSlug.ReplaceAllString(lower, "-"), "-")
	if len(slug) > maxSlugBytes {
		slug = strings.Trim(slug[:maxSlugBytes], "-")
	}
	if slug == "" {
		return DefaultSlug
	}
	return slug
}

// escapeHTML is CGI.escapeHTML: & < > " ' become &amp; &lt; &gt; &quot; &#39;.
var escapeHTML = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;").Replace

// SummaryDocument is the page written for a summary: the title escaped in
// <title> and <h1>, summaryHTML (a fragment the caller vouches for) inserted
// as it is, the stylesheet inline. It is byte for byte ServedHtmlWriter.document.
func SummaryDocument(title, summaryHTML string) string {
	return "<!doctype html>\n" +
		"<html lang=\"en\">\n" +
		"<head>\n" +
		"<meta charset=\"utf-8\">\n" +
		"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n" +
		"<title>" + escapeHTML(title) + "</title>\n" +
		"<style>\n" +
		SummaryStylesheet + "</style>\n" +
		"</head>\n" +
		"<body>\n" +
		"<main>\n" +
		"<h1>" + escapeHTML(title) + "</h1>\n" +
		summaryHTML + "\n" +
		"</main>\n" +
		"</body>\n" +
		"</html>\n"
}

// HTMLWriter writes a summary as a new served page: the port of
// ServedHtmlWriter.write.
type HTMLWriter struct {
	// Dir is the files directory (config FilesDir); it is created when missing.
	Dir string
	// DB records the new file in served_files.
	DB store.DB
}

// Write writes the page for title and summaryHTML to Dir as <slug>.html, or
// <slug>-1.html, <slug>-2.html, ... when the name is taken (a name is only ever
// claimed with an exclusive create, so nothing is overwritten even when two
// writers race), records it in served_files and returns its name.
//
// When only the recording fails, the page already exists and stays (the next
// sync picks it up): the name is returned TOGETHER WITH the error, so a caller
// that wants to carry on (serve or link the page) can use the name, and one that
// does not must not write again, which would make a second page.
func (w *HTMLWriter) Write(ctx context.Context, title, summaryHTML string) (string, error) {
	if err := os.MkdirAll(w.Dir, 0o777); err != nil {
		return "", err
	}
	data := []byte(SummaryDocument(title, summaryHTML))
	stem := Slugify(title)
	for n := 0; n < maxAttempts; n++ {
		suffix := ""
		if n > 0 {
			suffix = "-" + strconv.Itoa(n)
		}
		name := stem + suffix + ".html"
		err := CreateNew(w.Dir, name, data)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if err := store.RecordServedFile(ctx, w.DB, name); err != nil {
			return name, err
		}
		return name, nil
	}
	return "", errNoFreeName(stem)
}
