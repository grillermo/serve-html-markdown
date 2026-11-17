package markdown

import (
	"bytes"
	"strings"
	"unicode"
	"unicode/utf8"
	"unsafe"

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

// lineData returns the rest of the current line without its line ending, as a
// string that shares the memory of line (the parse source is never modified).
// Copying it for every trigger character made inputs with many triggers
// quadratic. comrak scans the whole paragraph, so there is always at least one
// more character after a URL candidate; the scanners below model that
// by treating the end of the data as a virtual newline.
func lineData(line []byte) string {
	line = bytes.TrimRight(line, "\r\n")
	return unsafe.String(unsafe.SliceData(line), len(line))
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
	if !wwwAhead(line) || inBrackets(pc) {
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

// wwwAhead reports, without allocating, whether "www." follows the trigger
// character(s) at the start of line. The parser is invoked for every space,
// parenthesis and so on, so this must be cheap and reject nearly everything.
func wwwAhead(line []byte) bool {
	www := []byte("www.")
	switch line[0] {
	case 'w':
		return bytes.HasPrefix(line, www)
	case '\\':
		return len(line) >= 2 && strings.IndexByte("(*_~[", line[1]) >= 0 && bytes.HasPrefix(line[2:], www)
	case '*', '_':
		run := 1
		for run < len(line) && line[run] == line[0] {
			run++
		}
		return bytes.HasPrefix(line[run:], www)
	}
	return bytes.HasPrefix(line[1:], www)
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

func isASCIISpace(c byte) bool {
	return c == ' ' || (c >= '\t' && c <= '\r')
}

func isASCIIAlpha(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }
func isASCIIAlnum(c byte) bool { return isASCIIAlpha(c) || (c >= '0' && c <= '9') }

// checkDomain mirrors comrak's check_domain. Like comrak, which slices the
// byte string in the middle of multi-byte characters, a non-ASCII character
// ends the scan.
func checkDomain(data string, allowShort bool) (int, bool) {
	i, np, uscore1, uscore2 := 1, 0, 0, 0
	for i < len(data) {
		if data[i] == '\\' && i < len(data)-1 {
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

// autolinkDelim trims trailing punctuation and unbalanced closing parentheses
// (comrak autolink.rs autolink_delim, itself from cmark-gfm). The parenthesis
// counts are kept for data[:counted] and adjusted as linkEnd shrinks, so a
// long run of ")" costs linear instead of quadratic time.
func autolinkDelim(data string, linkEnd int) int {
	if i := strings.IndexByte(data[:linkEnd], '<'); i >= 0 {
		linkEnd = i
	}
	counted, opening, closing := -1, 0, 0
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
			if counted < 0 {
				opening = strings.Count(data[:linkEnd], "(")
				closing = strings.Count(data[:linkEnd], ")")
			} else {
				for _, r := range data[linkEnd:counted] {
					switch r {
					case '(':
						opening--
					case ')':
						closing--
					}
				}
			}
			counted = linkEnd
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
