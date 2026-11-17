package files

import (
	"cmp"
	"context"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"serve-html-markdown/internal/store"
)

// firstVersion is the version number of the first rewrite (foo--v2.md).
const firstVersion = 2

var (
	// versionPattern is strict: only v2 and up are versions, with no leading
	// zeros. A file named foo--v1.md is its own document, not a version of
	// foo.md.
	versionPattern = regexp.MustCompile(`\A(.+?)--v([2-9]|[1-9][0-9]+)\z`)
	// reservedSuffix is what user-supplied names may not end in, so nothing
	// uploaded can land in a version slot or look like one.
	reservedSuffix = regexp.MustCompile(`--v[0-9]+\z`)
)

// ReservedSuffix reports whether stem ends in --v<digits>.
func ReservedSuffix(stem string) bool { return reservedSuffix.MatchString(stem) }

// FileVersions is a parsed file name: foo.md is version 1 of stem "foo" and
// foo--v2.md, foo--v3.md, ... are its rewrites.
type FileVersions struct {
	Stem      string
	Version   int
	Extension string
}

// ParseFileVersions splits a file name (any directories are dropped) into
// stem, version and extension. A version too large for an int is clamped to
// math.MaxInt.
func ParseFileVersions(name string) FileVersions {
	_, ext, fullStem := splitName(name)
	if m := versionPattern.FindStringSubmatch(fullStem); m != nil {
		v, err := strconv.Atoi(m[2])
		if err != nil {
			v = math.MaxInt
		}
		return FileVersions{Stem: m[1], Version: v, Extension: ext}
	}
	return FileVersions{Stem: fullStem, Version: 1, Extension: ext}
}

// BaseName is the version 1 name, e.g. "foo.md".
func (f FileVersions) BaseName() string { return f.Stem + f.Extension }

// NameFor is the file name of the given version (1 or lower is BaseName).
func (f FileVersions) NameFor(version int) string {
	if version <= 1 {
		return f.BaseName()
	}
	return f.Stem + "--v" + strconv.Itoa(version) + f.Extension
}

// FamilyNames returns the served file names of the family (the base name and
// its --v<N> rewrites with the same extension), ordered by version.
func (f FileVersions) FamilyNames(ctx context.Context, db store.DB) ([]string, error) {
	base := f.BaseName()
	if !utf8.ValidString(base) || strings.ContainsRune(base, 0) {
		return nil, nil // can never be a stored name
	}
	pattern := store.EscapeLike(f.Stem) + "--v%" + store.EscapeLike(f.Extension)
	names, err := store.ServedFileNames(ctx, db, pattern, base)
	if err != nil {
		return nil, err
	}
	type member struct {
		name    string
		version int
	}
	var members []member
	for _, n := range names {
		if p := ParseFileVersions(n); p.Stem == f.Stem {
			members = append(members, member{n, p.Version})
		}
	}
	slices.SortStableFunc(members, func(a, b member) int { return cmp.Compare(a.version, b.version) })
	family := make([]string, len(members))
	for i, m := range members {
		family[i] = m.name
	}
	return family, nil
}

// NextPath returns the first path in dir, from version 2 up, that has no file
// of that name yet (a dangling symlink counts as a file).
func (f FileVersions) NextPath(dir string) (string, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for v := firstVersion; ; v++ {
		p := filepath.Join(root, f.NameFor(v))
		taken, err := exists(p)
		if err != nil {
			return "", err
		}
		if !taken {
			return p, nil
		}
	}
}
