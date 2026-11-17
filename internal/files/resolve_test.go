package files_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"serve-html-markdown/internal/files"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// filesDir is a "files" directory with a sibling parent directory to escape to.
func filesDir(t *testing.T) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "files")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func wantError(t *testing.T, err error, kind error, status int, detail string) {
	t.Helper()
	if !errors.Is(err, kind) {
		t.Fatalf("err = %v, want kind %v", err, kind)
	}
	s, d, ok := files.StatusOf(err)
	if !ok || s != status || d != detail {
		t.Fatalf("StatusOf = %d %q %v, want %d %q", s, d, ok, status, detail)
	}
}

func TestResolveServesAllowedFiles(t *testing.T) {
	dir := filesDir(t)
	for _, name := range []string{"a.md", "b.HTML", "c.markdown"} {
		write(t, filepath.Join(dir, name), "x")
		got, err := files.Resolve(dir, name)
		if err != nil || got != filepath.Join(dir, name) {
			t.Fatalf("%s: got %q, %v", name, got, err)
		}
	}
	// An absolute name inside the directory resolves, like Pathname#join.
	if got, err := files.Resolve(dir, filepath.Join(dir, "a.md")); err != nil || got != filepath.Join(dir, "a.md") {
		t.Fatalf("absolute inside: %q, %v", got, err)
	}
	// A symlink that stays inside resolves to its target.
	if err := os.Symlink(filepath.Join(dir, "a.md"), filepath.Join(dir, "alias.md")); err != nil {
		t.Fatal(err)
	}
	if got, err := files.Resolve(dir, "alias.md"); err != nil || got != filepath.Join(dir, "a.md") {
		t.Fatalf("inner symlink: %q, %v", got, err)
	}
}

func TestResolveRejectsTraversal(t *testing.T) {
	dir := filesDir(t)
	write(t, filepath.Join(filepath.Dir(dir), "secret.md"), "secret")
	for _, name := range []string{"../secret.md", "../etc/passwd.md", "sub/../../secret.md", "", ".", "..", "a\x00.md", "/etc/passwd.md", filepath.Join(filepath.Dir(dir), "secret.md")} {
		_, err := files.Resolve(dir, name)
		wantError(t, err, files.ErrBadRequest, 400, "Invalid file path.")
	}
}

func TestResolveOrderOfChecks(t *testing.T) {
	dir := filesDir(t)
	// Traversal wins over the extension check.
	_, err := files.Resolve(dir, "../x.txt")
	wantError(t, err, files.ErrBadRequest, 400, "Invalid file path.")
	// The extension check wins over the missing check.
	_, err = files.Resolve(dir, "a.txt")
	wantError(t, err, files.ErrUnsupported, 404, "Only .html, .md, and .markdown files are supported.")
	if err != files.ErrUnsupported {
		t.Fatalf("want the ErrUnsupported value, got %v", err)
	}
	// Existing unsupported file, same answer.
	write(t, filepath.Join(dir, "private.txt"), "not served")
	_, err = files.Resolve(dir, "private.txt")
	wantError(t, err, files.ErrUnsupported, 404, "Only .html, .md, and .markdown files are supported.")
	// Leading dots are not an extension separator (File.extname).
	_, err = files.Resolve(dir, ".md")
	wantError(t, err, files.ErrUnsupported, 404, "Only .html, .md, and .markdown files are supported.")
}

func TestResolveMissing(t *testing.T) {
	dir := filesDir(t)
	_, err := files.Resolve(dir, "missing.md")
	wantError(t, err, files.ErrMissing, 404, "File not found: missing.md")
	if err := os.Mkdir(filepath.Join(dir, "folder.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = files.Resolve(dir, "folder.md")
	wantError(t, err, files.ErrMissing, 404, "File not found: folder.md")
	if err := os.Symlink(filepath.Join(dir, "nowhere.md"), filepath.Join(dir, "dangling.md")); err != nil {
		t.Fatal(err)
	}
	_, err = files.Resolve(dir, "dangling.md")
	wantError(t, err, files.ErrMissing, 404, "File not found: dangling.md")
}

func TestResolveRejectsSymlinkOutside(t *testing.T) {
	dir := filesDir(t)
	secret := filepath.Join(filepath.Dir(dir), "secret.md")
	write(t, secret, "secret")
	if err := os.Symlink(secret, filepath.Join(dir, "linked.md")); err != nil {
		t.Fatal(err)
	}
	_, err := files.Resolve(dir, "linked.md")
	wantError(t, err, files.ErrBadRequest, 400, "Invalid file path.")
}

func TestResolveThroughSymlinkedFilesDir(t *testing.T) {
	dir := filesDir(t)
	write(t, filepath.Join(dir, "a.md"), "x")
	link := filepath.Join(filepath.Dir(dir), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	got, err := files.Resolve(link, "a.md")
	if err != nil || got != filepath.Join(dir, "a.md") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestErrorKinds(t *testing.T) {
	err := files.BadRequest("nope")
	if !errors.Is(err, files.ErrBadRequest) || errors.Is(err, files.ErrMissing) || errors.Is(err, files.ErrUnsupported) {
		t.Fatalf("kind matching: %v", err)
	}
	var e *files.Error
	if !errors.As(files.Missing("gone"), &e) || e.Kind != files.KindMissing || e.Message != "gone" || e.Error() != "gone" {
		t.Fatalf("errors.As: %+v", e)
	}
	if _, _, ok := files.StatusOf(errors.New("plain")); ok {
		t.Fatal("StatusOf matched a plain error")
	}
}
