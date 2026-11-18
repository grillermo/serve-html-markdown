package linker

import (
	"strings"
	"unicode/utf8"
)

// Link ports SelectionLinker.link: it wraps the occurrence-th match of
// selectedText (as rendered text) in a link to url inside source. The source is
// HTML when extension is ".html"; any other extension is Markdown. A negative
// occurrence counts as 0; an occurrence beyond the last match falls back to the
// first.
//
// Expected failures are *Error (errors.Is ErrNotFound / ErrUnsafeMatch). A
// source or selection that is not valid UTF-8 yields ErrInvalidUTF8 (Ruby
// raises ArgumentError there).
func Link(source, extension, selectedText string, occurrence int, url string) (string, error) {
	if !utf8.ValidString(source) {
		return "", ErrInvalidUTF8
	}
	occurrence = max(occurrence, 0)
	markdown := extension != ".html"

	var m *Map
	if markdown {
		m = BuildMarkdownMap(source)
	} else {
		m = BuildHTMLMap(source)
	}
	plain, err := Locate(m, selectedText, occurrence)
	if err != nil {
		return "", err
	}
	segments, err := Plan(m, plain, source)
	if err != nil {
		return "", err
	}
	return splice(source, segments, markdown, url), nil
}

// splice ports SelectionLinker#splice. The Ruby replaces segments from last to
// first; the segments are disjoint and ascending, so one left-to-right pass
// builds the same string.
func splice(source string, segments []Range, markdown bool, url string) string {
	var b strings.Builder
	prev := 0
	for _, seg := range segments {
		b.WriteString(source[prev:seg.Begin])
		slice := source[seg.Begin:seg.End]
		if markdown {
			b.WriteByte('[')
			writeEscapedLabel(&b, slice)
			b.WriteString("](")
			b.WriteString(url)
			b.WriteByte(')')
		} else {
			b.WriteString(`<a href="`)
			b.WriteString(url)
			b.WriteString(`">`)
			b.WriteString(slice)
			b.WriteString("</a>")
		}
		prev = seg.End
	}
	b.WriteString(source[prev:])
	return b.String()
}

// escapeLabel ports slice.gsub(/(?<!\\)\]/) { "\\]" }: a ']' not already
// preceded by a backslash gets one.
func escapeLabel(s string) string {
	var b strings.Builder
	writeEscapedLabel(&b, s)
	return b.String()
}

func writeEscapedLabel(b *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		if s[i] == ']' && (i == 0 || s[i-1] != '\\') {
			b.WriteString(`\]`)
			continue
		}
		b.WriteByte(s[i])
	}
}
