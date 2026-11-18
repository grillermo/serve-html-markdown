package markdown

import (
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// trailingSpaceTransformer removes the spaces before a hard line break, as
// CommonMark requires and comrak does. goldmark only trims the last piece of
// the line: a long run of trailing spaces is cut into several text nodes (the
// parsers triggered by a space flush the text before them), and the earlier
// pieces keep their spaces, so "a<spaces>\n" rendered as "a<spaces><br />".
type trailingSpaceTransformer struct{}

func (trailingSpaceTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	src := reader.Source()
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		t, ok := n.(*ast.Text)
		if !entering || !ok || !t.HardLineBreak() {
			return ast.WalkContinue, nil
		}
		// Trim this node, then the text nodes before it for as long as the
		// spaces swallow a whole node.
		for cur := ast.Node(t); cur != nil; cur = cur.PreviousSibling() {
			ct, ok := cur.(*ast.Text)
			if !ok {
				break
			}
			ct.Segment = ct.Segment.TrimRightSpace(src)
			if ct.Segment.Len() > 0 {
				break
			}
		}
		return ast.WalkContinue, nil
	})
}
