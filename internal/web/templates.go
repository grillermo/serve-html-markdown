package web

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
)

// templateFuncs are available to every page template.
func (s *Server) templateFuncs() template.FuncMap {
	return template.FuncMap{
		// asset maps a logical asset name to its digested URL path.
		"asset": s.AssetPath,
	}
}

// sharedTemplates are parsed into every page's template set.
var sharedTemplates = []string{"templates/layout.html", "templates/expand_bootstrap.html"}

// parsePages parses each page (templates/<page>.html) together with the shared
// templates (layout.html, expand_bootstrap.html) into its own template set, so
// two pages may both define "content" and "title". It panics if a file is
// missing or malformed, which a test catches: the files are embedded.
func parsePages(fsys fs.FS, funcs template.FuncMap, pages ...string) map[string]*template.Template {
	set := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		files := append(slices.Clone(sharedTemplates), "templates/"+page+".html")
		t, err := template.New(page+".html").Funcs(funcs).ParseFS(fsys, files...)
		if err != nil {
			panic(fmt.Sprintf("web: parse template %q: %v", page, err))
		}
		set[page] = t
	}
	return set
}

// render executes the page into a buffer first, so a template error becomes a
// clean 500 instead of a half-written response.
func (s *Server) render(w http.ResponseWriter, status int, page string, data any) {
	t := s.pages[page]
	if t == nil {
		panic(fmt.Sprintf("web: unknown page template %q", page))
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, page+".html", data); err != nil {
		slog.Error("render", "page", page, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// Every rendered page gets Rails' default unless its handler chose another
	// (the sign-in page uses no-store). The server middleware applies the same
	// default to all dynamic responses; doing it here keeps pages correct when
	// rendered outside it.
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", defaultCacheControl)
	}
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}
