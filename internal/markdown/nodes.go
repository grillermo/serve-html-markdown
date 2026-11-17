package markdown

import (
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// plainText is an inline leaf holding already-decoded text. It is rendered
// HTML-escaped. Smart punctuation and autolink text use it so that they are
// treated exactly like comrak's Text nodes.
type plainText struct {
	ast.BaseInline
	value string
	// quote is set for a "'" whose glyph depends on pairing (see smart.go).
	quote *quoteState
}

var kindPlainText = ast.NewNodeKind("MarkdownPlainText")

func (*plainText) Kind() ast.NodeKind { return kindPlainText }

func (n *plainText) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"value": n.value}, nil)
}

func newPlainText(v string) *plainText { return &plainText{value: v} }

type plainTextRenderer struct{}

func (plainTextRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindPlainText, func(w util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			_, _ = w.Write(util.EscapeHTML([]byte(n.(*plainText).value)))
		}
		return ast.WalkContinue, nil
	})
}
