package linker

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func plan(t *testing.T, source, selected string) ([]Range, error) {
	t.Helper()
	m := BuildHTMLMap(source)
	pr, err := Locate(m, selected, 0)
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	return Plan(m, pr, source)
}

func span(source, needle string) Range {
	i := strings.Index(source, needle)
	return Range{i, i + len(needle)}
}

func wantUnsafe(t *testing.T, err error, msg string) {
	t.Helper()
	var le *Error
	if !errors.As(err, &le) || !errors.Is(err, ErrUnsafeMatch) || le.Message != msg {
		t.Fatalf("err = %v, want UnsafeMatch %q", err, msg)
	}
}

func TestPlanReturnsTheMatchedSourceRangeForAPlainMatch(t *testing.T) {
	src := "<p>alpha beta gamma</p>"
	got, err := plan(t, src, "beta")
	if err != nil || !slices.Equal(got, []Range{span(src, "beta")}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestPlanSnapsToCoverAPartiallyOverlappedInlineElement(t *testing.T) {
	src := "<p>x <em>alpha beta</em> gamma</p>"
	want := Range{strings.Index(src, "<em>"), strings.Index(src, " gamma") + len(" gamma")}
	got, err := plan(t, src, "beta gamma")
	if err != nil || !slices.Equal(got, []Range{want}) {
		t.Fatalf("got %v, %v want %v", got, err, want)
	}
}

func TestPlanDoesNotSnapWhenTheMatchSitsInsideAnInlineElement(t *testing.T) {
	src := "<p><span>alpha beta gamma</span></p>"
	got, err := plan(t, src, "beta")
	if err != nil || !slices.Equal(got, []Range{span(src, "beta")}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestPlanRejectsAMatchCrossingBlockElements(t *testing.T) {
	_, err := plan(t, "<p>one</p><p>two</p>", "one two")
	wantUnsafe(t, err, "Selection spans multiple paragraphs — select within one.")
}

func TestPlanSegmentsAroundAnExistingAnchor(t *testing.T) {
	src := `<p>before <a href="/old.html">beta</a> after</p>`
	got, err := plan(t, src, "before beta after")
	want := []Range{span(src, "before"), span(src, "after")}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("got %v, %v want %v", got, err, want)
	}
}

func TestPlanRejectsAMatchEntirelyInsideAnExistingAnchor(t *testing.T) {
	_, err := plan(t, `<p><a href="/old.html">alpha beta</a></p>`, "beta")
	wantUnsafe(t, err, "Selection overlaps an existing link.")
}

func TestPlanRejectsAMatchTouchingAnUnlinkableRange(t *testing.T) {
	src := "code here"
	m := BuildHTMLMap(src)
	m.UnlinkableRanges = append(m.UnlinkableRanges, Range{0, len(src)})
	pr, err := Locate(m, "code", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Plan(m, pr, src)
	wantUnsafe(t, err, "Selection is inside a code block.")
}

func TestPlanTrimsWhitespaceFromSegmentEdges(t *testing.T) {
	src := `<p>one <a href="/old.html">beta</a> two</p>`
	got, err := plan(t, src, "one beta two")
	if err != nil || len(got) != 2 || src[got[0].Begin:got[0].End] != "one" || src[got[1].Begin:got[1].End] != "two" {
		t.Fatalf("got %v, %v", got, err)
	}
}

// Beyond the Ruby tests: the sweep in segment must equal the Ruby's repeated
// subtraction, including overlapping and nested link ranges.
func TestSegmentSubtractsOverlappingAndNestedLinks(t *testing.T) {
	m := &Map{LinkRanges: []Range{{12, 14}, {3, 6}, {4, 5}, {5, 9}, {0, 1}, {20, 30}}}
	got := segment(m, Range{2, 20})
	want := []Range{{2, 3}, {9, 12}, {14, 20}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got := segment(&Map{LinkRanges: []Range{{0, 50}}}, Range{2, 20}); len(got) != 0 {
		t.Fatalf("fully covered: %v", got)
	}
}
