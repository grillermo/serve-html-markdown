package files_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/files"
	"serve-html-markdown/internal/store"
)

func recordedNames(t *testing.T, db store.DB) []string {
	t.Helper()
	rows, err := db.Query(context.Background(), `SELECT name FROM served_files ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Ruby: "writes slugified html and records it".
func TestWriterWritesSlugifiedHTMLAndRecordsIt(t *testing.T) {
	pool := testdb.New(t)
	dir := t.TempDir()
	w := &files.HTMLWriter{Dir: dir, DB: pool}

	name, err := w.Write(context.Background(), "The Big Idea!", "<p>Hi</p>")
	if err != nil {
		t.Fatal(err)
	}
	if name != "the-big-idea.html" {
		t.Fatalf("name = %q", name)
	}
	body := readFile(t, filepath.Join(dir, name))
	for _, want := range []string{"<title>The Big Idea!</title>", "<p>Hi</p>", "background: #111"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	if !regexp.MustCompile(`<main>\s*<h1>The Big Idea!</h1>\s*<p>Hi</p>\s*</main>`).MatchString(body) {
		t.Errorf("main block not as expected:\n%s", body)
	}
	if got := recordedNames(t, pool); !slices.Equal(got, []string{name}) {
		t.Errorf("served_files = %v", got)
	}
	info, err := os.Stat(filepath.Join(dir, name))
	if err != nil || info.Mode().Perm()&0o111 != 0 {
		t.Errorf("mode = %v, err = %v", info.Mode(), err)
	}
}

// Ruby: "avoids collisions with a numeric suffix".
func TestWriterAvoidsCollisionsWithANumericSuffix(t *testing.T) {
	pool := testdb.New(t)
	dir := t.TempDir()
	w := &files.HTMLWriter{Dir: dir, DB: pool}
	ctx := context.Background()

	first, err1 := w.Write(ctx, "Dup", "<p>1</p>")
	second, err2 := w.Write(ctx, "Dup", "<p>2</p>")
	if err1 != nil || err2 != nil || first != "dup.html" || second != "dup-1.html" {
		t.Fatalf("names = %q %q, errs = %v %v", first, second, err1, err2)
	}
	// What is on disk is never overwritten, and a hole is not refilled before a taken name.
	if !strings.Contains(readFile(t, filepath.Join(dir, first)), "<p>1</p>") ||
		!strings.Contains(readFile(t, filepath.Join(dir, second)), "<p>2</p>") {
		t.Error("a page was overwritten")
	}
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "dup-2.html")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dup-3.html"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := w.Write(ctx, "dup", "<p>3</p>")
	if err != nil || third != "dup-4.html" {
		t.Fatalf("third = %q, %v", third, err)
	}
	if got := readFile(t, filepath.Join(dir, "dup-3.html")); got != "mine" {
		t.Errorf("dup-3.html = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, "nowhere")); err == nil {
		t.Error("the dangling symlink was written through")
	}
}

func TestWriterRacingWritersGetDistinctNames(t *testing.T) {
	pool := testdb.New(t)
	dir := t.TempDir()
	w := &files.HTMLWriter{Dir: dir, DB: pool}
	const n = 12
	names := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name, err := w.Write(context.Background(), "Race", fmt.Sprintf("<p>%d</p>", i))
			if err != nil {
				t.Error(err)
			}
			names[i] = name
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, name := range names {
		if seen[name] {
			t.Errorf("name %q handed out twice", name)
		}
		seen[name] = true
		if !strings.Contains(readFile(t, filepath.Join(dir, name)), fmt.Sprintf("<p>%d</p>", i)) {
			t.Errorf("%s does not hold writer %d's page", name, i)
		}
	}
	if len(recordedNames(t, pool)) != n {
		t.Errorf("recorded = %v", recordedNames(t, pool))
	}
}

// Ruby: "falls back to a default slug when the title has no word characters".
func TestWriterFallsBackToTheDefaultSlug(t *testing.T) {
	w := &files.HTMLWriter{Dir: t.TempDir(), DB: testdb.New(t)}
	for _, title := range []string{"!!!", "", "日本語", " - "} {
		name, err := w.Write(context.Background(), title, "<p>x</p>")
		if err != nil || !strings.HasPrefix(name, "summary") || !strings.HasSuffix(name, ".html") {
			t.Errorf("%q: name = %q, err = %v", title, name, err)
		}
	}
}

func TestWriterCreatesAMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "files")
	name, err := (&files.HTMLWriter{Dir: dir, DB: testdb.New(t)}).Write(context.Background(), "T", "<p>x</p>")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Error(err)
	}
}

func TestWriterLongTitlesStayWithinFileNameLimits(t *testing.T) {
	dir := t.TempDir()
	w := &files.HTMLWriter{Dir: dir, DB: testdb.New(t)}
	title := strings.Repeat("word ", 200)
	first, err1 := w.Write(context.Background(), title, "<p>x</p>")
	second, err2 := w.Write(context.Background(), title, "<p>x</p>")
	if err1 != nil || err2 != nil || len(first) > 250 || len(second) > 255 || first == second {
		t.Fatalf("names = %q %q, errs = %v %v", first, second, err1, err2)
	}
}

type failingDB struct{ store.DB }

func (failingDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("db down")
}

// The page is already on disk when recording fails: the name comes back with
// the error so the caller can still serve or log it.
func TestWriterRecordFailureKeepsTheFileAndReportsIt(t *testing.T) {
	dir := t.TempDir()
	name, err := (&files.HTMLWriter{Dir: dir, DB: failingDB{}}).Write(context.Background(), "T", "<p>x</p>")
	if err == nil || name != "t.html" {
		t.Fatalf("name = %q, err = %v", name, err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Error(err)
	}
}

func TestSlugify(t *testing.T) {
	for title, want := range map[string]string{
		"The Big Idea!":        "the-big-idea",
		"!!!":                  "summary",
		"":                     "summary",
		"  --x--  ":            "x",
		"UPPER lower 123":      "upper-lower-123",
		"a/b\\c..d":            "a-b-c-d",
		"Ünïcode ß café":       "n-code-caf",
		"日本語 title":            "title",
		"İstanbul":             "i-stanbul",
		"Kelvin K":             "kelvin-k",
		"x\x00y\xffz":          "x-y-z",
		strings.Repeat("a", 3): "aaa",
	} {
		if got := files.Slugify(title); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", title, got, want)
		}
	}
	long := files.Slugify(strings.Repeat("ab ", 400))
	if len(long) > 240 || strings.HasSuffix(long, "-") || strings.HasPrefix(long, "-") {
		t.Errorf("long slug = %q (%d)", long, len(long))
	}
}

func TestSummaryDocumentEscapesTheTitleAndNotTheBody(t *testing.T) {
	doc := files.SummaryDocument(`<b>"Tom" & 'Jerry'</b>`, "<p>a & b</p>\n<p>c</p>")
	want := `&lt;b&gt;&quot;Tom&quot; &amp; &#39;Jerry&#39;&lt;/b&gt;`
	if strings.Count(doc, want) != 2 || !strings.Contains(doc, "<h1>"+want+"</h1>\n<p>a & b</p>\n<p>c</p>\n</main>") {
		t.Errorf("document:\n%s", doc)
	}
}

// --- Ruby parity ---

const goldenServedHTML = "../../testdata/golden/served_html.html"

// The Rails runner's output for ServedHtmlWriter.document("A & B", "<p>x</p>"),
// saved so the check survives the Ruby code's removal.
func TestSummaryDocumentMatchesTheGoldenFile(t *testing.T) {
	want, err := os.ReadFile(goldenServedHTML)
	if err != nil {
		t.Fatal(err)
	}
	if got := files.SummaryDocument("A & B", "<p>x</p>"); got != string(want) {
		t.Errorf("document differs from %s:\n got: %q\nwant: %q", goldenServedHTML, got, want)
	}
	sum := sha256.Sum256(want)
	if got := hex.EncodeToString(sum[:]); got != goldenServedHTMLSHA256 {
		t.Errorf("golden file sha256 = %s, want %s (the golden file was edited)", got, goldenServedHTMLSHA256)
	}
}

const goldenServedHTMLSHA256 = "e45f91c2117664b5581a506ccc4f506fa77d95a21185bcea7b44c873db1328d7"

const writerRuby = "served_html_writer.rb"

// TestWriterMatchesRubyInterpreter loads the Ruby class (its class methods need
// no Rails) and compares Slugify and SummaryDocument on awkward inputs. It
// skips only when the Ruby source is gone or there is no ruby binary at all.
func TestWriterMatchesRubyInterpreter(t *testing.T) {
	path := filepath.Join("..", "..", "app", "services", writerRuby)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("the Ruby source is gone; the golden file still guards the document")
	}
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("no ruby binary on PATH; the golden file still checks the document")
	}
	titles := []string{
		"The Big Idea!", "!!!", "", "A & B", `"quote" 'single' <b>bold</b>`, "İstanbul Ünïcode ß", "日本語 title",
		"  --x--  ", "UPPER lower 123", "a/b\\c..d", "Kelvin K", "emoji 🎉 party", "tab\tand\nnewline", "ÀÉÎ",
	}
	bodies := []string{"<p>x</p>", "", "<p>a</p>\n<p>b</p>\n", "  <h2>indented</h2>\n    <p>more</p>", "<p>é & ü</p>"}
	in, _ := json.Marshal(map[string]any{"titles": titles, "bodies": bodies})
	const script = `
require "json"
require "cgi"
require "pathname"
require "active_support/core_ext/object/blank"
module ResolvesServedFiles; end
load ARGV[0]
input = JSON.parse($stdin.read)
out = { "slugs" => input["titles"].map { |t| ServedHtmlWriter.slugify(t) },
        "docs" => input["titles"].flat_map { |t| input["bodies"].map { |b| ServedHtmlWriter.document(t, b) } },
        "css" => ServedHtmlWriter::STYLESHEET }
$stdout.binmode
print JSON.generate(out)
`
	run := func(env ...string) ([]byte, error) {
		cmd := exec.Command(ruby, "-e", script, path)
		cmd.Stdin = strings.NewReader(string(in))
		cmd.Env = append(os.Environ(), env...)
		return cmd.Output()
	}
	out, err := run()
	if err != nil {
		if v, rerr := os.ReadFile(filepath.Join("..", "..", ".ruby-version")); rerr == nil {
			out, err = run("RBENV_VERSION=" + strings.TrimSpace(string(v)))
		}
	}
	if err != nil {
		var stderr []byte
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = ee.Stderr
		}
		t.Fatalf("ruby (%s) failed: %v\n%s", ruby, err, stderr)
	}
	var got struct {
		Slugs []string
		Docs  []string
		CSS   string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.CSS != files.SummaryStylesheet {
		t.Errorf("SummaryStylesheet differs from Ruby's STYLESHEET")
	}
	for i, title := range titles {
		if want := got.Slugs[i]; files.Slugify(title) != want {
			t.Errorf("Slugify(%q) = %q, Ruby says %q", title, files.Slugify(title), want)
		}
		for j, body := range bodies {
			if want := got.Docs[i*len(bodies)+j]; files.SummaryDocument(title, body) != want {
				t.Errorf("SummaryDocument(%q, %q) differs from Ruby's:\n go:   %q\n ruby: %q",
					title, body, files.SummaryDocument(title, body), want)
			}
		}
	}
}
