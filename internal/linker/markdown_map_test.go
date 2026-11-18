package linker

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func idAt(m *docMap, needle string) int32 { return m.BlockIDs[bytes.Index(m.Plain, []byte(needle))] }

func covers(r srcRange, pos int) bool { return r.Begin <= pos && pos < r.End }

func wantPlain(t *testing.T, src, want string) *docMap {
	t.Helper()
	m := buildMarkdownMap(src)
	if got := plainOf(m); got != want {
		t.Fatalf("plain of %q = %q, want %q", src, got, want)
	}
	assertMonotonic(t, m)
	return m
}

func TestMarkdownMapProjectsParagraphTextWithSourceOffsets(t *testing.T) {
	m := wantPlain(t, "alpha beta\n", "alpha beta\n")
	if m.Starts[0] != 0 {
		t.Fatalf("starts[0] = %d", m.Starts[0])
	}
	if got := m.sourceRange(srcRange{0, 5}); got != (srcRange{0, 5}) {
		t.Fatalf("range = %v", got)
	}
}

func TestMarkdownMapBlankLineSeparatedParagraphsGetDifferentBlockIDs(t *testing.T) {
	m := wantPlain(t, "alpha\n\nbeta\n", "alpha\nbeta\n")
	if idAt(m, "alpha") == idAt(m, "beta") {
		t.Fatal("same block id")
	}
}

func TestMarkdownMapSoftLineBreaksKeepOneBlockID(t *testing.T) {
	m := buildMarkdownMap("alpha\nbeta\n")
	ids := slices.Clone(m.BlockIDs)
	slices.Sort(ids)
	if len(slices.Compact(ids)) != 1 {
		t.Fatalf("ids = %v", m.BlockIDs)
	}
}

func TestMarkdownMapDropsHeadingMarkersAndIsolatesHeadings(t *testing.T) {
	m := wantPlain(t, "# Title\ntext\n", "Title\ntext\n")
	if idAt(m, "Title") == idAt(m, "text") {
		t.Fatal("same block id")
	}
}

func TestMarkdownMapDropsListMarkersAndIsolatesItems(t *testing.T) {
	m := wantPlain(t, "- alpha\n- beta\n", "alpha\nbeta\n")
	if idAt(m, "alpha") == idAt(m, "beta") {
		t.Fatal("same block id")
	}
}

func TestMarkdownMapDropsBlockquotePrefixes(t *testing.T) {
	wantPlain(t, "> quoted text\n", "quoted text\n")
}

func TestMarkdownMapFencedCodeIsUnlinkableButKeptInPlain(t *testing.T) {
	src := "before\n\n```\ncode here\n```\n"
	m := buildMarkdownMap(src)
	if !strings.Contains(plainOf(m), "code here") {
		t.Fatalf("plain = %q", plainOf(m))
	}
	at := strings.Index(src, "code here")
	if !slices.ContainsFunc(m.UnlinkableRanges, func(r srcRange) bool { return covers(r, at) }) {
		t.Fatalf("unlinkable = %v", m.UnlinkableRanges)
	}
}

func TestMarkdownMapIndentedCodeIsUnlinkable(t *testing.T) {
	src := "before\n\n    indented code\n"
	m := buildMarkdownMap(src)
	if !strings.Contains(plainOf(m), "indented code") {
		t.Fatalf("plain = %q", plainOf(m))
	}
	at := strings.Index(src, "indented code")
	if !slices.ContainsFunc(m.UnlinkableRanges, func(r srcRange) bool { return covers(r, at) }) {
		t.Fatalf("unlinkable = %v", m.UnlinkableRanges)
	}
}

func TestMarkdownMapResolvesBackslashEscapes(t *testing.T) {
	m := wantPlain(t, "a \\* b\n", "a * b\n")
	i := bytes.IndexByte(m.Plain, '*')
	if m.Starts[i] != 2 || m.Ends[i] != 4 {
		t.Fatalf("star = [%d,%d)", m.Starts[i], m.Ends[i])
	}
}

func TestMarkdownMapDropsCodeSpanDelimitersAndRecordsTheElement(t *testing.T) {
	src := "this is `some` code\n"
	m := wantPlain(t, src, "this is some code\n")
	want := srcRange{strings.Index(src, "`"), strings.LastIndex(src, "`") + 1}
	if !slices.Contains(m.InlineRanges, want) {
		t.Fatalf("inline = %v, want %v", m.InlineRanges, want)
	}
}

func TestMarkdownMapKeepsLinkLabelsDropsURLsAndRecordsTheLink(t *testing.T) {
	src := "see [beta](/old.html) end\n"
	m := wantPlain(t, src, "see beta end\n")
	want := srcRange{strings.Index(src, "["), strings.Index(src, ")") + 1}
	if !slices.Contains(m.LinkRanges, want) {
		t.Fatalf("links = %v, want %v", m.LinkRanges, want)
	}
}

func TestMarkdownMapRecordsImagesAsLinksWithNoPlainContribution(t *testing.T) {
	src := "pic ![alt](i.png) end\n"
	m := wantPlain(t, src, "pic  end\n")
	want := srcRange{strings.Index(src, "!["), strings.Index(src, ")") + 1}
	if !slices.Contains(m.LinkRanges, want) {
		t.Fatalf("links = %v, want %v", m.LinkRanges, want)
	}
}

func TestMarkdownMapPairsEmphasisDelimitersAndRecordsTheElement(t *testing.T) {
	src := "make *this bold* now\n"
	m := wantPlain(t, src, "make this bold now\n")
	want := srcRange{strings.Index(src, "*"), strings.LastIndex(src, "*") + 1}
	if !slices.Contains(m.InlineRanges, want) {
		t.Fatalf("inline = %v, want %v", m.InlineRanges, want)
	}
}

func TestMarkdownMapLeavesUnmatchedEmphasisLiteral(t *testing.T) {
	wantPlain(t, "5 * 3 is fifteen\n", "5 * 3 is fifteen\n")
}

func TestMarkdownMapHandlesRawInlineHTMLAndAnchors(t *testing.T) {
	src := "word <span>in span</span> and <a href=\"/o.html\">old</a> end\n"
	m := wantPlain(t, src, "word in span and old end\n")
	want := srcRange{strings.Index(src, "<a"), strings.Index(src, "</a>") + len("</a>")}
	if !slices.Contains(m.LinkRanges, want) {
		t.Fatalf("links = %v, want %v", m.LinkRanges, want)
	}
}

func TestMarkdownMapDecodesEntities(t *testing.T) {
	wantPlain(t, "A &amp; B\n", "A & B\n")
}

// Beyond the Ruby tests: edge behaviours pinned against Ruby 3.4.8.

func TestMarkdownMapEdgeCases(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"crlf drops the carriage return, newline maps to it", "a\r\nb\r\n", "a\nb\n"},
		{"final line without newline", "tail", "tail"},
		{"blank lines emit nothing", "a\n\n\nb\n", "a\nb\n"},
		{"unclosed fence swallows the rest as code", "```\nx\ny\n", "x\ny\n"},
		{"tilde fence", "~~~\nx\n~~~\ny\n", "x\ny\n"},
		{"fence with other char does not close", "```\nx\n~~~\ny\n```\n", "x\n~~~\ny\n"},
		{"code span that never closes stays literal", "a `b c\n", "a `b c\n"},
		{"code span closer must be the same length", "a ``b` c``\n", "a b` c\n"},
		{"code span does not cross lines", "a `b\nc` d\n", "a `b\nc` d\n"},
		{"image without url stays literal", "![alt] x\n", "![alt] x\n"},
		{"link label with escaped bracket", "[a\\]b](u)\n", "a]b\n"},
		{"unclosed link paren stays literal", "[a](u\n", "[a](u\n"},
		{"br tag becomes newline", "a<br>b\n", "a\nb\n"},
		{"underscore emphasis", "x _y_ z\n", "x y z\n"},
		{"nested emphasis", "*a **b** c*\n", "a b c\n"},
		{"list marker only", "- \n", "\n"},
		{"indented code needs a preceding blank", "para\n    not code\n", "para\n    not code\n"},
		{"nul counts as strip whitespace", "a\n\x00\nb\n", "a\nb\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { wantPlain(t, tc.src, tc.want) })
	}
}

func TestFindClosingTicks(t *testing.T) {
	tests := []struct {
		src        string
		from, n    int
		start, end int
		ok         bool
	}{
		{"a``b`c``d", 3, 2, 6, 8, true},
		{"``` `` ```", 3, 2, 4, 6, true},
		{"a`b", 0, 2, 0, 0, false},
		{"x ```` y ` z", 2, 1, 9, 10, true},
	}
	for _, tc := range tests {
		s, e, ok := findClosingTicks(tc.src, tc.from, tc.n)
		if ok != tc.ok || s != tc.start || e != tc.end {
			t.Errorf("findClosingTicks(%q,%d,%d) = %d,%d,%v want %d,%d,%v", tc.src, tc.from, tc.n, s, e, ok, tc.start, tc.end, tc.ok)
		}
	}
}

// findClosingTicks is the straightforward scan the per-line run index in
// mdBuilder.closingTicks must agree with: the first maximal run of exactly n
// backticks starting at or after from (Ruby: /(?<!`)`{n}(?!`)/ from there).
func findClosingTicks(src string, from, n int) (int, int, bool) {
	for i := from; i < len(src); {
		if src[i] != '`' || (i > 0 && i == from && src[i-1] == '`') {
			i++
			continue
		}
		j := i
		for j < len(src) && src[j] == '`' {
			j++
		}
		if j-i == n && (j >= len(src) || src[j] != '`') {
			return i, j, true
		}
		i = j
	}
	return 0, 0, false
}

func TestClosingTicksIndexAgreesWithScan(t *testing.T) {
	line := "a` b`` c``` d`` e` f```` g`` h\n"
	b := &mdBuilder{src: line, m: &docMap{}}
	b.startLine(0, len(line), len(line)-1)
	for from := 0; from < len(line); from++ {
		if from > 0 && line[from-1] == '`' && line[from] == '`' {
			continue // Ruby never searches from inside a run
		}
		for n := 1; n <= 5; n++ {
			s1, e1, ok1 := findClosingTicks(line, from, n)
			s2, e2, ok2 := b.closingTicks(from, n)
			if s1 != s2 || e1 != e2 || ok1 != ok2 {
				t.Fatalf("from=%d n=%d: scan=(%d,%d,%v) index=(%d,%d,%v)", from, n, s1, e1, ok1, s2, e2, ok2)
			}
		}
	}
}
