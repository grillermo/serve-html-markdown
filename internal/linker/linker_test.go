package linker

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
)

const testURL = "/x.html"

func link(source, ext, selected string, occ int) (string, error) {
	return Link(source, ext, selected, occ, testURL)
}

func wantLink(t *testing.T, source, ext, selected string, occ int, want string) {
	t.Helper()
	got, err := link(source, ext, selected, occ)
	if err != nil || got != want {
		t.Fatalf("Link(%q, %q, %q, %d) = %q, %v\nwant %q", source, ext, selected, occ, got, err, want)
	}
}

func wantErr(t *testing.T, source, ext, selected string, occ int, kind error, msg string) {
	t.Helper()
	_, err := link(source, ext, selected, occ)
	var le *Error
	if !errors.Is(err, kind) || !errors.As(err, &le) {
		t.Fatalf("Link(%q, %q, %q, %d) err = %v, want %v", source, ext, selected, occ, err, kind)
	}
	if msg != "" && le.Message != msg {
		t.Fatalf("message = %q, want %q", le.Message, msg)
	}
}

const (
	crossBlock = "Selection spans multiple paragraphs — select within one."
	oldAnchorP = `<p>before <a href="/old.html">beta</a> after</p>`
)

// Each case ports one test of test/services/selection_linker_test.rb.
func TestLinkPortedFromRuby(t *testing.T) {
	t.Run("wraps a markdown selection in a link", func(t *testing.T) {
		wantLink(t, "Alpha beta gamma.", ".md", "beta", 0, "Alpha [beta](/x.html) gamma.")
	})
	t.Run("wraps an html selection in an anchor", func(t *testing.T) {
		wantLink(t, "<p>Alpha beta gamma.</p>", ".html", "beta", 0, `<p>Alpha <a href="/x.html">beta</a> gamma.</p>`)
	})
	t.Run("wraps an html selection containing ordinary greater-than text", func(t *testing.T) {
		wantLink(t, "<p>1 > 0</p>", ".html", ">", 1, `<p>1 <a href="/x.html">></a> 0</p>`)
	})
	t.Run("picks the requested occurrence", func(t *testing.T) {
		wantLink(t, "cat dog cat bird cat", ".md", "cat", 2, "cat dog cat bird [cat](/x.html)")
	})
	t.Run("falls back to the first occurrence when index is out of range", func(t *testing.T) {
		wantLink(t, "cat dog cat", ".md", "cat", 9, "[cat](/x.html) dog cat")
	})
	t.Run("raises NotFound when the text is absent", func(t *testing.T) {
		wantErr(t, "Alpha beta.", ".md", "missing", 0, ErrNotFound, "")
	})
	t.Run("escapes closing brackets in the markdown label", func(t *testing.T) {
		wantLink(t, "a b] c", ".md", "b]", 0, `a [b\]](/x.html) c`)
	})
	t.Run("rejects a match inside an existing markdown link label", func(t *testing.T) {
		wantErr(t, "see [beta gamma](/old.html) end", ".md", "beta", 0, ErrUnsafeMatch, "")
	})
	t.Run("does not find text that only appears in a markdown link url", func(t *testing.T) {
		wantErr(t, "see [label](/beta.html) end", ".md", "beta", 0, ErrNotFound, "")
	})
	t.Run("does not find text that only appears inside a tag", func(t *testing.T) {
		wantErr(t, `<p class="beta">x</p>`, ".html", "beta", 0, ErrNotFound, "")
	})
	t.Run("does not find text that only appears inside a script block", func(t *testing.T) {
		wantErr(t, "<script>var beta = 1;</script><p>x</p>", ".html", "beta", 0, ErrNotFound, "")
	})
	t.Run("rejects an html match inside an existing anchor", func(t *testing.T) {
		wantErr(t, `<a href="/old.html">beta</a>`, ".html", "beta", 0, ErrUnsafeMatch, "")
	})
	t.Run("does not find a selection containing raw anchor markup", func(t *testing.T) {
		wantErr(t, oldAnchorP, ".html", `<a href="/old.html">beta</a>`, 0, ErrNotFound, "")
	})
	t.Run("does not find a selection containing raw closing-anchor markup", func(t *testing.T) {
		wantErr(t, oldAnchorP, ".html", "beta</a> after", 0, ErrNotFound, "")
	})
	t.Run("does not find a selection containing raw markdown link syntax", func(t *testing.T) {
		wantErr(t, "see [beta](/old.html) end", ".md", "[beta](/old.html)", 0, ErrNotFound, "")
	})
	t.Run("does not find raw link syntax with an escaped closing bracket", func(t *testing.T) {
		wantErr(t, `see [b\]](/old.html) end`, ".md", `[b\]](/old.html)`, 0, ErrNotFound, "")
	})
	t.Run("does not find a selection containing raw syntax crossing a link", func(t *testing.T) {
		wantErr(t, "see [beta](/old.html) and more", ".md", "[beta](/old.html) and", 0, ErrNotFound, "")
	})
	t.Run("requested occurrence is judged as-is", func(t *testing.T) {
		wantErr(t, `<a href="/old.html">beta</a> and beta again`, ".html", "beta", 0, ErrUnsafeMatch, "")
	})
	t.Run("wraps a selection spanning an inline span", func(t *testing.T) {
		wantLink(t, "this is <span> some </span> code", ".html", "some code", 0, `this is <a href="/x.html"><span> some </span> code</a>`)
	})
	t.Run("matches across differing whitespace", func(t *testing.T) {
		wantLink(t, "<p>some\ncode here</p>", ".html", "some code", 0, "<p><a href=\"/x.html\">some\ncode</a> here</p>")
	})
	t.Run("matches text containing entities and preserves them", func(t *testing.T) {
		wantLink(t, "<p>A &amp; B here</p>", ".html", "A & B", 0, `<p><a href="/x.html">A &amp; B</a> here</p>`)
	})
	t.Run("snaps to cover a partially selected emphasis element", func(t *testing.T) {
		wantLink(t, "<p>x <em>alpha beta</em> gamma</p>", ".html", "beta gamma", 0, `<p>x <a href="/x.html"><em>alpha beta</em> gamma</a></p>`)
	})
	t.Run("segments around an existing anchor", func(t *testing.T) {
		wantLink(t, oldAnchorP, ".html", "before beta after", 0,
			`<p><a href="/x.html">before</a> <a href="/old.html">beta</a> <a href="/x.html">after</a></p>`)
	})
	t.Run("rejects a selection spanning two paragraphs", func(t *testing.T) {
		wantErr(t, "<p>one</p><p>two</p>", ".html", "one two", 0, ErrUnsafeMatch, crossBlock)
	})
	t.Run("counts occurrences in rendered text, not raw source", func(t *testing.T) {
		wantLink(t, `<p class="beta">beta one beta two</p>`, ".html", "beta", 1, `<p class="beta">beta one <a href="/x.html">beta</a> two</p>`)
	})
	t.Run("wraps a markdown selection spanning inline code", func(t *testing.T) {
		wantLink(t, "this is `some` code", ".md", "some code", 0, "this is [`some` code](/x.html)")
	})
	t.Run("snaps to cover partially selected markdown emphasis", func(t *testing.T) {
		wantLink(t, "make *this bold* now ok", ".md", "bold now", 0, "make [*this bold* now](/x.html) ok")
	})
	t.Run("segments around an existing markdown link", func(t *testing.T) {
		wantLink(t, "see [beta](/old.html) and more", ".md", "beta and", 0, "see [beta](/old.html) [and](/x.html) more")
	})
	t.Run("segments around an image", func(t *testing.T) {
		wantLink(t, "pic ![alt](i.png) end", ".md", "pic end", 0, "[pic](/x.html) ![alt](i.png) [end](/x.html)")
	})
	t.Run("rejects a markdown selection spanning two paragraphs", func(t *testing.T) {
		wantErr(t, "alpha\n\nbeta", ".md", "alpha beta", 0, ErrUnsafeMatch, crossBlock)
	})
	t.Run("rejects a selection inside a fenced code block", func(t *testing.T) {
		wantErr(t, "before\n\n```\ncode here\n```\n", ".md", "code here", 0, ErrUnsafeMatch, "Selection is inside a code block.")
	})
	t.Run("links heading text within the heading block", func(t *testing.T) {
		wantLink(t, "# Title\ntext", ".md", "Title", 0, "# [Title](/x.html)\ntext")
	})
	t.Run("links one list item but rejects selections across items", func(t *testing.T) {
		wantLink(t, "- alpha\n- beta\n", ".md", "alpha", 0, "- [alpha](/x.html)\n- beta\n")
		wantErr(t, "- alpha\n- beta\n", ".md", "alpha beta", 0, ErrUnsafeMatch, "")
	})
}

func TestLinkNegativeOccurrenceCountsAsZero(t *testing.T) {
	wantLink(t, "cat dog cat", ".md", "cat", -3, "[cat](/x.html) dog cat")
}

func TestLinkRejectsInvalidUTF8(t *testing.T) {
	for _, tc := range []struct{ src, ext, sel string }{
		{"a \xff b", ".md", "b"},
		{"<p>a \xff b</p>", ".html", "b"},
		{"a b", ".md", "\xff"},
		{"<p>&#xD800; a</p>", ".html", "a"}, // surrogate reference decodes to invalid bytes
	} {
		if _, err := link(tc.src, tc.ext, tc.sel, 0); !errors.Is(err, ErrInvalidUTF8) {
			t.Errorf("Link(%q) err = %v, want ErrInvalidUTF8", tc.src, err)
		}
	}
}

func TestWriteEscapedLabel(t *testing.T) {
	for in, want := range map[string]string{
		"b]": `b\]`, `b\]`: `b\]`, "]]": `\]\]`, "]": `\]`, "": "", `\\]`: `\\]`,
	} {
		var b strings.Builder
		writeEscapedLabel(&b, in)
		if got := b.String(); got != want {
			t.Errorf("writeEscapedLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// Markdown maps are not always monotone: a raw tag that overshoots a link
// label end emits its newline at the tag's end offset, and scanning resumes
// before it. Both repros were found by differential testing against Ruby
// (expected values captured from Ruby 3.4.8).
func TestLinkNonMonotoneMarkdownMaps(t *testing.T) {
	wantLink(t, "b[<br]())>e\n", ".md", ")", 0, "b[<br]()[)](/x.html)>e\n")
	wantLink(t, "[vamp;<br]()]> z)*w\n", ".md", "]", 0, "[vamp;<br]()[\\]](/x.html)> z)*w\n")
}

func TestNonMonotoneMapIsFlaggedAndFiltersLinearly(t *testing.T) {
	m := buildMarkdownMap("b[<br]())>e\n")
	if !m.nonMonotone {
		t.Fatal("expected the map to be flagged non-monotone")
	}
	if got, want := withinIndices(m, srcRange{9, 11}), linearWithin(m, srcRange{9, 11}); !slices.Equal(got, want) {
		t.Fatalf("plainWithin = %v, linear filter = %v", got, want)
	}
	if mono := buildMarkdownMap("plain *text* here\n"); mono.nonMonotone {
		t.Fatal("an ordinary map must stay on the binary-search path")
	}
}

// linearWithin is Ruby's Map#plain_indices_within verbatim:
// starts.each_index.select { |i| starts[i] >= begin && ends[i] <= end }.
func linearWithin(m *docMap, r srcRange) []int {
	out := []int{}
	for i := range m.Starts {
		if int(m.Starts[i]) >= r.Begin && int(m.Ends[i]) <= r.End {
			out = append(out, i)
		}
	}
	return out
}

func withinIndices(m *docMap, r srcRange) []int {
	out := []int{}
	m.plainWithin(r, func(i int) bool { out = append(out, i); return true })
	return out
}

func TestCheckSourceSize(t *testing.T) {
	if err := checkSourceSize(math.MaxInt32); err != nil {
		t.Fatalf("MaxInt32 bytes must be accepted: %v", err)
	}
	if err := checkSourceSize(math.MaxInt32 + 1); !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("err = %v, want ErrSourceTooLarge", err)
	}
}

// Every error Link returns carries the Ruby message of its raise site, never
// a sentinel placeholder.
func TestReturnedErrorsCarryRubyMessages(t *testing.T) {
	for _, tc := range []struct{ src, ext, sel, want string }{
		{"<p>one</p><p>two</p>", ".html", "one two", "Selection spans multiple paragraphs — select within one."},
		{"before\n\n```\ncode here\n```\n", ".md", "code here", "Selection is inside a code block."},
		{`<a href="/o">beta</a>`, ".html", "beta", "Selection overlaps an existing link."},
		{"alpha", ".md", "missing", "Selection not found in source — select a plainer run of text."},
	} {
		_, err := link(tc.src, tc.ext, tc.sel, 0)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%q: err = %v, want message %q", tc.sel, err, tc.want)
		}
	}
	if got := ErrUnsafeMatch.Error(); got != "linker: UnsafeMatch" {
		t.Errorf("sentinel Error() = %q", got)
	}
}
