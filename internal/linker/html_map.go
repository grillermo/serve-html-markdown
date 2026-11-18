package linker

import (
	"regexp"
	"strings"
)

// inlineTags ports HtmlMap::INLINE_TAGS.
var inlineTags = map[string]bool{
	"a": true, "abbr": true, "b": true, "cite": true, "code": true, "del": true, "em": true,
	"i": true, "ins": true, "kbd": true, "mark": true, "q": true, "s": true, "samp": true,
	"small": true, "span": true, "strong": true, "sub": true, "sup": true, "time": true,
	"u": true, "var": true,
}

// voidTags ports HtmlMap::VOID_TAGS.
var voidTags = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true,
	"input": true, "link": true, "meta": true, "param": true, "source": true, "track": true, "wbr": true,
}

// Raw-text close-tag patterns port %r{</#{name}\s*>}i. Ruby's /i applies full
// Unicode case folding, so U+017F (ſ) matches s (Go's (?i) agrees) and the
// ligatures U+FB05/U+FB06 match "st" (Go's does not; added by hand). \s is
// spelled out because Go's \s lacks \v. Verified exhaustively against Ruby.
var rawTextClose = map[string]*regexp.Regexp{
	"script": regexp.MustCompile(`(?i)</script[ \t-\r]*>`),
	"style":  regexp.MustCompile(`(?i)</(?:st|ﬅ|ﬆ)yle[ \t-\r]*>`),
}

type htmlBuilder struct {
	src     string
	m       *docMap
	blockID int32
	// open tags by name: source start of each unclosed opener. Ruby keeps one
	// stack and closes with rindex(name); only same-name order matters, so one
	// stack per name is equivalent and avoids a linear rindex.
	open map[string][]int

	// Memoised "first '>' at or after p" and "first '-->' at or after p": the
	// tokenizer would otherwise rescan to end of input for every unclosed '<'.
	gt, commentEnd searchMemo
}

// searchMemo caches the answer of an unbounded forward search: the first
// match at or after from is at (-1 for none). A query p with from <= p <= at
// (or any p >= from when none) has the same answer.
type searchMemo struct {
	valid    bool
	from, at int
}

func (s *searchMemo) get(p int) (at int, ok bool) {
	if s.valid && s.from <= p && (s.at == -1 || p <= s.at) {
		return s.at, true
	}
	return 0, false
}

func (s *searchMemo) put(p, at int) int {
	s.valid, s.from, s.at = true, p, at
	return at
}

func (b *htmlBuilder) indexGT(p int) int {
	if at, ok := b.gt.get(p); ok {
		return at
	}
	i := strings.IndexByte(b.src[p:], '>')
	if i < 0 {
		return b.gt.put(p, -1)
	}
	return b.gt.put(p, p+i)
}

func (b *htmlBuilder) indexCommentEnd(p int) int {
	if at, ok := b.commentEnd.get(p); ok {
		return at
	}
	i := strings.Index(b.src[p:], "-->")
	if i < 0 {
		return b.commentEnd.put(p, -1)
	}
	return b.commentEnd.put(p, p+i)
}

type tokenKind int

const (
	tokComment tokenKind = iota
	tokDecl
	tokTag
	tokEntity
)

// buildHTMLMap ports HtmlMap.build: it projects an HTML source into rendered
// text with offsets back to the source.
func buildHTMLMap(src string) *docMap {
	b := &htmlBuilder{src: src, m: newDocMap(len(src)), open: map[string][]int{}}
	position := 0
	for {
		start, end, kind := b.nextToken(position)
		if start < 0 {
			break
		}
		b.emitText(position, start)
		position = b.handleToken(start, end, kind)
	}
	b.emitText(position, len(src))
	return b.m
}

// nextToken ports @source.match(TOKEN, position), where
//
//	TOKEN = <!--.*?--> | </?[A-Za-z][^>]*> | <![^>]*> | &(?:[A-Za-z][A-Za-z0-9]*|#[0-9]+|#x[0-9A-Fa-f]+);
//
// (dot matches newline; leftmost start wins, then first alternative).
func (b *htmlBuilder) nextToken(from int) (start, end int, kind tokenKind) {
	src, n := b.src, len(b.src)
	for i := from; i < n; i++ {
		switch src[i] {
		case '<':
			if i+1 >= n {
				continue
			}
			switch c := src[i+1]; {
			case c == '!':
				if strings.HasPrefix(src[i:], "<!--") {
					if e := b.indexCommentEnd(i + 4); e >= 0 {
						return i, e + 3, tokComment
					}
				}
				if g := b.indexGT(i + 2); g >= 0 {
					return i, g + 1, tokDecl
				}
			case isAlpha(c):
				if g := b.indexGT(i + 1); g >= 0 {
					return i, g + 1, tokTag
				}
			case c == '/' && i+2 < n && isAlpha(src[i+2]):
				if g := b.indexGT(i + 2); g >= 0 {
					return i, g + 1, tokTag
				}
			}
		case '&':
			if e := entityEnd(src, i); e >= 0 {
				return i, e, tokEntity
			}
		}
	}
	return -1, -1, 0
}

// entityEnd returns the end of the entity at src[i] ('&'), or -1:
// &name; | &#digits; | &#xhex; (lowercase x only, like the Ruby regexp).
func entityEnd(src string, i int) int {
	n := len(src)
	j := i + 1
	if j >= n {
		return -1
	}
	switch c := src[j]; {
	case isAlpha(c):
		for j < n && isAlnum(src[j]) {
			j++
		}
	case c == '#':
		j++
		if j < n && src[j] == 'x' {
			j++
			k := j
			for j < n && isHex(src[j]) {
				j++
			}
			if j == k {
				return -1
			}
		} else {
			k := j
			for j < n && isDigit(src[j]) {
				j++
			}
			if j == k {
				return -1
			}
		}
	default:
		return -1
	}
	if j < n && src[j] == ';' {
		return j + 1
	}
	return -1
}

// tagName ports text[%r{\A</?([A-Za-z][A-Za-z0-9-]*)}, 1].downcase for a tag
// token text (which always starts with '<', optional '/', then a letter).
func tagName(text string) string {
	i := 1
	if text[i] == '/' {
		i++
	}
	j := i
	for j < len(text) && (isAlnum(text[j]) || text[j] == '-') {
		j++
	}
	return lowerASCII(text[i:j])
}

func (b *htmlBuilder) emitText(from, upto int) {
	for i := from; i < upto; i++ {
		b.m.emit(b.src[i], i, i+1, b.blockID)
	}
}

// handleToken ports HtmlMap#handle_token and returns the next scan position.
func (b *htmlBuilder) handleToken(start, end int, kind tokenKind) int {
	text := b.src[start:end]
	switch kind {
	case tokEntity:
		decoded := cgiUnescape(text)
		for i := 0; i < len(decoded); i++ {
			b.m.emit(decoded[i], start, end, b.blockID)
		}
		return end
	case tokTag:
		name := tagName(text)
		if strings.HasPrefix(text, "</") {
			b.closeTag(name, start, end)
			return end
		}
		return b.openTag(name, text, start, end)
	}
	return end // comments and <!doctype ...>
}

// openTag ports HtmlMap#open_tag.
func (b *htmlBuilder) openTag(name, text string, start, end int) int {
	if re, ok := rawTextClose[name]; ok {
		if loc := re.FindStringIndex(b.src[end:]); loc != nil {
			return end + loc[1]
		}
		return len(b.src)
	}
	if name == "br" || name == "hr" {
		b.m.emit('\n', end, end, b.blockID)
		return end
	}
	if voidTags[name] {
		return end
	}
	switch {
	case strings.HasSuffix(text, "/>"):
		if !inlineTags[name] {
			b.blockSeparator(start)
			b.blockID++
		}
	case inlineTags[name]:
		b.open[name] = append(b.open[name], start)
	default:
		b.blockSeparator(start)
		b.blockID++
		b.open[name] = append(b.open[name], start)
	}
	return end
}

// closeTag ports HtmlMap#close_tag.
func (b *htmlBuilder) closeTag(name string, start, end int) {
	stack := b.open[name]
	if len(stack) == 0 {
		return
	}
	openStart := stack[len(stack)-1]
	b.open[name] = stack[:len(stack)-1]
	if inlineTags[name] {
		r := srcRange{openStart, end}
		b.m.InlineRanges = append(b.m.InlineRanges, r)
		if name == "a" {
			b.m.LinkRanges = append(b.m.LinkRanges, r)
		}
		return
	}
	b.blockSeparator(start)
	b.blockID++
}

// blockSeparator ports HtmlMap#emit_block_separator: rendered text has line
// breaks between block elements even when the source has no whitespace there,
// so emit a zero-width newline (unless the text is empty or already ends in
// one). Selections spanning blocks then match and get rejected as cross-block
// rather than NotFound.
func (b *htmlBuilder) blockSeparator(position int) {
	n := len(b.m.Plain)
	if n == 0 || b.m.Plain[n-1] == '\n' {
		return
	}
	b.m.emit('\n', position, position, b.blockID)
}
