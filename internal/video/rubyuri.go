package video

import (
	"net/url"
	"regexp"
	"strings"
)

// rubyURI is the part of a parsed URI the video helpers use.
type rubyURI struct {
	host  string // as written (not lowercased), without port or userinfo
	path  string // raw (still percent-encoded), as Ruby's URI#path
	query string // raw, without the "?"
}

// queryJunk removes what uri 1.1.1's Generic#query= deletes (v.delete!("\t\r\n")):
// tab, CR and LF vanish from a query, and only then is it checked for bad escapes.
var queryJunk = strings.NewReplacer("\t", "", "\r", "", "\n", "")

// parseRubyURI parses raw like Ruby's URI.parse (uri 1.1.1, the RFC 3986
// parser) and returns the host, path and query, or false where Ruby raises
// URI::InvalidURIError (or a subclass). raw must already have a scheme (see
// WithScheme).
//
// The Ruby behaviours replicated here, each probed against Ruby 3.4.8:
//   - uriSafe: which characters each part refuses (non-ASCII everywhere; space,
//     control characters and |<>\^`{}[]" outside the query; "%" not followed by
//     two hex digits, though the query only refuses "%" plus two non-hex ones;
//     a second "#" in the fragment).
//   - tab, CR and LF are deleted from the query before it is checked and
//     returned, so "v=KpILHX2D-A<LF>A" is "v=KpILHX2D-AA" and "%4<LF>1" is a
//     valid "%41".
//   - URI::FTP drops the leading "/" of the path (an encoded leading "%2F"
//     becomes "/") and cuts it at ";type=".
//   - URI::LDAP/LDAPS refuse a fragment, URI::MailTo needs a query (these raise
//     in Ruby, where the services do not rescue them: Go reports "no match").
//
// Only the base (scheme, authority, path) goes through net/url; the query and
// the fragment are validated by uriSafe and split off by hand, because net/url
// refuses control characters in a query that Ruby accepts.
func parseRubyURI(raw string) (rubyURI, bool) {
	if !uriSafe(raw) {
		return rubyURI{}, false
	}
	rest, _, hasFragment := strings.Cut(raw, "#")
	base, query, hasQuery := strings.Cut(rest, "?")
	query = queryJunk.Replace(query)
	scheme, _, _ := strings.Cut(base, "://")
	switch strings.ToLower(scheme) {
	case "ldap", "ldaps":
		if hasFragment {
			return rubyURI{}, false
		}
	case "mailto":
		if !hasQuery {
			return rubyURI{}, false
		}
	}
	u, err := url.Parse(base)
	if err != nil {
		return rubyURI{}, false
	}
	path := u.EscapedPath()
	if strings.EqualFold(scheme, "ftp") {
		path = strings.TrimPrefix(path, "/")
		if strings.HasPrefix(path, "%2F") {
			path = "/" + path[3:]
		}
		if i := strings.Index(path, ";type="); i >= 0 {
			path = path[:i]
		}
	}
	return rubyURI{host: u.Hostname(), path: path, query: query}, true
}

// rubyStrip is Ruby's String#strip: it removes leading and trailing ASCII
// whitespace and NUL (not Unicode spaces such as U+00A0, which TrimSpace would).
func rubyStrip(s string) string {
	return strings.Trim(s, "\x00\t\n\v\f\r ")
}

// strictBad are the characters Ruby's URI.parse refuses outside the query.
const strictBad = "|<>\\^`{}[]\""

// badQueryPct is a "%" followed by two non-hex characters, which Ruby refuses
// even in the query (a lone "%", "%a" or "%az" are accepted there).
var badQueryPct = regexp.MustCompile(`%[^0-9A-Fa-f]{2}`)

// uriSafe reports whether Ruby's URI.parse would accept s (probed against Ruby
// 3.4 / uri 1.1): non-ASCII is refused everywhere; the part before the query
// and the fragment also refuse space and control characters, strictBad, and
// any "%" not followed by two hex digits; the fragment refuses a second "#";
// the query is lenient (see badQueryPct). It only checks characters: the
// scheme-specific rules are in parseRubyURI.
func uriSafe(s string) bool {
	if strings.ContainsFunc(s, func(r rune) bool { return r >= 0x80 }) {
		return false
	}
	rest, fragment, hasFragment := strings.Cut(s, "#")
	base, query, hasQuery := strings.Cut(rest, "?")
	if !strictPart(base) || (hasFragment && (strings.Contains(fragment, "#") || !strictPart(fragment))) {
		return false
	}
	return !hasQuery || !badQueryPct.MatchString(queryJunk.Replace(query))
}

var pctEscape = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)

func strictPart(s string) bool {
	if strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f || strings.ContainsRune(strictBad, r) }) {
		return false
	}
	// every "%" must start a %XX escape
	return strings.Count(s, "%") == len(pctEscape.FindAllString(s, -1))
}
