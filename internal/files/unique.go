package files

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"serve-html-markdown/internal/store"
)

// rubyBasename is File.basename for a "/"-separated path: trailing slashes are
// ignored, "" stays "" and "/" stays "/".
func rubyBasename(s string) string {
	if s == "" {
		return ""
	}
	s = strings.TrimRight(s, "/")
	if s == "" {
		return "/"
	}
	return s[strings.LastIndex(s, "/")+1:]
}

// splitName is the Ruby base name, its extension (File.extname) and the base
// name without that extension (File.basename(name, ext)).
func splitName(name string) (base, ext, stem string) {
	base = rubyBasename(name)
	ext = store.Ext(base)
	stem = strings.TrimSuffix(base, ext)
	return base, ext, stem
}

// Stem sanitises a user-supplied file name the way uploads did: backslashes
// become slashes, the base name is taken and its extension dropped. It fails
// with BadRequest "Invalid filename." for a blank stem, ".", "..", a lone "/",
// or text that cannot be a file name (NUL bytes, invalid UTF-8), and with the
// reserved-suffix message for a stem ending in --v<number>.
func Stem(filename string) (string, error) {
	if !utf8.ValidString(filename) || strings.ContainsRune(filename, 0) {
		return "", BadRequest("Invalid filename.")
	}
	_, _, stem := splitName(strings.ReplaceAll(filename, `\`, "/"))
	if strings.TrimSpace(stem) == "" || stem == "." || stem == ".." || strings.Contains(stem, "/") {
		return "", BadRequest("Invalid filename.")
	}
	if ReservedSuffix(stem) {
		return "", BadRequest("Filenames may not use the reserved --v<number> suffix.")
	}
	return stem, nil
}

// candidate is the path for the n-th try: stem+ext, then stem-1+ext, ...
func candidate(root, stem string, n int, ext string) (string, error) {
	suffix := ""
	if n > 0 {
		suffix = "-" + strconv.Itoa(n)
	}
	p := filepath.Clean(filepath.Join(root, stem+suffix+ext))
	if !within(root, p) {
		return "", BadRequest("Invalid filename.")
	}
	return p, nil
}

// exists reports whether anything, a dangling symlink included, is at p.
func exists(p string) (bool, error) {
	_, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// UniquePath returns the first free path in dir for a new file derived from
// filename (see Stem) with extension ext (such as ".md"): <stem><ext>, then
// <stem>-1<ext>, <stem>-2<ext>, and so on. It does not create anything, so
// two callers can get the same path; use CreateUnique to claim it atomically.
func UniquePath(dir, filename, ext string) (string, error) {
	stem, err := Stem(filename)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for n := 0; ; n++ {
		p, err := candidate(root, stem, n, ext)
		if err != nil {
			return "", err
		}
		taken, err := exists(p)
		if err != nil {
			return "", err
		}
		if !taken {
			return p, nil
		}
	}
}

// CreateUnique is UniquePath plus the write: it claims the name with
// O_CREATE|O_EXCL, so concurrent callers never get the same file, then writes
// data to it. It returns the path. A failed write removes the new file.
func CreateUnique(dir, filename, ext string, data []byte) (string, error) {
	stem, err := Stem(filename)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for n := 0; ; n++ {
		p, err := candidate(root, stem, n, ext)
		if err != nil {
			return "", err
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.Write(data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(p)
			return "", fmt.Errorf("write %s: %w", p, werr)
		}
		return p, nil
	}
}
