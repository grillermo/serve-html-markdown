// Package llm talks to sota-llms' chat-completions endpoint (it answers with
// the agy CLI): one prompt in, the reply text out. Errors never carry the API key or the upstream response body.
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
	// DefaultModel is the model used unless told otherwise: Gemini 3.8 Flash
	// (medium) through Antigravity, served by sota-llms.
	DefaultModel = "agy:gemini-3.8-flash-medium"
	// DefaultEndpoint is sota-llms' OpenAI-compatible chat completions.
	DefaultEndpoint = "https://sota-llms.chiq.me/v1/chat/completions"
	// DefaultTimeout is Net::HTTP's default open and read timeout (60 s), which
	// the Ruby client used. Here it bounds the whole call, body included.
	DefaultTimeout = 60 * time.Second
	// maxResponseBytes caps how much of a response is read.
	maxResponseBytes = 8 << 20
)

// FormattingFailed is the one message the formatter's errors carry and the
// web layer shows (as the 502 detail); it never includes the upstream's words.
const FormattingFailed = "sota-llms formatting failed."

// Messages of Error.
const (
	msgRequestFailed = "sota-llms request failed."
	msgUnparseable   = "sota-llms returned an unparseable response."
	msgNoOutput      = "sota-llms returned no output."
	msgNotCompleted  = "sota-llms request could not be completed."
	msgNotConfigured = "SOTA_LLMS_API_KEY is not configured."
)

// Error is a failed sota-llms call. Message is generic and safe to show or log; it
// never holds the upstream body or the API key. Cause (when set) is the
// underlying transport error, reachable with errors.Is/As but not printed by
// Error. Status (0 when there was no response) and the start of the response
// body are kept for the server log only (see Diagnostics).
type Error struct {
	Message string
	Status  int
	cause   error
	body    string
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

// ErrSotaLLMs makes an Error with msg.
func ErrSotaLLMs(msg string) error { return &Error{Message: msg} }

// ErrConfiguration is returned when SOTA_LLMS_API_KEY is blank.
var ErrConfiguration error = configurationError{}

type configurationError struct{}

func (configurationError) Error() string { return msgNotConfigured }

// Client posts prompts to the chat-completions endpoint. Endpoint and HTTPClient are
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

// Request is one chat-completions call.
type Request struct {
	// Model defaults to DefaultModel.
	Model string
	Input string
}

func (r Request) body() ([]byte, error) {
	model := r.Model
	if model == "" {
		model = DefaultModel
	}
	return json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": r.Input}},
	})
}

// Generate posts req and returns the content of the first choice's message. It returns ErrConfiguration, before any request, when the key
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return "", &Error{Message: msgRequestFailed, Status: resp.StatusCode, body: head(string(body), snippetBytes)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", &Error{Message: msgNotCompleted, Status: resp.StatusCode, cause: err}
	}
	if len(body) > maxResponseBytes {
		return "", &Error{Message: msgUnparseable, Status: resp.StatusCode, body: head(string(body), snippetBytes)}
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil || parsed == nil {
		return "", &Error{Message: msgUnparseable, Status: resp.StatusCode, cause: err, body: head(string(body), snippetBytes)}
	}
	text := outputText(parsed)
	if strings.TrimSpace(text) == "" {
		// The body says why there is no text (a finish_reason, an error object).
		return "", &Error{Message: msgNoOutput, Status: resp.StatusCode, body: head(string(body), snippetBytes)}
	}
	return text, nil
}

// outputText is choices[0].message.content, or "" when the body has no such string.
func outputText(parsed map[string]any) string {
	choices, _ := parsed["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	first, _ := choices[0].(map[string]any)
	msg, _ := first["message"].(map[string]any)
	text, _ := msg["content"].(string)
	return text
}
