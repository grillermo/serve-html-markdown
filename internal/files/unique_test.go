package files_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"serve-html-markdown/internal/files"
)

func TestUniquePath(t *testing.T) {
	dir := filesDir(t)
	got, err := files.UniquePath(dir, "notes", ".md")
	if err != nil || got != filepath.Join(dir, "notes.md") {
		t.Fatalf("first: %q, %v", got, err)
	}
	write(t, got, "x")
	got, err = files.UniquePath(dir, "notes", ".md")
	if err != nil || got != filepath.Join(dir, "notes-1.md") {
		t.Fatalf("second: %q, %v", got, err)
	}
	write(t, got, "x")
	if got, _ = files.UniquePath(dir, "notes", ".md"); got != filepath.Join(dir, "notes-2.md") {
		t.Fatalf("third: %q", got)
	}
	// A dangling symlink holds the name too.
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "linked.md")); err != nil {
		t.Fatal(err)
	}
	if got, _ = files.UniquePath(dir, "linked.md", ".md"); got != filepath.Join(dir, "linked-1.md") {
		t.Fatalf("dangling: %q", got)
	}
}

func TestUniquePathSanitisesFilenames(t *testing.T) {
	dir := filesDir(t)
	write(t, filepath.Join(dir, "note.md"), "existing")
	for filename, want := range map[string]string{
		"../nested/note.txt": "note-1.md", // the Rails controller test: stem kept, extension replaced
		`..\evil`:            "evil.md",
		`a\b\c.markdown`:     "c.md",
		"dir/":               "dir.md",
		"report-v2.md":       "report-v2.md",
		"report--version.md": "report--version.md",
		"a.b.c":              "a.b.md",
		"foo.":               "foo.md",
		".hidden":            ".hidden.md",
		" spaced ":           " spaced .md",
	} {
		got, err := files.UniquePath(dir, filename, ".md")
		if err != nil || got != filepath.Join(dir, want) {
			t.Errorf("%q: got %q, %v, want %q", filename, got, err, want)
		}
	}
	// The extension argument is used as given.
	if got, _ := files.UniquePath(dir, "page.htm", ".html"); got != filepath.Join(dir, "page.html") {
		t.Errorf("html ext: %q", got)
	}
}

func TestUniquePathRejectsBadStems(t *testing.T) {
	dir := filesDir(t)
	for _, filename := range []string{"", ".", "..", "...md", "/", "  ", "\t.md", "a/..", `..\`, "..\\..", "a\x00b", "bad\xffname"} {
		_, err := files.UniquePath(dir, filename, ".md")
		if filename == "...md" { // stem "..." is a legal (odd) name, not "." or ".."
			if err != nil {
				t.Errorf("%q: %v", filename, err)
			}
			continue
		}
		wantError(t, err, files.ErrBadRequest, 400, "Invalid filename.")
	}
}

func TestUniquePathRejectsReservedSuffix(t *testing.T) {
	dir := filesDir(t)
	for _, filename := range []string{"foo--v2", "report--v2.md", "report--v10.md", "x--v1.md", "x--v007.md", `a\foo--v3.html`} {
		_, err := files.UniquePath(dir, filename, ".md")
		wantError(t, err, files.ErrBadRequest, 400, "Filenames may not use the reserved --v<number> suffix.")
	}
	for _, filename := range []string{"foo--v2x", "foo-v2", "foo--v", "foo--v2.md.txt"} {
		if _, err := files.UniquePath(dir, filename, ".md"); err != nil && filename != "foo--v2.md.txt" {
			t.Errorf("%q: %v", filename, err)
		}
	}
}

func TestCreateUnique(t *testing.T) {
	dir := filesDir(t)
	write(t, filepath.Join(dir, "note.md"), "existing")
	got, err := files.CreateUnique(dir, "../nested/note.txt", ".md", []byte("new"))
	if err != nil || got != filepath.Join(dir, "note-1.md") {
		t.Fatalf("got %q, %v", got, err)
	}
	if b, _ := os.ReadFile(got); string(b) != "new" {
		t.Fatalf("content %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "note.md")); string(b) != "existing" {
		t.Fatalf("existing overwritten: %q", b)
	}
	_, err = files.CreateUnique(dir, "x--v2.md", ".md", nil)
	wantError(t, err, files.ErrBadRequest, 400, "Filenames may not use the reserved --v<number> suffix.")
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Fatalf("unexpected files: %v", entries)
	}
}

func TestCreateUniqueConcurrentNeverCollides(t *testing.T) {
	dir := filesDir(t)
	const n = 24
	var wg sync.WaitGroup
	paths := make([]string, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths[i], errs[i] = files.CreateUnique(dir, "same", ".md", []byte{byte('a' + i)})
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for i := range n {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if seen[paths[i]] {
			t.Fatalf("duplicate path %s", paths[i])
		}
		seen[paths[i]] = true
		if b, _ := os.ReadFile(paths[i]); len(b) != 1 || b[0] != byte('a'+i) {
			t.Fatalf("%s holds %q, want writer %d's data", paths[i], b, i)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != n {
		t.Fatalf("%d files, want %d", len(entries), n)
	}
}

func TestUniqueFunctionsOnlyAcceptServedExtensions(t *testing.T) {
	dir := filesDir(t)
	for _, ext := range []string{"/../a.md", ".lock", "", "md", ".txt", ".md.lock", ".md/", "/.md", ".md\x00"} {
		_, err := files.UniquePath(dir, "ev", ext)
		wantError(t, err, files.ErrBadRequest, 400, "Invalid filename.")
		_, err = files.CreateUnique(dir, "ev", ext, nil)
		wantError(t, err, files.ErrBadRequest, 400, "Invalid filename.")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files created: %v", entries)
	}
	if entries, _ := os.ReadDir(filepath.Dir(dir)); len(entries) != 1 {
		t.Fatalf("files created next to dir: %v", entries)
	}
	for _, ext := range []string{".MD", ".Html", ".markdown"} {
		got, err := files.CreateUnique(dir, "ok", ext, []byte("x"))
		if err != nil || got != filepath.Join(dir, "ok"+ext) {
			t.Errorf("%s: %q, %v", ext, got, err)
		}
	}
}

func TestUniqueFunctionsGiveUpAfterManyAttempts(t *testing.T) {
	dir := filesDir(t)
	write(t, filepath.Join(dir, "x.md"), "x")
	for i := 1; i < 10000; i++ {
		write(t, filepath.Join(dir, fmt.Sprintf("x-%d.md", i)), "x")
	}
	_, err := files.UniquePath(dir, "x", ".md")
	if err == nil || errors.Is(err, files.ErrBadRequest) {
		t.Fatalf("UniquePath: %v", err)
	}
	_, err = files.CreateUnique(dir, "x", ".md", nil)
	if err == nil || errors.Is(err, files.ErrBadRequest) {
		t.Fatalf("CreateUnique: %v", err)
	}
}

func TestUniqueFunctionsMapNameTooLong(t *testing.T) {
	dir := filesDir(t)
	long := strings.Repeat("a", 300)
	_, err := files.UniquePath(dir, long, ".md")
	wantError(t, err, files.ErrBadRequest, 400, "Invalid filename.")
	_, err = files.CreateUnique(dir, long, ".md", nil)
	wantError(t, err, files.ErrBadRequest, 400, "Invalid filename.")

	// 250 + ".md" fits; the -10 suffix pushes the 11th name past 255 bytes.
	stem := strings.Repeat("b", 250)
	for i := range 10 {
		if _, err := files.CreateUnique(dir, stem, ".md", []byte("x")); err != nil {
			t.Fatalf("file %d: %v", i, err)
		}
	}
	_, err = files.UniquePath(dir, stem, ".md")
	wantError(t, err, files.ErrBadRequest, 400, "Invalid filename.")
	_, err = files.CreateUnique(dir, stem, ".md", nil)
	wantError(t, err, files.ErrBadRequest, 400, "Invalid filename.")
}

// Ruby: upload_extension (UPLOAD_EXTENSIONS).
func TestUploadExtension(t *testing.T) {
	ok := map[string]string{
		"a.html": ".html", "a.HTM": ".html", "dir/a.htm": ".html", `..\a.Md`: ".md",
		"a.markdown": ".md", "x/../a.MARKDOWN": ".md", "a.b.md": ".md", "a.md/": ".md", ".x.md": ".md",
	}
	for in, want := range ok {
		if got, err := files.UploadExtension(in); err != nil || got != want {
			t.Errorf("files.UploadExtension(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "a", "a.txt", ".md", "md", "a.md.txt", "a.md.", "dir.md/a", `dir.md\a`} {
		_, err := files.UploadExtension(in)
		status, detail, isFile := files.StatusOf(err)
		if !isFile || status != 400 || detail != "Only .html, .md, and .markdown files are supported." {
			t.Errorf("files.UploadExtension(%q) err = %v (%d %q)", in, err, status, detail)
		}
	}
}
