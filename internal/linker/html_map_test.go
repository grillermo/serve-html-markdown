package linker

import (
	"bytes"
	"slices"
	"testing"
)

func plainOf(m *Map) string { return string(m.Plain) }

func TestHTMLMapProjectsTextAndDropsTags(t *testing.T) {
	m := BuildHTMLMap("<p>alpha <span>beta</span></p>")
	if got := plainOf(m); got != "alpha beta\n" {
		t.Fatalf("plain = %q", got)
	}
}

func TestHTMLMapMapsPlainBytesBackToSourceOffsets(t *testing.T) {
	m := BuildHTMLMap("<p>ab</p>")
	if got := plainOf(m); got != "ab\n" {
		t.Fatalf("plain = %q", got)
	}
	if m.Starts[0] != 3 || m.Ends[1] != 5 {
		t.Fatalf("starts[0]=%d ends[1]=%d", m.Starts[0], m.Ends[1])
	}
	if got := m.SourceRange(Range{0, 2}); got != (Range{3, 5}) {
		t.Fatalf("source range = %v", got)
	}
}

func TestHTMLMapDecodesEntitiesAsAtomicRuns(t *testing.T) {
	m := BuildHTMLMap("<p>A &amp; B</p>")
	if got := plainOf(m); got != "A & B\n" {
		t.Fatalf("plain = %q", got)
	}
	i := bytes.IndexByte(m.Plain, '&')
	if m.Starts[i] != 5 || m.Ends[i] != 10 {
		t.Fatalf("amp run = [%d,%d)", m.Starts[i], m.Ends[i])
	}
}

func TestHTMLMapMultiByteEntityMapsEveryByteToTheRun(t *testing.T) {
	m := BuildHTMLMap("<p>caf&#233;</p>")
	if got := plainOf(m); got != "café\n" {
		t.Fatalf("plain = %q", got)
	}
	for _, i := range []int{3, 4} { // the two bytes of é
		if m.Starts[i] != 6 || m.Ends[i] != 12 {
			t.Fatalf("byte %d = [%d,%d), want [6,12)", i, m.Starts[i], m.Ends[i])
		}
	}
}

func TestHTMLMapKeepsUnknownNamedEntitiesLiteral(t *testing.T) {
	m := BuildHTMLMap("<p>a&nbsp;b</p>")
	if got := plainOf(m); got != "a&nbsp;b\n" {
		t.Fatalf("plain = %q", got)
	}
}

func TestHTMLMapSkipsScriptAndStyleContents(t *testing.T) {
	m := BuildHTMLMap("<script>var beta = 1;</script><style>.x{}</style><p>x</p>")
	if got := plainOf(m); got != "x\n" {
		t.Fatalf("plain = %q", got)
	}
}

func TestHTMLMapUnclosedScriptSwallowsTheRest(t *testing.T) {
	m := BuildHTMLMap("<p>a</p><script>never closed <p>b</p>")
	if got := plainOf(m); got != "a\n" {
		t.Fatalf("plain = %q", got)
	}
}

func TestHTMLMapSeparatesBlocksWithZeroWidthNewlineAndDistinctIDs(t *testing.T) {
	m := BuildHTMLMap("<p>one</p><p>two</p>")
	if got := plainOf(m); got != "one\ntwo\n" {
		t.Fatalf("plain = %q", got)
	}
	one := m.BlockIDs[bytes.Index(m.Plain, []byte("one"))]
	two := m.BlockIDs[bytes.Index(m.Plain, []byte("two"))]
	if one == two {
		t.Fatalf("block ids equal: %d", one)
	}
	sep := bytes.IndexByte(m.Plain, '\n')
	if m.Starts[sep] != m.Ends[sep] {
		t.Fatalf("separator not zero-width: [%d,%d)", m.Starts[sep], m.Ends[sep])
	}
}

func TestHTMLMapKeepsOneBlockIDInsideParagraphWithInlineTags(t *testing.T) {
	m := BuildHTMLMap("<p>one <em>two</em> three</p>")
	ids := slices.Clone(m.BlockIDs)
	slices.Sort(ids)
	if len(slices.Compact(ids)) != 1 {
		t.Fatalf("block ids = %v", m.BlockIDs)
	}
}

func TestHTMLMapRecordsInlineElementRanges(t *testing.T) {
	src := "<p>a <em>b</em> c</p>"
	m := BuildHTMLMap(src)
	start := bytes.Index([]byte(src), []byte("<em>"))
	end := bytes.Index([]byte(src), []byte("</em>")) + len("</em>")
	if !slices.Contains(m.InlineRanges, Range{start, end}) {
		t.Fatalf("inline ranges = %v", m.InlineRanges)
	}
}

func TestHTMLMapRecordsAnchorsAsInlineAndLinkRanges(t *testing.T) {
	src := `<p><a href="/old.html">beta</a></p>`
	m := BuildHTMLMap(src)
	r := Range{bytes.Index([]byte(src), []byte("<a")), bytes.Index([]byte(src), []byte("</a>")) + len("</a>")}
	if !slices.Contains(m.LinkRanges, r) || !slices.Contains(m.InlineRanges, r) {
		t.Fatalf("link=%v inline=%v want %v", m.LinkRanges, m.InlineRanges, r)
	}
}

func TestHTMLMapEmitsNewlineForBr(t *testing.T) {
	m := BuildHTMLMap("<p>some<br>code</p>")
	if got := plainOf(m); got != "some\ncode\n" {
		t.Fatalf("plain = %q", got)
	}
}

func TestHTMLMapTreatsStrayAngleBracketsAndCommentsCorrectly(t *testing.T) {
	m := BuildHTMLMap("<p>1 > 0</p><!-- note -->")
	if got := plainOf(m); got != "1 > 0\n" {
		t.Fatalf("plain = %q", got)
	}
}

// Beyond the Ruby tests: raw-text close tags follow Ruby's case-insensitive
// regexp, including Unicode case folding (ſ folds to s, ﬆ to "st").
func TestHTMLMapRawTextCloseTagFollowsRubyCaseFolding(t *testing.T) {
	for _, src := range []string{
		"<script>x</SCRIPT ><p>y</p>",
		"<script>x</ſcript><p>y</p>",
		"<style>x</ﬆyle><p>y</p>",
		"<style>x</ﬅyle><p>y</p>",
		"<script>x</script\v><p>y</p>",
	} {
		if got := plainOf(BuildHTMLMap(src)); got != "y\n" {
			t.Errorf("%q: plain = %q", src, got)
		}
	}
	if got := plainOf(BuildHTMLMap("<script>x</script\u0085><p>y</p>")); got != "" {
		t.Errorf("NEL is not \\s: plain = %q", got)
	}
}

func TestHTMLMapPerNameStacksMatchRindexSemantics(t *testing.T) {
	// </b> closes the nearest <b>, leaving <i> open; </i> then closes it.
	src := "<p><b>a<i>b</b>c</i></p>"
	m := BuildHTMLMap(src)
	want := []Range{{3, 15}, {7, 20}}
	if !slices.Equal(m.InlineRanges, want) {
		t.Fatalf("inline ranges = %v, want %v", m.InlineRanges, want)
	}
}

func TestHTMLMapStartsAreMonotonic(t *testing.T) {
	m := BuildHTMLMap(`<h1>T</h1><p>a &amp; b<br>c <em>d</em></p><!-- x --><ul><li>e</li></ul>`)
	assertMonotonic(t, m)
}

func assertMonotonic(t *testing.T, m *Map) {
	t.Helper()
	for i := range m.Starts {
		if m.Starts[i] > m.Ends[i] || (i > 0 && (m.Starts[i] < m.Starts[i-1] || m.Ends[i] < m.Ends[i-1])) {
			t.Fatalf("map not monotonic at %d", i)
		}
	}
}
