package linker

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// fuzzURL cannot appear in sources the fuzzer is allowed to use, so inserted
// link syntax is distinguishable from source text.
const fuzzURL = "\x01U\x01"

// explainsSplice reports whether out can be obtained from src by wrapping
// non-overlapping slices of src in link syntax and nothing else: an HTML slice
// becomes <a href="URL">slice</a>; a Markdown slice becomes [slice](URL) with
// every ']' not already preceded by a backslash escaped. The invariant of
// SelectionLinker is that a link only adds syntax around existing source text,
// so the source is recoverable. Backtracking search with memoisation resolves
// the ambiguity of a source that itself contains "</a>" or "[".
func explainsSplice(src, out string, markdown bool) bool {
	open, close := `<a href="`+fuzzURL+`">`, "</a>"
	if markdown {
		open, close = "[", "]("+fuzzURL+")"
	}
	type state struct {
		i, j   int
		inLink bool
	}
	seen := map[state]bool{}
	var walk func(i, j int, inLink bool) bool
	walk = func(i, j int, inLink bool) bool {
		if i == len(out) {
			return j == len(src) && !inLink
		}
		s := state{i, j, inLink}
		if seen[s] {
			return false
		}
		seen[s] = true
		if !inLink && strings.HasPrefix(out[i:], open) && walk(i+len(open), j, true) {
			return true
		}
		if inLink && strings.HasPrefix(out[i:], close) && walk(i+len(close), j, false) {
			return true
		}
		if inLink && markdown && strings.HasPrefix(out[i:], `\]`) && j < len(src) && src[j] == ']' && walk(i+2, j+1, true) {
			return true
		}
		if j < len(src) && out[i] == src[j] {
			return walk(i+1, j+1, inLink)
		}
		return false
	}
	return walk(0, 0, false)
}

func checkMap(t *testing.T, name, src string, m *Map) {
	t.Helper()
	n := len(m.Plain)
	if len(m.Starts) != n || len(m.Ends) != n || len(m.BlockIDs) != n {
		t.Fatalf("%s: parallel slices disagree", name)
	}
	assertMonotonic(t, m)
	for i := 0; i < n; i++ {
		if m.Starts[i] < 0 || int(m.Ends[i]) > len(src) {
			t.Fatalf("%s: offsets out of bounds at %d", name, i)
		}
	}
	for _, rs := range [][]Range{m.InlineRanges, m.LinkRanges, m.UnlinkableRanges} {
		for _, r := range rs {
			if r.Begin < 0 || r.End > len(src) || r.Begin > r.End {
				t.Fatalf("%s: range %v out of bounds", name, r)
			}
		}
	}
	for _, r := range m.LinkRanges {
		if r.Begin == r.End {
			t.Fatalf("%s: empty link range (segment relies on non-empty links)", name)
		}
	}
}

// FuzzLink checks that Link never panics or hangs, that it returns only the
// documented errors, and that on success the output is the source plus link
// syntax and nothing else. Run it with: go test ./internal/linker -fuzz FuzzLink
func FuzzLink(f *testing.F) {
	raw, err := os.ReadFile("../../testdata/golden/linker/cases.json")
	if err != nil {
		f.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		f.Fatal(err)
	}
	for _, c := range cases {
		f.Add(c.Source, c.Extension == ".html", c.SelectedText, c.Occurrence)
	}
	f.Add("*a **b** c*\n[x](y) `z` <b>q</b>\n", false, "b c x", 0)
	f.Add("<p>a<b>c<i>d</b>e</i></p><!-- x --><script>y</script>", true, "c d e", 0)

	f.Fuzz(func(t *testing.T, source string, html bool, selected string, occurrence int) {
		if strings.Contains(source, fuzzURL) {
			return
		}
		ext := ".md"
		if html {
			ext = ".html"
		}
		out, err := Link(source, ext, selected, occurrence, fuzzURL)
		if utf8.ValidString(source) {
			if html {
				checkMap(t, "html", source, BuildHTMLMap(source))
			} else {
				checkMap(t, "md", source, BuildMarkdownMap(source))
			}
		}
		if err != nil {
			var le *Error
			if !errors.As(err, &le) && !errors.Is(err, ErrInvalidUTF8) {
				t.Fatalf("unexpected error type: %v", err)
			}
			if !utf8.ValidString(source) && !errors.Is(err, ErrInvalidUTF8) {
				t.Fatalf("invalid UTF-8 source gave %v", err)
			}
			return
		}
		if len(out) < len(source) || out == source {
			t.Fatalf("success must add link syntax: %q -> %q", source, out)
		}
		if len(source) <= 2048 && !explainsSplice(source, out, !html) {
			t.Fatalf("output is not source plus link syntax:\nsource=%q\nout=%q", source, out)
		}
		again, err2 := Link(source, ext, selected, occurrence, fuzzURL)
		if err2 != nil || again != out {
			t.Fatalf("not deterministic")
		}
	})
}
