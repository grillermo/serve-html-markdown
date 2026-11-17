package web

import "net/http"

// Handlers for routes that are not ported yet. Each answers 501 until the task
// named in its group comment replaces it; delete the stub when you implement
// the real handler.

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_, _ = w.Write([]byte(`{"detail":"Not implemented."}` + "\n"))
}

// Files (Task 4.5).
func (s *Server) lastFile(w http.ResponseWriter, r *http.Request)             { notImplemented(w, r) }
func (s *Server) indexFiles(w http.ResponseWriter, r *http.Request)           { notImplemented(w, r) }
func (s *Server) showFile(w http.ResponseWriter, r *http.Request)             { notImplemented(w, r) }
func (s *Server) createFile(w http.ResponseWriter, r *http.Request)           { notImplemented(w, r) }
func (s *Server) updateScrollPosition(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

// Uploads (Task 5.2).
func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

// Expansions (Task 8.3).
func (s *Server) createExpansion(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }
func (s *Server) showExpansion(w http.ResponseWriter, r *http.Request)   { notImplemented(w, r) }

// Videos (Task 9.7).
func (s *Server) createVideoSummary(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }
func (s *Server) showVideoSummary(w http.ResponseWriter, r *http.Request)   { notImplemented(w, r) }

// YouTube auth (Task 9.8).
func (s *Server) youtubeReauth(w http.ResponseWriter, r *http.Request)   { notImplemented(w, r) }
func (s *Server) youtubeCallback(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }
func (s *Server) youtubeDebug(w http.ResponseWriter, r *http.Request)    { notImplemented(w, r) }
