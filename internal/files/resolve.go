package files

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"

	"serve-html-markdown/internal/store"
)

func invalidPath() error { return BadRequest("Invalid file path.") }

// unsupported returns a fresh *Error each time, so callers may keep or change
// it without touching the shared ErrUnsupported sentinel.
func unsupported() error {
	return &Error{Kind: KindUnsupported, Message: ErrUnsupported.Message}
}

// Resolve maps a requested file name to the real path of a served file inside
// dir. The checks run in the order the Rails app ran them, so the error for a
// given name is the same:
//
//  1. the cleaned path must stay inside dir        -> BadRequest "Invalid file path."
//  2. the extension must be served                 -> ErrUnsupported
//  3. the path must be a regular file (symlinks followed) -> Missing "File not found: <name>"
//  4. the symlink-resolved path must stay inside the resolved dir
//     -> BadRequest "Invalid file path."
//
// Like Pathname#join, an absolute name replaces dir (and so is only accepted
// when it points inside it). Names with NUL bytes are a BadRequest, and a name
// the file system cannot look up (too long, ...) is Missing. The returned path
// is symlink-resolved and its last component has the on-disk case, even on a
// case-insensitive file system.
//
// Resolve returns a path, and the caller opens it later. It is therefore not
// safe against someone who can write to dir (or the directories it links to)
// and swaps a component for a symlink between Resolve and the open. Use
// OpenFile when that matters.
func Resolve(dir, name string) (string, error) {
	_, real, _, err := resolve(dir, name)
	return real, err
}

// resolve runs the checks of Resolve and returns the lexical path, the real
// path and the real (symlink-resolved) root.
func resolve(dir, name string) (p, real, realRoot string, err error) {
	root, p, err := lexical(dir, name)
	if err != nil {
		return "", "", "", err
	}
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", "", Missing("File not found: " + name)
	}
	real, err = filepath.EvalSymlinks(p)
	if err != nil {
		return "", "", "", Missing("File not found: " + name)
	}
	realRoot, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", "", invalidPath()
	}
	real = canonicalCase(real)
	if !within(realRoot, real) {
		return "", "", "", invalidPath()
	}
	return p, real, realRoot, nil
}

// lexical runs checks 1 and 2 of Resolve and returns the absolute root and the
// cleaned absolute path.
func lexical(dir, name string) (root, p string, err error) {
	if strings.ContainsRune(name, 0) {
		return "", "", invalidPath()
	}
	root, err = filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	p = name
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	p = filepath.Clean(p)
	if !within(root, p) {
		return "", "", invalidPath()
	}
	if !store.HasAllowedExtension(filepath.Base(p)) {
		return "", "", unsupported()
	}
	return root, p, nil
}

// OpenFile is Resolve for callers that read the file: it applies the same
// checks in the same order with the same errors, but opens the file through an
// os.Root on dir, so the open itself can never leave dir, whatever happens to
// the path after the checks. It returns the open file (a regular file, at the
// start) and its canonical name relative to dir: symlinks resolved, on-disk
// case. The caller closes the file.
//
// The returned name is computed with a separate path lookup, so under a race
// it may not be the file that was opened; the open file is the one to trust.
func OpenFile(dir, name string) (*os.File, string, error) {
	root, p, err := lexical(dir, name)
	if err != nil {
		return nil, "", err
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return nil, "", invalidPath()
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", Missing("File not found: " + name)
		}
		return nil, "", err
	}
	defer r.Close()

	f, err := openRegular(r, rel)
	if err != nil {
		// Work out which error Resolve would give. If it finds nothing wrong,
		// the path goes through a symlink with an absolute target that
		// points back inside dir, which os.Root refuses: retry on the real
		// relative path.
		_, real, realRoot, rerr := resolve(root, name)
		if rerr != nil {
			return nil, "", rerr
		}
		realRel, rerr := filepath.Rel(realRoot, real)
		if rerr != nil {
			return nil, "", invalidPath()
		}
		if f, err = openRegular(r, realRel); err != nil {
			return nil, "", Missing("File not found: " + name)
		}
	}

	canonical := rel
	if _, real, realRoot, err := resolve(root, name); err == nil {
		if r, err := filepath.Rel(realRoot, real); err == nil {
			canonical = r
		}
	}
	return f, canonical, nil
}

// openRegular opens rel for reading and closes it again unless it is a regular
// file. O_NONBLOCK keeps a FIFO from blocking the open.
func openRegular(r *os.Root, rel string) (*os.File, error) {
	f, err := r.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fs.ErrNotExist
	}
	return f, nil
}

// within reports whether p is strictly below root (both clean and absolute).
func within(root, p string) bool {
	return strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

// canonicalCase returns p with its last component spelled as it is on disk.
// On a case-insensitive file system os.Stat accepts any case, and Ruby's
// realpath reports the stored one; the app uses the name of the result to look
// up rows. p must be the symlink-resolved path of an existing file.
//
// It is cheap on case-sensitive file systems: one Lstat of the name with its
// case swapped, which finds nothing. Only when that name leads to the same file
// is the directory read to find the stored name.
func canonicalCase(p string) string {
	dir, base := filepath.Split(p)
	swapped := swapCase(base)
	if swapped == base {
		return p
	}
	info, err := os.Lstat(p)
	if err != nil {
		return p
	}
	other, err := os.Lstat(dir + swapped)
	if err != nil || !os.SameFile(info, other) {
		return p
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return p
	}
	match := ""
	for _, e := range entries {
		if e.Name() == base {
			return p // the exact name exists, so this is not a case alias
		}
		if match == "" && strings.EqualFold(e.Name(), base) {
			if ei, err := os.Lstat(dir + e.Name()); err == nil && os.SameFile(info, ei) {
				match = e.Name()
			}
		}
	}
	if match == "" {
		return p
	}
	return dir + match
}

// swapCase flips the case of every letter in s.
func swapCase(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case unicode.IsUpper(r):
			return unicode.ToLower(r)
		case unicode.IsLower(r):
			return unicode.ToUpper(r)
		}
		return r
	}, s)
}
