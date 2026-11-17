package markdown

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Smart punctuation, ported from cmark/comrak (parse.smart = true):
//
//   - "--"…"-" runs become em and en dashes: n%3==0 -> n/3 em; n%2==0 -> n/2
//     en; n%3==2 -> 1 en + (n-2)/3 em; otherwise 2 en + (n-4)/3 em (ems first)
//   - "..." becomes an ellipsis ("..", "." stay)
//   - '"' becomes ” when it can close, else “; “ when paired with a later closer
//   - "'" becomes ’, or ‘ when it is paired with a later closing quote
//
// goldmark's Typographer is heuristic and differs from this, so it is not used.
type smartParser struct{}

func (smartParser) Trigger() []byte { return []byte{'-', '.', '"', '\''} }

func (smartParser) Parse(_ ast.Node, block text.Reader, _ parser.Context) ast.Node {
	line, _ := block.PeekLine()
	switch line[0] {
	case '-':
		n := 0
		for n < len(line) && line[n] == '-' {
			n++
		}
		if n < 2 {
			return nil
		}
		block.Advance(n)
		return newPlainText(dashes(n))
	case '.':
		if len(line) >= 3 && line[1] == '.' && line[2] == '.' {
			block.Advance(3)
			return newPlainText("…")
		}
		return nil
	}
	// quotes
	before := block.PrecendingCharacter()
	after := '\n'
	if len(line) > 1 {
		after, _ = utf8.DecodeRune(line[1:])
	}
	open, close := quoteFlanking(before, after)
	block.Advance(1)
	q := newPlainText("’")
	if line[0] == '"' {
		q = newPlainText("“")
		if close {
			q.value = "”"
		}
	}
	q.quote = &quoteState{char: line[0], canOpen: open, canClose: close}
	return q
}

func dashes(n int) string {
	var em, en int
	switch {
	case n%3 == 0:
		em = n / 3
	case n%2 == 0:
		en = n / 2
	case n%3 == 2:
		en, em = 1, (n-2)/3
	default:
		en, em = 2, (n-4)/3
	}
	return strings.Repeat("—", em) + strings.Repeat("–", en)
}

// quoteFlanking is comrak's scan_delims for ' and ". An opening quote must be
// left-flanking and either not right-flanking or directly after ( or [
// (found empirically against Commonmarker).
func quoteFlanking(before, after rune) (canOpen, canClose bool) {
	bws, aws := isUnicodeSpace(before), isUnicodeSpace(after)
	bp, ap := isUnicodePunct(before), isUnicodePunct(after)
	left := !aws && !(ap && !bws && !bp)
	right := !bws && !(bp && !aws && !ap)
	return left && (!right || before == '(' || before == '[') && before != ']' && before != ')', right
}

func isUnicodeSpace(r rune) bool { return util.IsSpaceRune(r) || unicode.IsSpace(r) }

func isUnicodePunct(r rune) bool { return util.IsPunctRune(r) }

// quoteState marks a "'" that still has to be paired.
type quoteState struct {
	char              byte
	canOpen, canClose bool
}

// quoteTransformer pairs single quotes: scanning the children of every inline
// container left to right, a closing quote turns the nearest unmatched opening
// quote into ‘ (cmark's process_emphasis for the ' delimiter).
type quoteTransformer struct{}

func (quoteTransformer) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || n.Type() == ast.TypeInline && n.ChildCount() == 0 {
			return ast.WalkContinue, nil
		}
		openers := map[byte][]*plainText{}
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			p, ok := c.(*plainText)
			if !ok || p.quote == nil {
				continue
			}
			q := p.quote
			if list := openers[q.char]; q.canClose && len(list) > 0 {
				if q.char == '"' {
					list[len(list)-1].value = "“"
				} else {
					list[len(list)-1].value = "‘"
				}
				openers[q.char] = list[:len(list)-1]
			} else if q.canOpen {
				openers[q.char] = append(list, p)
			}
		}
		return ast.WalkContinue, nil
	})
}
