package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
)

// conditionalGet gives the wrapped handler what Rails' Rack::ETag and
// Rack::ConditionalGet give every response: a 200 with a body gets a weak ETag
// (the first 32 hex digits of the body's SHA-256) and Cache-Control
// "max-age=0, private, must-revalidate"; any other response gets "no-cache"
// unless the handler chose its own. A GET or HEAD whose If-None-Match matches
// the ETag is answered 304, and HEAD never gets a body.
//
// The handler's response is copied into memory until it returns. A handler
// that writes nothing at all, as when the client is gone, leaves the response
// untouched.
func conditionalGet(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bw := &bufferedResponse{header: w.Header()}
		next(bw, r)
		status := bw.status
		if status == 0 {
			return // the handler answered nothing (a cancelled request)
		}
		h := w.Header()
		etag := h.Get("ETag")
		if status == http.StatusOK && bw.body.Len() > 0 && etag == "" && h.Get("Last-Modified") == "" {
			sum := sha256.Sum256(bw.body.Bytes())
			etag = `W/"` + hex.EncodeToString(sum[:])[:32] + `"`
			h.Set("ETag", etag)
		}
		if h.Get("Cache-Control") == "" {
			if etag != "" {
				h.Set("Cache-Control", defaultCacheControl)
			} else {
				h.Set("Cache-Control", "no-cache")
			}
		}
		head := r.Method == http.MethodHead
		if (r.Method == http.MethodGet || head) && status == http.StatusOK && etagMatches(r.Header.Get("If-None-Match"), etag) {
			h.Del("Content-Type")
			h.Del("Content-Length")
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if bw.body.Len() > 0 {
			h.Set("Content-Length", strconv.Itoa(bw.body.Len()))
		}
		w.WriteHeader(status)
		if !head {
			_, _ = w.Write(bw.body.Bytes())
		}
	}
}

// etagMatches is the weak comparison of RFC 9110 for an If-None-Match value:
// "*" matches any ETag, otherwise any listed tag equal to etag, ignoring W/.
func etagMatches(header, etag string) bool {
	if etag == "" || header == "" {
		return false
	}
	want := strings.TrimPrefix(etag, "W/")
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || strings.TrimPrefix(tag, "W/") == want {
			return true
		}
	}
	return false
}

// bufferedResponse holds a handler's response for conditionalGet.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p) // copies p: the caller may reuse it
}
