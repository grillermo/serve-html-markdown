package markdown

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// rejected matches everything comrak's anchorizer drops from a heading id:
// anything that is not a letter, mark, number, connector punctuation, space or
// hyphen.
var rejected = regexp.MustCompile(`[^\p{L}\p{M}\p{N}\p{Pc} -]`)

// anchorizer reproduces comrak's Anchorizer: ids are unique within one
// document, later duplicates get -1, -2, … suffixes. A fresh one is created
// for every Render call so there is no shared state between calls.
type anchorizer struct {
	seen map[string]struct{}
	// next remembers, per base id, the suffix to try next. Every smaller
	// suffix is already taken (the set only grows), so skipping them gives the
	// same ids as comrak's linear probe without quadratic work on repeats.
	next map[string]int
}

func newAnchorizer() *anchorizer {
	return &anchorizer{seen: map[string]struct{}{}, next: map[string]int{}}
}

func (a *anchorizer) anchorize(text string) string {
	base := strings.ReplaceAll(rejected.ReplaceAllString(rustLower(text), ""), " ", "-")
	id := base
	if _, taken := a.seen[id]; taken {
		n := a.next[base]
		if n < 1 {
			n = 1
		}
		for {
			id = base + "-" + strconv.Itoa(n)
			if _, taken := a.seen[id]; !taken {
				break
			}
			n++
		}
		a.next[base] = n + 1
	}
	a.seen[id] = struct{}{}
	return id
}

// rustLower mimics Rust's str::to_lowercase, which uses full Unicode case
// mapping (İ becomes i + U+0307) and a context-sensitive final sigma, unlike
// Go's simple-mapping strings.ToLower.
func rustLower(s string) string {
	if isASCII(s) {
		return strings.ToLower(s)
	}
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range rs {
		switch {
		case r == 'İ':
			b.WriteString("i̇")
		case r == 'Σ':
			if isFinalSigma(rs, i) {
				b.WriteRune('ς')
			} else {
				b.WriteRune('σ')
			}
		default:
			b.WriteString(strings.ToLower(string(r)))
		}
	}
	return b.String()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// isFinalSigma reports whether the capital sigma at rs[i] is at the end of a
// word (preceded by a cased letter and not followed by one, ignoring
// case-ignorable characters), the condition under which Unicode lowercases it
// to ς instead of σ.
func isFinalSigma(rs []rune, i int) bool {
	before := false
	for j := i - 1; j >= 0; j-- {
		if isCaseIgnorable(rs[j]) {
			continue
		}
		before = isCased(rs[j])
		break
	}
	if !before {
		return false
	}
	for j := i + 1; j < len(rs); j++ {
		if isCaseIgnorable(rs[j]) {
			continue
		}
		return !isCased(rs[j])
	}
	return true
}

func isCased(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r)
}

func isCaseIgnorable(r rune) bool {
	switch r {
	case '\'', '.', ':', '·', '’', '‘', '^', '`', '¨', '¯', '´', '¸':
		return true
	}
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk)
}
