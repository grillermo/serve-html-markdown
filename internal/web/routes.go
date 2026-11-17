package web

import (
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"slices"
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
	for _, name := range publicNames {
		mux.Handle("GET /"+name, s.publicFile(name))
	}
	mux.Handle("GET /assets/{name}", s.assets())

	mux.HandleFunc("GET /users/sign_in", s.signInForm)
	mux.HandleFunc("POST /users/sign_in", s.signIn)
	mux.HandleFunc("DELETE /users/sign_out", s.signOut)

	mux.HandleFunc("POST /file/new", s.createFile)                // bearer
	mux.HandleFunc("POST /file/upload", s.uploadFile)             // bearer
	mux.HandleFunc("POST /summarize-video", s.createVideoSummary) // bearer
	mux.HandleFunc("POST /twitter-video", s.createVideoSummary)   // legacy name for iOS Shortcuts
	// TODO(Tasks 8.3, 9.7): numericID lets ids larger than int64 through, so
	// the real {id} handlers must treat a strconv parse error as 404.
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

	// Outermost first: logging sees every response, including the 403s from
	// cross-origin protection, and those still carry HSTS.
	return s.logRequests(s.securityHeaders(s.crossOriginProtection().Handler(mux)))
}

// crossOriginProtection rejects browser-originated cross-origin unsafe
// requests. The app sits behind a Cloudflare tunnel, where the browser's
// Origin is https://<Host>, so that origin is trusted explicitly.
func (s *Server) crossOriginProtection() *http.CrossOriginProtection {
	cop := http.NewCrossOriginProtection()
	if s.cfg.Host != "" && s.cfg.Host != "localhost" {
		if err := cop.AddTrustedOrigin("https://" + s.cfg.Host); err != nil {
			panic(fmt.Sprintf("web: invalid HOST %q for trusted origin: %v", s.cfg.Host, err))
		}
	}
	return cop
}

// numericID answers 404 unless the {id} path value is all ASCII digits, like
// the Rails route constraint `id: /\d+/`. It wraps outside requireUser so a
// malformed id is a 404 for everyone, signed in or not.
//
// It only checks the characters: an id too large for int64 passes, so the
// real handlers must treat a parse error as 404 too.
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

// publicNames are the files served from web/public.
var publicNames = []string{"robots.txt", "icon.svg", "icon.png", "400.html", "404.html", "406-unsupported-browser.html", "422.html", "500.html"}

// publicFile serves one embedded file from web/public with a long-lived
// Cache-Control, like config.public_file_server.headers in production.rb.
// A name outside publicNames yields a handler that answers 404; a listed name
// whose embedded file is missing panics at startup. HEAD is supported.
func (s *Server) publicFile(name string) http.Handler {
	if !slices.Contains(publicNames, name) {
		return http.NotFoundHandler()
	}
	return servePublic(webfs.FS, name)
}

func servePublic(fsys fs.FS, name string) http.Handler {
	data, err := fs.ReadFile(fsys, "public/"+name)
	if err != nil {
		panic(fmt.Sprintf("web: embedded public file %q is missing: %v", name, err))
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
