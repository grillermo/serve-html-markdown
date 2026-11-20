package llm

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Limits of the snippets kept on errors for the server log.
const (
	snippetBytes = 1000
	stderrBytes  = 2000
)

// Diagnostics returns slog attributes that explain err for the server log: the
// details its *ExpandError and *Error links carry (provider, exit code, CLI
// stderr, HTTP status and body, the start of a rejected output) and, when err
// wraps others, the whole chain with each link's type. They may quote the
// upstream's or the CLI's words, so they go to the log only, never to a page.
func Diagnostics(err error) []any {
	var attrs []any
	var chain []string
	for e := err; e != nil; e = errors.Unwrap(e) {
		chain = append(chain, fmt.Sprintf("%T: %s", e, e.Error()))
		switch v := e.(type) {
		case *ExpandError:
			attrs = append(attrs, v.details...)
		case *Error:
			if v.Status != 0 {
				attrs = append(attrs, "http_status", v.Status)
			}
			if v.body != "" {
				attrs = append(attrs, "http_body", v.body)
			}
		}
	}
	if len(chain) > 1 {
		attrs = append(attrs, "chain", strings.Join(chain, " <- "))
	}
	return attrs
}

// head is the first n bytes of s, cut on a rune boundary, marked when cut.
func head(s string, n int) string {
	if len(s) <= n {
		return strings.ToValidUTF8(s, "�")
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.ToValidUTF8(s[:n], "�") + "…"
}

// tail is the last n bytes of b (where a CLI prints its error), marked when cut.
func tail(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= n {
		return strings.ToValidUTF8(s, "�")
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return "…" + strings.ToValidUTF8(s[i:], "�")
}
