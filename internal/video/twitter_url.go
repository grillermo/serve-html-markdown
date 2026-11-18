package video

import (
	"errors"
	"regexp"
	"slices"
	"strings"
)

// ErrUnsupportedURL is returned by NormalizeTwitterURL for anything that is not
// a tweet link. Its text is Ruby's TwitterUrl::InvalidError message: the video
// endpoint shows it to API clients ("Unsupported URL: send an x.com/...").
// It is a shared value: never modify it.
var ErrUnsupportedURL = errors.New("Unsupported URL") //nolint:staticcheck // user-facing text, must stay capitalised

// twitterHosts is TwitterUrl::HOSTS: x.com and twitter.com, their mobile hosts,
// and the embed-fixer and mirror hosts people share links through. All of them
// point at the same tweet ids.
var twitterHosts = []string{
	"x.com", "twitter.com", "mobile.x.com", "mobile.twitter.com",
	"fxtwitter.com", "vxtwitter.com", "fixupx.com", "fixvx.com", "twittpr.com",
	"xcancel.com", "nitter.net",
}

// twitterStatusPath is TwitterUrl::STATUS_PATH: /<user>/status/<id>,
// /i/status/<id>, /i/web/status/<id>, with any trailing /video/1 or /photo/1 and
// "statuses" in older links. \d is ASCII-only in both Ruby and Go.
var twitterStatusPath = regexp.MustCompile(`\A/(?:i/web|([^/]+))/status(?:es)?/(\d+)`)

// NormalizeTwitterURL returns the canonical https://x.com/<user>/status/<id> of
// any tweet link, without its tracking query string, or ErrUnsupportedURL. It
// mirrors Ruby's TwitterUrl.normalize: surrounding whitespace is ignored, a
// missing scheme is allowed, "www." is dropped from the host, and URLs Ruby's
// URI.parse refuses (see uriSafe) are unsupported. Only the host and the path
// matter, so the query and the fragment are checked for validity but never
// handed to net/url (which would refuse some that Ruby accepts).
func NormalizeTwitterURL(rawURL string) (string, error) {
	u, ok := parseRubyURI(WithScheme(rubyStrip(rawURL)))
	if !ok {
		return "", ErrUnsupportedURL
	}
	host := strings.TrimPrefix(strings.ToLower(u.host), "www.")
	if !slices.Contains(twitterHosts, host) {
		return "", ErrUnsupportedURL
	}
	m := twitterStatusPath.FindStringSubmatch(u.path)
	if m == nil {
		return "", ErrUnsupportedURL
	}
	user := m[1]
	if user == "" {
		user = "i"
	}
	return "https://x.com/" + user + "/status/" + m[2], nil
}
