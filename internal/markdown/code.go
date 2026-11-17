package markdown

import (
	"bytes"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Colours of syntect's base16-ocean.dark theme, the Commonmarker default.
const (
	oceanBg      = "#2b303b"
	oceanFg      = "#c0c5ce"
	oceanComment = "#65737e"
	oceanRed     = "#bf616a"
	oceanOrange  = "#d08770"
	oceanYellow  = "#ebcb8b"
	oceanGreen   = "#a3be8c"
	oceanCyan    = "#96b5b4"
	oceanBlue    = "#8fa1b3"
	oceanMagenta = "#b48ead"
	oceanBrown   = "#ab7967"
)

// base16OceanDark maps chroma token types onto the colours syntect's
// base16-ocean.dark theme gives the equivalent scopes.
var base16OceanDark = chroma.MustNewStyle("base16-ocean.dark", chroma.StyleEntries{
	chroma.Background:            "bg:" + oceanBg + " " + oceanFg,
	chroma.Text:                  oceanFg,
	chroma.Punctuation:           oceanFg,
	chroma.Operator:              oceanFg,
	chroma.Comment:               oceanComment,
	chroma.CommentPreproc:        oceanMagenta,
	chroma.Keyword:               oceanMagenta,
	chroma.KeywordConstant:       oceanOrange,
	chroma.KeywordType:           oceanYellow,
	chroma.Name:                  oceanFg,
	chroma.NameBuiltin:           oceanCyan,
	chroma.NameFunction:          oceanBlue,
	chroma.NameClass:             oceanYellow,
	chroma.NameNamespace:         oceanYellow,
	chroma.NameVariable:          oceanRed,
	chroma.NameTag:               oceanRed,
	chroma.NameAttribute:         oceanBlue,
	chroma.NameConstant:          oceanOrange,
	chroma.NameDecorator:         oceanBlue,
	chroma.LiteralString:         oceanGreen,
	chroma.LiteralStringEscape:   oceanCyan,
	chroma.LiteralStringInterpol: oceanBrown,
	chroma.LiteralStringRegex:    oceanCyan,
	chroma.LiteralStringSymbol:   oceanGreen,
	chroma.LiteralNumber:         oceanOrange,
	chroma.GenericDeleted:        oceanRed,
	chroma.GenericInserted:       oceanGreen,
	chroma.GenericHeading:        oceanBlue,
	chroma.GenericSubheading:     oceanBlue,
	chroma.GenericEmph:           oceanMagenta,
	chroma.GenericStrong:         oceanOrange,
})

const preOpen = `<pre style="background-color:` + oceanBg + `;">`

// codeRenderer renders fenced and indented code blocks the way Commonmarker
// does with its default syntect highlighter: an inline-styled <pre> wrapper,
// <code class="language-x"> when the info string names a language, and one
// <span style="color:…;"> per token. Unknown or missing languages are plain
// text, one span per line.
type codeRenderer struct{}

func (codeRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, renderCode)
	reg.Register(ast.KindCodeBlock, renderCode)
}

// highlightBudget caps how many bytes of fenced code one document gets
// lexed. chroma costs roughly 1.5 ms per KB of Go source, so an uncapped
// 200 KB document of code would take about 0.3 s. Blocks past the budget are
// still wrapped identically but rendered as plain text (one span per line).
const highlightBudget = 32 << 10

// maxHighlightLine is the longest line a block may contain and still be
// highlighted. Chroma lexers are quadratic on long unbalanced lines (a 30 KB
// line of "[" took 83 s), and minified code is not worth colouring anyway.
const maxHighlightLine = 2048

// highlightTimeout caps the wall-clock time spent lexing one block.
var highlightTimeout = 100 * time.Millisecond

// plainAttr marks a code block that is over the highlight budget.
const plainAttr = "markdown-plain"

// highlightBudgetTransformer walks code blocks in document order and marks
// those that exceed the per-document highlighting budget.
type highlightBudgetTransformer struct{}

func (highlightBudgetTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	src := reader.Source()
	spent := 0
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		fb, ok := n.(*ast.FencedCodeBlock)
		if !ok || !entering || fb.Info == nil {
			return ast.WalkContinue, nil
		}
		if firstWord(decodeText(fb.Info.Segment.Value(src))) == "" {
			return ast.WalkContinue, nil
		}
		size, longLine := 0, false
		for i := 0; i < fb.Lines().Len(); i++ {
			seg := fb.Lines().At(i)
			size += seg.Len()
			longLine = longLine || seg.Len() > maxHighlightLine
		}
		if longLine {
			fb.SetAttributeString(plainAttr, true)
		} else if spent+size > highlightBudget {
			fb.SetAttributeString(plainAttr, true)
		} else {
			spent += size
		}
		return ast.WalkContinue, nil
	})
}

func renderCode(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	var lang string
	if fb, ok := n.(*ast.FencedCodeBlock); ok && fb.Info != nil {
		lang = firstWord(decodeText(fb.Info.Segment.Value(src)))
	}
	_, plain := n.AttributeString(plainAttr)
	var code bytes.Buffer
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		code.Write(seg.Value(src))
	}
	if code.Len() > 0 && code.Bytes()[code.Len()-1] != '\n' {
		code.WriteByte('\n')
	}

	_, _ = w.WriteString(preOpen)
	if lang != "" {
		_, _ = w.WriteString(`<code class="language-`)
		_, _ = w.Write(util.EscapeHTML([]byte(lang)))
		_, _ = w.WriteString(`">`)
	} else {
		_, _ = w.WriteString("<code>")
	}
	if plain {
		writePlain(w, code.String())
	} else {
		writeHighlighted(w, lang, code.String())
	}
	_, _ = w.WriteString("</code></pre>\n")
	return ast.WalkContinue, nil
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\n\r\v\f"); i >= 0 {
		return s[:i]
	}
	return s
}

func writeHighlighted(w util.BufWriter, lang, code string) {
	var lexer chroma.Lexer
	if lang != "" {
		lexer = lexers.Get(lang)
	}
	if lexer == nil {
		writePlain(w, code)
		return
	}
	it, err := lexer.Tokenise(nil, code)
	if err != nil {
		writePlain(w, code)
		return
	}
	// Chroma's regexp2 lexers can go quadratic on pathological input, so the
	// tokens are pulled one at a time under a wall-clock cap; past it the work
	// is discarded and the block is rendered plain.
	deadline := time.Now().Add(highlightTimeout)
	var tokens []chroma.Token
	for tok := it(); tok != chroma.EOF; tok = it() {
		tokens = append(tokens, tok)
		if time.Now().After(deadline) {
			writePlain(w, code)
			return
		}
	}
	// Some chroma lexers drop or duplicate characters on odd input; the text
	// must survive intact, so fall back to plain text when it does not.
	var sb strings.Builder
	for _, tok := range tokens {
		sb.WriteString(tok.Value)
	}
	if sb.String() != code {
		writePlain(w, code)
		return
	}
	for _, tok := range tokens {
		colour := oceanFg
		if e := base16OceanDark.Get(tok.Type); e.Colour.IsSet() {
			colour = e.Colour.String()
		}
		writeSpans(w, colour, tok.Value)
	}
}

// writePlain writes text as syntect does for plain text: one span per line,
// the newline inside the span.
func writePlain(w util.BufWriter, code string) {
	for len(code) > 0 {
		line := code
		if i := strings.IndexByte(code, '\n'); i >= 0 {
			line, code = code[:i+1], code[i+1:]
		} else {
			code = ""
		}
		writeSpan(w, oceanFg, line)
	}
}

// writeSpans writes a token, splitting it after each newline so every line
// ends with its newline inside a span, as syntect's per-line output does.
func writeSpans(w util.BufWriter, colour, tok string) {
	for len(tok) > 0 {
		piece := tok
		if i := strings.IndexByte(tok, '\n'); i >= 0 {
			piece, tok = tok[:i+1], tok[i+1:]
		} else {
			tok = ""
		}
		writeSpan(w, colour, piece)
	}
}

func writeSpan(w util.BufWriter, colour, s string) {
	_, _ = w.WriteString(`<span style="color:` + colour + `;">`)
	writeCodeEscaped(w, s)
	_, _ = w.WriteString("</span>")
}

// writeCodeEscaped escapes code text the way syntect does, which unlike
// the prose renderer also escapes the apostrophe.
func writeCodeEscaped(w util.BufWriter, s string) {
	start := 0
	for i := 0; i < len(s); i++ {
		var rep string
		switch s[i] {
		case '&':
			rep = "&amp;"
		case '<':
			rep = "&lt;"
		case '>':
			rep = "&gt;"
		case '"':
			rep = "&quot;"
		case '\'':
			rep = "&#39;"
		default:
			continue
		}
		_, _ = w.WriteString(s[start:i])
		_, _ = w.WriteString(rep)
		start = i + 1
	}
	_, _ = w.WriteString(s[start:])
}
