package web

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"time"

	webfs "serve-html-markdown/web"
)

// routes builds the full route table. Patterns for handlers that are not
// ported yet already live here (their handlers are 501 stubs in stubs.go), so
// precedence between routes is decided in one place:
// exact paths beat /{file_name}, and /{$} matches only the root.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("HEAD /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	for _, name := range []string{"robots.txt", "icon.svg", "icon.png", "400.html", "404.html", "406-unsupported-browser.html", "422.html", "500.html"} {
		mux.Handle("GET /"+name, s.publicFile(name))
	}
	mux.Handle("GET /assets/{name}", s.assets())

	mux.HandleFunc("GET /users/sign_in", s.signInForm)
	mux.HandleFunc("POST /users/sign_in", s.signIn)
	mux.HandleFunc("DELETE /users/sign_out", s.signOut)

	mux.HandleFunc("POST /file/new", s.createFile)                                           // bearer
	mux.HandleFunc("POST /file/upload", s.uploadFile)                                        // bearer
	mux.HandleFunc("POST /summarize-video", s.createVideoSummary)                            // bearer
	mux.HandleFunc("POST /twitter-video", s.createVideoSummary)                              // legacy name for iOS Shortcuts
	mux.Handle("GET /summarize-video/{id}", numericID(http.HandlerFunc(s.showVideoSummary))) // public, like Rails
	mux.Handle("GET /twitter-video/{id}", numericID(http.HandlerFunc(s.showVideoSummary)))

	mux.Handle("GET /youtube/reauth", s.requireUser(s.youtubeReauth))
	mux.Handle("GET /youtube/callback", s.requireUser(s.youtubeCallback))
	mux.Handle("GET /youtube/debug", s.requireUser(s.youtubeDebug))
	mux.Handle("POST /expansions", s.requireUser(s.createExpansion))
	mux.Handle("GET /expansions/{id}", numericID(s.requireUser(s.showExpansion)))
	mux.Handle("PATCH /scroll_position", s.requireUser(s.updateScrollPosition))
	mux.Handle("POST /scroll_position", s.requireUser(s.updateScrollPosition))

	mux.Handle("GET /{$}", s.requireUser(s.lastFile))
	mux.Handle("GET /last", s.requireUser(s.lastFile))
	mux.Handle("GET /index", s.requireUser(s.indexFiles))
	mux.Handle("GET /{file_name}", s.requireUser(s.showFile))

	return s.securityHeaders(http.NewCrossOriginProtection().Handler(s.logRequests(mux)))
}

// numericID answers 404 unless the {id} path value is all ASCII digits, like
// the Rails route constraint `id: /\d+/`. It wraps outside requireUser so a
// malformed id is a 404 for everyone, signed in or not.
func numericID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.NotFound(w, r)
			return
		}
		for i := 0; i < len(id); i++ {
			if id[i] < '0' || id[i] > '9' {
				http.NotFound(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

var publicContentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".txt":  "text/plain; charset=utf-8",
	".svg":  "image/svg+xml",
	".png":  "image/png",
}

// publicFile serves one embedded file from web/public with a long-lived
// Cache-Control, like config.public_file_server.headers in production.rb.
// An unknown name yields a handler that answers 404. HEAD is supported.
func (s *Server) publicFile(name string) http.Handler {
	data, err := fs.ReadFile(webfs.FS, "public/"+name)
	if err != nil {
		return http.NotFoundHandler()
	}
	ctype := publicContentTypes[path.Ext(name)]
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "public, max-age=31536000")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	})
}
