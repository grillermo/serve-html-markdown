package markdown

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// Autolinks (extension.autolink), ported from comrak's autolink.rs.
//
// comrak recognises "scheme://" and "www." links while parsing inlines, on the
// raw source (so smart punctuation, entities and backslashes inside the URL
// are untouched), and never while a "[" is still open. Email addresses are
// recognised afterwards, on the concatenated text of consecutive Text nodes
// (so smart-punctuation glyphs, decoded entities and decoded escapes are part
// of the text). The two halves are reproduced the same way here: inline
// parsers for URLs and www links, and an AST transformer for emails.

var inBracketsKey = parser.NewContextKey()

// bracketTracker mirrors comrak's within_brackets flag: set by "[" or "![",
// cleared by any "]". It never consumes input.
type bracketTracker struct{}

func (bracketTracker) Trigger() []byte { return []byte{'!', '[', ']'} }

func (bracketTracker) Parse(_ ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, _ := block.PeekLine()
	switch line[0] {
	case '[':
		pc.Set(inBracketsKey, true)
	case '!':
		if len(line) > 1 && line[1] == '[' {
			pc.Set(inBracketsKey, true)
		}
	case ']':
		pc.Set(inBracketsKey, false)
	}
	return nil
}

func (bracketTracker) CloseBlock(_ ast.Node, _ text.Reader, pc parser.Context) {
	pc.Set(inBracketsKey, false)
}

func inBrackets(pc parser.Context) bool {
	v, _ := pc.Get(inBracketsKey).(bool)
	return v
}

// lineData returns the rest of the current line with exactly one trailing
// newline. comrak scans the whole paragraph, so there is always at least one
// more character after a URL candidate, which its domain check relies on.
func lineData(line []byte) string {
	return strings.TrimRight(string(line), "\r\n") + "\n"
}

func newExtLink(url, label string) *extLink {
	l := &extLink{url: url}
	l.AppendChild(l, newPlainText(label))
	return l
}

// urlParser handles "scheme://" links; it is triggered by the colon.
type urlParser struct{}

func (urlParser) Trigger() []byte { return []byte{':'} }

func (urlParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, seg := block.PeekLine()
	if len(line) < 3 || line[1] != '/' || line[2] != '/' || inBrackets(pc) {
		return nil
	}
	prev, _ := parent.LastChild().(*ast.Text)
	if prev == nil || prev.Segment.Stop != seg.Start || prev.SoftLineBreak() || prev.HardLineBreak() {
		return nil
	}
	src := block.Source()
	before := prev.Segment.Value(src)
	rewind := 0
	for rewind < len(before) && isASCIIAlpha(before[len(before)-rewind-1]) {
		rewind++
	}
	switch string(before[len(before)-rewind:]) {
	case "http", "https", "ftp":
	default:
		return nil
	}
	data := lineData(line)
	d, ok := checkDomain(data[2:], true)
	if !ok {
		return nil
	}
	linkEnd := 2 + d
	for linkEnd < len(data) && !isASCIISpace(data[linkEnd]) && data[linkEnd] != '<' {
		linkEnd++
	}
	linkEnd = autolinkDelim(data, linkEnd)
	if linkEnd == 0 {
		return nil
	}
	label := string(before[len(before)-rewind:]) + data[:linkEnd]
	if rewind == len(before) {
		parent.RemoveChild(parent, prev)
	} else {
		prev.Segment = prev.Segment.WithStop(prev.Segment.Stop - rewind)
	}
	block.Advance(linkEnd)
	return newExtLink(label, label)
}

// wwwParser handles "www." links. Such a link must be preceded by whitespace,
// the start of the line or one of ( * _ ~, so it is triggered by those.
type wwwParser struct{}

func (wwwParser) Trigger() []byte { return []byte{' ', '(', '*', '_', '~', '\\'} }

func (wwwParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, seg := block.PeekLine()
	if inBrackets(pc) {
		return nil
	}
	switch line[0] {
	case 'w': // start of a line, or right after an inline that was just parsed
		switch before := block.PrecendingCharacter(); before {
		case '*', '_', '~', '(':
		default:
			if before >= 0x80 || !isASCIISpace(byte(before)) {
				return nil
			}
		}
		data := lineData(line)
		linkEnd, ok := wwwEnd(data)
		if !ok {
			return nil
		}
		block.Advance(linkEnd)
		return newExtLink("http://"+data[:linkEnd], data[:linkEnd])
	case '\\': // an escaped ( * _ ~ directly before www. (the escape is plain text)
		if len(line) < 3 || strings.IndexByte("(*_~[", line[1]) < 0 {
			return nil
		}
		data := lineData(line[2:])
		linkEnd, ok := wwwEnd(data)
		if !ok {
			return nil
		}
		parent.AppendChild(parent, ast.NewTextSegment(seg.WithStop(seg.Start+2)))
		block.Advance(2 + linkEnd)
		parent.AppendChild(parent, newExtLink("http://"+data[:linkEnd], data[:linkEnd]))
		return newPlainText("")
	case '*', '_':
		run := 1
		for run < len(line) && line[run] == line[0] {
			run++
		}
		data := lineData(line[run:])
		linkEnd, ok := wwwEnd(data)
		if !ok {
			return nil
		}
		before := block.PrecendingCharacter()
		d := parser.ScanDelimiter(line, before, 1, emphasisProcessor{})
		d.Segment = seg.WithStop(seg.Start + d.OriginalLength)
		block.Advance(run)
		pc.PushDelimiter(d)
		parent.AppendChild(parent, d)
		block.Advance(linkEnd)
		return newExtLink("http://"+data[:linkEnd], data[:linkEnd])
	default: // whitespace, ( or ~
		if !isASCIISpace(line[0]) && line[0] != '(' && line[0] != '~' {
			return nil // reached through the start-of-line trigger
		}
		data := lineData(line[1:])
		linkEnd, ok := wwwEnd(data)
		if !ok {
			return nil
		}
		parent.AppendChild(parent, ast.NewTextSegment(seg.WithStop(seg.Start+1)))
		block.Advance(1 + linkEnd)
		parent.AppendChild(parent, newExtLink("http://"+data[:linkEnd], data[:linkEnd]))
		return newPlainText("")
	}
}

// emphasisProcessor is goldmark's emphasis delimiter processor, which is not
// exported; the wwwParser needs it to push "*" and "_" delimiters itself.
type emphasisProcessor struct{}

func (emphasisProcessor) IsDelimiter(b byte) bool { return b == '*' || b == '_' }
func (emphasisProcessor) CanOpenCloser(opener, closer *parser.Delimiter) bool {
	return opener.Char == closer.Char
}
func (emphasisProcessor) OnMatch(consumes int) ast.Node { return ast.NewEmphasis(consumes) }

// wwwEnd returns the length of the "www." link at the start of data.
func wwwEnd(data string) (int, bool) {
	if !strings.HasPrefix(data, "www.") {
		return 0, false
	}
	d, ok := checkDomain(data, false)
	if !ok {
		return 0, false
	}
	linkEnd := d
	for linkEnd < len(data) && !isASCIISpace(data[linkEnd]) && data[linkEnd] != '<' {
		linkEnd++
	}
	linkEnd = autolinkDelim(data, linkEnd)
	return linkEnd, linkEnd > 0
}

// emailTransformer links email addresses in runs of consecutive text nodes,
// never inside links, images, code or raw HTML; a line break ends a run.
type emailTransformer struct{}

func (emailTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	src := reader.Source()
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.(type) {
		case *ast.Link, *ast.Image, *ast.AutoLink, *ast.CodeSpan, *ast.RawHTML, *extLink:
			return ast.WalkSkipChildren, nil
		}
		if n.FirstChild() != nil && n.FirstChild().Type() == ast.TypeInline {
			emailChildren(n, src)
		}
		return ast.WalkContinue, nil
	})
}

func emailChildren(parent ast.Node, src []byte) {
	var run []ast.Node
	flush := func() {
		if len(run) > 0 {
			emailRun(parent, run, src)
			run = run[:0]
		}
	}
	for c := parent.FirstChild(); c != nil; {
		next := c.NextSibling() // the run, including c, may be replaced below
		switch t := c.(type) {
		case *ast.Text:
			run = append(run, c)
			if t.SoftLineBreak() || t.HardLineBreak() {
				flush()
			}
		case *plainText:
			run = append(run, c)
		default:
			flush()
		}
		c = next
	}
	flush()
}

func emailRun(parent ast.Node, run []ast.Node, src []byte) {
	var sb strings.Builder
	for _, n := range run {
		switch t := n.(type) {
		case *ast.Text:
			sb.WriteString(decodeText(t.Segment.Value(src)))
		case *plainText:
			sb.WriteString(t.value)
		}
	}
	s := sb.String()
	if !strings.Contains(s, "@") {
		return
	}
	pieces := splitEmails(s)
	if pieces == nil {
		return
	}
	// A trailing break flag lives on the last Text of the run; keep it.
	var soft, hard bool
	if t, ok := run[len(run)-1].(*ast.Text); ok {
		soft, hard = t.SoftLineBreak(), t.HardLineBreak()
	}
	first := run[0]
	for _, p := range pieces {
		if p.url == "" {
			parent.InsertBefore(parent, first, newPlainText(p.text))
		} else {
			parent.InsertBefore(parent, first, newExtLink(p.url, p.text))
		}
	}
	if soft || hard {
		t := ast.NewTextSegment(text.NewSegment(0, 0))
		t.SetSoftLineBreak(soft)
		t.SetHardLineBreak(hard)
		parent.InsertBefore(parent, first, t)
	}
	for _, n := range run {
		parent.RemoveChild(parent, n)
	}
}

type autolinkPiece struct {
	text string
	url  string // empty for plain text
}

// splitEmails returns nil when s contains no email autolink.
func splitEmails(s string) []autolinkPiece {
	var out []autolinkPiece
	found := false
	for len(s) > 0 {
		hit := false
		depth := 0 // signed count of [ minus ] seen so far in this text node
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '[':
				depth++
				continue
			case ']':
				depth--
				continue
			case '@':
			default:
				continue
			}
			if depth > 0 {
				continue
			}
			url, rewind, skip, ok := emailMatch(s, i)
			if !ok {
				continue
			}
			start := i - rewind
			if start > 0 {
				out = append(out, autolinkPiece{text: s[:start]})
			}
			out = append(out, autolinkPiece{text: s[start : start+skip], url: url})
			s = s[start+skip:]
			hit, found = true, true
			break
		}
		if !hit {
			out = append(out, autolinkPiece{text: s})
			break
		}
	}
	if !found {
		return nil
	}
	return out
}

func isASCIISpace(c byte) bool {
	return c == ' ' || (c >= '\t' && c <= '\r')
}

func isASCIIAlpha(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }
func isASCIIAlnum(c byte) bool { return isASCIIAlpha(c) || (c >= '0' && c <= '9') }

// emailMatch handles local@domain; i is the index of the "@". The local part
// may carry a "mailto:" or "xmpp:" prefix, which is then part of the link
// text and keeps the href as is (for xmpp it may be followed by a "/resource").
func emailMatch(s string, i int) (url string, rewind, skip int, ok bool) {
	prefixed, isXMPP := false, false
	for rewind < i {
		c := s[i-rewind-1]
		if isASCIIAlnum(c) || c == '.' || c == '+' || c == '-' || c == '_' {
			rewind++
			continue
		}
		if c == ':' {
			if word := prefixWord(s[:i-rewind-1]); word != "" {
				rewind += 1 + len(word)
				prefixed = true
				isXMPP = isXMPP || word == "xmpp"
				continue
			}
		}
		break
	}
	if rewind == 0 {
		return "", 0, 0, false
	}
	size := len(s)
	linkEnd, nb, np := 0, 0, 0
	for linkEnd < size-i {
		c := s[i+linkEnd]
		switch {
		case isASCIIAlnum(c):
		case c == '@':
			nb++
		case c == '.' && linkEnd < size-i-1 && isASCIIAlnum(s[i+linkEnd+1]):
			np++
		case c == '/' && isXMPP && nb == 1 && np > 0:
		case c != '-' && c != '_':
			goto done
		}
		linkEnd++
	}
done:
	if linkEnd < 2 || nb != 1 || np == 0 || (!isASCIIAlpha(s[i+linkEnd-1]) && s[i+linkEnd-1] != '.') {
		return "", 0, 0, false
	}
	linkEnd = autolinkDelim(s[i:], linkEnd)
	if linkEnd == 0 {
		return "", 0, 0, false
	}
	text := s[i-rewind : i+linkEnd]
	if prefixed {
		return text, rewind, rewind + linkEnd, true
	}
	return "mailto:" + text, rewind, rewind + linkEnd, true
}

// prefixWord returns "mailto" or "xmpp" when that word ends s and is not
// directly preceded by a letter, otherwise "".
func prefixWord(s string) string {
	for _, w := range []string{"mailto", "xmpp"} {
		if strings.HasSuffix(s, w) {
			rest := s[:len(s)-len(w)]
			if rest == "" || !isASCIIAlpha(rest[len(rest)-1]) {
				return w
			}
		}
	}
	return ""
}

// checkDomain mirrors comrak's check_domain. Like comrak, which slices the
// byte string in the middle of multi-byte characters, a non-ASCII character
// ends the scan.
func checkDomain(data string, allowShort bool) (int, bool) {
	i, np, uscore1, uscore2 := 1, 0, 0, 0
	for i < len(data)-1 {
		if data[i] == '\\' && i < len(data)-2 {
			i++ // the escaped character is still examined below
		}
		c := data[i]
		switch {
		case c == '_':
			uscore2++
		case c == '.':
			uscore1, uscore2 = uscore2, 0
			np++
		case !isValidHostChar(data[i:]) && c != '-':
			goto done
		}
		i++
	}
done:
	if (uscore1 > 0 || uscore2 > 0) && np <= 10 {
		return 0, false
	}
	if allowShort || np > 0 {
		return i, true
	}
	return 0, false
}

func isValidHostChar(s string) bool {
	if !utf8.RuneStart(s[0]) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s)
	return !unicode.IsSpace(r) && !isUnicodePunct(r)
}

// autolinkDelim trims trailing punctuation and unbalanced closing parentheses.
func autolinkDelim(data string, linkEnd int) int {
	if i := strings.IndexByte(data[:linkEnd], '<'); i >= 0 {
		linkEnd = i
	}
	for linkEnd > 0 {
		c := data[linkEnd-1]
		switch {
		case strings.IndexByte("?!.,:*_~'\"", c) >= 0:
			linkEnd--
		case c == ';':
			newEnd := linkEnd - 2
			for newEnd > 0 && isASCIIAlpha(data[newEnd]) {
				newEnd--
			}
			if newEnd >= 0 && newEnd < linkEnd-2 && data[newEnd] == '&' {
				linkEnd = newEnd
			} else {
				linkEnd--
			}
		case c == ')':
			opening := strings.Count(data[:linkEnd], "(")
			closing := strings.Count(data[:linkEnd], ")")
			if closing <= opening {
				return linkEnd
			}
			linkEnd--
		default:
			return linkEnd
		}
	}
	return linkEnd
}
