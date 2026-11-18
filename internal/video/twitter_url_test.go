package video_test

import (
	"errors"
	"testing"

	"serve-html-markdown/internal/video"
)

// Ruby: TwitterUrlTest "normalizes every tweet link variant to x.com without
// tracking query" (test/services/twitter_url_test.rb), every pair verbatim.
func TestNormalizeTwitterURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://x.com/foo/status/123", "https://x.com/foo/status/123"},
		{"https://x.com/foo/status/123?s=20&t=abc", "https://x.com/foo/status/123"},
		{"https://twitter.com/foo/status/123", "https://x.com/foo/status/123"},
		{"https://www.twitter.com/foo/status/123", "https://x.com/foo/status/123"},
		{"https://mobile.twitter.com/foo/status/123", "https://x.com/foo/status/123"},
		{"https://mobile.x.com/foo/status/123", "https://x.com/foo/status/123"},
		{"https://x.com/foo/status/123/video/1", "https://x.com/foo/status/123"},
		{"https://x.com/foo/status/123/photo/2", "https://x.com/foo/status/123"},
		{"https://twitter.com/foo/statuses/123", "https://x.com/foo/status/123"},
		{"https://x.com/i/status/123", "https://x.com/i/status/123"},
		{"https://twitter.com/i/web/status/123", "https://x.com/i/status/123"},
		{"https://fxtwitter.com/foo/status/123", "https://x.com/foo/status/123"},
		{"https://vxtwitter.com/foo/status/123", "https://x.com/foo/status/123"},
		{"https://fixupx.com/foo/status/123", "https://x.com/foo/status/123"},
		{"https://fixvx.com/foo/status/123", "https://x.com/foo/status/123"},
		{"https://xcancel.com/foo/status/123", "https://x.com/foo/status/123"},
		{"http://x.com/foo/status/123", "https://x.com/foo/status/123"},
		{"x.com/foo/status/123", "https://x.com/foo/status/123"},
		{" https://X.com/foo/status/123 \n", "https://x.com/foo/status/123"},
		// hosts in Ruby's HOSTS that the Ruby test does not exercise
		{"https://mobile.twitter.com/foo/status/123", "https://x.com/foo/status/123"},
		{"https://twittpr.com/foo/status/123", "https://x.com/foo/status/123"},
		{"https://nitter.net/foo/status/123", "https://x.com/foo/status/123"},
		{"https://www.x.com/foo/status/123", "https://x.com/foo/status/123"},
		{"https://x.com:443/foo/status/123", "https://x.com/foo/status/123"},
		{"https://x.com/foo/status/123#frag", "https://x.com/foo/status/123"},
		{"https://x.com/foo/status/123abc", "https://x.com/foo/status/123"},
		{"https://x.com/foo/status/0012", "https://x.com/foo/status/0012"},
		{"https://x.com/foo/status/123?a=|{}", "https://x.com/foo/status/123"}, // lenient query, like Ruby's URI
		{"https://x.com/foo/status/123?a=%4", "https://x.com/foo/status/123"},
	}
	for _, c := range cases {
		got, err := video.NormalizeTwitterURL(c.in)
		if err != nil || got != c.want {
			t.Errorf("NormalizeTwitterURL(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

// Ruby: "rejects non-status and non-twitter urls", plus more shapes.
func TestNormalizeTwitterURLRejects(t *testing.T) {
	for _, in := range []string{
		"https://x.com/foo",
		"https://x.com/foo/status/abc",
		"https://youtube.com/watch?v=x",
		"https://notx.com/foo/status/1",
		"",
		// further shapes
		"   ",
		"https://x.com",
		"https://x.com/",
		"https://x.com/status/123",
		"https://x.com/foo/bar/status/123",
		"https://x.com/foo/status/",
		"https://x.com/foo/status",
		"https://x.com/?next=/foo/status/1",
		"https://x.com.evil.com/foo/status/1",
		"https://evil.com/x.com/foo/status/1",
		"https://x.com@evil.com/foo/status/1",
		"https://studio.twitter.com/foo/status/1",
		"https://x.com/foo/status/1 2",
		"https://x.com/fo o/status/1",
		"https://x.com/foo/status/١٢٣", // non-ASCII digits
		"https://x.com/fóo/status/1",
		"https://%zz/foo/status/1",
		"https://x.com/foo/status/1#a#b",
		"https://x.com/foo/status/123?u=%zz",
		"https://x.com/foo/status/123?a=é",
		"not a url",
		"https://x.com:abc/foo/status/1",
	} {
		got, err := video.NormalizeTwitterURL(in)
		if !errors.Is(err, video.ErrUnsupportedURL) || got != "" {
			t.Errorf("NormalizeTwitterURL(%q) = %q, %v; want ErrUnsupportedURL", in, got, err)
		}
	}
	if video.ErrUnsupportedURL.Error() != "Unsupported URL" {
		t.Errorf("message = %q; the controller shows it to API clients", video.ErrUnsupportedURL)
	}
}
