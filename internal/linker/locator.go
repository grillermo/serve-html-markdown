package linker

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

const notFoundMessage = "Selection not found in source — select a plainer run of text."

// locate ports SelectionLocator.locate: it finds the occurrence-th match of
// the selected text in the map's plain text, where any run of whitespace in
// the selection matches any run of whitespace in the plain text. It returns
// the match as a range over Plain.
//
// The Ruby builds /tok1\s+tok2\s+.../ from the whitespace-split selection and
// scans every overlapping match. The tokens contain no whitespace, so the
// match is fully determined by its start and is done here with literal
// byte matching (no regexp, hence no pattern-size limits). A match is found
// at every position, stepping one character, so overlapping matches count.
//
// An out-of-range occurrence falls back to the first match. A negative
// occurrence indexes from the end like Ruby's Array#fetch (Link clamps
// negatives to 0 before calling).
func locate(m *docMap, selected string, occurrence int) (srcRange, error) {
	if !utf8.ValidString(selected) {
		return srcRange{}, ErrInvalidUTF8 // Ruby: split raises ArgumentError
	}
	tokens := splitSpace(selected)
	if len(tokens) == 0 {
		return srcRange{}, notFound()
	}
	if !utf8.Valid(m.Plain) {
		return srcRange{}, ErrInvalidUTF8 // Ruby: regexp match raises ArgumentError
	}

	var matches []srcRange
	first := []byte(tokens[0])
	plain := m.Plain
	for pos := 0; pos <= len(plain); {
		i := bytes.Index(plain[pos:], first)
		if i < 0 {
			break
		}
		start := pos + i
		if end, ok := matchRest(plain, start+len(first), tokens[1:]); ok {
			matches = append(matches, srcRange{start, end})
			if occurrence >= 0 && len(matches) > occurrence {
				return matches[occurrence], nil // later matches cannot change the answer
			}
		}
		pos = start + 1 // overlapping matches; non-start bytes never begin a token
	}
	if len(matches) == 0 {
		return srcRange{}, notFound()
	}
	if occurrence < 0 {
		if idx := len(matches) + occurrence; idx >= 0 {
			return matches[idx], nil
		}
		return matches[0], nil
	}
	return matches[0], nil // occurrence beyond the last match
}

// matchRest matches \s+tok for each remaining token starting at p. \s+ is
// greedy and tokens never begin with whitespace, so no backtracking is needed.
func matchRest(plain []byte, p int, tokens []string) (int, bool) {
	for _, tok := range tokens {
		w := p
		for w < len(plain) && isSpace(plain[w]) {
			w++
		}
		if w == p || !bytes.HasPrefix(plain[w:], []byte(tok)) {
			return 0, false
		}
		p = w + len(tok)
	}
	return p, true
}

// splitSpace ports selected_text.split(/\s+/).reject(&:empty?) with Ruby's
// ASCII-only \s.
func splitSpace(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r < utf8.RuneSelf && isSpace(byte(r)) })
}
