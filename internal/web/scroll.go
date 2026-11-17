package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/store"
)

// maxScrollBodyBytes bounds a scroll position request body.
const maxScrollBodyBytes = 64 << 10

// updateScrollPosition is PATCH/POST /scroll_position
// (ScrollPositionsController#update): remember the anchor the signed-in user
// reached in file_name. Like Ruby it checks only the format of both values,
// not that the file exists, and unknown parameters (expand.js still sends an
// empty authenticity_token) are ignored.
func (s *Server) updateScrollPosition(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFrom(r.Context())
	if user == nil {
		s.unauthenticated(w, r)
		return
	}
	params, err := parseParams(w, r, maxScrollBodyBytes)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeDetail(w, http.StatusRequestEntityTooLarge, "Request body is too large.")
			return
		}
		if errors.Is(err, errBadBody) {
			writeDetail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
		slog.Error("read scroll position body", "err", err)
		writeDetail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	fileName, anchor := params.get("file_name"), params.get("anchor")
	if strings.TrimSpace(fileName) == "" {
		writeDetail(w, http.StatusBadRequest, "Missing file_name.")
		return
	}
	if strings.TrimSpace(anchor) == "" {
		writeDetail(w, http.StatusBadRequest, "Missing anchor.")
		return
	}
	switch err := store.UpsertScrollPosition(r.Context(), s.pool, user.ID, fileName, anchor); {
	case err == nil:
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrInvalidAnchor):
		writeDetail(w, http.StatusUnprocessableEntity, "Anchor is invalid")
	case errors.Is(err, store.ErrInvalidFileName):
		// A name Postgres cannot store (NUL byte); Rails would answer 500.
		writeDetail(w, http.StatusUnprocessableEntity, "File name is invalid")
	default:
		s.fileError(w, r, err)
	}
}
