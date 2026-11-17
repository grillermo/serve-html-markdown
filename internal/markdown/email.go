package markdown

import (
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

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
			if nb > 1 {
				// Already a failure (nb != 1 below); stopping here keeps
				// "a@a@a@..." linear instead of rescanning to the end per "@".
				return "", 0, 0, false
			}
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
