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

// srcRange is a half-open byte range [Begin, End), like Ruby's a...b.
type srcRange struct{ Begin, End int }

// docMap is an offset-preserving projection of a source document (ports
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
// Starts and Ends are usually non-decreasing, but NOT always: in Markdown a
// raw inline tag found while scanning a link label can overshoot the label
// end ("[<br]()..." emits the tag's newline at the tag's end offset, then the
// scan resumes inside the tag), so later plain bytes can map to smaller
// offsets than earlier ones. emit tracks whether the sequences are still
// non-decreasing (nonMonotone) so plainWithin can binary-search when they are
// and fall back to Ruby's linear filter when they are not.
type docMap struct {
	Plain            []byte
	Starts           []int32
	Ends             []int32
	BlockIDs         []int32
	InlineRanges     []srcRange
	LinkRanges       []srcRange
	UnlinkableRanges []srcRange

	// nonMonotone is set once some emit appended a Start or End smaller than
	// its predecessor. It stays set conservatively even if a later compaction
	// removes the offending byte. The zero value (a hand-built map) means
	// "monotone"; hand-built maps must use emit.
	nonMonotone bool
}

// newDocMap returns a map whose parallel slices have room for sizeHint plain
// bytes (the source length is a good hint: the projection rarely exceeds it).
func newDocMap(sizeHint int) *docMap {
	return &docMap{
		Plain:    make([]byte, 0, sizeHint),
		Starts:   make([]int32, 0, sizeHint),
		Ends:     make([]int32, 0, sizeHint),
		BlockIDs: make([]int32, 0, sizeHint),
	}
}

// sourceRange ports Map#source_range_for: the source range covered by a plain
// range.
func (m *docMap) sourceRange(plain srcRange) srcRange {
	return srcRange{int(m.Starts[plain.Begin]), int(m.Ends[plain.End-1])}
}

// plainWithin ports Map#plain_indices_within: it calls fn for every plain
// index i with Starts[i] >= r.Begin && Ends[i] <= r.End, in increasing order,
// until fn returns false.
//
// Ruby selects over all indices. When Starts and Ends are non-decreasing the
// qualifying indices are contiguous and found by binary search; otherwise the
// exact Ruby filter runs over every index.
func (m *docMap) plainWithin(r srcRange, fn func(i int) bool) {
	n := len(m.Starts)
	if m.nonMonotone {
		for i := 0; i < n; i++ {
			if int(m.Starts[i]) >= r.Begin && int(m.Ends[i]) <= r.End && !fn(i) {
				return
			}
		}
		return
	}
	lo := sort.Search(n, func(i int) bool { return int(m.Starts[i]) >= r.Begin })
	hi := sort.Search(n, func(i int) bool { return int(m.Ends[i]) > r.End })
	for i := lo; i < hi; i++ {
		if !fn(i) {
			return
		}
	}
}

// emit appends one plain byte with its source range and block id.
func (m *docMap) emit(b byte, start, end int, block int32) {
	if n := len(m.Starts); n > 0 && (int32(start) < m.Starts[n-1] || int32(end) < m.Ends[n-1]) {
		m.nonMonotone = true
	}
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
