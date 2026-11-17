package files_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
	if err == files.ErrUnsupported {
		t.Fatal("Resolve must return a fresh error, not the shared sentinel")
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

// A case is run through Resolve and OpenFile, which must agree.
type resolveCase struct {
	name   string
	file   string // name to resolve; "$DIR" and "$PARENT" are replaced
	setup  func(t *testing.T, dir, parent string)
	want   string // canonical name relative to dir when it resolves
	kind   error
	status int
	detail string
}

func TestResolveAndOpenFileAgree(t *testing.T) {
	const invalid = "Invalid file path."
	const unsupportedMsg = "Only .html, .md, and .markdown files are supported."
	secret := func(t *testing.T, dir, parent string) { write(t, filepath.Join(parent, "secret.md"), "secret") }
	both := func(fs ...func(*testing.T, string, string)) func(*testing.T, string, string) {
		return func(t *testing.T, dir, parent string) {
			for _, f := range fs {
				f(t, dir, parent)
			}
		}
	}
	page := func(t *testing.T, dir, parent string) { write(t, filepath.Join(dir, "a.md"), "page") }
	cases := []resolveCase{
		{name: "plain", file: "a.md", setup: page, want: "a.md"},
		{name: "upper-case extension", file: "b.HTML", setup: func(t *testing.T, dir, parent string) { write(t, filepath.Join(dir, "b.HTML"), "x") }, want: "b.HTML"},
		{name: "subdirectory", file: "sub/x.markdown", setup: func(t *testing.T, dir, parent string) {
			os.Mkdir(filepath.Join(dir, "sub"), 0o755)
			write(t, filepath.Join(dir, "sub", "x.markdown"), "x")
		}, want: "sub/x.markdown"},
		{name: "absolute name inside", file: "$DIR/a.md", setup: page, want: "a.md"},
		{name: "relative symlink inside", file: "alias.md", setup: both(page, func(t *testing.T, dir, parent string) {
			os.Symlink("a.md", filepath.Join(dir, "alias.md"))
		}), want: "a.md"},
		{name: "absolute symlink inside", file: "alias.md", setup: both(page, func(t *testing.T, dir, parent string) {
			os.Symlink(filepath.Join(dir, "a.md"), filepath.Join(dir, "alias.md"))
		}), want: "a.md"},
		{name: "dot-dot traversal", file: "../secret.md", setup: secret, kind: files.ErrBadRequest, status: 400, detail: invalid},
		{name: "traversal through a subdirectory", file: "sub/../../secret.md", setup: secret, kind: files.ErrBadRequest, status: 400, detail: invalid},
		{name: "traversal to an unsupported name", file: "../x.txt", kind: files.ErrBadRequest, status: 400, detail: invalid},
		{name: "absolute name outside", file: "$PARENT/secret.md", setup: secret, kind: files.ErrBadRequest, status: 400, detail: invalid},
		{name: "absolute system file", file: "/etc/passwd.md", kind: files.ErrBadRequest, status: 400, detail: invalid},
		{name: "empty", file: "", kind: files.ErrBadRequest, status: 400, detail: invalid},
		{name: "dot", file: ".", kind: files.ErrBadRequest, status: 400, detail: invalid},
		{name: "NUL byte", file: "a\x00.md", kind: files.ErrBadRequest, status: 400, detail: invalid},
		{name: "unsupported existing", file: "private.txt", setup: func(t *testing.T, dir, parent string) { write(t, filepath.Join(dir, "private.txt"), "x") }, kind: files.ErrUnsupported, status: 404, detail: unsupportedMsg},
		{name: "unsupported missing", file: "a.txt", kind: files.ErrUnsupported, status: 404, detail: unsupportedMsg},
		{name: "extension only", file: ".md", kind: files.ErrUnsupported, status: 404, detail: unsupportedMsg},
		{name: "missing", file: "missing.md", kind: files.ErrMissing, status: 404, detail: "File not found: missing.md"},
		{name: "directory", file: "folder.md", setup: func(t *testing.T, dir, parent string) { os.Mkdir(filepath.Join(dir, "folder.md"), 0o755) }, kind: files.ErrMissing, status: 404, detail: "File not found: folder.md"},
		{name: "dangling symlink", file: "dangling.md", setup: func(t *testing.T, dir, parent string) {
			os.Symlink(filepath.Join(dir, "nowhere.md"), filepath.Join(dir, "dangling.md"))
		}, kind: files.ErrMissing, status: 404, detail: "File not found: dangling.md"},
		{name: "dangling symlink to outside", file: "dangling.md", setup: func(t *testing.T, dir, parent string) {
			os.Symlink(filepath.Join(parent, "nowhere.md"), filepath.Join(dir, "dangling.md"))
		}, kind: files.ErrMissing, status: 404, detail: "File not found: dangling.md"},
		{name: "fifo", file: "x.md", setup: func(t *testing.T, dir, parent string) {
			if err := syscall.Mkfifo(filepath.Join(dir, "x.md"), 0o644); err != nil {
				t.Skip("mkfifo:", err)
			}
		}, kind: files.ErrMissing, status: 404, detail: "File not found: x.md"},
		{name: "percent-encoded dot-dot dir", file: "%2e%2e/secret.md", setup: secret, kind: files.ErrMissing, status: 404, detail: "File not found: %2e%2e/secret.md"},
		{name: "percent-encoded dot-dot and slash", file: "%2e%2e%2fsecret.md", setup: secret, kind: files.ErrMissing, status: 404, detail: "File not found: %2e%2e%2fsecret.md"},
		{name: "percent-encoded slash", file: "..%2fsecret.md", setup: secret, kind: files.ErrMissing, status: 404, detail: "File not found: ..%2fsecret.md"},
		{name: "name too long", file: strings.Repeat("a", 300) + ".md", kind: files.ErrMissing, status: 404, detail: "File not found: " + strings.Repeat("a", 300) + ".md"},
		{name: "symlink to a file outside", file: "linked.md", setup: both(secret, func(t *testing.T, dir, parent string) {
			os.Symlink(filepath.Join(parent, "secret.md"), filepath.Join(dir, "linked.md"))
		}), kind: files.ErrBadRequest, status: 400, detail: invalid},
		{name: "relative symlink to a file outside", file: "linked.md", setup: both(secret, func(t *testing.T, dir, parent string) {
			os.Symlink("../secret.md", filepath.Join(dir, "linked.md"))
		}), kind: files.ErrBadRequest, status: 400, detail: invalid},
		{name: "symlinked directory outside", file: "sub/secret.md", setup: both(secret, func(t *testing.T, dir, parent string) {
			os.Symlink(parent, filepath.Join(dir, "sub"))
		}), kind: files.ErrBadRequest, status: 400, detail: invalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filesDir(t)
			parent := filepath.Dir(dir)
			if c.setup != nil {
				c.setup(t, dir, parent)
			}
			name := strings.NewReplacer("$DIR", dir, "$PARENT", parent).Replace(c.file)

			path, rerr := files.Resolve(dir, name)
			f, rel, oerr := files.OpenFile(dir, name)
			if f != nil {
				defer f.Close()
			}
			if c.kind == nil {
				if rerr != nil || oerr != nil {
					t.Fatalf("Resolve: %v, OpenFile: %v", rerr, oerr)
				}
				if want := filepath.Join(dir, filepath.FromSlash(c.want)); path != want {
					t.Fatalf("Resolve = %q, want %q", path, want)
				}
				if rel != filepath.FromSlash(c.want) {
					t.Fatalf("OpenFile name = %q, want %q", rel, c.want)
				}
				if b, err := io.ReadAll(f); err != nil || len(b) == 0 {
					t.Fatalf("read: %q, %v", b, err)
				}
				return
			}
			if path != "" || f != nil || rel != "" {
				t.Fatalf("results on error: %q %v %q", path, f, rel)
			}
			wantError(t, rerr, c.kind, c.status, c.detail)
			wantError(t, oerr, c.kind, c.status, c.detail)
		})
	}
}

func TestOpenFileMissingDirectory(t *testing.T) {
	_, _, err := files.OpenFile(filepath.Join(t.TempDir(), "nope"), "a.md")
	wantError(t, err, files.ErrMissing, 404, "File not found: a.md")
}

// caseInsensitive reports whether the temp file system folds case.
func caseInsensitive(t *testing.T, dir string) bool {
	t.Helper()
	write(t, filepath.Join(dir, "probe.md"), "x")
	_, err := os.Stat(filepath.Join(dir, "PROBE.MD"))
	os.Remove(filepath.Join(dir, "probe.md"))
	return err == nil
}

func TestResolveReportsOnDiskCase(t *testing.T) {
	dir := filesDir(t)
	if !caseInsensitive(t, dir) {
		t.Skip("case-sensitive file system")
	}
	write(t, filepath.Join(dir, "Notes.md"), "x")
	want := filepath.Join(dir, "Notes.md")
	for _, name := range []string{"notes.md", "NOTES.MD", "Notes.md", "nOTES.md"} {
		got, err := files.Resolve(dir, name)
		if err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v, want %q", name, got, err, want)
		}
		f, rel, err := files.OpenFile(dir, name)
		if err != nil || rel != "Notes.md" {
			t.Errorf("OpenFile(%q) name = %q, %v", name, rel, err)
		}
		if f != nil {
			f.Close()
		}
	}
	// A symlink resolves to the target's stored case.
	if err := os.Symlink("Notes.md", filepath.Join(dir, "Alias.md")); err != nil {
		t.Fatal(err)
	}
	if got, err := files.Resolve(dir, "alias.md"); err != nil || got != want {
		t.Errorf("alias: %q, %v", got, err)
	}
}

func TestResolveKeepsCaseOnCaseSensitiveFileSystem(t *testing.T) {
	dir := filesDir(t)
	if caseInsensitive(t, dir) {
		t.Skip("case-insensitive file system")
	}
	write(t, filepath.Join(dir, "a.md"), "lower")
	write(t, filepath.Join(dir, "A.md"), "upper")
	for _, name := range []string{"a.md", "A.md"} {
		if got, err := files.Resolve(dir, name); err != nil || got != filepath.Join(dir, name) {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
	_, err := files.Resolve(dir, "A.MD")
	wantError(t, err, files.ErrMissing, 404, "File not found: A.MD")
}
