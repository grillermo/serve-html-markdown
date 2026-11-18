package video_test

import (
	"encoding/json"
	"os"
	"testing"
	"unicode/utf8"

	"serve-html-markdown/internal/video"
)

// goldenCase is one entry of testdata/golden/video/cases.json, produced by
// script/golden_video.rb from the real Ruby TwitterUrl, YoutubeUrl and VttText.
type goldenCase struct {
	Kind    string  `json:"kind"`
	Input   string  `json:"input"`
	Twitter *string `json:"twitter"`
	Youtube *string `json:"youtube"`
	Output  *string `json:"output"`
	Error   *string `json:"error"`
	// RubyRaises is set where an exception escapes the Ruby services (a URI
	// class with extra rules, e.g. mailto:// without a query).
	RubyRaises string `json:"ruby_raises"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// TestGoldenRuby checks the Go port against Ruby's own answers for a few
// thousand generated inputs (hosts, paths, queries, fragments, stray control and
// non-ASCII characters; WebVTT with tags, entities, odd whitespace and repeats).
// VIDEO_GOLDEN_FILE points it at a different corpus (see the script's header).
func TestGoldenRuby(t *testing.T) {
	path := os.Getenv("VIDEO_GOLDEN_FILE")
	if path == "" {
		path = "../../testdata/golden/video/cases.json"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 1000 {
		t.Fatalf("only %d golden cases", len(cases))
	}
	var urls, vtts, rubyRaised, tweets, yts int
	failures := 0
	fail := func(format string, args ...any) {
		if failures++; failures <= 40 {
			t.Errorf(format, args...)
		}
	}
	for _, c := range cases {
		switch c.Kind {
		case "url":
			urls++
			if c.RubyRaises != "" {
				// Ruby answers with a 500 here; Go treats the link as unsupported.
				rubyRaised++
				if _, err := video.NormalizeTwitterURL(c.Input); err == nil {
					fail("NormalizeTwitterURL(%q) accepted a URL on which Ruby raises %s", c.Input, c.RubyRaises)
				}
				if id := video.YoutubeVideoID(c.Input); id != "" {
					fail("YoutubeVideoID(%q) = %q on a URL on which Ruby raises %s", c.Input, id, c.RubyRaises)
				}
				continue
			}
			got, err := video.NormalizeTwitterURL(c.Input)
			if c.Twitter == nil {
				if err == nil {
					fail("NormalizeTwitterURL(%q) = %q, Ruby rejects it", c.Input, got)
				}
			} else {
				tweets++
				if err != nil || got != *c.Twitter {
					fail("NormalizeTwitterURL(%q) = %q, %v; Ruby: %q", c.Input, got, err, *c.Twitter)
				}
			}
			if id := video.YoutubeVideoID(c.Input); id != str(c.Youtube) {
				fail("YoutubeVideoID(%q) = %q; Ruby: %q", c.Input, id, str(c.Youtube))
			}
			if c.Youtube != nil {
				yts++
			}
		case "vtt":
			vtts++
			got := video.VttText(c.Input)
			if c.Error != nil {
				// Ruby raises (invalid UTF-8 after decoding a surrogate reference);
				// Go replaces the bad bytes instead and must still return valid text.
				rubyRaised++
				if !utf8.ValidString(got) {
					fail("VttText(%q) is not valid UTF-8 (%q)", c.Input, got)
				}
			} else if got != str(c.Output) {
				fail("VttText(%q) = %q; Ruby: %q", c.Input, got, str(c.Output))
			}
		default:
			t.Fatalf("unknown kind %q", c.Kind)
		}
	}
	t.Logf("%d url cases (%d tweets, %d youtube), %d vtt cases (%d where Ruby raises, in total); %d mismatches", urls, tweets, yts, vtts, rubyRaised, failures)
}
