package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/config"
	"serve-html-markdown/internal/files"
	"serve-html-markdown/internal/llm"
	"serve-html-markdown/internal/store"
)

// Deadlines. The http.Server built by the serve wiring must not undercut them:
// give it a ReadTimeout and WriteTimeout that are zero or longer than the
// largest of these (the handlers extend their own deadlines per request with
// http.NewResponseController), and keep ReadHeaderTimeout for slow headers.
const (
	// writeSlack is added to the OpenAI timeout for the rest of a create.
	writeSlack = 15 * time.Second
	// uploadDeadline is how long a client has to send an upload body and get
	// its answer.
	uploadDeadline = 5 * time.Minute
	// recordTimeout bounds the served_files insert after a file is written.
	recordTimeout = 10 * time.Second
)

const (
	// MaxCreateBytes caps the body of POST /file/new. Rails had no limit; the
	// cap keeps one request from exhausting memory.
	MaxCreateBytes = 16 << 20
	// MaxUploadBytes caps the whole multipart body of POST /file/upload, so the
	// file part is bounded by it too.
	MaxUploadBytes = 64 << 20
	// uploadMemory is how much of a multipart body is parsed in memory; the rest
	// spills to temporary files that are removed after the request.
	uploadMemory = 8 << 20

	msgBodyTooLarge = "Request body is too large."
	msgBadBody      = "Invalid request body."
	msgNotUTF8      = "File must be UTF-8 text."
	// msgFileTooLarge: a file over the cap for what the server would show. The
	// cap is that of the serve path (MaxRenderBytes for .md, MaxHTMLBytes for
	// .html), so nothing is stored that GET /<name> would then refuse with 413.
	msgFileTooLarge = "File is too large."
)

// Formatter turns the posted text into Markdown. The default is the
// OpenAI-backed llm.Formatter; tests inject fakes with WithFormatter.
//
// An error that is llm.ErrConfiguration or an *llm.Error is an upstream
// failure and answers 502; any other error is a 500.
type Formatter interface {
	Format(ctx context.Context, content string) (string, error)
}

// WithFormatter replaces the Markdown formatter used by POST /file/new.
func WithFormatter(f Formatter) Option {
	return func(s *Server) { s.formatter = f }
}

// newFormatter is the real formatter: OPENAI_API_KEY, falling back to
// EXPANSION_LLM_API_KEY.
func newFormatter(cfg config.Config) *llm.Formatter {
	return llm.NewFormatter(llm.NewClient(llm.APIKey(cfg.OpenAIKey, cfg.ExpansionLLMKey)))
}

// publicURL is the link returned for a created file. Like Rails it does not
// URL-encode the name.
func (s *Server) publicURL(name string) string {
	return "https://" + s.cfg.Host + "/" + name
}

// marshalJSON encodes v like Rails' to_json: <, > and & stay as they are
// (U+2028 and U+2029 are still escaped), and there is no trailing newline.
func marshalJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // maps of strings cannot fail
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// extendDeadlines moves the request's read and write deadlines to now+read and
// now+write (0 leaves one alone). A ResponseWriter that has no deadlines, such
// as httptest's recorder, is fine.
func extendDeadlines(w http.ResponseWriter, read, write time.Duration) {
	rc := http.NewResponseController(w)
	now := time.Now()
	if read > 0 {
		if err := rc.SetReadDeadline(now.Add(read)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			slog.Warn("set read deadline", "err", err)
		}
	}
	if write > 0 {
		if err := rc.SetWriteDeadline(now.Add(write)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			slog.Warn("set write deadline", "err", err)
		}
	}
}

// writeJSON answers 200 with body as JSON.
func writeJSON(w http.ResponseWriter, body map[string]string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(marshalJSON(body))
}

// bodyError answers a failed body read or parse with 413 or 400.
func bodyError(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeDetail(w, http.StatusRequestEntityTooLarge, msgBodyTooLarge)
		return
	}
	writeDetail(w, http.StatusBadRequest, msgBadBody)
}

// recordCreated records name in served_files so /last finds it at once. The
// file already exists on disk, so a database failure is a 500 but the file stays.
//
// The insert does not follow the request's context: a client that hangs up right
// after the file is written would otherwise leave it without a row. A file
// that still ends up without one is picked up by store.SyncServedFiles.
//
// TODO(serve wiring): call store.SyncServedFiles at startup so files written
// without a row (a crash or a failed insert between the write and the insert)
// heal.
func (s *Server) recordCreated(w http.ResponseWriter, r *http.Request, name string) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), recordTimeout)
	defer cancel()
	if err := store.RecordServedFile(ctx, s.pool, name); err != nil {
		s.fileError(w, r, err)
		return false
	}
	return true
}

// createFile is POST /file/new (FilesController#create): format content with
// OpenAI and save it as <filename>.md, with -1, -2, ... on a name clash. It is
// authorized by the bearer token only; a session cookie does not count.
//
// Like Ruby the file name is checked before the formatter runs, so a bad name
// costs no OpenAI call. Unlike Ruby the name is claimed only when the file is
// written (O_EXCL), after the formatter's answer is in.
func (s *Server) createFile(w http.ResponseWriter, r *http.Request) {
	if !auth.BearerOK(r, s.cfg.APIToken) {
		writeDetail(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	// The formatter call can take as long as the OpenAI client waits.
	extendDeadlines(w, 0, llm.DefaultTimeout+writeSlack)
	params, err := parseParams(w, r, s.maxCreateBytes)
	if err != nil {
		bodyError(w, err)
		return
	}
	content, ok := params.lookup("content")
	if !ok {
		writeDetail(w, http.StatusBadRequest, "Missing content.")
		return
	}
	// Rails refuses a parameter that is not valid UTF-8 with a 400.
	if !utf8.ValidString(content) {
		writeDetail(w, http.StatusBadRequest, msgNotUTF8)
		return
	}
	filename := params.get("filename")
	if _, err := files.Stem(filename); err != nil {
		s.fileError(w, r, err)
		return
	}
	formatted, err := s.formatter.Format(r.Context(), content)
	if err != nil {
		s.formatError(w, r, err)
		return
	}
	// Like an upload, never store what the server would then refuse to show.
	if int64(len(formatted)) > s.maxRenderBytes {
		writeDetail(w, http.StatusRequestEntityTooLarge, msgFileTooLarge)
		return
	}
	path, err := files.CreateUnique(s.cfg.FilesDir, filename, ".md", []byte(formatted))
	if err != nil {
		s.fileError(w, r, err)
		return
	}
	name := filepath.Base(path)
	if s.recordCreated(w, r, name) {
		writeJSON(w, map[string]string{"url": s.publicURL(name)})
	}
}

// formatError answers a failed Format: 502 with a fixed text for an OpenAI
// failure or a missing key (neither shows the upstream's words), 500 for
// anything else.
func (s *Server) formatError(w http.ResponseWriter, r *http.Request, err error) {
	var upstream *llm.Error
	if errors.Is(err, llm.ErrConfiguration) || errors.As(err, &upstream) {
		slog.Error("OpenAI formatting failed", "err", err.Error())
		writeDetail(w, http.StatusBadGateway, llm.FormattingFailed)
		return
	}
	s.fileError(w, r, err)
}

// uploadFile is POST /file/upload (FilesController#upload): store the multipart
// part "file" as it is (no formatting) under the "filename" parameter, or the
// part's own name. Only .html/.htm and .md/.markdown are accepted, and the
// content must be valid UTF-8. Bearer token only.
//
// Like Ruby it checks, in order: the file is there, the extension, the name,
// then the UTF-8 content.
func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request) {
	if !auth.BearerOK(r, s.cfg.APIToken) {
		writeDetail(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	// A big upload may take longer than the server's default deadlines.
	extendDeadlines(w, uploadDeadline, uploadDeadline+writeSlack)
	r.Body = http.MaxBytesReader(w, r.Body, s.maxUploadBytes)
	err := r.ParseMultipartForm(uploadMemory)
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}
	if err != nil && !errors.Is(err, http.ErrNotMultipart) && !errors.Is(err, http.ErrMissingBoundary) {
		bodyError(w, err)
		return
	}
	var header *multipart.FileHeader
	if r.MultipartForm != nil {
		if hs := r.MultipartForm.File["file"]; len(hs) > 0 {
			header = hs[len(hs)-1]
		}
	}
	if header == nil {
		writeDetail(w, http.StatusBadRequest, "Missing file.")
		return
	}
	filename := uploadParam(r, "filename")
	if strings.TrimSpace(filename) == "" {
		filename = header.Filename
	}
	ext, err := files.UploadExtension(filename)
	if err == nil {
		_, err = files.Stem(filename)
	}
	if err != nil {
		s.fileError(w, r, err)
		return
	}
	// The cap of the serve path for this kind of file; the size is known before
	// the part is read.
	limit := s.maxRenderBytes
	if ext == ".html" {
		limit = s.maxHTMLBytes
	}
	if header.Size > limit {
		writeDetail(w, http.StatusRequestEntityTooLarge, msgFileTooLarge)
		return
	}
	data, err := readPart(header, limit)
	if err != nil {
		s.fileError(w, r, err)
		return
	}
	if int64(len(data)) > limit {
		writeDetail(w, http.StatusRequestEntityTooLarge, msgFileTooLarge)
		return
	}
	if !utf8.Valid(data) {
		writeDetail(w, http.StatusBadRequest, msgNotUTF8)
		return
	}
	path, err := files.CreateUnique(s.cfg.FilesDir, filename, ext, data)
	if err != nil {
		s.fileError(w, r, err)
		return
	}
	name := filepath.Base(path)
	if s.recordCreated(w, r, name) {
		writeJSON(w, map[string]string{"url": s.publicURL(name)})
	}
}

// uploadParam is a parameter of an upload: the query string wins over the
// multipart fields, and the last of repeated values wins.
func uploadParam(r *http.Request, key string) string {
	if vs := r.URL.Query()[key]; len(vs) > 0 {
		return vs[len(vs)-1]
	}
	if vs := r.MultipartForm.Value[key]; len(vs) > 0 {
		return vs[len(vs)-1]
	}
	return ""
}

// readPart reads an uploaded part, at most limit+1 bytes: one more than the
// cap tells "at the cap" from "over it".
func readPart(h *multipart.FileHeader, limit int64) ([]byte, error) {
	f, err := h.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit+1))
}
