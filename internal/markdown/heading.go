package markdown

import (
	"bytes"
	"strconv"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
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
