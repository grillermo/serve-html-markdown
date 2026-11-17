package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/files"
	"serve-html-markdown/internal/markdown"
	"serve-html-markdown/internal/store"
)

const (
	// MaxRenderBytes is the largest file that is rendered as Markdown; a
	// bigger one is a 413. It bounds memory and the work of one render, but not
	// its time: the parser has quadratic cases that markdown.Render rejects up
	// front (ErrTooComplex) and that renderTimeout cuts off when they slip
	// through, in both cases the file is shown as plain text.
	MaxRenderBytes = 2 << 20
	// MaxHTMLBytes is the largest .html file that is served. HTML is only
	// scanned for </body>, never rendered, so its cap is higher, but each
	// request holds about three copies of the file (see htmlLimit).
	MaxHTMLBytes = 20 << 20
	// DefaultRenderConcurrency is how many Markdown renders run at once unless
	// WithRenderConcurrency says otherwise.
	DefaultRenderConcurrency = 4
	// DefaultHTMLConcurrency is how many .html files are read and injected at
	// once unless WithHTMLConcurrency says otherwise.
	DefaultHTMLConcurrency = 2
	// DefaultRenderTimeout is how long a request waits for a Markdown render
	// before it falls back to plain text.
	DefaultRenderTimeout = 10 * time.Second

	msgTooLarge = "File is too large to display."
	msgInternal = "Internal server error."
)

// limiter is a counting semaphore whose waiters give up with their request.
// waiting runs when a caller has to queue, acquired once it holds a slot; both
// are test seams.
type limiter struct {
	slots             chan struct{}
	waiting, acquired func()
}

func newLimiter(n int) *limiter { return &limiter{slots: make(chan struct{}, n)} }

// acquire takes a slot, waiting for one while respecting ctx. It reports false,
// with no slot held, when ctx ended first; the caller must then return without
// writing. On true the caller calls release exactly once.
func (l *limiter) acquire(ctx context.Context) bool {
	select {
	case l.slots <- struct{}{}:
	default:
		if l.waiting != nil {
			l.waiting()
		}
		select {
		case l.slots <- struct{}{}:
		case <-ctx.Done():
			return false
		}
	}
	if ctx.Err() != nil {
		l.release()
		return false
	}
	if l.acquired != nil {
		l.acquired()
	}
	return true
}

func (l *limiter) release() { <-l.slots }

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
	f, canonical, err := s.openFile(s.cfg.FilesDir, r.PathValue("file_name"))
	if err != nil {
		s.fileError(w, r, err)
		return
	}
	defer f.Close()
	name := filepath.Base(canonical)
	isHTML := strings.EqualFold(store.Ext(name), ".html")
	if isHTML {
		// A .html request holds the file, the injected copy and the buffered
		// response at once; few at a time keeps memory bounded.
		if !s.htmlLimit.acquire(r.Context()) {
			return
		}
		defer s.htmlLimit.release()
	}

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
	rendered, err := s.renderBounded(ctx, content)
	switch {
	case errors.Is(err, errClientGone):
		return // nothing to answer
	case errors.Is(err, markdown.ErrTooComplex), errors.Is(err, errRenderTimeout):
		slog.Warn("markdown file shown as plain text", "file", name, "reason", err)
		rendered = plainTextPage(content)
	case err != nil:
		s.fileError(w, r, err)
		return
	}
	// The renderer allows raw HTML on purpose (see renderMarkdownPage).
	s.renderMarkdownPage(w, name, template.HTML(rendered), bootstrap)
}

var (
	errClientGone    = errors.New("web: client went away")
	errRenderTimeout = errors.New("web: markdown render timed out")
	errRenderPanic   = errors.New("web: markdown render panicked")
)

// plainTextPage is the fallback for a document that cannot be rendered: its
// source, escaped, in a <pre> that wraps long lines.
func plainTextPage(content []byte) []byte {
	var b bytes.Buffer
	b.WriteString(`<pre style="white-space: pre-wrap; overflow-wrap: anywhere">`)
	b.WriteString(html.EscapeString(string(content)))
	b.WriteString("</pre>\n")
	return b.Bytes()
}

// renderBounded renders content under the render limiter. The render runs in
// its own goroutine, which owns the slot until the render really returns, so a
// runaway cannot be multiplied by retries: after renderDeadline fires the
// request moves on (errRenderTimeout) but the slot stays taken. A panic in the
// render is logged and becomes errRenderPanic; the slot is released either way.
// errClientGone means the request ended while it waited, and nothing may be
// written.
func (s *Server) renderBounded(ctx context.Context, content []byte) ([]byte, error) {
	if !s.renders.acquire(ctx) {
		return nil, errClientGone
	}
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1) // buffered: the goroutine never blocks on an abandoned request
	go func() {
		defer s.renders.release() // runs last, after the recover below
		defer func() {
			if p := recover(); p != nil {
				slog.Error("markdown render panicked", "panic", p, "stack", string(debug.Stack()))
				done <- result{err: errRenderPanic}
			}
		}()
		out, err := s.renderMarkdown(content)
		done <- result{out, err}
	}()
	deadline, stop := s.renderDeadline()
	defer stop()
	select {
	case r := <-done:
		return r.out, r.err
	case <-deadline:
		return nil, errRenderTimeout
	case <-ctx.Done():
		return nil, errClientGone
	}
}

// newDeadline is the default renderDeadline: a timer of DefaultRenderTimeout.
func newDeadline() (<-chan time.Time, func()) {
	t := time.NewTimer(DefaultRenderTimeout)
	return t.C, func() { t.Stop() }
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
