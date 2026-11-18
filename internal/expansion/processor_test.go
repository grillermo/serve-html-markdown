package expansion

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/files"
	"serve-html-markdown/internal/linker"
	"serve-html-markdown/internal/llm"
	"serve-html-markdown/internal/store"
)

const answerHTML = "<!DOCTYPE html><html><body>answer</body></html>"

var ctx = context.Background()

// fakeExpander answers with fixed text, or with what fn returns, and records
// how it was called.
type fakeExpander struct {
	mu       sync.Mutex
	expands  []llm.Params
	rewrites []llm.Params
	recs     []llm.Recorder
	expand   func(p llm.Params) (string, error)
	rewrite  func(p llm.Params) (string, error)
}

func (f *fakeExpander) Expand(_ context.Context, p llm.Params, rec llm.Recorder) (string, error) {
	f.mu.Lock()
	f.expands, f.recs = append(f.expands, p), append(f.recs, rec)
	f.mu.Unlock()
	if f.expand == nil {
		return answerHTML, nil
	}
	return f.expand(p)
}

func (f *fakeExpander) Rewrite(_ context.Context, p llm.Params, rec llm.Recorder) (string, error) {
	f.mu.Lock()
	f.rewrites, f.recs = append(f.rewrites, p), append(f.recs, rec)
	f.mu.Unlock()
	if f.rewrite == nil {
		return "", errors.New("unexpected rewrite")
	}
	return f.rewrite(p)
}

func (f *fakeExpander) called() int { return len(f.expands) + len(f.rewrites) }

type env struct {
	t    *testing.T
	pool *pgxpool.Pool
	dir  string
	user *store.User
	exp  *fakeExpander
	proc *Processor
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "files")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	user, err := store.UpsertUser(ctx, pool, "processor@example.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	exp := &fakeExpander{}
	return &env{t: t, pool: pool, dir: dir, user: user, exp: exp,
		proc: &Processor{FilesDir: dir, DB: pool, Expander: exp}}
}

func (e *env) write(name, content string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, name), []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) read(name string) string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dir, name))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func (e *env) exists(name string) bool {
	_, err := os.Lstat(filepath.Join(e.dir, name))
	return err == nil
}

// expansion creates the row of notes.md / "beta" / "Why?" and applies tweak.
func (e *env) expansion(tweak func(n *store.NewExpansion)) *store.Expansion {
	e.t.Helper()
	n := store.NewExpansion{UserID: e.user.ID, FileName: "notes.md", SelectedText: "beta", Question: "Why?"}
	if tweak != nil {
		tweak(&n)
	}
	x, err := store.CreateExpansion(ctx, e.pool, n)
	if err != nil {
		e.t.Fatal(err)
	}
	return x
}

func editInPlace(n *store.NewExpansion) { n.Mode = "edit_in_place" }

func (e *env) process(x *store.Expansion) (string, error) {
	e.t.Helper()
	return e.proc.Process(ctx, x)
}

func (e *env) mustProcess(x *store.Expansion) string {
	e.t.Helper()
	url, err := e.process(x)
	if err != nil {
		e.t.Fatal(err)
	}
	return url
}

func (e *env) servedFile(name string) *store.ServedFile {
	e.t.Helper()
	var f store.ServedFile
	err := e.pool.QueryRow(ctx, `SELECT id, name, created_at, updated_at FROM served_files WHERE name = $1`, name).
		Scan(&f.ID, &f.Name, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil
	}
	return &f
}

func (e *env) reload(x *store.Expansion) *store.Expansion {
	e.t.Helper()
	got, err := store.FindExpansion(ctx, e.pool, x.ID)
	if err != nil || got == nil {
		e.t.Fatal(err)
	}
	return got
}

// Ruby: "generates an expansion and links the latest source while holding its lock".
func TestLinksTheLatestSource(t *testing.T) {
	e := newEnv(t)
	e.write("notes.md", "Alpha beta gamma.")
	// The source changes while the model works: the link goes into the new text.
	e.exp.expand = func(llm.Params) (string, error) {
		e.write("notes.md", "Alpha beta gamma, edited meanwhile.")
		return answerHTML, nil
	}
	url := e.mustProcess(e.expansion(nil))
	if url != "/notes--expand-1.html" {
		t.Errorf("url = %q", url)
	}
	if got := e.read("notes--expand-1.html"); got != answerHTML {
		t.Errorf("page = %q", got)
	}
	if got := e.read("notes.md"); got != "Alpha [beta](/notes--expand-1.html) gamma, edited meanwhile." {
		t.Errorf("source = %q", got)
	}
	// A second expansion takes the next number.
	if url := e.mustProcess(e.expansion(func(n *store.NewExpansion) { n.SelectedText = "gamma" })); url != "/notes--expand-2.html" {
		t.Errorf("second url = %q", url)
	}
}

// Ruby: "records the new expand file and bumps the source file's updated_at".
func TestRecordsServedFiles(t *testing.T) {
	e := newEnv(t)
	e.write("notes.md", "Alpha beta gamma.")
	old := time.Now().UTC().Add(-48 * time.Hour)
	if _, err := e.pool.Exec(ctx, `INSERT INTO served_files (name, created_at, updated_at) VALUES ('notes.md', $1, $1)`, old); err != nil {
		t.Fatal(err)
	}
	e.mustProcess(e.expansion(nil))
	if e.servedFile("notes--expand-1.html") == nil {
		t.Error("expected the new expand file to be recorded")
	}
	src := e.servedFile("notes.md")
	if src == nil || !src.UpdatedAt.After(old.Add(time.Hour)) || src.CreatedAt.After(old.Add(time.Minute)) {
		t.Errorf("source row = %+v, want updated_at bumped and created_at kept", src)
	}
}

// Ruby: "does not write either file when the expander fails".
func TestExpanderFailureWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.write("notes.md", "Alpha beta gamma.")
	want := llm.ErrExpand("cli unavailable")
	e.exp.expand = func(llm.Params) (string, error) { return "", want }
	if _, err := e.process(e.expansion(nil)); err != want {
		t.Fatalf("err = %v", err)
	}
	if e.read("notes.md") != "Alpha beta gamma." || e.exists("notes--expand-1.html") {
		t.Error("files were written")
	}
	if e.servedFile("notes--expand-1.html") != nil {
		t.Error("served file recorded")
	}
}

// Ruby: "uses the next suffix and leaves source unchanged when latest source cannot be linked".
func TestUnlinkableSelectionLeavesEverythingAlone(t *testing.T) {
	e := newEnv(t)
	e.write("notes.md", "Alpha gamma.")
	e.write("notes--expand-1.html", "taken")
	_, err := e.process(e.expansion(nil))
	var le *linker.Error
	if !errors.As(err, &le) || !errors.Is(err, linker.ErrNotFound) {
		t.Fatalf("err = %v, want a linker NotFound", err)
	}
	if e.read("notes.md") != "Alpha gamma." || e.read("notes--expand-1.html") != "taken" || e.exists("notes--expand-2.html") {
		t.Error("files changed")
	}
}

// Ruby: "stamps source_read, lock_acquired, link_rewritten, and files_written".
func TestStampsTheStages(t *testing.T) {
	e := newEnv(t)
	e.write("notes.md", "Alpha beta gamma.")
	x := e.expansion(nil)
	e.mustProcess(x)
	timings := e.reload(x).Timings
	var prev int64
	for _, stage := range []string{StageSourceRead, StageLockAcquired, StageLinkRewritten, StageFilesWritten} {
		at, ok := timings[stage]
		if !ok {
			t.Fatalf("%s not stamped: %v", stage, timings)
		}
		if at < prev {
			t.Errorf("%s (%d) is before the previous stage (%d)", stage, at, prev)
		}
		prev = at
	}
}

// Ruby: "passes the expansion to the expander": the document, the selection and
// the question, and a recorder that writes to this expansion's row.
func TestPassesTheExpansionToTheExpander(t *testing.T) {
	e := newEnv(t)
	e.write("notes.md", "Alpha beta gamma.")
	x := e.expansion(func(n *store.NewExpansion) { n.UseOpenAI = true })
	e.exp.expand = func(llm.Params) (string, error) {
		e.exp.recs[0].Stamp(llm.StageRequestStart)
		e.exp.recs[0].RecordResponse("claude", len(answerHTML))
		return answerHTML, nil
	}
	e.mustProcess(x)
	want := llm.Params{FileName: "notes.md", Document: "Alpha beta gamma.", Selection: "beta", Question: "Why?", UseOpenAI: true}
	if len(e.exp.expands) != 1 || e.exp.expands[0] != want {
		t.Errorf("params = %+v", e.exp.expands)
	}
	got := e.reload(x)
	if _, ok := got.Timings[llm.StageRequestStart]; !ok {
		t.Errorf("recorder did not stamp the expansion: %v", got.Timings)
	}
	if got.ProviderUsed == nil || *got.ProviderUsed != "claude" || got.HTMLBytes == nil || *got.HTMLBytes != len(answerHTML) {
		t.Errorf("response not recorded: %v %v", got.ProviderUsed, got.HTMLBytes)
	}
}

// An .html source links with an <a>, and the page keeps the source's stem.
func TestLinksAnHTMLSource(t *testing.T) {
	e := newEnv(t)
	e.write("Page.HTML", "<html><body><p>Alpha beta gamma.</p></body></html>")
	url := e.mustProcess(e.expansion(func(n *store.NewExpansion) { n.FileName = "Page.HTML" }))
	if url != "/Page--expand-1.html" {
		t.Errorf("url = %q", url)
	}
	if got := e.read("Page.HTML"); !strings.Contains(got, `<a href="/Page--expand-1.html">beta</a>`) {
		t.Errorf("source = %q", got)
	}
}

func TestNamesWithSpacesAndUnicodeAreEncodedInTheURL(t *testing.T) {
	e := newEnv(t)
	e.write("mi nota é.md", "Alpha beta gamma.")
	url := e.mustProcess(e.expansion(func(n *store.NewExpansion) { n.FileName = "mi nota é.md" }))
	if url != "/mi%20nota%20%C3%A9--expand-1.html" {
		t.Errorf("url = %q", url)
	}
	if !e.exists("mi nota é--expand-1.html") {
		t.Error("page not written under the plain name")
	}
	if got := e.read("mi nota é.md"); got != "Alpha [beta](/mi%20nota%20%C3%A9--expand-1.html) gamma." {
		t.Errorf("source = %q", got)
	}
}

// Ruby: "writes the rewrite to the next version and leaves the source untouched".
func TestRewriteWritesTheNextVersion(t *testing.T) {
	e := newEnv(t)
	e.write("notes.md", "Alpha beta gamma.")
	e.exp.rewrite = func(llm.Params) (string, error) {
		return "Alpha ⟦EXPANSION_ANCHOR⟧beta, at length, gamma. Plus much more text here.", nil
	}
	x := e.expansion(editInPlace)
	if url := e.mustProcess(x); url != "/notes--v2.md#expansion-anchor" {
		t.Errorf("url = %q", url)
	}
	if e.read("notes.md") != "Alpha beta gamma." {
		t.Error("source was touched")
	}
	if got := e.read("notes--v2.md"); got != `Alpha <a id="expansion-anchor"></a>beta, at length, gamma. Plus much more text here.` {
		t.Errorf("version = %q", got)
	}
	if e.servedFile("notes--v2.md") == nil {
		t.Error("version not recorded")
	}
	if len(e.exp.expands) != 0 || len(e.exp.rewrites) != 1 || e.exp.rewrites[0].Document != "Alpha beta gamma." {
		t.Errorf("calls: %+v %+v", e.exp.expands, e.exp.rewrites)
	}
	timings := e.reload(x).Timings
	for _, stage := range []string{StageSourceRead, StageLockAcquired, StageFilesWritten} {
		if _, ok := timings[stage]; !ok {
			t.Errorf("%s not stamped: %v", stage, timings)
		}
	}
	if _, ok := timings[StageLinkRewritten]; ok {
		t.Errorf("link_rewritten stamped for a rewrite")
	}
}

// Ruby: "includes the fallback anchor in the completed url" (and ERB's url_encode).
func TestRewriteURLCarriesTheFallback(t *testing.T) {
	cases := map[string]string{
		"section two":  "/notes--v2.md?fallback=section%20two#expansion-anchor",
		"a&b=c/ü+~_-.": "/notes--v2.md?fallback=a%26b%3Dc%2F%C3%BC%2B~_-.#expansion-anchor",
		"   ":          "/notes--v2.md#expansion-anchor",
		"":             "/notes--v2.md#expansion-anchor",
	}
	for fallback, want := range cases {
		e := newEnv(t)
		e.write("notes.md", "Alpha beta gamma.")
		e.exp.rewrite = func(llm.Params) (string, error) {
			return "Alpha ⟦EXPANSION_ANCHOR⟧beta expanded gamma, and then some more.", nil
		}
		url := e.mustProcess(e.expansion(func(n *store.NewExpansion) { n.Mode, n.FallbackAnchor = "edit_in_place", fallback }))
		if url != want {
			t.Errorf("fallback %q: url = %q, want %q", fallback, url, want)
		}
	}
}

// Ruby: "rewrites a versioned file into the next version of the same family".
func TestRewriteOfAVersionGoesToTheNextOne(t *testing.T) {
	e := newEnv(t)
	e.write("notes.md", "Alpha beta gamma.")
	e.write("notes--v2.md", "Alpha beta gamma, expanded once already.")
	e.exp.rewrite = func(llm.Params) (string, error) {
		return "Alpha ⟦EXPANSION_ANCHOR⟧beta expanded twice now, with more words.", nil
	}
	url := e.mustProcess(e.expansion(func(n *store.NewExpansion) { n.Mode, n.FileName = "edit_in_place", "notes--v2.md" }))
	if url != "/notes--v3.md#expansion-anchor" || !e.exists("notes--v3.md") {
		t.Errorf("url = %q", url)
	}
	if e.exp.rewrites[0].FileName != "notes--v2.md" || e.exp.rewrites[0].Document != "Alpha beta gamma, expanded once already." {
		t.Errorf("rewrote %+v", e.exp.rewrites[0])
	}
	// The family is found from the base too.
	url = e.mustProcess(e.expansion(editInPlace))
	if url != "/notes--v4.md#expansion-anchor" {
		t.Errorf("from the base: url = %q", url)
	}
}

// Ruby: "serializes concurrent edit_in_place rewrites across the same version family".
func TestConcurrentRewritesOfOneFamilyGetDistinctVersions(t *testing.T) {
	e := newEnv(t)
	e.write("notes.md", "Alpha beta gamma, the original document body here.")
	e.write("notes--v2.md", "Alpha beta gamma, expanded once already in this body.")
	e.exp.rewrite = func(llm.Params) (string, error) {
		return "Alpha ⟦EXPANSION_ANCHOR⟧beta expanded with plenty of extra text to pass ratio.", nil
	}
	const n = 8
	xs := make([]*store.Expansion, n)
	for i := range xs {
		name := "notes.md"
		if i%2 == 1 {
			name = "notes--v2.md"
		}
		xs[i] = e.expansion(func(x *store.NewExpansion) { x.Mode, x.FileName = "edit_in_place", name })
	}
	urls := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			urls[i], errs[i] = e.process(xs[i])
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(urls)
	var want []string
	for v := 3; v < 3+n; v++ {
		want = append(want, fmt.Sprintf("/notes--v%d.md#expansion-anchor", v))
	}
	slices.SortFunc(want, strings.Compare)
	if !slices.Equal(urls, want) {
		t.Errorf("urls = %v, want %v", urls, want)
	}
	for v := 3; v < 3+n; v++ {
		if !e.exists(fmt.Sprintf("notes--v%d.md", v)) || e.servedFile(fmt.Sprintf("notes--v%d.md", v)) == nil {
			t.Errorf("v%d missing", v)
		}
	}
}

// blockedBy reports whether process stays blocked while the test holds lock key
// (a base name), and finishes once it is released.
func blockedBy(t *testing.T, e *env, lockKey string, x *store.Expansion) {
	t.Helper()
	hold := make(chan struct{})
	held := make(chan struct{})
	released := make(chan error, 1)
	go func() {
		released <- withLock(e.dir, lockKey, func() error { close(held); <-hold; return nil })
	}()
	<-held
	done := make(chan error, 1)
	go func() { _, err := e.process(x); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("finished while .%s.expansion.lock was held (err = %v)", lockKey, err)
	case <-time.After(300 * time.Millisecond):
	}
	close(hold)
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("still blocked after the lock was released")
	}
}

// Ruby: "locks rewrite_in_place on the version family's base name, not the requested file".
func TestRewriteLocksOnTheFamilyBaseName(t *testing.T) {
	for _, requested := range []string{"notes.md", "notes--v2.md", "notes--v3.md"} {
		e := newEnv(t)
		for _, name := range []string{"notes.md", "notes--v2.md", "notes--v3.md"} {
			e.write(name, "Alpha beta gamma.")
		}
		e.exp.rewrite = func(llm.Params) (string, error) { return "Alpha ⟦EXPANSION_ANCHOR⟧beta gamma, expanded.", nil }
		blockedBy(t, e, "notes.md", e.expansion(func(n *store.NewExpansion) { n.Mode, n.FileName = "edit_in_place", requested }))
	}
}

// A new page locks on the source file's own base name (with its extension).
func TestLinkNewPageLocksOnTheSourceFile(t *testing.T) {
	e := newEnv(t)
	e.write("notes--v2.md", "Alpha beta gamma.")
	blockedBy(t, e, "notes--v2.md", e.expansion(func(n *store.NewExpansion) { n.FileName = "notes--v2.md" }))
	if info, err := os.Stat(filepath.Join(e.dir, ".notes--v2.md.expansion.lock")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("lock file: %v %v", info, err)
	}
}

func TestConcurrentNewPagesOfOneSourceAllLink(t *testing.T) {
	e := newEnv(t)
	words := []string{"one", "two", "three", "four", "five", "six"}
	e.write("notes.md", strings.Join(words, " ")+".")
	var wg sync.WaitGroup
	urls := make([]string, len(words))
	errs := make([]error, len(words))
	xs := make([]*store.Expansion, len(words))
	for i, w := range words {
		xs[i] = e.expansion(func(n *store.NewExpansion) { n.SelectedText = w })
	}
	for i := range words {
		wg.Add(1)
		go func() { defer wg.Done(); urls[i], errs[i] = e.process(xs[i]) }()
	}
	wg.Wait()
	src := e.read("notes.md")
	for i, w := range words {
		if errs[i] != nil {
			t.Fatalf("%s: %v", w, errs[i])
		}
		if !strings.Contains(src, "["+w+"]("+urls[i]+")") {
			t.Errorf("source lost the link for %q (%s):\n%s", w, urls[i], src)
		}
	}
}

// Ruby: "keeps only the first anchor sentinel".
func TestRewriteKeepsOnlyTheFirstAnchor(t *testing.T) {
	e := newEnv(t)
	e.write("notes.md", "Alpha beta gamma.")
	e.exp.rewrite = func(llm.Params) (string, error) {
		return "A ⟦EXPANSION_ANCHOR⟧B ⟦EXPANSION_ANCHOR⟧C with plenty of extra text.", nil
	}
	e.mustProcess(e.expansion(editInPlace))
	if got := e.read("notes--v2.md"); got != `A <a id="expansion-anchor"></a>B C with plenty of extra text.` {
		t.Errorf("version = %q", got)
	}
}

// Ruby: "still writes a version when the model omits the sentinel".
func TestRewriteWithoutASentinel(t *testing.T) {
	e := newEnv(t)
	e.write("notes.md", "Alpha beta gamma.")
	e.exp.rewrite = func(llm.Params) (string, error) { return "Alpha beta gamma, expanded without any marker at all.", nil }
	if url := e.mustProcess(e.expansion(editInPlace)); url != "/notes--v2.md#expansion-anchor" {
		t.Errorf("url = %q", url)
	}
	if got := e.read("notes--v2.md"); got != "Alpha beta gamma, expanded without any marker at all." {
		t.Errorf("version = %q", got)
	}
}

func TestInsertAnchorEdges(t *testing.T) {
	const s = llm.AnchorSentinel
	for in, want := range map[string]string{
		s:             `<a id="expansion-anchor"></a>`,
		s + s:         `<a id="expansion-anchor"></a>`,
		"x" + s:       `x<a id="expansion-anchor"></a>`,
		s + "x" + s:   `<a id="expansion-anchor"></a>x`,
		"a" + s + "b": `a<a id="expansion-anchor"></a>b`,
		"none":        "none",
	} {
		if got := insertAnchor(in); got != want {
			t.Errorf("insertAnchor(%q) = %q, want %q", in, got, want)
		}
	}
}

// Ruby: "refuses a rewrite shorter than half the source".
func TestRefusesATruncatedRewrite(t *testing.T) {
	e := newEnv(t)
	e.write("notes.md", "Alpha beta gamma, a reasonably long document body.")
	e.exp.rewrite = func(llm.Params) (string, error) { return "Alpha.", nil }
	_, err := e.process(e.expansion(editInPlace))
	if !errors.Is(err, ErrTruncatedRewrite) || err.Error() != "Rewrite looked truncated." {
		t.Fatalf("err = %v", err)
	}
	var pe *Error
	if !errors.As(err, &pe) {
		t.Errorf("not an *Error: %T", err)
	}
	if e.exists("notes--v2.md") || e.read("notes.md") != "Alpha beta gamma, a reasonably long document body." {
		t.Error("files changed")
	}
}

// The ratio counts characters, as Ruby's length does, not bytes.
func TestTruncationRatioCountsCharacters(t *testing.T) {
	e := newEnv(t)
	e.write("notes.md", strings.Repeat("é", 10)+" beta")                       // 15 characters, 25 bytes
	e.exp.rewrite = func(llm.Params) (string, error) { return "abcdefg", nil } // 7 chars < 7.5, 7 bytes < 12.5
	if _, err := e.process(e.expansion(editInPlace)); !errors.Is(err, ErrTruncatedRewrite) {
		t.Fatalf("7 of 15 characters: err = %v", err)
	}
	e.exp.rewrite = func(llm.Params) (string, error) { return "ééééééééé", nil } // 9 chars, 18 bytes
	if _, err := e.process(e.expansion(editInPlace)); err != nil {
		t.Fatalf("9 of 15 characters: err = %v", err)
	}
	// Exactly half is accepted.
	e.write("half.md", "abcdefghij beta")
	e.exp.rewrite = func(llm.Params) (string, error) { return "ééééééé", nil } // 7 chars of 15: refused
	if _, err := e.process(e.expansion(func(n *store.NewExpansion) { n.Mode, n.FileName = "edit_in_place", "half.md" })); !errors.Is(err, ErrTruncatedRewrite) {
		t.Fatalf("err = %v", err)
	}
	e.write("even.md", "abcdefghij")
	e.exp.rewrite = func(llm.Params) (string, error) { return "ééééé", nil } // 5 of 10, but 10 bytes
	if _, err := e.process(e.expansion(func(n *store.NewExpansion) { n.Mode, n.FileName = "edit_in_place", "even.md" })); err != nil {
		t.Fatalf("exactly half: err = %v", err)
	}
}

// Policy: a source that is not valid UTF-8 fails before any model call, without
// its content in the error.
func TestInvalidUTF8SourceFailsBeforeTheModel(t *testing.T) {
	for _, mode := range []string{"create_new", "edit_in_place"} {
		e := newEnv(t)
		e.write("notes.md", "Alpha beta \xff\xfe SECRET-CONTENT gamma.")
		_, err := e.process(e.expansion(func(n *store.NewExpansion) { n.Mode = mode }))
		if !errors.Is(err, ErrSourceNotUTF8) || !errors.Is(err, linker.ErrInvalidUTF8) {
			t.Fatalf("%s: err = %v", mode, err)
		}
		if strings.Contains(err.Error(), "SECRET") || e.exp.called() != 0 {
			t.Errorf("%s: error leaks content or the model was called (%d)", mode, e.exp.called())
		}
		var pe *Error
		if errors.As(err, &pe) {
			t.Errorf("%s: should not be a user-facing *Error", mode)
		}
	}
}

// A source that becomes invalid while the model works fails under the lock.
func TestSourceBecomingInvalidUTF8UnderTheLock(t *testing.T) {
	e := newEnv(t)
	e.write("notes.md", "Alpha beta gamma.")
	e.exp.expand = func(llm.Params) (string, error) {
		e.write("notes.md", "Alpha beta \xff gamma.")
		return answerHTML, nil
	}
	if _, err := e.process(e.expansion(nil)); !errors.Is(err, ErrSourceNotUTF8) {
		t.Fatalf("err = %v", err)
	}
	if e.exists("notes--expand-1.html") {
		t.Error("page written")
	}
}

func TestSelectionTooLarge(t *testing.T) {
	e := newEnv(t)
	e.write("notes.md", "Alpha beta gamma.")
	x := e.expansion(nil)
	x.SelectedText = strings.Repeat("a", MaxSelectedTextBytes+1)
	_, err := e.process(x)
	if !errors.Is(err, ErrSelectionTooLarge) || e.exp.called() != 0 {
		t.Fatalf("err = %v, calls = %d", err, e.exp.called())
	}
	x.SelectedText = strings.Repeat("a", MaxSelectedTextBytes)
	if _, err := e.process(x); errors.Is(err, ErrSelectionTooLarge) {
		t.Errorf("exactly the cap should pass the check: %v", err)
	}
}

// The source goes through files.OpenFile: traversal, unsupported types, missing
// files and symlinks out of the directory never reach the model.
func TestSourceProtections(t *testing.T) {
	e := newEnv(t)
	outside := filepath.Join(filepath.Dir(e.dir), "secret.md")
	if err := os.WriteFile(outside, []byte("beta outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.dir, "escape.md")); err != nil {
		t.Fatal(err)
	}
	e.write("data.txt", "beta")
	for name, want := range map[string]error{
		"../secret.md": files.ErrBadRequest,
		"escape.md":    files.ErrBadRequest,
		"data.txt":     files.ErrUnsupported,
		"missing.md":   files.ErrMissing,
	} {
		_, err := e.process(e.expansion(func(n *store.NewExpansion) { n.FileName = name }))
		if !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", name, err, want)
		}
	}
	if e.exp.called() != 0 {
		t.Errorf("model called %d times", e.exp.called())
	}
	if b, _ := os.ReadFile(outside); string(b) != "beta outside" {
		t.Errorf("outside file changed: %q", b)
	}
}

// The source is replaced atomically: a symlink to it stays, its mode stays, no
// temporary file is left, and a reader never sees a partial document.
func TestSourceIsReplacedAtomically(t *testing.T) {
	e := newEnv(t)
	e.write("real.md", "Alpha beta gamma.")
	if err := os.Chmod(filepath.Join(e.dir, "real.md"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.md", filepath.Join(e.dir, "notes.md")); err != nil {
		t.Fatal(err)
	}
	e.mustProcess(e.expansion(nil))
	// The expansion is named after the resolved file, as in Ruby.
	if got := e.read("real.md"); got != "Alpha [beta](/real--expand-1.html) gamma." {
		t.Errorf("real.md = %q", got)
	}
	if target, err := os.Readlink(filepath.Join(e.dir, "notes.md")); err != nil || target != "real.md" {
		t.Errorf("symlink = %q, %v", target, err)
	}
	if info, _ := os.Stat(filepath.Join(e.dir, "real.md")); info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
	list, _ := os.ReadDir(e.dir)
	for _, f := range list {
		if strings.HasPrefix(f.Name(), ".tmp-") {
			t.Errorf("temporary file left behind: %s", f.Name())
		}
	}
	if e.servedFile("real.md") == nil || e.servedFile("real--expand-1.html") == nil {
		t.Error("served files not recorded under the resolved name")
	}
}

func TestReadersNeverSeeAPartialSource(t *testing.T) {
	e := newEnv(t)
	big := strings.Repeat("filler line of text\n", 20000)
	e.write("notes.md", "Alpha beta gamma.\n"+big)
	stop := make(chan struct{})
	bad := make(chan string, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			b, err := os.ReadFile(filepath.Join(e.dir, "notes.md"))
			if err != nil {
				continue
			}
			s := string(b)
			if s != "Alpha beta gamma.\n"+big && s != "Alpha [beta](/notes--expand-1.html) gamma.\n"+big {
				select {
				case bad <- fmt.Sprintf("saw %d bytes starting %.40q", len(s), s):
				default:
				}
				return
			}
		}
	}()
	e.mustProcess(e.expansion(nil))
	close(stop)
	wg.Wait()
	select {
	case msg := <-bad:
		t.Fatal(msg)
	default:
	}
}

func TestURLEncode(t *testing.T) {
	for in, want := range map[string]string{
		"foo.md": "foo.md", "a b": "a%20b", "a+b": "a%2Bb", "é": "%C3%A9", "~_-.": "~_-.",
		"a/b?c=d&e#f": "a%2Fb%3Fc%3Dd%26e%23f", "": "",
	} {
		if got := urlEncode(in); got != want {
			t.Errorf("urlEncode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRecorderStampsAndRecords(t *testing.T) {
	e := newEnv(t)
	x := e.expansion(nil)
	rec := NewRecorder(ctx, e.pool, x.ID)
	rec.Stamp(llm.StageResponse)
	rec.RecordResponse("codex", 42)
	got := e.reload(x)
	if _, ok := got.Timings[llm.StageResponse]; !ok || got.ProviderUsed == nil || *got.ProviderUsed != "codex" || *got.HTMLBytes != 42 {
		t.Errorf("got %+v", got)
	}
}
