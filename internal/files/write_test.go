package files_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"serve-html-markdown/internal/files"
)

func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

func TestFreeIndexedNameCountsFromOne(t *testing.T) {
	dir := filesDir(t)
	name, err := files.FreeIndexedName(dir, "notes--expand-", ".html")
	if err != nil || name != "notes--expand-1.html" {
		t.Fatalf("got %q, %v", name, err)
	}
	write(t, filepath.Join(dir, "notes--expand-1.html"), "x")
	write(t, filepath.Join(dir, "notes--expand-3.html"), "x")
	if name, _ = files.FreeIndexedName(dir, "notes--expand-", ".html"); name != "notes--expand-2.html" {
		t.Fatalf("got %q", name)
	}
	// A dangling symlink holds a name too.
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "notes--expand-2.html")); err != nil {
		t.Fatal(err)
	}
	if name, _ = files.FreeIndexedName(dir, "notes--expand-", ".html"); name != "notes--expand-4.html" {
		t.Fatalf("got %q", name)
	}
}

func TestFreeIndexedNameRejectsBadNames(t *testing.T) {
	dir := filesDir(t)
	for _, c := range [][2]string{{"a/b--expand-", ".html"}, {"x-", ".lock"}, {"x-", ""}, {"a\x00", ".html"}, {"\xff", ".html"}} {
		_, err := files.FreeIndexedName(dir, c[0], c[1])
		wantError(t, err, files.ErrBadRequest, 400, "Invalid filename.")
	}
}

func TestCreateNewWritesAllOrNothing(t *testing.T) {
	dir := filesDir(t)
	if err := files.CreateNew(dir, "a--expand-1.html", []byte("<html>")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a--expand-1.html")); string(b) != "<html>" {
		t.Fatalf("content %q", b)
	}
	info, _ := os.Stat(filepath.Join(dir, "a--expand-1.html"))
	if want := fs.FileMode(0o666) &^ currentUmask(); info.Mode().Perm() != want {
		t.Errorf("mode = %v, want %v", info.Mode().Perm(), want)
	}
	if got := entries(t, dir); len(got) != 1 {
		t.Fatalf("temporary files left behind: %v", got)
	}
}

func currentUmask() fs.FileMode {
	old := syscall.Umask(0)
	syscall.Umask(old)
	return fs.FileMode(old)
}

func TestCreateNewNeverOverwrites(t *testing.T) {
	dir := filesDir(t)
	write(t, filepath.Join(dir, "a.html"), "existing")
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "dangling.html")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.html", "dangling.html"} {
		if err := files.CreateNew(dir, name, []byte("new")); !errors.Is(err, fs.ErrExist) {
			t.Errorf("%s: err = %v, want fs.ErrExist", name, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.html")); string(b) != "existing" {
		t.Errorf("overwritten: %q", b)
	}
	if _, err := os.Lstat(filepath.Join(dir, "nowhere")); !os.IsNotExist(err) {
		t.Errorf("wrote through the dangling symlink: %v", err)
	}
	if got := entries(t, dir); len(got) != 2 {
		t.Fatalf("temporary files left behind: %v", got)
	}
}

func TestCreateNewRejectsBadNames(t *testing.T) {
	dir := filesDir(t)
	for _, name := range []string{"", ".", "..", "../x.html", "sub/x.html", "x.txt", "x", "x\x00.html", ".lock"} {
		wantError(t, files.CreateNew(dir, name, nil), files.ErrBadRequest, 400, "Invalid filename.")
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Fatalf("created %v", got)
	}
}

func TestCreateNewConcurrentOnlyOneWins(t *testing.T) {
	dir := filesDir(t)
	const n = 16
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = files.CreateNew(dir, "same.html", []byte{byte('a' + i)})
		}()
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
		} else if !errors.Is(err, fs.ErrExist) {
			t.Fatal(err)
		}
	}
	if wins != 1 || len(entries(t, dir)) != 1 {
		t.Fatalf("wins = %d, entries = %v", wins, entries(t, dir))
	}
}

func TestReplaceFileKeepsModeAndIsWhole(t *testing.T) {
	dir := filesDir(t)
	path := filepath.Join(dir, "a.md")
	write(t, path, "old")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := files.ReplaceFile(dir, "a.md", []byte("new content")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(b) != "new content" || info.Mode().Perm() != 0o640 {
		t.Errorf("content %q mode %v", b, info.Mode().Perm())
	}
	if got := entries(t, dir); len(got) != 1 {
		t.Fatalf("temporary files left behind: %v", got)
	}
}

func TestReplaceFileInASubdirectoryAndThroughASymlinkTarget(t *testing.T) {
	dir := filesDir(t)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "sub", "real.md"), "old")
	if err := os.Symlink("sub/real.md", filepath.Join(dir, "link.md")); err != nil {
		t.Fatal(err)
	}
	// OpenFile resolves the link; ReplaceFile gets the canonical name.
	f, rel, err := files.OpenFile(dir, "link.md")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := files.ReplaceFile(dir, rel, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "sub", "real.md")); string(b) != "new" {
		t.Errorf("real file holds %q", b)
	}
	if target, _ := os.Readlink(filepath.Join(dir, "link.md")); target != "sub/real.md" {
		t.Errorf("symlink changed to %q", target)
	}
}

func TestReplaceFileStaysInsideDir(t *testing.T) {
	dir := filesDir(t)
	outside := filepath.Join(filepath.Dir(dir), "outside.md")
	write(t, outside, "keep")
	if err := files.ReplaceFile(dir, "../outside.md", []byte("x")); err == nil {
		t.Error("replaced a file outside dir")
	}
	if err := os.Symlink(outside, filepath.Join(dir, "esc.md")); err != nil {
		t.Fatal(err)
	}
	if err := files.ReplaceFile(dir, "esc.md", []byte("x")); err == nil {
		t.Error("followed a symlink out of dir")
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep" {
		t.Errorf("outside file holds %q", b)
	}
}

func TestReplaceFileMissingOrNotRegular(t *testing.T) {
	dir := filesDir(t)
	if err := files.ReplaceFile(dir, "missing.md", nil); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "d.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := files.ReplaceFile(dir, "d.md", nil); err == nil {
		t.Error("replaced a directory")
	}
}

func hardLinkFails(err error) func(*os.Root, string, string) error {
	return func(*os.Root, string, string) error { return &os.LinkError{Op: "link", Err: err} }
}

// A file system without hard links: the file is still created, whole, with no
// temporary file left.
func TestCreateNewFallsBackWhenHardLinksAreUnsupported(t *testing.T) {
	for _, errno := range []error{syscall.EPERM, syscall.ENOTSUP, syscall.EXDEV, syscall.ENOSYS} {
		dir := filesDir(t)
		files.SetLinkFile(t, hardLinkFails(errno))
		if err := files.CreateNew(dir, "a--expand-1.html", []byte("<html>")); err != nil {
			t.Fatalf("%v: %v", errno, err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "a--expand-1.html")); string(b) != "<html>" {
			t.Errorf("%v: content %q", errno, b)
		}
		if got := entries(t, dir); len(got) != 1 {
			t.Errorf("%v: files = %v", errno, got)
		}
	}
}

func TestCreateNewFallbackNeverOverwrites(t *testing.T) {
	dir := filesDir(t)
	files.SetLinkFile(t, hardLinkFails(syscall.EPERM))
	write(t, filepath.Join(dir, "a.html"), "existing")
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "dangling.html")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.html", "dangling.html"} {
		if err := files.CreateNew(dir, name, []byte("new")); !errors.Is(err, fs.ErrExist) {
			t.Errorf("%s: err = %v, want fs.ErrExist", name, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.html")); string(b) != "existing" {
		t.Errorf("overwritten: %q", b)
	}
	if _, err := os.Lstat(filepath.Join(dir, "nowhere")); !os.IsNotExist(err) {
		t.Errorf("wrote through the dangling symlink: %v", err)
	}
	if got := entries(t, dir); len(got) != 2 {
		t.Fatalf("files = %v", got)
	}
}

// Any other link failure is returned as is, and the temporary file is removed.
func TestCreateNewCleansUpWhenTheLinkFails(t *testing.T) {
	dir := filesDir(t)
	files.SetLinkFile(t, hardLinkFails(syscall.EIO))
	if err := files.CreateNew(dir, "a.html", []byte("x")); !errors.Is(err, syscall.EIO) {
		t.Fatalf("err = %v", err)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Fatalf("left behind: %v", got)
	}
}
