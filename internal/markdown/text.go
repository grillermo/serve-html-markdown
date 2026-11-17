package markdown

import (
	"bytes"
	stdhtml "html"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

// collectText mirrors comrak's collect_text: the literal text of Text and Code
// nodes (entities and backslash escapes decoded), line breaks as a single
// space, recursing into every other node. Raw inline HTML contributes nothing.
func collectText(buf *bytes.Buffer, n ast.Node, src []byte) { collect(buf, n, src, false) }

// collectAlt is collectText for image alt text. comrak's plain-text image
// rendering keeps raw inline HTML as literal text (`![<b>](q)` has alt
// "<b>"), whereas the heading id collector ignores it.
func collectAlt(buf *bytes.Buffer, n ast.Node, src []byte) { collect(buf, n, src, true) }

func collect(buf *bytes.Buffer, n ast.Node, src []byte, withHTML bool) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch t := c.(type) {
		case *ast.Text:
			buf.WriteString(decodeText(t.Segment.Value(src)))
			if t.SoftLineBreak() || t.HardLineBreak() {
				buf.WriteByte(' ')
			}
		case *plainText:
			buf.WriteString(t.value)
		case *ast.CodeSpan:
			for cc := t.FirstChild(); cc != nil; cc = cc.NextSibling() {
				if tt, ok := cc.(*ast.Text); ok {
					buf.Write(bytes.ReplaceAll(tt.Segment.Value(src), []byte("\n"), []byte(" ")))
				}
			}
		case *ast.RawHTML:
			if withHTML {
				for i := 0; i < t.Segments.Len(); i++ {
					seg := t.Segments.At(i)
					buf.Write(seg.Value(src))
				}
			}
		case *ast.AutoLink:
			buf.WriteString(decodeEntities(t.Label(src)))
		default:
			collect(buf, c, src, withHTML)
		}
	}
}

// decodeText resolves entities, numeric references and backslash escapes the
// way goldmark's writer does when it renders a Text segment, but returns the
// plain text.
func decodeText(raw []byte) string {
	var b bytes.Buffer
	w := bufWriter{&b}
	html.DefaultWriter.Write(w, raw)
	return stdhtml.UnescapeString(b.String())
}

// bufWriter adapts a bytes.Buffer to util.BufWriter.
type bufWriter struct{ *bytes.Buffer }

func (bufWriter) Available() int { return 1 << 30 }
func (bufWriter) Buffered() int  { return 0 }
func (bufWriter) Flush() error   { return nil }

var _ util.BufWriter = bufWriter{}
