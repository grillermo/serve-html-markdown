// Package linker wraps a selected run of rendered text in a link inside the
// Markdown or HTML source document. It is a port of the Rails
// SelectionLinker (app/services/selection_linker*.rb): the Ruby code and its
// tests are the specification.
//
// All offsets are byte offsets into the source string. Ruby indexes by
// character; because the source must be valid UTF-8 (see ErrInvalidUTF8) and
// every offset the algorithm produces falls on a character boundary, the two
// views agree.
package linker

import "sort"

// Range is a half-open byte range [Begin, End), like Ruby's a...b.
type Range struct{ Begin, End int }

// Map is an offset-preserving projection of a source document (ports
// SelectionLinker::Map).
//
//	Plain             rendered-text projection of the source, one entry per byte
//	Starts            source offset where plain byte i begins
//	Ends              source offset (exclusive) where plain byte i ends; a
//	                  multi-byte source run (an entity) maps every plain byte to
//	                  the full run so a splice can never split one
//	BlockIDs          block id for plain byte i; a match touching two ids
//	                  crosses a block boundary
//	InlineRanges      full source ranges of inline elements (snap targets)
//	LinkRanges        full source ranges of existing links/images
//	UnlinkableRanges  source ranges where a link must not be spliced
//
// Starts and Ends are non-decreasing (every builder emits plain bytes in
// source order), which PlainIndicesWithin relies on.
type Map struct {
	Plain            []byte
	Starts           []int32
	Ends             []int32
	BlockIDs         []int32
	InlineRanges     []Range
	LinkRanges       []Range
	UnlinkableRanges []Range
}

// SourceRange ports Map#source_range_for: the source range covered by a plain
// range.
func (m *Map) SourceRange(plain Range) Range {
	return Range{int(m.Starts[plain.Begin]), int(m.Ends[plain.End-1])}
}

// PlainIndicesWithin ports Map#plain_indices_within. Ruby returns every plain
// index i with Starts[i] >= r.Begin && Ends[i] <= r.End; since Starts and Ends
// are non-decreasing those indices are contiguous, so it returns them as the
// half-open index range [lo, hi) found by binary search.
func (m *Map) PlainIndicesWithin(r Range) (lo, hi int) {
	n := len(m.Starts)
	lo = sort.Search(n, func(i int) bool { return int(m.Starts[i]) >= r.Begin })
	hi = sort.Search(n, func(i int) bool { return int(m.Ends[i]) > r.End })
	if hi < lo {
		hi = lo
	}
	return lo, hi
}

// emit appends one plain byte with its source range and block id.
func (m *Map) emit(b byte, start, end int, block int32) {
	m.Plain = append(m.Plain, b)
	m.Starts = append(m.Starts, int32(start))
	m.Ends = append(m.Ends, int32(end))
	m.BlockIDs = append(m.BlockIDs, block)
}

// isSpace reports whether b matches Ruby's /\s/: ASCII [ \t\r\n\f\v]. (Go's
// regexp \s omits \v, and Ruby's \s never matches non-ASCII.)
func isSpace(b byte) bool {
	return b == ' ' || (b >= '\t' && b <= '\r')
}

func isAlpha(b byte) bool { return (b|0x20) >= 'a' && (b|0x20) <= 'z' }

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func isAlnum(b byte) bool { return isAlpha(b) || isDigit(b) }

func isHex(b byte) bool { return isDigit(b) || ((b|0x20) >= 'a' && (b|0x20) <= 'f') }

func lowerASCII(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}
