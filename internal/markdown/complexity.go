package markdown

import "bytes"

const (
	// maxNesting is the most container markers (">" and list bullets) plus
	// indentation levels the leading prefix of one line may carry. goldmark
	// handles a line of n markers in O(n^2) (a 80 KiB run of ">" takes 2 s, a
	// 320 KiB run over 20 s), and real documents nest a handful of levels.
	maxNesting = 64
	// maxReferenceDefinitions is the most "[label]: destination" lines a
	// document may have; goldmark's reference handling is quadratic in them
	// (54,000 of them take over a second).
	maxReferenceDefinitions = 10000
)

// tooComplex reports whether src (already normalized) nests containers deeper
// than maxNesting on some line, or defines more than maxReferenceDefinitions
// link references. It is one linear pass that allocates nothing. The limits are
// far above anything a hand-written document reaches, so it never changes the
// output for a real one.
func tooComplex(src []byte) bool {
	refs := 0
	for len(src) > 0 {
		line := src
		if i := bytes.IndexByte(src, '\n'); i >= 0 {
			line, src = src[:i], src[i+1:]
		} else {
			src = nil
		}
		if nestingDepth(line) > maxNesting {
			return true
		}
		if isReferenceDefinition(line) {
			if refs++; refs > maxReferenceDefinitions {
				return true
			}
		}
	}
	return false
}

// nestingDepth counts the levels in the leading prefix of line: each container
// marker (">", or a bullet: "-", "*", "+" or "1." / "1)" followed by a space or
// tab) is one, and the white space before a marker adds one level per two
// columns. Text after the last marker, however indented, adds nothing: plain
// indented text is cheap.
func nestingDepth(line []byte) int {
	depth, indent := 0, 0
	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case c == ' ':
			indent++
			i++
			continue
		case c == '\t':
			indent += 4
			i++
			continue
		case c == '>':
			i++
		case (c == '-' || c == '*' || c == '+') && i+1 < len(line) && (line[i+1] == ' ' || line[i+1] == '\t'):
			i++
		case c >= '0' && c <= '9':
			j := i
			for j < len(line) && line[j] >= '0' && line[j] <= '9' {
				j++
			}
			if j-i > 9 || j+1 >= len(line) || (line[j] != '.' && line[j] != ')') || (line[j+1] != ' ' && line[j+1] != '\t') {
				return depth
			}
			i = j + 1
		default:
			return depth
		}
		depth += 1 + indent/2
		indent = 0
	}
	return depth
}

// isReferenceDefinition reports whether line looks like "[label]: ..." with at
// most three spaces of indentation.
func isReferenceDefinition(line []byte) bool {
	i := 0
	for i < len(line) && line[i] == ' ' {
		i++
	}
	if i > 3 || i >= len(line) || line[i] != '[' {
		return false
	}
	j := bytes.IndexByte(line[i+1:], ']')
	return j > 0 && i+1+j+1 < len(line) && line[i+1+j+1] == ':'
}
