// Package markdown renders Markdown to HTML so that the output matches what
// Commonmarker (comrak) produces for the Rails app's MARKDOWN_OPTIONS, which
// keeps saved scroll anchors (heading ids) and page appearance stable.
package markdown

import (
	"bytes"
	"errors"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

// blockParsers are goldmark's defaults with the HTML block parser replaced.
// The default is found by identity (it is a singleton), not by priority, and
// the replacement is verified in TestHTMLBlockParserIsInstalled.
func blockParsers() []util.PrioritizedValue {
	ps := parser.DefaultBlockParsers()
	stock := parser.NewHTMLBlockParser()
	for i, p := range ps {
		if p.Value == stock {
			ps[i] = util.Prioritized(newHTMLBlockParser(), p.Priority)
			return ps
		}
	}
	panic("markdown: goldmark's HTML block parser not found among the default block parsers")
}

var md = goldmark.New(
	goldmark.WithParser(parser.NewParser(
		parser.WithBlockParsers(blockParsers()...),
		parser.WithInlineParsers(parser.DefaultInlineParsers()...),
		parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
	)),
	goldmark.WithParserOptions(
		parser.WithInlineParsers(
			util.Prioritized(bracketTracker{}, 50),
			util.Prioritized(urlParser{}, 150),
			util.Prioritized(wwwParser{}, 400),
			util.Prioritized(smartParser{}, 9000),
		),
		parser.WithASTTransformers(
			util.Prioritized(trailingSpaceTransformer{}, 50),
			util.Prioritized(quoteTransformer{}, 100),
			util.Prioritized(emailTransformer{}, 200),
			util.Prioritized(highlightBudgetTransformer{}, 300),
			util.Prioritized(headingIDTransformer{}, 1000),
		),
	),
	goldmark.WithRendererOptions(
		html.WithUnsafe(),
		html.WithXHTML(),
		renderer.WithNodeRenderers(
			util.Prioritized(headingRenderer{}, 100),
			util.Prioritized(codeRenderer{}, 100),
			util.Prioritized(plainTextRenderer{}, 100),
			util.Prioritized(linkRenderer{}, 100),
		),
	),
)

// ErrInvalidUTF8 is returned for input that is not valid UTF-8. Commonmarker
// raises an EncodingError for such input, so the Rails app failed the request;
// callers decide how to degrade (for example by serving the raw file).
var ErrInvalidUTF8 = errors.New("markdown: input is not valid UTF-8")

// ErrTooComplex is returned for input whose structure would take the parser
// far too long: container nesting hundreds of levels deep on one line, or tens
// of thousands of link reference definitions (see tooComplex). Callers show the
// text unrendered.
var ErrTooComplex = errors.New("markdown: input is too complex to render")

// Render converts Markdown to HTML. It is safe for concurrent use: the
// goldmark instance is shared and read-only, and all per-document state (the
// heading id anchorizer, bracket tracking) lives in the parse context or the
// AST of the call.
func Render(src []byte) ([]byte, error) {
	if !utf8.Valid(src) {
		return nil, ErrInvalidUTF8
	}
	src = normalize(src)
	if tooComplex(src) {
		return nil, ErrTooComplex
	}
	var buf bytes.Buffer
	if err := md.Convert(src, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// normalize applies the input rules comrak applies before parsing: a leading
// byte order mark is dropped, CRLF and lone CR line endings become LF, and NUL
// bytes become U+FFFD.
func normalize(src []byte) []byte {
	src = bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))
	if !bytes.ContainsAny(src, "\r\x00") {
		return src
	}
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	src = bytes.ReplaceAll(src, []byte("\r"), []byte("\n"))
	return bytes.ReplaceAll(src, []byte("\x00"), []byte("\uFFFD"))
}
