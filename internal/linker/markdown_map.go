package linker

import (
	"sort"
	"strings"
)

// mdBuilder ports SelectionLinker::MarkdownMap. It is a line-oriented
// projector, not a Markdown parser: it tracks fences, indented code,
// headings, list items and blockquotes per line, and tokenizes inline syntax
// within a line.
//
// Differences from the Ruby that are purely algorithmic (same results, linear
// time on adversarial input; verified by the differential harness):
//   - inline tokens are found by a hand-written scanner bounded to the current
//     line instead of INLINE_TOKEN.match(source, position) over the whole
//     remaining document (a token starting at or after the line end is
//     ignored by Ruby anyway);
//   - closing backtick runs, label ends and ')' positions are indexed once per
//     line instead of searched per opener;
//   - emphasis removal tombstones the opener's bytes and compacts once at the
//     end instead of Array#slice! (the opener being removed is always the
//     newest still-live byte range of the stack, so earlier indices never
//     shift);
//   - open raw-HTML tags use one stack per name (see htmlBuilder.open).
type mdBuilder struct {
	src     string
	m       *docMap
	blockID int32

	emph    []emphEntry
	removed []srcRange // plain index ranges to drop at the end
	open    map[string][]int

	// per-line state
	lineStart, lineEnd, contentEnd int
	gt                             searchMemo
	runs                           map[int][]int32 // backtick run length -> start offsets
	labelsReady                    bool
	labelEnd                       []int32 // see ensureLabels
	parens                         []int32
}

type emphEntry struct {
	text       string
	start      int
	plainIndex int
}

// buildMarkdownMap ports MarkdownMap.build.
func buildMarkdownMap(src string) *docMap {
	b := &mdBuilder{src: src, m: newDocMap(len(src)), open: map[string][]int{}}
	b.build()
	b.compact()
	return b.m
}

func isStripSpace(c byte) bool { return c == 0 || isSpace(c) }

// lstripOffset is the offset after Ruby's String#lstrip (ASCII whitespace and NUL).
func lstripOffset(s string) int {
	i := 0
	for i < len(s) && isStripSpace(s[i]) {
		i++
	}
	return i
}

func blankLine(s string) bool { return lstripOffset(s) == len(s) } // content.strip.empty?

// fenceChar ports FENCE (\A(`{3,}|~{3,})) on s: the fence character (the
// first character of capture group 1), or 0 when s does not start with a fence.
func fenceChar(s string) byte {
	if len(s) >= 3 && (s[0] == '`' || s[0] == '~') && s[1] == s[0] && s[2] == s[0] {
		return s[0]
	}
	return 0
}

// chomp ports String#chomp: drop one trailing "\r\n", "\n" or "\r".
func chomp(s string) string {
	switch {
	case strings.HasSuffix(s, "\r\n"):
		return s[:len(s)-2]
	case strings.HasSuffix(s, "\n"), strings.HasSuffix(s, "\r"):
		return s[:len(s)-1]
	}
	return s
}

func isSpaceTab(c byte) bool { return c == ' ' || c == '\t' }

// headingEnd ports HEADING (\A#{1,6}[ \t]+): the match end, or -1.
func headingEnd(s string) int {
	k := 0
	for k < len(s) && s[k] == '#' {
		k++
	}
	if k < 1 || k > 6 || k >= len(s) || !isSpaceTab(s[k]) {
		return -1
	}
	for k < len(s) && isSpaceTab(s[k]) {
		k++
	}
	return k
}

// listItemEnd ports LIST_ITEM (\A[ \t]*(?:[-*+]|\d{1,9}[.)])[ \t]+).
func listItemEnd(s string) int {
	i := 0
	for i < len(s) && isSpaceTab(s[i]) {
		i++
	}
	switch {
	case i < len(s) && (s[i] == '-' || s[i] == '*' || s[i] == '+'):
		i++
	default:
		d := i
		for d < len(s) && isDigit(s[d]) {
			d++
		}
		if d == i || d-i > 9 || d >= len(s) || (s[d] != '.' && s[d] != ')') {
			return -1
		}
		i = d + 1
	}
	k := i
	for k < len(s) && isSpaceTab(s[k]) {
		k++
	}
	if k == i {
		return -1
	}
	return k
}

// blockquoteEnd ports BLOCKQUOTE (\A[ \t]{0,3}>[ \t]?).
func blockquoteEnd(s string) int {
	i := 0
	for i < len(s) && i < 3 && isSpaceTab(s[i]) {
		i++
	}
	if i >= len(s) || s[i] != '>' {
		return -1
	}
	i++
	if i < len(s) && isSpaceTab(s[i]) {
		i++
	}
	return i
}

// indentedCode ports INDENTED_CODE (\A(?: {4}|\t)).
func indentedCode(s string) bool {
	return strings.HasPrefix(s, "    ") || strings.HasPrefix(s, "\t")
}

// build ports MarkdownMap#build.
func (b *mdBuilder) build() {
	src := b.src
	inFence := false
	var fence byte
	previousBlank := true
	inIndentedCode := false

	for offset := 0; offset < len(src); {
		lineStart := offset
		lineLen := len(src) - offset
		if nl := strings.IndexByte(src[offset:], '\n'); nl >= 0 {
			lineLen = nl + 1
		}
		line := src[lineStart : lineStart+lineLen]
		offset += lineLen
		content := chomp(line)
		contentEnd := lineStart + len(content)
		b.startLine(lineStart, lineStart+lineLen, contentEnd)

		stripped := content[lstripOffset(content):]
		if inFence {
			if fenceChar(stripped) == fence {
				inFence = false
				b.blockID++
			} else {
				b.emitVerbatim(lineStart, lineStart+lineLen)
				b.m.UnlinkableRanges = append(b.m.UnlinkableRanges, srcRange{lineStart, lineStart + lineLen})
			}
			continue
		}

		if f := fenceChar(stripped); f != 0 {
			inFence = true
			fence = f
			b.blockID++
			previousBlank = false
			continue
		}

		if blankLine(content) {
			b.blockID++
			previousBlank = true
			inIndentedCode = false
			continue
		}

		if indentedCode(content) && (previousBlank || inIndentedCode) {
			if !inIndentedCode {
				b.blockID++
			}
			inIndentedCode = true
			b.emitVerbatim(lineStart, lineStart+lineLen)
			b.m.UnlinkableRanges = append(b.m.UnlinkableRanges, srcRange{lineStart, lineStart + lineLen})
			previousBlank = false
			continue
		}
		inIndentedCode = false

		scanStart := lineStart
		heading := false
		if e := headingEnd(content); e >= 0 {
			b.blockID++
			heading = true
			scanStart = lineStart + e
		} else if e := listItemEnd(content); e >= 0 {
			b.blockID++
			scanStart = lineStart + e
		} else if e := blockquoteEnd(content); e >= 0 {
			scanStart = lineStart + e
		}

		b.scanInline(scanStart, contentEnd)
		if strings.HasSuffix(line, "\n") {
			b.m.emit('\n', contentEnd, contentEnd+1, b.blockID)
		}
		if heading {
			b.blockID++
		}
		previousBlank = false
	}
}

func (b *mdBuilder) startLine(start, end, contentEnd int) {
	b.lineStart, b.lineEnd, b.contentEnd = start, end, contentEnd
	b.gt = searchMemo{}
	b.runs = nil
	b.labelsReady = false
}

// ---- inline scanning ------------------------------------------------------

type inlineKind int

const (
	inEscape inlineKind = iota
	inTicks
	inImage
	inBracket
	inTag
	inEntity
	inEmphasis
)

// scanInline ports MarkdownMap#scan_inline.
func (b *mdBuilder) scanInline(from, upto int) {
	position := from
	for position < upto {
		start, end, kind := b.nextInlineToken(position)
		if start < 0 || start >= upto {
			b.emitVerbatim(position, upto)
			return
		}
		b.emitVerbatim(position, start)
		position = b.handleInlineToken(start, end, kind, upto)
	}
}

func escapable(c byte) bool { // [!-/:-@\[-`{-~]
	return (c >= '!' && c <= '/') || (c >= ':' && c <= '@') || (c >= '[' && c <= '`') || (c >= '{' && c <= '~')
}

func (b *mdBuilder) indexGT(p int) int {
	if at, ok := b.gt.get(p); ok {
		return at
	}
	i := strings.IndexByte(b.src[p:b.lineEnd], '>')
	if i < 0 {
		return b.gt.put(p, -1)
	}
	return b.gt.put(p, p+i)
}

// nextInlineToken ports @source.match(INLINE_TOKEN, position) restricted to the
// current line:
//
//	\\[!-/:-@\[-`{-~] | `+ | !?\[ | </?[A-Za-z][^>\n]*> | &(?:name|#dec|#xhex); | \*{1,2} | _{1,2}
func (b *mdBuilder) nextInlineToken(from int) (start, end int, kind inlineKind) {
	src, n := b.src, b.lineEnd
	for i := from; i < n; i++ {
		switch c := src[i]; c {
		case '\\':
			if i+1 < n && escapable(src[i+1]) {
				return i, i + 2, inEscape
			}
		case '`':
			j := i + 1
			for j < n && src[j] == '`' {
				j++
			}
			return i, j, inTicks
		case '!':
			if i+1 < n && src[i+1] == '[' {
				return i, i + 2, inImage
			}
		case '[':
			return i, i + 1, inBracket
		case '<':
			j := i + 1
			if j < n && src[j] == '/' {
				j++
			}
			if j < n && isAlpha(src[j]) {
				if g := b.indexGT(j); g >= 0 {
					return i, g + 1, inTag
				}
			}
		case '&':
			if e := entityEnd(src[:n], i); e >= 0 {
				return i, e, inEntity
			}
		case '*', '_':
			j := i + 1
			if j < n && src[j] == c {
				j++
			}
			return i, j, inEmphasis
		}
	}
	return -1, -1, 0
}

// handleInlineToken ports MarkdownMap#handle_inline_token.
func (b *mdBuilder) handleInlineToken(start, end int, kind inlineKind, upto int) int {
	text := b.src[start:end]
	switch kind {
	case inEscape:
		b.m.emit(text[1], start, end, b.blockID)
		return end
	case inTicks:
		return b.codeSpan(start, len(text), upto)
	case inImage:
		return b.image(start, upto)
	case inBracket:
		return b.link(start, upto)
	case inTag:
		return b.htmlTag(text, start, end)
	case inEntity:
		decoded := cgiUnescape(text)
		for i := 0; i < len(decoded); i++ {
			b.m.emit(decoded[i], start, end, b.blockID)
		}
		return end
	}
	return b.emphasis(text, start, end)
}

// codeSpan ports MarkdownMap#code_span. The Ruby closer is
// /(?<!`)`{n}(?!`)/ searched from the end of the opener over the whole
// document; closer.end > upto rejects it (so only the current line matters).
// The lookbehind/lookahead make it "the first maximal backtick run of exactly
// n", which the per-line run index answers.
func (b *mdBuilder) codeSpan(start, tickCount, upto int) int {
	openerEnd := start + tickCount
	cs, ce, ok := b.closingTicks(openerEnd, tickCount)
	if !ok || ce > upto {
		b.emitVerbatim(start, openerEnd)
		return openerEnd
	}
	b.emitVerbatim(openerEnd, cs)
	b.m.InlineRanges = append(b.m.InlineRanges, srcRange{start, ce})
	return ce
}

// closingTicks returns the first maximal backtick run of length n starting at
// or after from, within the current line.
func (b *mdBuilder) closingTicks(from, n int) (int, int, bool) {
	if b.runs == nil {
		b.runs = map[int][]int32{}
		for i := b.lineStart; i < b.lineEnd; {
			if b.src[i] != '`' {
				i++
				continue
			}
			j := i
			for j < b.lineEnd && b.src[j] == '`' {
				j++
			}
			b.runs[j-i] = append(b.runs[j-i], int32(i))
			i = j
		}
	}
	starts := b.runs[n]
	k := sort.Search(len(starts), func(i int) bool { return int(starts[i]) >= from })
	if k == len(starts) {
		return 0, 0, false
	}
	return int(starts[k]), int(starts[k]) + n, true
}

// ensureLabels precomputes, for the current line, labelEnd[i] = result of the
// Ruby find_label_end scan started at lineStart+i with no upper bound (the
// absolute offset of the first ']' reached, stepping 2 over a backslash, or
// -1), and the offsets of every ')' in the line.
func (b *mdBuilder) ensureLabels() {
	if b.labelsReady {
		return
	}
	b.labelsReady = true
	n := b.contentEnd - b.lineStart
	if cap(b.labelEnd) < n+2 {
		b.labelEnd = make([]int32, n+2)
	}
	b.labelEnd = b.labelEnd[:n+2]
	b.labelEnd[n], b.labelEnd[n+1] = -1, -1
	b.parens = b.parens[:0]
	for i := n - 1; i >= 0; i-- {
		switch b.src[b.lineStart+i] {
		case ']':
			b.labelEnd[i] = int32(b.lineStart + i)
		case '\\':
			b.labelEnd[i] = b.labelEnd[i+2]
		default:
			b.labelEnd[i] = b.labelEnd[i+1]
		}
		if b.src[b.lineStart+i] == ')' {
			b.parens = append(b.parens, int32(b.lineStart+i))
		}
	}
	// parens were collected right to left
	for i, j := 0, len(b.parens)-1; i < j; i, j = i+1, j-1 {
		b.parens[i], b.parens[j] = b.parens[j], b.parens[i]
	}
}

// findLabelEnd ports MarkdownMap#find_label_end: the offset of the first ']'
// at or after from and before upto (a backslash skips the next byte), or -1.
func (b *mdBuilder) findLabelEnd(from, upto int) int {
	b.ensureLabels()
	i := from - b.lineStart
	if i < 0 || i >= len(b.labelEnd)-2 {
		return -1
	}
	if r := int(b.labelEnd[i]); r >= 0 && r < upto {
		return r
	}
	return -1
}

// indexParen ports @source.index(")", from), restricted to the current line
// (the caller only accepts a result below upto <= contentEnd).
func (b *mdBuilder) indexParen(from int) int {
	b.ensureLabels()
	k := sort.Search(len(b.parens), func(i int) bool { return int(b.parens[i]) >= from })
	if k == len(b.parens) {
		return -1
	}
	return int(b.parens[k])
}

// link ports MarkdownMap#link.
func (b *mdBuilder) link(start, upto int) int {
	if labelEnd := b.findLabelEnd(start+1, upto); labelEnd >= 0 && labelEnd+1 < len(b.src) && b.src[labelEnd+1] == '(' {
		if close := b.indexParen(labelEnd + 2); close >= 0 && close < upto {
			b.scanInline(start+1, labelEnd)
			b.m.LinkRanges = append(b.m.LinkRanges, srcRange{start, close + 1})
			return close + 1
		}
	}
	b.emitVerbatim(start, start+1)
	return start + 1
}

// image ports MarkdownMap#image.
func (b *mdBuilder) image(start, upto int) int {
	if labelEnd := b.findLabelEnd(start+2, upto); labelEnd >= 0 && labelEnd+1 < len(b.src) && b.src[labelEnd+1] == '(' {
		if close := b.indexParen(labelEnd + 2); close >= 0 && close < upto {
			b.m.LinkRanges = append(b.m.LinkRanges, srcRange{start, close + 1})
			return close + 1
		}
	}
	b.emitVerbatim(start, start+1)
	return start + 1
}

// htmlTag ports MarkdownMap#html_tag.
func (b *mdBuilder) htmlTag(text string, start, finish int) int {
	name := tagName(text)
	switch {
	case name == "br":
		b.m.emit('\n', finish, finish, b.blockID)
	case strings.HasPrefix(text, "</"):
		if stack := b.open[name]; len(stack) > 0 {
			openStart := stack[len(stack)-1]
			b.open[name] = stack[:len(stack)-1]
			r := srcRange{openStart, finish}
			b.m.InlineRanges = append(b.m.InlineRanges, r)
			if name == "a" {
				b.m.LinkRanges = append(b.m.LinkRanges, r)
			}
		}
	case inlineTags[name] && !strings.HasSuffix(text, "/>"):
		b.open[name] = append(b.open[name], start)
	}
	return finish
}

// emphasis ports MarkdownMap#emphasis. Simplified pairing: a delimiter run
// closes the stack top when it matches exactly (same text) and is preceded by
// non-space; otherwise it opens when followed by non-space; otherwise it stays
// literal. Openers are emitted provisionally and removed from the projection
// when their closer arrives, so an opener that never closes stays literal.
func (b *mdBuilder) emphasis(text string, start, finish int) int {
	if b.closer(start) && len(b.emph) > 0 && b.emph[len(b.emph)-1].text == text {
		top := b.emph[len(b.emph)-1]
		b.emph = b.emph[:len(b.emph)-1]
		b.removed = append(b.removed, srcRange{top.plainIndex, top.plainIndex + len(text)})
		b.m.InlineRanges = append(b.m.InlineRanges, srcRange{top.start, finish})
	} else if b.opener(finish) {
		b.emph = append(b.emph, emphEntry{text, start, len(b.m.Plain)})
		b.emitVerbatim(start, finish)
	} else {
		b.emitVerbatim(start, finish)
	}
	return finish
}

func (b *mdBuilder) opener(finish int) bool {
	return finish < len(b.src) && !isSpace(b.src[finish])
}

func (b *mdBuilder) closer(start int) bool {
	return start > 0 && !isSpace(b.src[start-1])
}

// compact drops the tombstoned emphasis delimiters (ports remove_plain).
func (b *mdBuilder) compact() {
	if len(b.removed) == 0 {
		return
	}
	drop := make([]bool, len(b.m.Plain))
	for _, r := range b.removed {
		for i := r.Begin; i < r.End; i++ {
			drop[i] = true
		}
	}
	m := b.m
	w := 0
	for i := range m.Plain {
		if drop[i] {
			continue
		}
		m.Plain[w], m.Starts[w], m.Ends[w], m.BlockIDs[w] = m.Plain[i], m.Starts[i], m.Ends[i], m.BlockIDs[i]
		w++
	}
	m.Plain, m.Starts, m.Ends, m.BlockIDs = m.Plain[:w], m.Starts[:w], m.Ends[:w], m.BlockIDs[:w]
}

func (b *mdBuilder) emitVerbatim(from, upto int) {
	for i := from; i < upto; i++ {
		b.m.emit(b.src[i], i, i+1, b.blockID)
	}
}
