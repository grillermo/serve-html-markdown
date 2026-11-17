package markdown

import (
	"bytes"
	stdhtml "html"
	"strconv"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// headingIDKey is the attribute under which the id transformer stores the
// comrak-style anchor id on each heading.
const headingIDKey = "comrak-id"

// headingIDTransformer assigns every heading its comrak anchor id, in document
// order, using a fresh anchorizer per document.
type headingIDTransformer struct{}

func (headingIDTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	src := reader.Source()
	az := newAnchorizer()
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if h, ok := n.(*ast.Heading); ok && entering {
			var buf bytes.Buffer
			collectText(&buf, h, src)
			h.SetAttributeString(headingIDKey, []byte(az.anchorize(buf.String())))
		}
		return ast.WalkContinue, nil
	})
}

// collectText mirrors comrak's collect_text: the literal text of Text and Code
// nodes (entities and backslash escapes decoded), line breaks as a single
// space, recursing into every other node. Raw inline HTML contributes nothing.
func collectText(buf *bytes.Buffer, n ast.Node, src []byte) {
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
		case *ast.AutoLink:
			buf.WriteString(decodeEntities(t.Label(src)))
		default:
			collectText(buf, c, src)
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

// headingRenderer writes comrak's heading markup:
//
//	<h2><a href="#id" aria-hidden="true" class="anchor" id="id"></a>text</h2>
type headingRenderer struct{}

func (headingRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindHeading, func(w util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		h := n.(*ast.Heading)
		level := strconv.Itoa(h.Level)
		if !entering {
			_, _ = w.WriteString("</h" + level + ">\n")
			return ast.WalkContinue, nil
		}
		_, _ = w.WriteString("<h" + level + ">")
		if raw, ok := h.AttributeString(headingIDKey); ok {
			id := string(util.EscapeHTML(raw.([]byte)))
			_, _ = w.WriteString(`<a href="#` + id + `" aria-hidden="true" class="anchor" id="` + id + `"></a>`)
		}
		return ast.WalkContinue, nil
	})
}
