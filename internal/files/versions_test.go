package files_test

import (
	"context"
	"math"
	"path/filepath"
	"reflect"
	"testing"

	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/files"
	"serve-html-markdown/internal/store"
)

func TestParseBareFilenameIsVersionOne(t *testing.T) {
	p := files.ParseFileVersions("foo.md")
	if p.Stem != "foo" || p.Version != 1 || p.Extension != ".md" || p.BaseName() != "foo.md" {
		t.Fatalf("%+v", p)
	}
}

func TestParseVersionedFilename(t *testing.T) {
	p := files.ParseFileVersions("foo--v3.html")
	if p.Stem != "foo" || p.Version != 3 || p.Extension != ".html" || p.BaseName() != "foo.html" {
		t.Fatalf("%+v", p)
	}
	if p := files.ParseFileVersions("foo--v10.md"); p.Stem != "foo" || p.Version != 10 {
		t.Fatalf("v10: %+v", p)
	}
	if p := files.ParseFileVersions("foo--v2.md"); p.Stem != "foo" || p.Version != 2 {
		t.Fatalf("v2: %+v", p)
	}
}

func TestParseTreatsV1AndPaddedVersionsAsStem(t *testing.T) {
	if p := files.ParseFileVersions("foo--v1.md"); p.Version != 1 || p.Stem != "foo--v1" {
		t.Fatalf("v1: %+v", p)
	}
	if p := files.ParseFileVersions("foo--v02.md"); p.Version != 1 || p.Stem != "foo--v02" {
		t.Fatalf("v02: %+v", p)
	}
	if p := files.ParseFileVersions("foo--v0.md"); p.Version != 1 || p.Stem != "foo--v0" {
		t.Fatalf("v0: %+v", p)
	}
}

func TestParseEdgeCases(t *testing.T) {
	// Directories are dropped, the case of the extension is kept.
	if p := files.ParseFileVersions("dir/foo--v4.MD"); p != (files.FileVersions{Stem: "foo", Version: 4, Extension: ".MD"}) {
		t.Fatalf("%+v", p)
	}
	// Lazy stem: the first --v<N> that ends the stem wins.
	if p := files.ParseFileVersions("a--v2--v3.md"); p.Stem != "a--v2" || p.Version != 3 {
		t.Fatalf("%+v", p)
	}
	// The stem needs at least one character.
	if p := files.ParseFileVersions("--v2.md"); p.Version != 1 || p.Stem != "--v2" {
		t.Fatalf("%+v", p)
	}
	// A newline is not matched by . (and \z does not allow a trailing one).
	if p := files.ParseFileVersions("a\n--v2.md"); p.Version != 1 {
		t.Fatalf("newline: %+v", p)
	}
	if p := files.ParseFileVersions(""); p != (files.FileVersions{Version: 1}) {
		t.Fatalf("empty: %+v", p)
	}
	if p := files.ParseFileVersions("noext--v2"); p.Stem != "noext" || p.Version != 2 || p.Extension != "" {
		t.Fatalf("noext: %+v", p)
	}
	// A version that overflows is clamped.
	if p := files.ParseFileVersions("foo--v99999999999999999999.md"); p.Version != math.MaxInt || p.Stem != "foo" {
		t.Fatalf("overflow: %+v", p)
	}
}

func TestNameFor(t *testing.T) {
	p := files.ParseFileVersions("foo--v2.md")
	if got := p.NameFor(4); got != "foo--v4.md" {
		t.Fatal(got)
	}
	if got := p.NameFor(1); got != "foo.md" {
		t.Fatal(got)
	}
	if got := p.NameFor(0); got != "foo.md" {
		t.Fatal(got)
	}
}

func seed(t *testing.T, db store.DB, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := store.RecordServedFile(context.Background(), db, n); err != nil {
			t.Fatal(err)
		}
	}
}

func family(t *testing.T, db store.DB, name string) []string {
	t.Helper()
	got, err := files.ParseFileVersions(name).FamilyNames(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestFamilyNamesInVersionOrder(t *testing.T) {
	db := testdb.New(t)
	seed(t, db, "foo.md", "foo--v3.md", "foo--v2.md", "unrelated.md", "foo--v10.md")
	want := []string{"foo.md", "foo--v2.md", "foo--v3.md", "foo--v10.md"}
	if got := family(t, db, "foo--v2.md"); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFamilyNamesExcludesOtherExtensions(t *testing.T) {
	db := testdb.New(t)
	seed(t, db, "foo.md", "foo--v2.html", "foo--v2.md")
	if got, want := family(t, db, "foo.md"), []string{"foo.md", "foo--v2.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFamilyNamesExcludesOtherStems(t *testing.T) {
	db := testdb.New(t)
	seed(t, db, "foo.md", "foo-bar--v2.md", "foo--v2.md", "foo--v1.md", "foo--v02.md", "foo--bar--v2.md")
	if got, want := family(t, db, "foo.md"), []string{"foo.md", "foo--v2.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// foo--bar--v2.md is a version of "foo--bar".
	if got, want := family(t, db, "foo--bar.md"), []string{"foo--bar--v2.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("foo--bar: got %v, want %v", got, want)
	}
}

func TestFamilyNamesEscapesLikeWildcards(t *testing.T) {
	db := testdb.New(t)
	seed(t, db, "a_b.md", "axb--v2.md", "a_b--v2.md", "a%b.md", "aXXb--v2.md", "a%b--v2.md", `a\b.md`, `a\b--v2.md`, "azzb--v3.md")
	if got, want := family(t, db, "a_b.md"), []string{"a_b.md", "a_b--v2.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("underscore: got %v, want %v", got, want)
	}
	if got, want := family(t, db, "a%b.md"), []string{"a%b.md", "a%b--v2.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("percent: got %v, want %v", got, want)
	}
	if got, want := family(t, db, `a\b.md`), []string{`a\b.md`, `a\b--v2.md`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("backslash: got %v, want %v", got, want)
	}
}

func TestFamilyNamesWithoutRowsAndBadNames(t *testing.T) {
	db := testdb.New(t)
	if got := family(t, db, "foo.md"); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	if got := family(t, db, "bad\x00.md"); len(got) != 0 {
		t.Fatalf("NUL: %v", got)
	}
	if got := family(t, db, "bad\xff.md"); len(got) != 0 {
		t.Fatalf("utf8: %v", got)
	}
}

func TestNextPathReturnsFirstFreeVersionFromTwo(t *testing.T) {
	dir := filesDir(t)
	write(t, filepath.Join(dir, "foo.md"), "v1")
	write(t, filepath.Join(dir, "foo--v2.md"), "v2")
	got, err := files.ParseFileVersions("foo.md").NextPath(dir)
	if err != nil || got != filepath.Join(dir, "foo--v3.md") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestNextPathFromVersionedFileStaysInFamily(t *testing.T) {
	dir := filesDir(t)
	write(t, filepath.Join(dir, "foo--v2.md"), "v2")
	got, err := files.ParseFileVersions("foo--v2.md").NextPath(dir)
	if err != nil || got != filepath.Join(dir, "foo--v3.md") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestNextPathSkipsGapsAndNeverUsesVersionOne(t *testing.T) {
	dir := filesDir(t)
	got, err := files.ParseFileVersions("foo.md").NextPath(dir)
	if err != nil || got != filepath.Join(dir, "foo--v2.md") {
		t.Fatalf("empty dir: %q, %v", got, err)
	}
	write(t, filepath.Join(dir, "foo--v3.md"), "v3")
	if got, _ = files.ParseFileVersions("foo.md").NextPath(dir); got != filepath.Join(dir, "foo--v2.md") {
		t.Fatalf("gap: %q", got)
	}
}

func TestReservedSuffix(t *testing.T) {
	for stem, want := range map[string]bool{
		"foo--v2": true, "foo--v1": true, "foo--v007": true, "--v1": true,
		"foo--version": false, "foo-v2": false, "foo--v": false, "foo--v2x": false, "foo--v2\n": false,
	} {
		if got := files.ReservedSuffix(stem); got != want {
			t.Errorf("ReservedSuffix(%q) = %v, want %v", stem, got, want)
		}
	}
}
