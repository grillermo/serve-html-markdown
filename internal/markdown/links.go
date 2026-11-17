package markdown

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// extLink is the link produced by the autolink extension. Its URL is already
// decoded text, unlike ast.Link whose destination is raw source.
type extLink struct {
	ast.BaseInline
	url string
}

var kindExtLink = ast.NewNodeKind("MarkdownExtLink")

func (*extLink) Kind() ast.NodeKind { return kindExtLink }

func (n *extLink) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"url": n.url}, nil)
}

// linkRenderer renders links, autolinks and images with comrak's escaping:
// hrefs are percent-encoded by escapeHref, a missing or empty title is omitted
// and image alt text is the plain text of the image's children.
type linkRenderer struct{}

func (linkRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindLink, renderLink)
	reg.Register(kindExtLink, renderExtLink)
	reg.Register(ast.KindAutoLink, renderAutoLink)
	reg.Register(ast.KindImage, renderImage)
	reg.Register(ast.KindHTMLBlock, renderHTMLBlock)
}

func writeOpenA(w util.BufWriter, href, title string) {
	_, _ = w.WriteString(`<a href="`)
	writeHref(w, href)
	_ = w.WriteByte('"')
	if title != "" {
		_, _ = w.WriteString(` title="`)
		_, _ = w.Write(util.EscapeHTML([]byte(title)))
		_ = w.WriteByte('"')
	}
	_ = w.WriteByte('>')
}

func renderLink(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Link)
	if !entering {
		_, _ = w.WriteString("</a>")
		return ast.WalkContinue, nil
	}
	title := ""
	if n.Title != nil {
		title = decodeText(n.Title)
	}
	writeOpenA(w, decodeText(n.Destination), title)
	return ast.WalkContinue, nil
}

func renderExtLink(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		_, _ = w.WriteString("</a>")
		return ast.WalkContinue, nil
	}
	writeOpenA(w, node.(*extLink).url, "")
	return ast.WalkContinue, nil
}

func renderAutoLink(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.AutoLink)
	url := decodeEntities(n.URL(src))
	if n.AutoLinkType == ast.AutoLinkEmail {
		url = "mailto:" + url
	}
	writeOpenA(w, url, "")
	_, _ = w.Write(util.EscapeHTML([]byte(decodeEntities(n.Label(src)))))
	_, _ = w.WriteString("</a>")
	return ast.WalkContinue, nil
}

func renderImage(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.Image)
	_, _ = w.WriteString(`<img src="`)
	writeHref(w, decodeText(n.Destination))
	_, _ = w.WriteString(`" alt="`)
	var alt bytes.Buffer
	collectText(&alt, n, src)
	_, _ = w.Write(util.EscapeHTML(alt.Bytes()))
	_ = w.WriteByte('"')
	if n.Title != nil {
		if title := decodeText(n.Title); title != "" {
			_, _ = w.WriteString(` title="`)
			_, _ = w.Write(util.EscapeHTML([]byte(title)))
			_ = w.WriteByte('"')
		}
	}
	_, _ = w.WriteString(" />")
	return ast.WalkSkipChildren, nil
}

// renderHTMLBlock writes raw HTML blocks verbatim, making sure the block ends
// with a newline as comrak does even when the document does not.
func renderHTMLBlock(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.HTMLBlock)
	var last []byte
	if entering {
		for i := 0; i < n.Lines().Len(); i++ {
			line := n.Lines().At(i)
			last = line.Value(src)
			_, _ = w.Write(last)
		}
		if !n.HasClosure() && len(last) > 0 && last[len(last)-1] != '\n' {
			_ = w.WriteByte('\n')
		}
		return ast.WalkContinue, nil
	}
	if n.HasClosure() {
		closure := n.ClosureLine
		last = closure.Value(src)
		_, _ = w.Write(last)
		if len(last) > 0 && last[len(last)-1] != '\n' {
			_ = w.WriteByte('\n')
		}
	}
	return ast.WalkContinue, nil
}

// decodeEntities resolves character references (but not backslash escapes).
func decodeEntities(b []byte) string {
	return string(util.ResolveEntityNames(util.ResolveNumericReferences(b)))
}

const hexDigits = "0123456789ABCDEF"

// writeHref writes a URL as comrak's escape_href does: bytes outside the safe
// set are percent-encoded, & becomes &amp; and ' becomes &#x27;.
func writeHref(w util.BufWriter, href string) {
	start := 0
	for i := 0; i < len(href); i++ {
		c := href[i]
		var rep string
		switch {
		case c == '&':
			rep = "&amp;"
		case c == '\'':
			rep = "&#x27;"
		case hrefSafe(c):
			continue
		default:
			rep = "%" + string(hexDigits[c>>4]) + string(hexDigits[c&15])
		}
		_, _ = w.WriteString(href[start:i])
		_, _ = w.WriteString(rep)
		start = i + 1
	}
	_, _ = w.WriteString(href[start:])
}

func hrefSafe(c byte) bool {
	if c >= 0x80 {
		return false
	}
	return isASCIIAlnum(c) || strings.IndexByte("!#$%()*+,-./:;=?@_~", c) >= 0
}
