package linker

import (
	"errors"
	"testing"
)

// mapFor ports the Ruby test helper map_for: a one-byte-per-char identity map.
func mapFor(plain string) *Map {
	m := &Map{}
	for i := 0; i < len(plain); i++ {
		m.emit(plain[i], i, i+1, 0)
	}
	return m
}

func TestLocate(t *testing.T) {
	tests := []struct {
		name       string
		plain      string
		selected   string
		occurrence int
		want       Range
	}{
		{"finds a literal match", "alpha beta gamma", "beta", 0, Range{6, 10}},
		{"matches across collapsed whitespace", "some  \n code", "some code", 0, Range{0, 12}},
		{"picks the requested occurrence", "cat dog cat", "cat", 1, Range{8, 11}},
		{"falls back to the first occurrence when out of range", "cat dog cat", "cat", 9, Range{0, 3}},
		{"escapes regex metacharacters in the selection", "cost is $5 (usd)", "$5 (usd)", 0, Range{8, 16}},
		// Beyond the Ruby tests.
		{"overlapping matches count separately", "aaa", "aa", 1, Range{1, 3}},
		{"leading and trailing selection whitespace is ignored", "x beta y", "  beta\t", 0, Range{2, 6}},
		{"vertical tab is whitespace like Ruby's \\s", "a\vb", "a b", 0, Range{0, 3}},
		{"non-ASCII whitespace is not whitespace", "a b a b", "a b", 0, Range{5, 8}},
		{"multi-byte text keeps byte offsets", "ñandú café", "café", 0, Range{8, 13}},
		{"negative occurrence indexes from the end like fetch", "cat dog cat", "cat", -1, Range{8, 11}},
		{"negative occurrence past the start falls back to first", "cat dog cat", "cat", -9, Range{0, 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Locate(mapFor(tc.plain), tc.selected, tc.occurrence)
			if err != nil || got != tc.want {
				t.Fatalf("Locate(%q, %q, %d) = %v, %v; want %v", tc.plain, tc.selected, tc.occurrence, got, err, tc.want)
			}
		})
	}
}

func TestLocateErrors(t *testing.T) {
	tests := []struct {
		name, plain, selected string
		want                  error
	}{
		{"raises NotFound when absent", "alpha", "missing", ErrNotFound},
		{"raises NotFound for a blank selection", "alpha", "   ", ErrNotFound},
		{"raises NotFound for an empty selection", "alpha", "", ErrNotFound},
		{"a token must be followed by whitespace, not text", "ab", "a b", ErrNotFound},
		{"invalid UTF-8 selection", "alpha", "\xff", ErrInvalidUTF8},
		{"invalid UTF-8 plain text", "a\xffb", "a", ErrInvalidUTF8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Locate(mapFor(tc.plain), tc.selected, 0)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestLocateNotFoundIsLinkerError(t *testing.T) {
	_, err := Locate(mapFor("alpha"), "missing", 0)
	var le *Error
	if !errors.As(err, &le) || le.Label() != "NotFound: Selection not found in source — select a plainer run of text." {
		t.Fatalf("got %v", err)
	}
	if errors.Is(err, ErrUnsafeMatch) {
		t.Fatal("NotFound must not match ErrUnsafeMatch")
	}
}
