package video_test

import (
	"testing"

	"serve-html-markdown/internal/video"
)

// Every form Ruby's YoutubeUrl.video_id accepts or rejects (test/services and
// app/services/youtube_url.rb).
func TestYoutubeVideoID(t *testing.T) {
	const id = "KpILHX2D-AA"
	cases := []struct {
		url  string
		want bool
	}{
		// watch
		{"https://www.youtube.com/watch?v=" + id, true},
		{"https://youtube.com/watch?v=" + id + "&t=5", true},
		{"https://m.youtube.com/watch?v=" + id, true},
		{"https://music.youtube.com/watch?v=" + id, true},
		{"https://gaming.youtube.com/watch?v=" + id, true},
		{"https://www.youtube-nocookie.com/watch?v=" + id, true},
		{"https://www.youtube.com/watch/?v=" + id, true},
		{"https://WWW.YouTube.COM/watch?v=" + id, true},
		{"HTTPS://www.youtube.com/watch?v=" + id, true},
		{"  https://www.youtube.com/watch?v=" + id + "\n", true},
		{"https://www.youtube.com:443/watch?v=" + id, true},
		{"https://www.youtube.com/watch?v=" + id + "#t=1", true},
		{"https://www.youtube.com/watch?feature=x&v=" + id, true},
		// schemeless
		{"youtube.com/watch?v=" + id, true},
		{"www.youtube.com/watch?v=" + id, true},
		{"youtu.be/" + id, true},
		// repeated v: the last one wins, like to_h
		{"https://www.youtube.com/watch?v=short&v=" + id, true},
		{"https://www.youtube.com/watch?v=" + id + "&v=short", false},
		// percent/plus decoding of the query, as decode_www_form
		{"https://www.youtube.com/watch?v=%4BpILHX2D-AA", true},
		{"https://www.youtube.com/watch?v=%ZZ", false},
		// youtu.be
		{"https://youtu.be/" + id, true},
		{"https://youtu.be/" + id + "?si=abc", true},
		{"https://youtu.be/" + id + "/extra", true},
		{"https://youtu.be/", false},
		{"https://youtu.be/tooshort", false},
		{"https://youtu.be/?v=" + id, false},
		// path forms
		{"https://www.youtube.com/shorts/" + id, true},
		{"https://www.youtube.com/live/" + id, true},
		{"https://www.youtube.com/embed/" + id, true},
		{"https://www.youtube.com/v/" + id, true},
		{"https://www.youtube.com/e/" + id, true},
		{"https://www.youtube.com/watch/" + id, true},
		{"https://www.youtube.com/shorts/" + id + "?feature=share", true},
		{"https://www.youtube.com/shorts/" + id + "/more", true},
		{"https://www.youtube.com/shorts/", false},
		{"https://www.youtube.com/shorts/tooshort", false},
		{"https://www.youtube.com/channel/" + id, false},
		{"https://www.youtube.com/user/" + id, false},
		{"https://www.youtube.com/" + id, false},
		// attribution links
		{"https://www.youtube.com/attribution_link?a=x&u=%2Fwatch%3Fv%3D" + id + "%26feature%3Dem", true},
		{"https://www.youtube.com/attribution_link?u=/shorts/" + id, true},
		{"https://www.youtube.com/attribution_link?u=%2Fwatch%3Fv%3Dshort", false},
		{"https://www.youtube.com/attribution_link", false},
		{"https://www.youtube.com/attribution_link?u=%2Fattribution_link", false},
		// id rules: 11 chars of [A-Za-z0-9_-]
		{"https://www.youtube.com/watch?v=" + id + "X", false},
		{"https://www.youtube.com/watch?v=KpILHX2D-A", false},
		{"https://www.youtube.com/watch?v=KpILHX2D-A!", false},
		{"https://www.youtube.com/watch?v=KpILHX2D-A%C3%A9", false},
		{"https://www.youtube.com/watch?v=", false},
		{"https://www.youtube.com/watch", false},
		{"https://www.youtube.com/", false},
		{"https://www.youtube.com", false},
		// hosts that are not YouTube
		{"https://notyoutube.com/watch?v=" + id, false},
		{"https://youtube.com.evil.com/watch?v=" + id, false},
		{"https://evil.com/youtube.com/watch?v=" + id, false},
		{"https://studio.youtube.com/watch?v=" + id, false},
		// tweets and other URLs, including ones that carry a YouTube link in the query
		{"https://x.com/a/status/1", false},
		{"https://x.com/a/status/1?u=youtube.com/watch?v=" + id, false},
		{"https://x.com/a/status/1?url=https://youtu.be/" + id, false},
		{"https://example.com/?next=youtu.be/" + id, false},
		{"https://x.com/youtube.com/watch?v=" + id, false},
		{"u", false},
		{"", false},
		{"   ", false},
		// URI.parse rejects these
		{"https://www.youtube.com/watch?v=" + id + " x", false},
		{"https://www.youtube.com/wa tch?v=" + id, false},
		{"https://%zz/watch?v=" + id, false},
		// Ruby's URI.parse: non-ASCII is refused everywhere ...
		{"https://www.youtube.com/watch?v=" + id + "&t=é", false},
		{"https://www.youtube.com/watch?v=" + id + "#é", false},
		{"https://www.youtube.com/wátch?v=" + id, false},
		// ... the query is otherwise lenient (these were probed against Ruby 3.4) ...
		{"https://www.youtube.com/watch?v=" + id + "&t=a|b", true},
		{"https://www.youtube.com/watch?v=" + id + "&t=<b>", true},
		{"https://www.youtube.com/watch?v=" + id + "&t=a\\b", true},
		{"https://www.youtube.com/watch?v=" + id + "&t=a^b", true},
		{"https://www.youtube.com/watch?v=" + id + "&t=`b`", true},
		{"https://www.youtube.com/watch?v=" + id + "&t={b}", true},
		{"https://www.youtube.com/watch?v=" + id + "&t=[b]", true},
		{"https://www.youtube.com/watch?v=" + id + "&t=\"b\"", true},
		{"https://www.youtube.com/watch?v=" + id + "&t=a b", true},
		{"https://www.youtube.com/watch?v=" + id + "&t=a%", true},
		{"https://www.youtube.com/watch?v=" + id + "&t=a%zb", true},
		{"https://www.youtube.com/watch?v=" + id + "&t=a%4z", true},
		{"https://www.youtube.com/watch?v=" + id + "&t=a%zz", false},
		{"https://www.youtube.com/watch?v=" + id + "&t=a%%%", false},
		// ... while the path, host, userinfo and fragment refuse them
		{"https://www.youtube.com/wa|tch?v=" + id, false},
		{"https://www.youtube.com/wa<tch?v=" + id, false},
		{"https://www.youtube.com/wa\\tch?v=" + id, false},
		{"https://www.youtube.com/wa^tch?v=" + id, false},
		{"https://www.youtube.com/wa`tch?v=" + id, false},
		{"https://www.youtube.com/wa{tch}?v=" + id, false},
		{"https://www.youtube.com/wa[tch]?v=" + id, false},
		{"https://www.youtube.com/wa\"tch?v=" + id, false},
		{"https://www.youtube.com/watch%?v=" + id, false},
		{"https://www.you|tube.com/watch?v=" + id, false},
		{"https://u|x@www.youtube.com/watch?v=" + id, false},
		{"https://www.youtube.com/watch?v=" + id + "#a|b", false},
		{"https://www.youtube.com/watch?v=" + id + "#a b", false},
		{"https://www.youtube.com/watch?v=" + id + "#a%", false},
		{"https://www.youtube.com/watch?v=" + id + "#a#b", false},
		{"https://www.youtube.com/watch?v=" + id + "#a%41", true},
		{"https://www.youtube.com/watch?v=" + id + "#", true},
		{"https://www.youtube.com/watch?v=" + id + "\t", true}, // trailing whitespace is stripped first
	}
	// uri 1.1.1 deletes tab, CR and LF from a query, so they neither hide nor break an id
	for _, c := range []struct {
		url  string
		want bool
	}{
		{"https://www.youtube.com/watch?v=KpILHX2D-A\nA", true},
		{"https://www.youtube.com/watch?v=KpILHX2D-A\rA", true},
		{"https://www.youtube.com/watch?v=KpILHX2D-A\tA", true},
		{"https://www.youtube.com/watch?v=KpILHX2D-A\r\nA", true},
		{"https://www.youtube.com/watch?v=\nKpILHX2D-AA", true},
		{"https://www.youtube.com/watch?\nv=KpILHX2D-AA", true},
		{"https://www.youtube.com/watch?v=KpILHX2D%4\n1A", false}, // %41 is "A": KpILHX2DAA has 10 characters
		{"https://www.youtube.com/watch?v=KpILHX2D-A%4\n1", true},
		{"https://www.youtube.com/watch?v=KpILHX2D-A%\n41", true},
		{"https://www.youtube.com/watch?v=KpILHX2D-A%\n\nzz", false}, // still a bad escape once the line breaks are gone
		{"https://www.youtube.com/watch?v=KpILHX2D-A%\nzz", false},
		{"https://youtu.be/KpILHX2D-AA?si=a\nb", true},
		{"https://www.youtube.com/attribution_link?a=xyz&u=%2Fwatch%3Fv%3DKpILHX2D-A%0AA%26feature%3Dshare", true}, // the decoded LF lands in the re-parsed query and is deleted
		{"https://www.youtube.com/attribution_link?u=%2Fwatch%3Fv%3DKpILHX2D-\nAA", true},
		{"https://www.youtube.com/attribution_link?u=/shorts/KpILHX2D\n-AA", true},
		{"https://www.youtube.com/attribution_link?u\n=%2Fwatch%3Fv%3DKpILHX2D-AA", true},
		{"https://www.youtube.com/watch\n?v=KpILHX2D-AA", false}, // a line break in the path is refused
	} {
		cases = append(cases, c)
	}
	for _, c := range cases {
		want := ""
		if c.want {
			want = id
		}
		if got := video.YoutubeVideoID(c.url); got != want {
			t.Errorf("YoutubeVideoID(%q) = %q, want %q", c.url, got, want)
		}
	}
}

// Ruby: YoutubeUrl.watch_url.
func TestYoutubeWatchURL(t *testing.T) {
	if got := video.YoutubeWatchURL("KpILHX2D-AA"); got != "https://www.youtube.com/watch?v=KpILHX2D-AA" {
		t.Errorf("YoutubeWatchURL = %q", got)
	}
	// What the store keys on: the watch URL maps back to the same id.
	if got := video.YoutubeVideoID(video.YoutubeWatchURL("KpILHX2D-AA")); got != "KpILHX2D-AA" {
		t.Errorf("round trip = %q", got)
	}
}

// Ruby: YoutubeUrl.with_scheme (a URL with any scheme://, any case, is left alone).
func TestWithScheme(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"youtube.com/watch", "https://youtube.com/watch"},
		{"https://a", "https://a"},
		{"http://a", "http://a"},
		{"HTTPS://A", "HTTPS://A"},
		{"ftp://a", "ftp://a"},
		{"a+b.c-d://x", "a+b.c-d://x"},
		{"1a://x", "https://1a://x"},
		{"x:/y", "https://x:/y"},
		{"x//y", "https://x//y"},
		{"", "https://"},
		{"\nhttps://a", "https://\nhttps://a"},
	} {
		if got := video.WithScheme(c.in); got != c.want {
			t.Errorf("WithScheme(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Ruby: YoutubeUrlTest, each of its three tests verbatim.
func TestYoutubeVideoIDRubyTests(t *testing.T) {
	const id = "KpILHX2D-AA"
	for _, url := range []string{
		"https://www.youtube.com/watch?v=" + id,
		"https://youtube.com/watch?v=" + id + "&t=42",
		"https://www.youtube.com/watch?feature=share&v=" + id,
		"https://www.youtube.com/watch/?v=" + id,
		"https://m.youtube.com/watch?v=" + id + "&list=PL123&index=2",
		"https://music.youtube.com/watch?v=" + id + "&si=abc",
		"https://gaming.youtube.com/watch?v=" + id,
		"https://youtu.be/" + id,
		"https://youtu.be/" + id + "?si=3zdm0UjTBIYF-k0c&t=10",
		"https://www.youtube.com/shorts/" + id + "?feature=share",
		"https://youtube.com/live/" + id + "?si=x",
		"https://www.youtube.com/embed/" + id + "?start=5",
		"https://www.youtube-nocookie.com/embed/" + id,
		"https://www.youtube.com/v/" + id,
		"https://www.youtube.com/e/" + id,
		"https://www.youtube.com/watch/" + id,
		"https://www.youtube.com/attribution_link?a=xyz&u=%2Fwatch%3Fv%3D" + id + "%26feature%3Dshare",
		"http://www.youtube.com/watch?v=" + id,
		"HTTPS://WWW.YOUTUBE.COM/watch?v=" + id,
		"www.youtube.com/watch?v=" + id,
		"youtu.be/" + id,
		"  https://youtu.be/" + id + "\n",
	} {
		if got := video.YoutubeVideoID(url); got != id {
			t.Errorf("YoutubeVideoID(%q) = %q, want %q", url, got, id)
		}
	}
	if got := video.YoutubeVideoID("https://www.youtube.com/watch?v=hWe-j78vNpc"); got != "hWe-j78vNpc" {
		t.Errorf("reported link: %q", got)
	}
	for _, url := range []string{
		"https://x.com/a/status/1",
		"https://www.youtube.com/watch?v=x",
		"https://www.youtube.com/@channel",
		"https://www.youtube.com/playlist?list=PL123",
		"https://notyoutube.com/watch?v=" + id,
		"not a url",
		"",
	} {
		if got := video.YoutubeVideoID(url); got != "" {
			t.Errorf("YoutubeVideoID(%q) = %q, want none", url, got)
		}
	}
}
