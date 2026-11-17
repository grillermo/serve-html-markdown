// Package web is the HTTP layer: the server, the route table and middleware.
package web

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"serve-html-markdown/internal/config"
)

// Server is the application's http.Handler. Later tasks add the dependencies
// their handlers need as fields set in New.
type Server struct {
	cfg     config.Config
	pool    *pgxpool.Pool
	handler http.Handler
}

// New builds a Server and its route table. It panics, via http.ServeMux, if
// two route patterns conflict.
func New(cfg config.Config, pool *pgxpool.Pool) *Server {
	s := &Server{cfg: cfg, pool: pool}
	s.handler = s.routes()
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// securityHeaders sets Strict-Transport-Security on every response, matching
// Rails' force_ssl with assume_ssl (TLS terminates at the Cloudflare tunnel).
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// logRequests logs one slog line per request.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration", time.Since(start),
		)
	})
}

// statusWriter records the response status. Unwrap lets
// http.NewResponseController reach the underlying writer (Flush, deadlines).
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}

// Flush implements http.Flusher.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
