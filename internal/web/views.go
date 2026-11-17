package web

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"

	_ "time/tzdata" // America/Mexico_City must resolve on a host without system tzdata

	"serve-html-markdown/internal/store"
	webfs "serve-html-markdown/web"
)

// requiredAssets are the logical names the shipped templates use; New panics
// if one is not embedded.
var requiredAssets = []string{"application.css", "markdown.css", "expand.js"}

// FileVersionEntry is one element of window.__fileVersions, read by expand.js.
// The JSON field names are the contract with the script.
type FileVersionEntry struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Version int    `json:"version"`
	Current bool   `json:"current"`
}

// NewFileVersionEntry builds the entry for name: its url is "/" plus the
// ERB::Util.url_encode of the name, as in FilesController#file_versions_for.
func NewFileVersionEntry(name string, version int, current bool) FileVersionEntry {
	return FileVersionEntry{Name: name, URL: "/" + URLEncode(name), Version: version, Current: current}
}

// ExpandBootstrap is the data of the script tags expand.js needs, whichever
// way the document is served (the markdown layout, or string injection into a
// raw .html file).
type ExpandBootstrap struct {
	// ScrollAnchor is the saved scroll anchor; empty omits window.__scrollAnchor.
	ScrollAnchor string
	// FileVersions lists the document's versions; nil is sent as [].
	FileVersions  []FileVersionEntry
	ExpansionMode string
	// ExpandJS is the digested URL path of expand.js (Server.AssetPath).
	ExpandJS string
}

var expandBootstrapTemplate = sync.OnceValue(func() *template.Template {
	return template.Must(template.New("expand_bootstrap.html").
		ParseFS(webfs.FS, "templates/expand_bootstrap.html"))
})

// ExpandBootstrapHTML renders the bootstrap <script> tags as one string.
// It needs no Server, so the handler can splice it into a raw HTML document
// before </body>. Values are JSON-encoded with "<", ">", "&" and "'" escaped,
// so no value can close the script element.
func ExpandBootstrapHTML(b ExpandBootstrap) (template.HTML, error) {
	if b.FileVersions == nil {
		b.FileVersions = []FileVersionEntry{} // marshals as [], not null
	}
	var buf bytes.Buffer
	if err := expandBootstrapTemplate().ExecuteTemplate(&buf, "expand_bootstrap", b); err != nil {
		return "", fmt.Errorf("render expand bootstrap: %w", err)
	}
	return template.HTML(buf.String()), nil
}

// NewExpandBootstrap fills ExpandJS with the digested path of expand.js.
func (s *Server) NewExpandBootstrap(scrollAnchor string, versions []FileVersionEntry, mode string) ExpandBootstrap {
	return ExpandBootstrap{ScrollAnchor: scrollAnchor, FileVersions: versions, ExpansionMode: mode, ExpandJS: s.AssetPath("expand.js")}
}

// URLEncode is Ruby's ERB::Util.url_encode: everything except the RFC 3986
// unreserved characters (letters, digits, "-", ".", "_", "~") becomes %XX of
// its UTF-8 bytes. net/url's PathEscape would leave "$&+,:;=@" alone.
func URLEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

type markdownPage struct {
	Title     string
	Rendered  template.HTML
	Bootstrap ExpandBootstrap
}

// renderMarkdownPage writes the markdown layout (layouts/markdown.html.erb):
// title is the file name, renderedHTML the trusted output of the markdown
// renderer, which is inserted unescaped, like @rendered.html_safe.
func (s *Server) renderMarkdownPage(w http.ResponseWriter, title, renderedHTML string, bootstrap ExpandBootstrap) {
	if bootstrap.FileVersions == nil {
		bootstrap.FileVersions = []FileVersionEntry{}
	}
	s.render(w, http.StatusOK, "markdown", markdownPage{
		Title:     title,
		Rendered:  template.HTML(renderedHTML),
		Bootstrap: bootstrap,
	})
}

type indexRow struct {
	Name      string
	URL       string
	UpdatedAt string
}

type indexPage struct{ Files []indexRow }

// indexTimeFormat is Ruby's "%b %-d, %Y %-I:%M %p".
const indexTimeFormat = "Jan 2, 2006 3:04 PM"

// renderIndex writes the file list (files/index.html.erb) in the order given
// (the handler passes newest update first), with each updated_at in
// America/Mexico_City.
func (s *Server) renderIndex(w http.ResponseWriter, files []store.ServedFile) {
	rows := make([]indexRow, len(files))
	for i, f := range files {
		rows[i] = indexRow{
			Name:      f.Name,
			URL:       "/" + URLEncode(f.Name),
			UpdatedAt: f.UpdatedAt.In(s.mexicoCity).Format(indexTimeFormat),
		}
	}
	s.render(w, http.StatusOK, "index", indexPage{Files: rows})
}
