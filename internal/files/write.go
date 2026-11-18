package files

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// plainName checks that name is a single served file name: one path element,
// valid text, one of the served extensions.
func plainName(name string) error {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) ||
		strings.ContainsAny(name, "/\x00") {
		return BadRequest("Invalid filename.")
	}
	return checkExt(filepath.Ext(strings.TrimLeft(name, ".")))
}

// FreeIndexedName returns the first name prefix+N+suffix, N counting up from 1,
// that nothing in dir holds yet (a dangling symlink holds it). Nothing is
// created, so it only reserves a name for a caller that serialises its
// callers; CreateNew then claims it. suffix must end in a served extension and
// prefix must not contain a path separator.
func FreeIndexedName(dir, prefix, suffix string) (string, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for n := 1; n <= maxAttempts; n++ {
		name := prefix + strconv.Itoa(n) + suffix
		if err := plainName(name); err != nil {
			return "", err
		}
		taken, err := exists(filepath.Join(root, name))
		if err != nil {
			return "", err
		}
		if !taken {
			return name, nil
		}
	}
	return "", errNoFreeName(prefix)
}

// CreateNew creates dir/name with data, all or nothing: the data is written to
// a hidden temporary file that is then hard-linked to name, so a reader never
// sees a partial file and a name that is taken (by a file or a dangling
// symlink) is never overwritten. It returns an error satisfying
// errors.Is(err, fs.ErrExist) then. All access goes through an os.Root on dir.
func CreateNew(dir, name string, data []byte) error {
	if err := plainName(name); err != nil {
		return err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer r.Close()
	tmp, err := writeTemp(r, ".", 0o666, false, data)
	if err != nil {
		return err
	}
	defer r.Remove(tmp)
	return nameError(r.Link(tmp, name))
}

// ReplaceFile replaces the contents of the existing regular file rel (a path
// relative to dir, symlinks already resolved, as OpenFile returns it) with
// data, atomically: it writes a temporary file next to it, gives it the old
// permissions and renames it over the old one, so a concurrent reader gets the
// old or the new file whole. All access goes through an os.Root on dir.
func ReplaceFile(dir, rel string, data []byte) error {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer r.Close()
	info, err := r.Stat(rel)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fs.ErrInvalid
	}
	tmp, err := writeTemp(r, filepath.Dir(rel), info.Mode().Perm(), true, data)
	if err != nil {
		return err
	}
	if err := r.Rename(tmp, rel); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	return nil
}

// writeTemp writes data to a new, exclusively created file in subdirectory sub
// of r and returns its path relative to r. The file is created with perm (less
// the umask), or with exactly perm when exact. A failure leaves nothing behind.
func writeTemp(r *os.Root, sub string, perm fs.FileMode, exact bool, data []byte) (string, error) {
	var lastErr error
	for range 16 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		tmp := filepath.Join(sub, ".tmp-"+hex.EncodeToString(b[:]))
		f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if errors.Is(err, fs.ErrExist) {
			lastErr = err
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.Write(data)
		if werr == nil && exact {
			werr = f.Chmod(perm)
		}
		if werr == nil {
			werr = f.Sync()
		}
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = r.Remove(tmp)
			return "", werr
		}
		return tmp, nil
	}
	return "", lastErr
}
