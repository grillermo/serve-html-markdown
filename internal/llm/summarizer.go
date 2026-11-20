package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"serve-html-markdown/internal/transcript"
)

// SummaryPrompt is prepended to the transcript. It is byte for byte PROMPT of
// app/services/openai_summarizer.rb (see summarizer_test.go).
const SummaryPrompt = "You are a professional writer. You are given the transcript of a video, " +
	"taken from its captions. Do not summarize it: write it up as a full blog article about the " +
	"video, covering everything the speaker says, in the speaker's order, keeping " +
	"their arguments, examples, numbers and memorable quotes. Turn the spoken words " +
	"into polished, well-structured prose with an engaging introduction, clear " +
	"sections and a closing paragraph. Ignore filler words, false starts and " +
	"caption errors. Write in the same language as the transcript and " +
	"do not invent facts that are not in it. Return JSON with exactly " +
	"two keys: \"title\" (the article's headline, plain text, no markdown) and " +
	"\"summary_html\" (the article body as a clean HTML fragment, using <p>, <h2>, " +
	"<h3>, <ul>, <li>, <blockquote>, <strong>, <em> as needed, no <html>/<body> " +
	"wrapper and no <h1>). Transcript:\n\n"

// Messages of the summarizer's own errors (Ruby: OpenaiSummarizer).
const (
	// SummarizationFailed is the message of every upstream failure.
	SummarizationFailed = "sota-llms summarization failed."
	msgIncomplete       = "sota-llms returned incomplete summary."
	msgUnparseableJSON  = "sota-llms returned unparseable summary."
)

// Summary is an article written from a transcript: a plain-text headline and
// the body as an HTML fragment.
type Summary struct {
	Title       string
	SummaryHTML string
}

// Summarizer turns a video's captions into an article with the sota-llms model.
type Summarizer struct {
	Client *Client
}

// NewSummarizer returns a Summarizer that calls client.
func NewSummarizer(client *Client) *Summarizer { return &Summarizer{Client: client} }

// fences are the Markdown code fence the model sometimes wraps its JSON in:
// ```json at the very start, ``` at the very end (Ruby's gsub of
// /\A```(?:json)?\s*|\s*```\z/). \s here is Ruby's: space, \t \n \v \f \r.
var (
	leadingFence  = regexp.MustCompile("\\A```(?:json)?[ \\t\\n\\v\\f\\r]*")
	trailingFence = regexp.MustCompile("[ \\t\\n\\v\\f\\r]*```\\z")
)

// Summarize writes captions (a WebVTT file or plain text; WebVTT is reduced to
// its spoken words first) up as an article. A missing API key is
// ErrConfiguration. Every upstream failure is an *Error with the generic
// message "sota-llms summarization failed." (the cause, such as context.Canceled,
// stays reachable with errors.Is), a model reply that is not JSON is
// "sota-llms returned unparseable summary.", and one without a non-blank title
// and summary_html is "sota-llms returned incomplete summary.". No message ever
// holds the upstream's words or the model's text.
func (s *Summarizer) Summarize(ctx context.Context, captions string) (Summary, error) {
	text, err := s.Client.Generate(ctx, Request{
		Model: DefaultModel,
		Input: SummaryPrompt + transcript.VttText(captions),
	})
	if err != nil {
		if errors.Is(err, ErrConfiguration) {
			return Summary{}, ErrConfiguration
		}
		return Summary{}, &Error{Message: SummarizationFailed, cause: err}
	}
	return parseSummary(text)
}

// parseSummary reads the model's JSON. Anything but an object, or a title or
// summary_html that is not a non-blank string, is incomplete (the Ruby raised
// TypeError or coerced with to_s; Structured Outputs makes both unreachable).
func parseSummary(text string) (Summary, error) {
	j := rubyStrip(trailingFence.ReplaceAllString(leadingFence.ReplaceAllString(text, ""), ""))
	var data any
	if err := json.Unmarshal([]byte(j), &data); err != nil {
		if json.Unmarshal(escapeControlsInStrings(j), &data) != nil {
			return Summary{}, &Error{Message: msgUnparseableJSON}
		}
	}
	obj, _ := data.(map[string]any)
	title, _ := obj["title"].(string)
	html, _ := obj["summary_html"].(string)
	title, html = rubyStrip(title), rubyStrip(html)
	if title == "" || html == "" {
		return Summary{}, &Error{Message: msgIncomplete}
	}
	return Summary{Title: title, SummaryHTML: html}, nil
}

// escapeControlsInStrings rewrites the raw control characters (U+0000–U+001F)
// found inside JSON strings as escapes, leaving the rest of j alone. Gemini
// sometimes pretty-prints summary_html with literal newlines and indentation
// inside the string, which JSON forbids; this reads them as the \n and \t it
// meant (Python's json.loads(strict=False)). Whitespace between tokens stays
// as is, since it sits outside any string.
func escapeControlsInStrings(j string) []byte {
	out := make([]byte, 0, len(j)+64)
	inString, escaped := false, false
	for i := 0; i < len(j); i++ {
		c := j[i]
		switch {
		case !inString:
			inString = c == '"'
		case escaped:
			escaped = false
		case c == '\\':
			escaped = true
		case c == '"':
			inString = false
		case c < 0x20:
			switch c {
			case '\n':
				out = append(out, `\n`...)
			case '\r':
				out = append(out, `\r`...)
			case '\t':
				out = append(out, `\t`...)
			default:
				out = fmt.Appendf(out, `\u%04x`, c)
			}
			continue
		}
		out = append(out, c)
	}
	return out
}
