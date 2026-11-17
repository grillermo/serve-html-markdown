package web

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
)

// templateFuncs are available to every page template.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		// asset maps a stylesheet or script name to its URL. For now it is the
		// plain /assets/<name> path; Task 4.4 replaces it with digested paths.
		"asset": func(name string) string { return "/assets/" + name },
	}
}

// parsePages parses each page (templates/<page>.html) together with the shared
// templates/layout.html into its own template set, so two pages may both
// define "content" and "title". It panics if a file is missing or malformed,
// which a test catches: the files are embedded.
func parsePages(fsys fs.FS, pages ...string) map[string]*template.Template {
	set := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		t, err := template.New(page+".html").Funcs(templateFuncs()).
			ParseFS(fsys, "templates/layout.html", "templates/"+page+".html")
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}
