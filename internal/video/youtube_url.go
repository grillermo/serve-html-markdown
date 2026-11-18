// Package video holds the video-link helpers shared by the web layer, the
// jobs and the store. It is a leaf package: it must never import
// internal/store (store imports it), nor anything else that does.
package video

import (
	"encoding/hex"
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
// URLs Ruby's URI.parse refuses are refused too; parseRubyURI lists the Ruby
// behaviours replicated (including tab, CR and LF being deleted from the query).
func YoutubeVideoID(rawURL string) string {
	u, ok := parseRubyURI(WithScheme(rubyStrip(rawURL)))
	if !ok {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.host), "www.")
	if !slices.Contains(youtubeHosts, host) {
		return ""
	}
	query := decodeWWWForm(u.query)
	path := u.path
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

// YoutubeWatchURL is the canonical watch link of a video id (Ruby's
// YoutubeUrl.watch_url). The store keys YouTube-link rows on it, and
// YoutubeVideoID maps it back to the same id.
func YoutubeWatchURL(videoID string) string {
	return "https://www.youtube.com/watch?v=" + videoID
}

// WithScheme prefixes "https://" unless url already starts with a scheme
// ("[a-z][a-z0-9+.-]*://", any case): Ruby's YoutubeUrl.with_scheme.
func WithScheme(url string) string {
	if hasScheme.MatchString(url) {
		return url
	}
	return "https://" + url
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
