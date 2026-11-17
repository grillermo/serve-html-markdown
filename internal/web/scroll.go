package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/store"
)

// maxScrollBodyBytes bounds a scroll position request body.
const maxScrollBodyBytes = 64 << 10

// requestParams are the parameters of a request, merged the way Rails merges
// them into params: the query string wins over the body, and within one source
// the last of repeated keys wins. The body is JSON when the Content-Type says
// json or the text starts with "{" (navigator.sendBeacon posts a Blob typed
// application/json, but some clients send text/plain), else a form
// (urlencoded or multipart). Rails reads a body as JSON only with a JSON
// Content-Type; reading it by content as well is deliberate.
type requestParams struct {
	query url.Values
	form  url.Values
	json  map[string]any
}

// errBadBody is a body that does not parse in the format it claims.
var errBadBody = errors.New("web: malformed request body")

// parseParams reads r's body (at most limit bytes; a bigger one yields an
// *http.MaxBytesError) and merges it with the query string.
func parseParams(w http.ResponseWriter, r *http.Request, limit int64) (requestParams, error) {
	p := requestParams{query: r.URL.Query()}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return p, err
	}
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch {
	case len(trimmed) == 0:
	case strings.Contains(strings.ToLower(mediaType), "json") || trimmed[0] == '{':
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		var obj any
		if err := dec.Decode(&obj); err != nil {
			return p, errBadBody
		}
		// A JSON body that is not an object has no named parameters (Rails
		// wraps it as _json), so the fields are simply missing.
		p.json, _ = obj.(map[string]any)
	case mediaType == "multipart/form-data":
		r.Body = io.NopCloser(bytes.NewReader(body))
		if err := r.ParseMultipartForm(limit); err != nil {
			return p, errBadBody
		}
		p.form = r.MultipartForm.Value
	default:
		form, err := url.ParseQuery(string(body))
		if err != nil {
			return p, errBadBody
		}
		p.form = form
	}
	return p, nil
}

// get is params[key].to_s: "" when the parameter is absent or null. Strings
// are taken as they are, numbers and booleans as their JSON text (Ruby's to_s
// of an integer or true gives the same), arrays and objects as JSON text,
// which no valid anchor can match.
func (p requestParams) get(key string) string {
	if vs := p.query[key]; len(vs) > 0 {
		return vs[len(vs)-1]
	}
	if v, ok := p.json[key]; ok {
		switch v := v.(type) {
		case nil:
			return ""
		case string:
			return v
		case json.Number:
			return v.String()
		case bool:
			if v {
				return "true"
			}
			return "false"
		default:
			b, _ := json.Marshal(v)
			return string(b)
		}
	}
	if vs := p.form[key]; len(vs) > 0 {
		return vs[len(vs)-1]
	}
	return ""
}

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
