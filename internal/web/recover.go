package web

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// recoverPanics turns a panic in a handler into a logged 500 JSON
// {"detail":"Internal server error."}, as Rails does for an unhandled
// exception, instead of net/http's bare connection reset. If the response has
// already started it cannot be replaced, so the connection is aborted
// (http.ErrAbortHandler) and the client sees a failed transfer rather than a
// truncated page that looks complete. http.ErrAbortHandler itself passes through.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &startedWriter{ResponseWriter: w}
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			if p == http.ErrAbortHandler {
				panic(p)
			}
			slog.Error("panic in handler", "method", r.Method, "path", r.URL.Path, "panic", p, "stack", string(debug.Stack()))
			if sw.started {
				panic(http.ErrAbortHandler)
			}
			writeDetail(w, http.StatusInternalServerError, msgInternal)
		}()
		next.ServeHTTP(sw, r)
	})
}

// startedWriter records whether the response has started.
type startedWriter struct {
	http.ResponseWriter
	started bool
}

func (w *startedWriter) WriteHeader(code int) {
	if code >= 200 {
		w.started = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *startedWriter) Write(b []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(b)
}

// Flush implements http.Flusher.
func (w *startedWriter) Flush() {
	w.started = true
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter.
func (w *startedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
