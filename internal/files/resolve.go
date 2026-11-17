package files

import (
	"os"
	"path/filepath"
	"strings"

	"serve-html-markdown/internal/store"
)

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
// when it points inside it). Names with NUL bytes are a BadRequest. The
// returned path is the symlink-resolved one.
func Resolve(dir, name string) (string, error) {
	if strings.ContainsRune(name, 0) {
		return "", BadRequest("Invalid file path.")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	p := name
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	p = filepath.Clean(p)
	if !within(root, p) {
		return "", BadRequest("Invalid file path.")
	}
	if !store.HasAllowedExtension(filepath.Base(p)) {
		return "", ErrUnsupported
	}
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return "", Missing("File not found: " + name)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", Missing("File not found: " + name)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil || !within(realRoot, real) {
		return "", BadRequest("Invalid file path.")
	}
	return real, nil
}

// within reports whether p is strictly below root (both clean and absolute).
func within(root, p string) bool {
	return strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}
