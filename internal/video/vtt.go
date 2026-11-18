package video

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"serve-html-markdown/internal/textutil"
)

var (
	// vttHeader is VttText::HEADER minus its NOTE\b branch (see isVttHeader).
	vttHeader = regexp.MustCompile(`\A(?:WEBVTT|Kind:|Language:)`)
	vttCueNum = regexp.MustCompile(`\A[0-9]+\z`)
	vttTag    = regexp.MustCompile(`<[^>]*>`)
)

// VttText returns the plain spoken text of a WebVTT caption file, one line per
// caption line. It ports Ruby's VttText.call: YouTube's auto-captions tag every
// word with a timestamp and repeat each line across consecutive cues, so a
// 40-minute talk is ~400 KB of VTT for ~40 KB of words.
//
// Lines are split on "\n" and stripped; empty lines, header lines (WEBVTT,
// Kind:, Language:, NOTE), timing lines (containing "-->") and bare cue numbers
// are dropped. In the rest, <...> tags are removed, HTML references are decoded
// the way CGI.unescapeHTML does, runs of spaces become one, and the line is
// stripped again. Lines left empty are dropped and consecutive duplicates are
// collapsed.
//
// Ruby raises ArgumentError on input that is not valid UTF-8, and on a line that
// decodes to invalid UTF-8 (a numeric reference to a surrogate, "&#xD800;"). Here
// each invalid byte becomes U+FFFD instead, so a damaged caption file still
// yields a transcript. For all other input the result is identical to Ruby's.
func VttText(vtt string) string {
	vtt = strings.ToValidUTF8(vtt, "\uFFFD")
	var out []string
	for line := range strings.SplitSeq(vtt, "\n") {
		line = rubyStrip(line)
		if line == "" || isVttHeader(line) || strings.Contains(line, "-->") || vttCueNum.MatchString(line) {
			continue
		}
		line = strings.ToValidUTF8(textutil.CGIUnescape(vttTag.ReplaceAllString(line, "")), "\uFFFD")
		line = rubyStrip(squeezeSpaces(line))
		if line == "" || (len(out) > 0 && out[len(out)-1] == line) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// squeezeSpaces is Ruby's String#squeeze(" "): runs of spaces become one space.
func squeezeSpaces(s string) string {
	if !strings.Contains(s, "  ") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' && i > 0 && s[i-1] == ' ' {
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// isVttHeader is VttText::HEADER, /\A(WEBVTT|Kind:|Language:|NOTE\b)/. Ruby's \b
// is Unicode-aware, unlike Go's: "NOTEé" is not a NOTE header there, so the
// boundary after "NOTE" is checked by hand.
func isVttHeader(line string) bool {
	if vttHeader.MatchString(line) {
		return true
	}
	rest, ok := strings.CutPrefix(line, "NOTE")
	if !ok {
		return false
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return rest == "" || !isWordRune(r)
}

// isWordRune is Onigmo's word character for UTF-8: Alphabetic, Mark, Decimal
// Number or Connector Punctuation.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.Is(unicode.Nd, r) || unicode.Is(unicode.Pc, r) ||
		unicode.Is(unicode.Nl, r) || unicode.Is(unicode.Other_Alphabetic, r)
}
