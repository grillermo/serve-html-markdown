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
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
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
//
// An Error made by writeFailed also has a Cause (reachable with errors.Is/As,
// never printed): the raw file system error, whose text holds paths.
type Error struct {
	Message string
	cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

// writeFailed is the user-facing error for a file system failure while saving
// the result; the raw error (with its paths) stays out of the message.
func writeFailed(cause error) error {
	return &Error{Message: "Could not save the expansion.", cause: cause}
}

// The user-facing errors. They are shared values: never modify them.
var (
	// ErrTruncatedRewrite is TruncatedRewrite: the model returned less than
	// half of the document.
	ErrTruncatedRewrite error = &Error{Message: "Rewrite looked truncated."}
	// ErrSelectionTooLarge is returned for a selection over MaxSelectedTextBytes.
	ErrSelectionTooLarge error = &Error{Message: "Selected text is too long."}
	// ErrDocumentTooLarge is returned when the prompt would not fit in the CLIs'
	// argument list (llm.MaxPromptArgBytes); the model is not called.
	ErrDocumentTooLarge error = &Error{Message: "Document is too large to expand."}
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

	// beforeCreate, when set, runs just before a new page is created, with the
	// name picked for it. Tests use it to force a name collision.
	beforeCreate func(pageName string)
}

// maxNameAttempts bounds how often a new page retries after another writer
// (a different source with the same stem, foo.md and foo.html) took its name.
const maxNameAttempts = 5

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

// generationError turns the one expander failure the reader can act on into a
// user-facing error and leaves the others (generic *llm.ExpandError) as they are.
func generationError(err error) error {
	if errors.Is(err, llm.ErrPromptTooLarge) {
		return ErrDocumentTooLarge
	}
	return err
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
		return "", generationError(err)
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
		// foo.md and foo.html both want foo--expand-1.html and lock on different
		// keys: the exclusive create is what arbitrates, and the loser picks
		// the next number and links to that.
		var pageName string
		for attempt := 1; ; attempt++ {
			if attempt > maxNameAttempts {
				return writeFailed(fs.ErrExist)
			}
			pageName, err = files.FreeIndexedName(p.FilesDir, stem+"--expand-", ".html")
			if err != nil {
				return err
			}
			url = "/" + urlEncode(pageName)
			linked, err := linker.Link(latest, strings.ToLower(store.Ext(name)), e.SelectedText, e.Occurrence, url)
			if err != nil {
				return err
			}
			p.stamp(ctx, e, StageLinkRewritten)
			if p.beforeCreate != nil {
				p.beforeCreate(pageName)
			}
			err = files.CreateNew(p.FilesDir, pageName, []byte(html))
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			if err != nil {
				return writeFailed(err)
			}
			if err := files.ReplaceFile(p.FilesDir, rel, []byte(linked)); err != nil {
				// Do not leave an orphan page behind.
				if rerr := os.Remove(filepath.Join(p.FilesDir, pageName)); rerr != nil {
					slog.Error("[Expansion] could not remove the orphan page", "file", pageName, "err", rerr)
				}
				return writeFailed(err)
			}
			break
		}
		p.persistResult(e, url)
		p.stamp(ctx, e, StageFilesWritten)
		p.recordServedFiles(ctx, pageName, name)
		return nil
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
		return "", generationError(err)
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
			return writeFailed(err)
		}
		url = versionURL(versionName, e.FallbackAnchor)
		p.persistResult(e, url)
		p.stamp(ctx, e, StageFilesWritten)
		p.recordServedFiles(ctx, versionName)
		return nil
	})
	if err != nil {
		return "", err
	}
	return url, nil
}

// persistResult stores the url on the expansion row, as soon as the files are
// written and before anything else can fail, without changing its status. If
// the job dies before it completes the expansion, the re-run finds the url and
// completes with it instead of generating (and writing) a second result. It is
// best effort and runs on its own short context: the files exist, so a
// cancelled job context must not skip it, and a failure only means the re-run
// regenerates, as before.
func (p *Processor) persistResult(e *store.Expansion, url string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.SetExpansionURL(ctx, p.DB, e.ID, url); err != nil && !errors.Is(err, store.ErrExpansionFinished) {
		slog.Error("[Expansion] could not record the result url", "expansion_id", e.ID, "err", err)
	}
}

// recordServedFiles records the files just written: the first name as new, the
// others (the modified source) as touched. The files are already on disk and
// the expansion has succeeded, so a failure is only logged (names, no content):
// files.SyncServedFiles adds the missing rows later.
func (p *Processor) recordServedFiles(ctx context.Context, created string, modified ...string) {
	if err := store.RecordServedFile(ctx, p.DB, created); err != nil {
		slog.Error("[Expansion] could not record the new served file", "file", created, "err", err)
	}
	for _, name := range modified {
		if err := store.RecordServedFileModification(ctx, p.DB, name); err != nil {
			slog.Error("[Expansion] could not record the modified served file", "file", name, "err", err)
		}
	}
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
		return writeFailed(err)
	}
	defer root.Close()
	f, err := root.OpenFile("."+keyName+".expansion.lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return writeFailed(err)
	}
	defer f.Close()
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return writeFailed(err)
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
