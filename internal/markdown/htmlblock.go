package markdown

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// htmlBlockParser wraps goldmark's HTML block parser, keeping its Continue,
// Close and friends, and replaces Open to follow cmark (and so comrak) where
// goldmark deviates:
//
//   - a closing tag is `</name`, with no space after the slash, and `</pre>`,
//     `</script>` and `</style>` alone on a line are type 7 blocks;
//   - a tab counts as whitespace after a complete tag (type 7) or after the tag
//     name (type 6), not only a space;
//   - a type 7 block (a complete tag alone on a line) may start on a line that
//     is only a lazy paragraph continuation, because cmark only refuses when
//     the paragraph itself is the container the line matched; the unmatched
//     list item or quote is closed and the tag becomes an HTML block.
type htmlBlockParser struct{ parser.BlockParser }

func newHTMLBlockParser() parser.BlockParser {
	return htmlBlockParser{parser.NewHTMLBlockParser()}
}

var allowedBlockTags = map[string]bool{}

func init() {
	for _, t := range strings.Fields(`address article aside base basefont blockquote body caption center col
		colgroup dd details dialog dir div dl dt fieldset figcaption figure footer form frame frameset h1 h2 h3 h4
		h5 h6 head header hr html iframe legend li link main menu menuitem meta nav noframes ol optgroup option p
		param search section summary table tbody td tfoot th thead title tr track ul`) {
		allowedBlockTags[t] = true
	}
}

const attributePattern = `(?:[\r\n \t]+[a-zA-Z_:][a-zA-Z0-9:._-]*(?:[\r\n \t]*=[\r\n \t]*(?:[^\"'=<>` + "`" + `\x00-\x20]+|'[^']*'|"[^"]*"))?)`

var (
	htmlBlockType1OpenRegexp = regexp.MustCompile(`(?i)^[ ]{0,3}<(script|pre|style|textarea)(?:\s.*|>.*|/>.*|)(?:\r\n|\n)?$`)
	htmlBlockType2OpenRegexp = regexp.MustCompile(`^[ ]{0,3}<!\-\-`)
	htmlBlockType3OpenRegexp = regexp.MustCompile(`^[ ]{0,3}<\?`)
	htmlBlockType4OpenRegexp = regexp.MustCompile(`^[ ]{0,3}<![A-Z]+.*(?:\r\n|\n)?$`)
	htmlBlockType5OpenRegexp = regexp.MustCompile(`^[ ]{0,3}<\!\[CDATA\[`)
	htmlBlockType6Regexp     = regexp.MustCompile(`^[ ]{0,3}</?([a-zA-Z]+[a-zA-Z0-9\-]*)(?:[ \t].*|>.*|/>.*|)(?:\r\n|\n)?$`)
	htmlBlockType7Regexp     = regexp.MustCompile(`^[ ]{0,3}<(/)?([a-zA-Z]+[a-zA-Z0-9\-]*)(` + attributePattern + `*)[ \t]*(?:>|/>)[ \t]*(?:\r\n|\n)?$`)
)

func (b htmlBlockParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	var node *ast.HTMLBlock
	line, segment := reader.PeekLine()
	last := pc.LastOpenedBlock().Node
	// The line only continues the paragraph lazily when the paragraph is not
	// a child of the container this line matched.
	canStartType7 := !ast.IsParagraph(last) || last.Parent() != parent

	switch {
	case htmlBlockType1OpenRegexp.Match(line):
		node = ast.NewHTMLBlock(ast.HTMLBlockType1)
	case htmlBlockType2OpenRegexp.Match(line):
		node = ast.NewHTMLBlock(ast.HTMLBlockType2)
	case htmlBlockType3OpenRegexp.Match(line):
		node = ast.NewHTMLBlock(ast.HTMLBlockType3)
	case htmlBlockType4OpenRegexp.Match(line):
		node = ast.NewHTMLBlock(ast.HTMLBlockType4)
	case htmlBlockType5OpenRegexp.Match(line):
		node = ast.NewHTMLBlock(ast.HTMLBlockType5)
	default:
		if match := htmlBlockType7Regexp.FindSubmatchIndex(line); match != nil {
			isCloseTag := match[2] > -1 && bytes.Equal(line[match[2]:match[3]], []byte("/"))
			hasAttr := match[6] != match[7]
			tagName := strings.ToLower(string(line[match[4]:match[5]]))
			if allowedBlockTags[tagName] {
				node = ast.NewHTMLBlock(ast.HTMLBlockType6)
			} else if canStartType7 && !(isCloseTag && hasAttr) {
				node = ast.NewHTMLBlock(ast.HTMLBlockType7)
			}
		}
	}
	if node == nil {
		if match := htmlBlockType6Regexp.FindSubmatchIndex(line); match != nil {
			if allowedBlockTags[strings.ToLower(string(line[match[2]:match[3]]))] {
				node = ast.NewHTMLBlock(ast.HTMLBlockType6)
			}
		}
	}
	if node != nil {
		reader.AdvanceToEOL()
		node.Lines().Append(segment)
		return node, parser.NoChildren
	}
	return nil, parser.NoChildren
}
