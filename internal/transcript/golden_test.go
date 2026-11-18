package transcript_test

import (
	"encoding/json"
	"os"
	"testing"
	"unicode/utf8"

	"serve-html-markdown/internal/transcript"
)

// goldenCase is the part of an entry of testdata/golden/video/cases.json (made
// by script/golden_video.rb from the real Ruby VttText) this package uses.
type goldenCase struct {
	Kind   string  `json:"kind"`
	Input  string  `json:"input"`
	Output *string `json:"output"`
	Error  *string `json:"error"`
}

// TestGoldenRuby replays the "vtt" cases of the corpus (WebVTT with tags,
// entities, odd whitespace and repeats) against Ruby's own answers.
// VIDEO_GOLDEN_FILE points it at a different corpus.
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
	vtts, rubyRaised, failures := 0, 0, 0
	for _, c := range cases {
		if c.Kind != "vtt" {
			continue
		}
		vtts++
		got := transcript.VttText(c.Input)
		var msg string
		if c.Error != nil {
			// Ruby raises (invalid UTF-8 after decoding a surrogate reference);
			// Go replaces the bad bytes instead and must still return valid text.
			rubyRaised++
			if !utf8.ValidString(got) {
				msg = "is not valid UTF-8"
			}
		} else if c.Output == nil || got != *c.Output {
			msg = "differs from Ruby's answer"
		}
		if msg != "" {
			if failures++; failures <= 40 {
				t.Errorf("VttText(%q) = %q %s", c.Input, got, msg)
			}
		}
	}
	if vtts < 100 {
		t.Fatalf("only %d vtt golden cases", vtts)
	}
	t.Logf("%d vtt cases (%d where Ruby raises); %d mismatches", vtts, rubyRaised, failures)
}
