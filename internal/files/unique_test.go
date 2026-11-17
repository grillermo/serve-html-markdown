package files_test

import (
	"os"
	"path/filepath"
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
