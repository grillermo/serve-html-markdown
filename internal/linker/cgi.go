package linker

import "strings"

// maxNumericRef: Ruby's CGI.unescapeHTML decodes a numeric reference only
// when its value is below U+10FFFF (verified exhaustively against Ruby 3.4;
// U+10FFFF itself is left as-is).
const maxNumericRef = 0x10FFFF

// cgiUnescape ports Ruby's CGI.unescapeHTML. It decodes only &amp; &lt; &gt;
// &quot; &apos; (lowercase) and decimal/hex numeric references (&#NN;
// &#xHH; &#XHH;), never named entities such as &nbsp;. Values are emitted as
// raw UTF-8 without validation, exactly like Ruby: a surrogate reference
// yields the invalid 3-byte sequence, which later makes the locator fail with
// ErrInvalidUTF8 just as Ruby's regexp raises ArgumentError.
func cgiUnescape(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	var out []byte
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '&' {
			continue
		}
		repl, n, ok := decodeRef(s[i:])
		if !ok {
			continue
		}
		out = append(out, s[last:i]...)
		out = append(out, repl...)
		i += n - 1
		last = i + 1
	}
	if out == nil {
		return s
	}
	out = append(out, s[last:]...)
	return string(out)
}

// decodeRef decodes the reference at the start of s (which begins with '&').
// It returns the replacement, the number of bytes consumed, and whether s
// started with a decodable reference.
func decodeRef(s string) (repl string, n int, ok bool) {
	for _, e := range [...]struct{ name, repl string }{
		{"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", `"`}, {"&apos;", "'"},
	} {
		if strings.HasPrefix(s, e.name) {
			return e.repl, len(e.name), true
		}
	}
	if len(s) < 4 || s[1] != '#' {
		return "", 0, false
	}
	i, base := 2, 10
	if s[i] == 'x' || s[i] == 'X' {
		i, base = 3, 16
	}
	start := i
	value := 0
	for i < len(s) && (isDigit(s[i]) || (base == 16 && isHex(s[i]))) {
		if value <= maxNumericRef { // saturate: huge values stay undecoded
			value = value*base + hexVal(s[i])
		}
		i++
	}
	if i == start || i >= len(s) || s[i] != ';' || value >= maxNumericRef {
		return "", 0, false
	}
	return string(appendUTF8Raw(nil, value)), i + 1, true
}

func hexVal(b byte) int {
	switch {
	case b <= '9':
		return int(b - '0')
	default:
		return int(b|0x20-'a') + 10
	}
}

// appendUTF8Raw encodes v as UTF-8 without rejecting surrogates (unlike
// utf8.AppendRune, which substitutes U+FFFD), matching Ruby's byte output.
func appendUTF8Raw(b []byte, v int) []byte {
	switch {
	case v < 0x80:
		return append(b, byte(v))
	case v < 0x800:
		return append(b, 0xC0|byte(v>>6), 0x80|byte(v&0x3F))
	case v < 0x10000:
		return append(b, 0xE0|byte(v>>12), 0x80|byte(v>>6&0x3F), 0x80|byte(v&0x3F))
	default:
		return append(b, 0xF0|byte(v>>18), 0x80|byte(v>>12&0x3F), 0x80|byte(v>>6&0x3F), 0x80|byte(v&0x3F))
	}
}
