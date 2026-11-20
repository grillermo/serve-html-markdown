package llm

import (
	"context"
	"errors"
)

// FormatPrompt is prepended to the content to format. It is byte for byte
// FORMAT_PROMPT of app/services/openai_formatter.rb (see formatter_test.go).
const FormatPrompt = "format the text for markdown, leave the content intact just add line breaks " +
	"and spaces when needed, your response should be the formatted markdown content " +
	"and nothing else, no remarks or preambules, no adding of titles or subtitles that weren't there originally \n\n"

// Formatter turns raw text into Markdown with the sota-llms model.
type Formatter struct {
	Client *Client
}

// NewFormatter returns a Formatter that calls client.
func NewFormatter(client *Client) *Formatter { return &Formatter{Client: client} }

// Format returns content formatted as Markdown. A missing API key is
// ErrConfiguration; every other failure is an *Error with the generic message
// "sota-llms formatting failed." (the cause, such as context.Canceled, stays
// reachable with errors.Is).
func (f *Formatter) Format(ctx context.Context, content string) (string, error) {
	out, err := f.Client.Generate(ctx, Request{Model: DefaultModel, Input: FormatPrompt + content})
	if err == nil {
		return out, nil
	}
	if errors.Is(err, ErrConfiguration) {
		return "", ErrConfiguration
	}
	return "", &Error{Message: FormattingFailed, cause: err}
}
