package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/files"
	"serve-html-markdown/internal/markdown"
	"serve-html-markdown/internal/store"
)

const (
	// MaxRenderBytes is the largest file that is rendered as Markdown. The
	// renderer has upstream quadratic behaviour on adversarial input, so bigger
	// files get a 413 instead of tying up the CPU.
	MaxRenderBytes = 5 << 20
	// MaxHTMLBytes is the largest .html file that is served. HTML is only
	// scanned for </body>, never rendered, so its cap is much higher.
	MaxHTMLBytes = 50 << 20
	// DefaultRenderConcurrency is how many Markdown renders run at once unless
	// WithRenderConcurrency says otherwise.
	DefaultRenderConcurrency = 4

	msgTooLarge = "File is too large to display."
	msgInternal = "Internal server error."
)

// writeDetail answers with {"detail": msg} like Rails' `render json: { detail: }`.
// Rack::ETag gives a response without an ETag (anything but a non-empty 200)
// Cache-Control "no-cache" (config: `middleware.use Rack::ETag, "no-cache"`).
func writeDetail(w http.ResponseWriter, status int, msg string) {
	body, _ := json.Marshal(map[string]string{"detail": msg})
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// fileError answers a failure of OpenFile, a lookup or a render: client errors
// from the files package with their own status and detail, anything else as a
// logged 500 that does not reveal the cause.
func (s *Server) fileError(w http.ResponseWriter, r *http.Request, err error) {
	if status, detail, ok := files.StatusOf(err); ok {
		writeDetail(w, status, detail)
		return
	}
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeDetail(w, http.StatusInternalServerError, msgInternal)
}

// showFile is GET /{file_name} (FilesController#show). The file is opened
// through files.OpenFile, whose canonical on-disk name (symlinks resolved, stored
// case, base name only) keys the scroll position and the version family, so
// /Notes.MD on a case-insensitive file system finds the rows of notes.md.
//
// .html files are sent as they are with the expand bootstrap spliced in; any
// other served extension (.md, .markdown) is rendered as Markdown into the
// markdown layout.
func (s *Server) showFile(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFrom(r.Context())
	if user == nil {
		s.unauthenticated(w, r)
		return
	}
	f, canonical, err := files.OpenFile(s.cfg.FilesDir, r.PathValue("file_name"))
	if err != nil {
		s.fileError(w, r, err)
		return
	}
	defer f.Close()
	name := filepath.Base(canonical)
	isHTML := strings.EqualFold(store.Ext(name), ".html")

	limit := s.maxRenderBytes
	if isHTML {
		limit = s.maxHTMLBytes
	}
	// One byte more than the cap tells "exactly at the cap" from "over it", and
	// stops a file that grows while it is read.
	content, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		s.fileError(w, r, err)
		return
	}
	if int64(len(content)) > limit {
		writeDetail(w, http.StatusRequestEntityTooLarge, msgTooLarge)
		return
	}

	ctx := r.Context()
	anchor, err := store.FindScrollPosition(ctx, s.pool, user.ID, name)
	if err != nil {
		s.fileError(w, r, err)
		return
	}
	versions, err := s.fileVersionsFor(ctx, name)
	if err != nil {
		s.fileError(w, r, err)
		return
	}
	bootstrap := s.NewExpandBootstrap(anchor, versions, user.ExpansionMode)

	if isHTML {
		snippet, err := ExpandBootstrapHTML(bootstrap)
		if err != nil {
			s.fileError(w, r, err)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(injectBootstrap(content, string(snippet)))
		return
	}

	if !utf8.Valid(content) {
		// Commonmarker raises on this and Rails answers 500; a half-readable
		// page is more useful to the one reader of these files.
		slog.Warn("markdown file is not valid UTF-8; replacing bad bytes with U+FFFD", "file", name)
		content = []byte(strings.ToValidUTF8(string(content), "�"))
	}
	if !s.acquireRender(ctx) {
		return // the client went away while queued; nothing to answer
	}
	rendered, err := func() ([]byte, error) {
		defer func() { <-s.renderSlots }()
		return markdown.Render(content)
	}()
	if err != nil {
		s.fileError(w, r, err)
		return
	}
	// The renderer allows raw HTML on purpose (see renderMarkdownPage).
	s.renderMarkdownPage(w, name, template.HTML(rendered), bootstrap)
}

// acquireRender takes one of the render slots, waiting for it while respecting
// ctx. It reports false, with no slot held, when ctx ended first; the caller must
// then return without writing. On true the caller releases with <-s.renderSlots.
func (s *Server) acquireRender(ctx context.Context) bool {
	select {
	case s.renderSlots <- struct{}{}:
	default:
		if s.renderWaiting != nil {
			s.renderWaiting()
		}
		select {
		case s.renderSlots <- struct{}{}:
		case <-ctx.Done():
			return false
		}
	}
	if ctx.Err() != nil {
		<-s.renderSlots
		return false
	}
	if s.renderAcquired != nil {
		s.renderAcquired()
	}
	return true
}

// fileVersionsFor lists the documents of name's family (name itself included)
// for window.__fileVersions, or none when the family has fewer than two.
func (s *Server) fileVersionsFor(ctx context.Context, name string) ([]FileVersionEntry, error) {
	names, err := files.ParseFileVersions(name).FamilyNames(ctx, s.pool)
	if err != nil || len(names) < 2 {
		return nil, err
	}
	entries := make([]FileVersionEntry, len(names))
	for i, sibling := range names {
		entries[i] = NewFileVersionEntry(sibling, files.ParseFileVersions(sibling).Version, sibling == name)
	}
	return entries, nil
}

// injectBootstrap is FilesController#inject_expand_script: the snippet goes in
// front of the first </body> (any case), which, like in Ruby, comes out in lower
// case; without one it is appended. It works on bytes and never needs the
// document to be valid UTF-8 (Ruby would raise on such a file).
func injectBootstrap(content []byte, snippet string) []byte {
	i := indexBodyClose(content)
	if i < 0 {
		out := make([]byte, 0, len(content)+len(snippet))
		return append(append(out, content...), snippet...)
	}
	const closing = "</body>"
	out := make([]byte, 0, len(content)+len(snippet))
	out = append(out, content[:i]...)
	out = append(out, snippet...)
	out = append(out, closing...)
	return append(out, content[i+len(closing):]...)
}

// indexBodyClose is the index of the first "</body>" in b, ignoring ASCII
// case, or -1. It is one linear pass.
func indexBodyClose(b []byte) int {
	for i := 0; ; i++ {
		j := bytes.IndexByte(b[i:], '<')
		if j < 0 {
			return -1
		}
		i += j
		if i+7 <= len(b) && b[i+1] == '/' && b[i+6] == '>' &&
			b[i+2]|0x20 == 'b' && b[i+3]|0x20 == 'o' && b[i+4]|0x20 == 'd' && b[i+5]|0x20 == 'y' {
			return i
		}
	}
}

// indexFiles is GET /index: every served file, most recently updated first.
func (s *Server) indexFiles(w http.ResponseWriter, r *http.Request) {
	rows, err := store.ServedFilesByUpdated(r.Context(), s.pool)
	if err != nil {
		s.fileError(w, r, err)
		return
	}
	s.renderIndex(w, rows)
}

// lastFile is GET / and GET /last: a 302 to the most recently added file.
// Rails sends an absolute URL; the Location here is the path, which every
// client resolves against the request.
func (s *Server) lastFile(w http.ResponseWriter, r *http.Request) {
	latest, err := store.NewestServedFile(r.Context(), s.pool)
	if err != nil {
		s.fileError(w, r, err)
		return
	}
	if latest == nil {
		writeDetail(w, http.StatusNotFound, "No files found.")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	redirect(w, "/"+URLEncode(latest.Name), http.StatusFound)
}

// conditionalGet gives the wrapped handler what Rails' Rack::ETag and
// Rack::ConditionalGet give every response: a 200 with a body gets a weak ETag
// (the first 32 hex digits of the body's SHA-256) and Cache-Control
// "max-age=0, private, must-revalidate"; any other response gets "no-cache"
// unless the handler chose its own. A GET or HEAD whose If-None-Match matches
// the ETag is answered 304, and HEAD never gets a body.
//
// The handler's response is held in memory until it returns. Slices passed to
// Write are kept, not copied, so a wrapped handler must not change them
// afterwards (none does). A handler that writes nothing at all, as when the
// client is gone, leaves the response untouched.
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
		if status == http.StatusOK && bw.size > 0 && etag == "" && h.Get("Last-Modified") == "" {
			sum := sha256.New()
			for _, p := range bw.parts {
				sum.Write(p)
			}
			etag = `W/"` + hex.EncodeToString(sum.Sum(nil))[:32] + `"`
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
		if bw.size > 0 {
			h.Set("Content-Length", strconv.Itoa(bw.size))
		}
		w.WriteHeader(status)
		if head {
			return
		}
		for _, p := range bw.parts {
			if _, err := w.Write(p); err != nil {
				return
			}
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
	parts  [][]byte
	size   int
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
	if len(p) > 0 {
		b.parts = append(b.parts, p)
		b.size += len(p)
	}
	return len(p), nil
}
