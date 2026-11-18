// Package expansion generates an expansion: it reads the document the reader
// selected from, asks the LLM (internal/llm) and writes the result into the
// files directory, either as a new page linked from the selection or as the
// next version of the document.
package expansion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"serve-html-markdown/internal/files"
	"serve-html-markdown/internal/linker"
	"serve-html-markdown/internal/llm"
	"serve-html-markdown/internal/store"
)

const (
	// AnchorID is the id of the element that replaces the anchor sentinel.
	AnchorID = "expansion-anchor"
	// MinRewriteRatio is how much of the source's length a rewrite must have.
	MinRewriteRatio = 0.5
	// MaxSelectedTextBytes caps the selection: the linker is O(n*m) on it.
	MaxSelectedTextBytes = 100 * 1024
	// MaxSourceBytes caps the document that is read into memory.
	MaxSourceBytes = 32 << 20
)

// Stages the processor stamps on the expansion (the LLM stages are llm.Stage*).
const (
	StageSourceRead    = "source_read"
	StageLockAcquired  = "lock_acquired"
	StageLinkRewritten = "link_rewritten"
	StageFilesWritten  = "files_written"
)

// Error is a failure whose Message is meant for the reader (the job stores it as
// the expansion's error detail). Other errors the processor returns are not.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

// The user-facing errors. They are shared values: never modify them.
var (
	// ErrTruncatedRewrite is TruncatedRewrite: the model returned less than
	// half of the document.
	ErrTruncatedRewrite error = &Error{Message: "Rewrite looked truncated."}
	// ErrSelectionTooLarge is returned for a selection over MaxSelectedTextBytes.
	ErrSelectionTooLarge error = &Error{Message: "Selected text is too long."}
	// ErrSourceTooLarge is returned for a document over MaxSourceBytes.
	ErrSourceTooLarge error = &Error{Message: "File is too large to expand."}
)

// ErrSourceNotUTF8 is returned, before any model call, for a document that is
// not valid UTF-8. It never carries document content. It matches
// linker.ErrInvalidUTF8 with errors.Is, and is not an *Error: the job reports it
// generically.
var ErrSourceNotUTF8 = fmt.Errorf("expansion: source file is not valid UTF-8: %w", linker.ErrInvalidUTF8)

// Expander is the LLM side: llm.ExpandService.
type Expander interface {
	Expand(ctx context.Context, p llm.Params, rec llm.Recorder) (string, error)
	Rewrite(ctx context.Context, p llm.Params, rec llm.Recorder) (string, error)
}

// Processor turns an expansion row into files.
type Processor struct {
	// FilesDir is the directory of served files.
	FilesDir string
	// DB records the served files and the expansion's stages.
	DB       store.DB
	Expander Expander
}

// Process generates the expansion and returns the url of the result: the new
// page for create_new mode (the source now links to it), or the new version,
// with the fallback and the anchor, for edit_in_place. Errors: *Error for what
// the reader should read (ErrTruncatedRewrite, ErrSelectionTooLarge, ...),
// *files.Error for a bad or missing source, *linker.Error when the selection
// cannot be linked, *llm.ExpandError when generation failed, and
// ErrSourceNotUTF8; anything else is an internal failure.
func (p *Processor) Process(ctx context.Context, e *store.Expansion) (string, error) {
	if len(e.SelectedText) > MaxSelectedTextBytes {
		return "", ErrSelectionTooLarge
	}
	file, rel, err := files.OpenFile(p.FilesDir, e.FileName)
	if err != nil {
		return "", err
	}
	source, err := readSource(file)
	if err != nil {
		return "", err
	}
	p.stamp(ctx, e, StageSourceRead)
	if !utf8.ValidString(source) {
		return "", ErrSourceNotUTF8
	}
	// The generated names derive from the resolved file's base name, as Ruby's
	// file_path.basename did (even when the file is in a subdirectory).
	name := filepath.Base(rel)
	params := llm.Params{
		FileName: name, Document: source, Selection: e.SelectedText, Question: e.Question, UseOpenAI: e.UseOpenAI,
	}
	rec := NewRecorder(ctx, p.DB, e.ID)
	if e.EditInPlace() {
		return p.rewriteInPlace(ctx, e, name, source, params, rec)
	}
	return p.linkNewPage(ctx, e, name, params, rec)
}

func (p *Processor) stamp(ctx context.Context, e *store.Expansion, stage string) {
	store.StampExpansion(ctx, p.DB, e.ID, stage, store.NowMS())
}

// readSource reads and closes file, refusing more than MaxSourceBytes.
func readSource(file *os.File) (string, error) {
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxSourceBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > MaxSourceBytes {
		return "", ErrSourceTooLarge
	}
	return string(data), nil
}

// linkNewPage: the page is generated outside the lock (it takes a minute); then,
// holding the source's lock, the source is read again (the reader may have
// edited it meanwhile), the next free --expand-N name is picked, the selection
// is linked to it and both files are written.
func (p *Processor) linkNewPage(ctx context.Context, e *store.Expansion, name string,
	params llm.Params, rec llm.Recorder) (string, error) {
	html, err := p.Expander.Expand(ctx, params, rec)
	if err != nil {
		return "", err
	}
	var url string
	err = withLock(p.FilesDir, name, func() error {
		p.stamp(ctx, e, StageLockAcquired)
		file, rel, err := files.OpenFile(p.FilesDir, e.FileName)
		if err != nil {
			return err
		}
		latest, err := readSource(file)
		if err != nil {
			return err
		}
		if !utf8.ValidString(latest) {
			return ErrSourceNotUTF8
		}
		stem := strings.TrimSuffix(name, store.Ext(name))
		pageName, err := files.FreeIndexedName(p.FilesDir, stem+"--expand-", ".html")
		if err != nil {
			return err
		}
		url = "/" + urlEncode(pageName)
		linked, err := linker.Link(latest, strings.ToLower(store.Ext(name)), e.SelectedText, e.Occurrence, url)
		if err != nil {
			return err
		}
		p.stamp(ctx, e, StageLinkRewritten)

		if err := files.CreateNew(p.FilesDir, pageName, []byte(html)); err != nil {
			return err
		}
		if err := files.ReplaceFile(p.FilesDir, rel, []byte(linked)); err != nil {
			_ = os.Remove(filepath.Join(p.FilesDir, pageName)) // do not leave an orphan page
			return err
		}
		p.stamp(ctx, e, StageFilesWritten)
		if err := store.RecordServedFile(ctx, p.DB, pageName); err != nil {
			return err
		}
		return store.RecordServedFileModification(ctx, p.DB, name)
	})
	if err != nil {
		return "", err
	}
	return url, nil
}

// rewriteInPlace: the rewrite is generated outside the lock and the source is
// not touched; the next version of the family (foo.md, foo--v2.md, ...) is
// written under a lock on the family's base name, so concurrent rewrites of any
// member get distinct versions.
func (p *Processor) rewriteInPlace(ctx context.Context, e *store.Expansion, name, source string,
	params llm.Params, rec llm.Recorder) (string, error) {
	rewritten, err := p.Expander.Rewrite(ctx, params, rec)
	if err != nil {
		return "", err
	}
	if float64(utf8.RuneCountInString(rewritten)) < float64(utf8.RuneCountInString(source))*MinRewriteRatio {
		return "", ErrTruncatedRewrite
	}
	anchored := insertAnchor(rewritten)

	family := files.ParseFileVersions(name)
	var url string
	err = withLock(p.FilesDir, family.BaseName(), func() error {
		p.stamp(ctx, e, StageLockAcquired)
		path, err := family.NextPath(p.FilesDir)
		if err != nil {
			return err
		}
		versionName := filepath.Base(path)
		if err := files.CreateNew(p.FilesDir, versionName, []byte(anchored)); err != nil {
			return err
		}
		p.stamp(ctx, e, StageFilesWritten)
		if err := store.RecordServedFile(ctx, p.DB, versionName); err != nil {
			return err
		}
		url = versionURL(versionName, e.FallbackAnchor)
		return nil
	})
	if err != nil {
		return "", err
	}
	return url, nil
}

// insertAnchor keeps the first anchor sentinel, as an element with the anchor
// id, and drops any others: the model is asked for exactly one, and the page
// must never carry a duplicate id.
func insertAnchor(document string) string {
	i := strings.Index(document, llm.AnchorSentinel)
	if i < 0 {
		return document
	}
	rest := strings.ReplaceAll(document[i+len(llm.AnchorSentinel):], llm.AnchorSentinel, "")
	return document[:i] + `<a id="` + AnchorID + `"></a>` + rest
}

// versionURL is the page's url. The fallback rides in the query because the
// page reloads: the client cannot keep it in memory across the navigation.
func versionURL(name string, fallback *string) string {
	url := "/" + urlEncode(name)
	if fallback != nil && strings.TrimSpace(*fallback) != "" {
		url += "?fallback=" + urlEncode(*fallback)
	}
	return url + "#" + AnchorID
}

// urlEncode is ERB::Util.url_encode: everything but A-Z a-z 0-9 _ - . ~ is
// percent-encoded, byte by byte (a space is %20, not +).
func urlEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

// withLock runs fn holding an exclusive flock on <filesDir>/.<keyName>.expansion.lock,
// the lock file the Ruby app used, so a Rails and a Go process on one directory
// serialise against each other. The key is a base name: the source file's for a
// new page (writes to that one file are serialised) and the version family's
// base name for a rewrite (version numbers are allocated across the family).
func withLock(filesDir, keyName string, fn func() error) error {
	root, err := os.OpenRoot(filesDir)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.OpenFile("."+keyName+".expansion.lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck // closing the file releases it anyway
	return fn()
}

// recorder is the store-backed llm.Recorder of one expansion.
type recorder struct {
	ctx context.Context
	db  store.DB
	id  int64
}

// NewRecorder returns the llm.Recorder that stamps stages on, and records the
// response of, expansion id. Failures are logged by the store and never
// returned: they are diagnostics.
func NewRecorder(ctx context.Context, db store.DB, id int64) llm.Recorder {
	return &recorder{ctx: ctx, db: db, id: id}
}

func (r *recorder) Stamp(stage string) { store.StampExpansion(r.ctx, r.db, r.id, stage, store.NowMS()) }

func (r *recorder) RecordResponse(provider string, htmlBytes int) {
	if err := store.RecordExpansionResponse(r.ctx, r.db, r.id, provider, htmlBytes); err != nil {
		slogError("record expansion response", err)
	}
}

func slogError(what string, err error) { slog.Error("[Expansion] "+what+" failed", "err", err) }
