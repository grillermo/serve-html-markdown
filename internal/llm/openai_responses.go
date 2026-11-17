// Package llm talks to OpenAI's Responses API: one prompt in, the output text
// out. Errors never carry the API key or the upstream response body.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultModel is the model OpenAIResponses used unless told otherwise.
	DefaultModel = "gpt-6-luna"
	// DefaultEndpoint is OpenAI's Responses API.
	DefaultEndpoint = "https://api.openai.com/v1/responses"
	// DefaultTimeout is Net::HTTP's default open and read timeout (60 s), which
	// the Ruby client used. Here it bounds the whole call, body included.
	DefaultTimeout = 60 * time.Second
	// maxResponseBytes caps how much of a response is read.
	maxResponseBytes = 8 << 20
)

// FormattingFailed is the one message the formatter's errors carry and the
// web layer shows (as the 502 detail); it never includes the upstream's words.
const FormattingFailed = "OpenAI formatting failed."

// Messages of Error, as the Ruby client raised them.
const (
	msgRequestFailed = "OpenAI request failed."
	msgUnparseable   = "OpenAI returned an unparseable response."
	msgNoOutput      = "OpenAI returned no output."
	msgNotCompleted  = "OpenAI request could not be completed."
	msgNotConfigured = "OPENAI_API_KEY is not configured."
)

// Error is a failed OpenAI call. Message is generic and safe to show or log; it
// never holds the upstream body or the API key. Cause (when set) is the
// underlying transport error, reachable with errors.Is/As but not printed by
// Error.
type Error struct {
	Message string
	cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

// ErrOpenAI makes an Error with msg.
func ErrOpenAI(msg string) error { return &Error{Message: msg} }

// ErrConfiguration is returned when no API key is configured.
var ErrConfiguration error = configurationError{}

type configurationError struct{}

func (configurationError) Error() string { return msgNotConfigured }

// APIKey is OPENAI_API_KEY, falling back to EXPANSION_LLM_API_KEY when the
// first is blank (Ruby's `.presence ||`).
func APIKey(openAI, expansion string) string {
	if strings.TrimSpace(openAI) != "" {
		return openAI
	}
	return expansion
}

// Client posts prompts to the Responses API. Endpoint and HTTPClient are
// fields so tests can point it at an httptest server.
type Client struct {
	APIKey     string
	Endpoint   string
	HTTPClient *http.Client
}

// NewClient returns a Client for the real endpoint with the 60 s timeout.
func NewClient(apiKey string) *Client {
	return &Client{APIKey: apiKey, Endpoint: DefaultEndpoint, HTTPClient: newHTTPClient()}
}

// newHTTPClient never follows redirects (Net::HTTP does not), so the key is
// never sent anywhere but the configured endpoint.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       DefaultTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Request is one Responses API call.
type Request struct {
	// Model defaults to DefaultModel.
	Model string
	Input string
	// ReasoningEffort, when set, is sent as reasoning.effort.
	ReasoningEffort string
	// TextFormat, when set, is sent as text.format (e.g. a json_schema).
	TextFormat any
}

func (r Request) body() ([]byte, error) {
	model := r.Model
	if model == "" {
		model = DefaultModel
	}
	body := map[string]any{"model": model, "input": r.Input}
	if r.ReasoningEffort != "" {
		body["reasoning"] = map[string]any{"effort": r.ReasoningEffort}
	}
	if r.TextFormat != nil {
		body["text"] = map[string]any{"format": r.TextFormat}
	}
	return json.Marshal(body)
}

// Generate posts req and returns the joined text of every output_text of every
// message item. It returns ErrConfiguration, before any request, when the key
// is blank, and an *Error for everything that goes wrong afterwards.
func (c *Client) Generate(ctx context.Context, req Request) (string, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return "", ErrConfiguration
	}
	payload, err := req.body()
	if err != nil {
		return "", &Error{Message: msgNotCompleted, cause: err}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", &Error{Message: msgNotCompleted}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	hc := c.HTTPClient
	if hc == nil {
		hc = newHTTPClient()
	}
	resp, err := hc.Do(httpReq)
	if err != nil {
		// A url.Error names the endpoint; keep it out of the message.
		return "", &Error{Message: msgNotCompleted, cause: errors.Unwrap(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return "", &Error{Message: msgRequestFailed}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", &Error{Message: msgNotCompleted, cause: err}
	}
	if len(body) > maxResponseBytes {
		return "", &Error{Message: msgUnparseable}
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil || parsed == nil {
		return "", &Error{Message: msgUnparseable}
	}
	text := outputText(parsed)
	if strings.TrimSpace(text) == "" {
		return "", &Error{Message: msgNoOutput}
	}
	return text, nil
}

// outputText joins every output_text of every message item, skipping anything
// else the way the Ruby select chain did.
func outputText(parsed map[string]any) string {
	var sb strings.Builder
	items, _ := parsed["output"].([]any)
	for _, item := range items {
		m, _ := item.(map[string]any)
		if m["type"] != "message" {
			continue
		}
		contents, _ := m["content"].([]any)
		for _, c := range contents {
			cm, _ := c.(map[string]any)
			if cm["type"] != "output_text" {
				continue
			}
			if s, ok := cm["text"].(string); ok {
				sb.WriteString(s)
			}
		}
	}
	return sb.String()
}
