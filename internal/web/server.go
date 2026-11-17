// Package web is the HTTP layer: the server, the route table and middleware.
package web

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/config"
	webfs "serve-html-markdown/web"
)

// Server is the application's http.Handler.
type Server struct {
	cfg  config.Config
	pool *pgxpool.Pool
	// Clock returns the current time; WithClock replaces it in tests.
	Clock   func() time.Time
	handler http.Handler
	// codec signs the session cookie (purpose "session"); it reads the time
	// from Clock, so tests that move the clock also move cookie expiry.
	codec auth.Codec
	// pages holds the parsed page templates by name (see parsePages).
	pages map[string]*template.Template
	// digests maps asset names to digested paths and files (see newAssetTable).
	digests *assetTable
	// mexicoCity is the zone the index page shows timestamps in.
	mexicoCity *time.Location
	// renderConcurrency is how many Markdown renders may run at once; renderSlots
	// is its semaphore (built in New, after the options).
	renderConcurrency int
	renderSlots       chan struct{}
	// maxRenderBytes and maxHTMLBytes cap the size of a file shown as Markdown or
	// as HTML (see MaxRenderBytes, MaxHTMLBytes); tests lower them.
	maxRenderBytes, maxHTMLBytes int64
	// Test seams: renderWaiting runs when a request has to queue for a render
	// slot, renderAcquired once it holds one.
	renderWaiting, renderAcquired func()
}

// Option customizes a Server before its routes are built. Later tasks add
// their own options for injected dependencies (templates, formatter,
// expander, enqueuer).
type Option func(*Server)

// WithClock sets the Server's time source.
func WithClock(clock func() time.Time) Option {
	return func(s *Server) { s.Clock = clock }
}

// WithRenderConcurrency sets how many Markdown renders may run at once
// (default DefaultRenderConcurrency); n below 1 is ignored.
func WithRenderConcurrency(n int) Option {
	return func(s *Server) {
		if n >= 1 {
			s.renderConcurrency = n
		}
	}
}

// New builds a Server and its route table, applying opts first. pool may be
// nil for tests that never touch the database. It panics at startup if
// cfg.SecretKeyBase is empty (an unsigned session cookie would be forgeable),
// two route patterns conflict, an embedded public file or template is missing,
// or cfg.Host is not a valid origin host.
func New(cfg config.Config, pool *pgxpool.Pool, opts ...Option) *Server {
	if cfg.SecretKeyBase == "" {
		panic("web: SECRET_KEY_BASE is required to sign session cookies")
	}
	s := &Server{
		cfg: cfg, pool: pool, Clock: time.Now,
		renderConcurrency: DefaultRenderConcurrency,
		maxRenderBytes:    MaxRenderBytes, maxHTMLBytes: MaxHTMLBytes,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.renderSlots = make(chan struct{}, s.renderConcurrency)
	s.codec = auth.NewCodec(cfg.SecretKeyBase, "session").WithClock(func() time.Time { return s.Clock() })
	s.digests = newAssetTable(webfs.FS)
	for _, name := range requiredAssets {
		if _, ok := s.digests.public[name]; !ok {
			panic(fmt.Sprintf("web: embedded asset %q is missing", name))
		}
	}
	loc, err := time.LoadLocation("America/Mexico_City")
	if err != nil {
		panic(fmt.Sprintf("web: load time zone: %v", err))
	}
	s.mexicoCity = loc
	s.pages = parsePages(webfs.FS, s.templateFuncs(), "sign_in", "markdown", "index")
	s.handler = s.routes()
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// defaultCacheControl is what Rails sends for a dynamic response that sets no
// Cache-Control of its own. Static files, assets and the sign-in page override it.
const defaultCacheControl = "max-age=0, private, must-revalidate"

// defaultSecurityHeaders are Rails' default_headers (the app overrides none).
var defaultSecurityHeaders = [][2]string{
	{"X-Frame-Options", "SAMEORIGIN"},
	{"X-Content-Type-Options", "nosniff"},
	{"Referrer-Policy", "strict-origin-when-cross-origin"},
	{"X-Permitted-Cross-Domain-Policies", "none"},
	{"X-XSS-Protection", "0"},
}

// securityHeaders sets Strict-Transport-Security (Rails' force_ssl with
// assume_ssl; TLS terminates at the Cloudflare tunnel) and Rails' default
// headers on every response. A handler may replace any of them with Set. The
// default Cache-Control is applied lazily, when the response starts, and only
// if the handler left it unset.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		for _, kv := range defaultSecurityHeaders {
			if h.Get(kv[0]) == "" {
				h.Set(kv[0], kv[1])
			}
		}
		next.ServeHTTP(&cacheDefaultWriter{ResponseWriter: w}, r)
	})
}

// cacheDefaultWriter adds the default Cache-Control when the response starts
// without one.
type cacheDefaultWriter struct {
	http.ResponseWriter
	started bool
}

func (w *cacheDefaultWriter) start() {
	if w.started {
		return
	}
	w.started = true
	if h := w.Header(); h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", defaultCacheControl)
	}
}

func (w *cacheDefaultWriter) WriteHeader(code int) {
	// 1xx responses are interim; the final one is still to come.
	if code >= 200 || code == http.StatusSwitchingProtocols {
		w.start()
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *cacheDefaultWriter) Write(b []byte) (int, error) {
	w.start()
	return w.ResponseWriter.Write(b)
}

// Flush implements http.Flusher.
func (w *cacheDefaultWriter) Flush() {
	w.start()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter.
func (w *cacheDefaultWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

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
