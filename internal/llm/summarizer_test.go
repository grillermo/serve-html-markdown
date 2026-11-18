package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// envelope wraps model text in a Responses API body.
func envelope(t *testing.T, text string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"output": []any{map[string]any{
		"type": "message", "content": []any{map[string]any{"type": "output_text", "text": text}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func modelJSON(title, html string) string {
	b, _ := json.Marshal(map[string]string{"title": title, "summary_html": html})
	return string(b)
}

// Ruby: "summarizes transcript into title and html".
func TestSummarizerSummarizesTranscriptIntoTitleAndHTML(t *testing.T) {
	srv, got := fakeEndpoint(t, 200, envelope(t, modelJSON("The Big Idea", "<p>Short summary.</p>")))
	res, err := NewSummarizer(clientFor(srv, "k")).Summarize(context.Background(), "full transcript")
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "The Big Idea" || res.SummaryHTML != "<p>Short summary.</p>" {
		t.Errorf("result = %+v", res)
	}
	if got.path != "/v1/responses" {
		t.Errorf("path = %q", got.path)
	}
	if got.body["model"] != "gpt-6-luna" {
		t.Errorf("model = %v", got.body["model"])
	}
	if in, _ := got.body["input"].(string); in != SummaryPrompt+"full transcript" {
		t.Errorf("input = %q", in)
	}
	format := got.body["text"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["strict"] != true || format["name"] != "article" {
		t.Errorf("text.format = %v", format)
	}
	if _, ok := got.body["reasoning"]; ok {
		t.Error("the Ruby summarizer sends no reasoning")
	}
	// The exact bytes of the text format, as Ruby's to_json writes them.
	if !strings.Contains(got.rawBody, `"text":{"format":`+SummaryTextFormat+`}`) {
		t.Errorf("raw body does not carry SummaryTextFormat verbatim: %s", got.rawBody)
	}
}

// The Ruby summarizer reduces a WebVTT file to its words (VttText) before the prompt.
func TestSummarizerReducesVTTToItsSpokenText(t *testing.T) {
	srv, got := fakeEndpoint(t, 200, envelope(t, modelJSON("T", "<p>x</p>")))
	vtt := "WEBVTT\n\n1\n00:00:00.000 --> 00:00:02.000\nhello <00:00:01.000><c>world</c>\n\n2\n00:00:02.000 --> 00:00:03.000\nhello world\n"
	if _, err := NewSummarizer(clientFor(srv, "k")).Summarize(context.Background(), vtt); err != nil {
		t.Fatal(err)
	}
	if in, _ := got.body["input"].(string); in != SummaryPrompt+"hello world" {
		t.Errorf("input = %q", in)
	}
}

// Ruby: "raises generic error on upstream failure without leaking body" and
// "... on malformed envelope without leaking body".
func TestSummarizerUpstreamFailuresAreGeneric(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"rejected":  {500, "secret upstream"},
		"malformed": {200, "{secret upstream"},
		"no output": {200, `{"output":[]}`},
	} {
		srv, _ := fakeEndpoint(t, tc.status, tc.body)
		_, err := NewSummarizer(clientFor(srv, "k")).Summarize(context.Background(), "t")
		var e *Error
		if !errors.As(err, &e) || e.Message != SummarizationFailed {
			t.Errorf("%s: err = %v, want %q", name, err, SummarizationFailed)
			continue
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: error leaks the body: %v", name, err)
		}
	}
}

// Ruby: "rejects blank api key".
func TestSummarizerRejectsBlankAPIKey(t *testing.T) {
	srv, got := fakeEndpoint(t, 200, envelope(t, modelJSON("T", "<p>x</p>")))
	_, err := NewSummarizer(clientFor(srv, "")).Summarize(context.Background(), "t")
	if !errors.Is(err, ErrConfiguration) {
		t.Fatalf("err = %v", err)
	}
	if got.path != "" {
		t.Error("a request was made without a key")
	}
}

func TestSummarizerKeepsCancellationVisible(t *testing.T) {
	srv, _ := fakeEndpoint(t, 200, okResponse)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewSummarizer(clientFor(srv, "k")).Summarize(ctx, "x")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to unwrap to context.Canceled", err)
	}
}

// The model's JSON: fences stripped, title and summary_html required.
func TestSummarizerParsesTheModelJSON(t *testing.T) {
	good := modelJSON("  T  ", "  <p>x</p>\n")
	for name, tc := range map[string]struct {
		text      string
		title     string
		html      string
		wantError string
	}{
		"plain":      {text: good, title: "T", html: "<p>x</p>"},
		"json fence": {text: "```json\n" + good + "\n```", title: "T", html: "<p>x</p>"},
		"bare fence": {text: "```\n" + good + "\n```", title: "T", html: "<p>x</p>"},
		// \z is the very end, so Ruby leaves a fence followed by a newline in place.
		"fence then newline": {text: "```json\n" + good + "\n```\n", wantError: "OpenAI returned unparseable summary."},
		"fence no newline":   {text: "```json" + good + "```", title: "T", html: "<p>x</p>"},
		"missing title":      {text: `{"summary_html":"<p>x</p>"}`, wantError: "OpenAI returned incomplete summary."},
		"blank title":        {text: modelJSON("  ", "<p>x</p>"), wantError: "OpenAI returned incomplete summary."},
		"blank html":         {text: modelJSON("T", " \n"), wantError: "OpenAI returned incomplete summary."},
		"null html":          {text: `{"title":"T","summary_html":null}`, wantError: "OpenAI returned incomplete summary."},
		"not an object":      {text: `["a"]`, wantError: "OpenAI returned incomplete summary."},
		"not json":           {text: "Sure! here is the article", wantError: "OpenAI returned unparseable summary."},
		"truncated":          {text: `{"title":"T","summary_html":"<p>x`, wantError: "OpenAI returned unparseable summary."},
		"only a fence":       {text: "```json\n```", wantError: "OpenAI returned unparseable summary."},
	} {
		srv, _ := fakeEndpoint(t, 200, envelope(t, tc.text))
		res, err := NewSummarizer(clientFor(srv, "k")).Summarize(context.Background(), "t")
		if tc.wantError != "" {
			var e *Error
			if !errors.As(err, &e) || e.Message != tc.wantError {
				t.Errorf("%s: err = %v, want %q", name, err, tc.wantError)
			}
			continue
		}
		if err != nil || res.Title != tc.title || res.SummaryHTML != tc.html {
			t.Errorf("%s: res = %+v, err = %v", name, res, err)
		}
	}
}

// --- the prompt and the text format, byte for byte ---

const (
	goldenSummaryPromptSHA256 = "f54e56921a2fe7e0ecfa2911025d4c926ac91990fc652947317bd6d953623201"
	goldenSummaryFormatSHA256 = "b792892c6f750d819fbb3a5fe26bccc3dc159984a14e0e9472d7e7536ba7295f"
)

func TestSummaryPromptAndFormatGolden(t *testing.T) {
	if got := sha(SummaryPrompt); got != goldenSummaryPromptSHA256 {
		t.Errorf("SummaryPrompt sha256 = %s, want %s", got, goldenSummaryPromptSHA256)
	}
	if got := sha(SummaryTextFormat); got != goldenSummaryFormatSHA256 {
		t.Errorf("SummaryTextFormat sha256 = %s, want %s", got, goldenSummaryFormatSHA256)
	}
}

const summarizerRuby = "openai_summarizer.rb"

func summarizerSource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "app", "services", summarizerRuby))
	if os.IsNotExist(err) {
		t.Skip("the Ruby source is gone; TestSummaryPromptAndFormatGolden still guards the text")
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(src)
}

// TestSummaryPromptMatchesRubySource reads PROMPT's quoted pieces from the Ruby
// source and decodes their escapes (only \n and \" appear), with no interpreter.
func TestSummaryPromptMatchesRubySource(t *testing.T) {
	src := summarizerSource(t)
	start := strings.Index(src, "PROMPT = (")
	if start < 0 {
		t.Fatal("PROMPT not found")
	}
	body := src[start+len("PROMPT = ("):]
	body = body[:strings.Index(body, "\n  )")]
	pieces := regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`).FindAllStringSubmatch(body, -1)
	if len(pieces) == 0 {
		t.Fatal("no string pieces found")
	}
	var want strings.Builder
	for _, p := range pieces {
		s, err := strconv.Unquote(`"` + p[1] + `"`)
		if err != nil {
			t.Fatal(err)
		}
		want.WriteString(s)
	}
	if SummaryPrompt != want.String() {
		t.Errorf("SummaryPrompt differs from the Ruby source:\n go:   %q\n ruby: %q", SummaryPrompt, want.String())
	}
}

// TestSummaryConstantsMatchRubyInterpreter loads the Ruby class (its constants
// need no Rails) and compares PROMPT and TEXT_FORMAT.to_json with Go's. It
// skips only when there is no ruby binary at all.
func TestSummaryConstantsMatchRubyInterpreter(t *testing.T) {
	summarizerSource(t)
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("no ruby binary on PATH; the source comparison and the golden hashes still check the constants")
	}
	const script = `
require "json"
module OpenaiResponses; end
load ARGV[0]
$stdout.binmode
print OpenaiSummarizer::PROMPT, "\0", OpenaiSummarizer::TEXT_FORMAT.to_json
`
	path := filepath.Join("..", "..", "app", "services", summarizerRuby)
	run := func(env ...string) ([]byte, error) {
		cmd := exec.Command(ruby, "-e", script, path)
		cmd.Env = append(os.Environ(), env...)
		return cmd.Output()
	}
	out, err := run()
	if err != nil {
		if v, rerr := os.ReadFile(filepath.Join("..", "..", ".ruby-version")); rerr == nil {
			out, err = run("RBENV_VERSION=" + strings.TrimSpace(string(v)))
		}
	}
	if err != nil {
		var stderr []byte
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = ee.Stderr
		}
		t.Fatalf("ruby (%s) failed: %v\n%s", ruby, err, stderr)
	}
	prompt, format, ok := strings.Cut(string(out), "\x00")
	if !ok {
		t.Fatalf("unexpected ruby output %q", out)
	}
	if prompt != SummaryPrompt {
		t.Errorf("SummaryPrompt differs from Ruby's PROMPT:\n go:   %q\n ruby: %q", SummaryPrompt, prompt)
	}
	if format != SummaryTextFormat {
		t.Errorf("SummaryTextFormat differs from Ruby's TEXT_FORMAT.to_json:\n go:   %s\n ruby: %s", SummaryTextFormat, format)
	}
}
