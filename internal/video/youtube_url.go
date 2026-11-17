// Package video holds the video-link helpers shared by the web layer, the
// jobs and the store. It is a leaf package: it must never import
// internal/store (store imports it), nor anything else that does.
package video

import (
	"encoding/hex"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var (
	// youtubeVideoID is Ruby's YoutubeUrl::ID; \w is ASCII-only in both.
	youtubeVideoID = regexp.MustCompile(`\A[\w-]{11}\z`)
	// youtubePathID is Ruby's YoutubeUrl::PATH_ID: /shorts/ID, /live/ID, /embed/ID, /v/ID, /e/ID, /watch/ID.
	youtubePathID = regexp.MustCompile(`\A/(?:shorts|live|embed|v|e|watch)/([^/?#]+)`)
	hasScheme     = regexp.MustCompile(`(?i)\A[a-z][a-z0-9+.-]*://`)
	youtubeHosts  = []string{"youtube.com", "m.youtube.com", "music.youtube.com", "gaming.youtube.com", "youtube-nocookie.com", "youtu.be"}
)

// YoutubeVideoID returns the 11-character video id of a YouTube link, or ""
// when it is not one. It mirrors Ruby's YoutubeUrl.video_id: surrounding
// whitespace is ignored, a missing scheme is allowed, the host is one of the
// YouTube hosts (case insensitive, "www." dropped), and the id comes from
// youtu.be/ID, /watch?v=ID (the last v= wins), /shorts|live|embed|v|e|watch/ID,
// or from an /attribution_link whose u= is one of those paths on
// www.youtube.com.
//
// URLs Ruby's URI.parse refuses are refused too (see uriSafe for the rules and
// the one known gap).
func YoutubeVideoID(rawURL string) string {
	raw := strings.TrimSpace(rawURL)
	if !hasScheme.MatchString(raw) {
		raw = "https://" + raw
	}
	if !uriSafe(raw) {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if !slices.Contains(youtubeHosts, host) {
		return ""
	}
	query := decodeWWWForm(u.RawQuery)
	path := u.EscapedPath()
	var id string
	switch {
	case host == "youtu.be":
		id, _, _ = strings.Cut(strings.TrimPrefix(path, "/"), "/")
	case path == "/attribution_link":
		return YoutubeVideoID("https://www.youtube.com" + query["u"])
	case strings.TrimSuffix(path, "/") == "/watch":
		id = query["v"]
	default:
		if m := youtubePathID.FindStringSubmatch(path); m != nil {
			id = m[1]
		}
	}
	if !youtubeVideoID.MatchString(id) {
		return ""
	}
	return id
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
// the query is lenient (see badQueryPct). One known gap: Go's url.Parse refuses
// control characters in the query, which Ruby accepts.
func uriSafe(s string) bool {
	if strings.ContainsFunc(s, func(r rune) bool { return r >= 0x80 }) {
		return false
	}
	rest, fragment, hasFragment := strings.Cut(s, "#")
	base, query, hasQuery := strings.Cut(rest, "?")
	if !strictPart(base) || (hasFragment && (strings.Contains(fragment, "#") || !strictPart(fragment))) {
		return false
	}
	return !hasQuery || !badQueryPct.MatchString(query)
}

var pctEscape = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)

func strictPart(s string) bool {
	if strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f || strings.ContainsRune(strictBad, r) }) {
		return false
	}
	// every "%" must start a %XX escape
	return strings.Count(s, "%") == len(pctEscape.FindAllString(s, -1))
}

// decodeWWWForm is Ruby's URI.decode_www_form(query).to_h: pairs split on "&"
// (empty ones skipped), "+" and valid %XX escapes decoded (other "%" are kept
// as they are, like Ruby), the last duplicate key wins.
func decodeWWWForm(query string) map[string]string {
	m := map[string]string{}
	for pair := range strings.SplitSeq(query, "&") {
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		m[formUnescape(k)] = formUnescape(v)
	}
	return m
}

func formUnescape(s string) string {
	s = strings.ReplaceAll(s, "+", " ")
	return pctEscape.ReplaceAllStringFunc(s, func(esc string) string {
		b, _ := hex.DecodeString(esc[1:])
		return string(b)
	})
}
