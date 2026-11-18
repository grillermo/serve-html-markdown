package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// SlackNotifier posts one line of text to a Slack incoming webhook: one for
// good news, one for bad. It ports Ruby's SlackNotifier: a blank URL means
// "not configured" and sends nothing, and any failure is logged and swallowed,
// because a missing notification must never fail the job that sends it.
//
// The webhook URL is a secret (whoever has it can post to the channel), so it
// is never logged, and neither is the text of an error, which can quote it.
type SlackNotifier struct {
	SuccessURL string
	FailureURL string
	// HTTPClient defaults to a client with Net::HTTP's 60 s timeout that
	// follows no redirects. Tests point it at an httptest server.
	HTTPClient *http.Client
}

// NewSlackNotifier returns a notifier for the two webhook URLs (either may be
// blank: SLACK_SUCCESS_WEBHOOK and SLACK_FAILURE_WEBHOOK).
func NewSlackNotifier(successURL, failureURL string) *SlackNotifier {
	return &SlackNotifier{SuccessURL: successURL, FailureURL: failureURL, HTTPClient: newHTTPClient()}
}

// Success posts text to the success webhook.
func (n *SlackNotifier) Success(ctx context.Context, text string) { n.post(ctx, n.SuccessURL, text) }

// Failure posts text to the failure webhook.
func (n *SlackNotifier) Failure(ctx context.Context, text string) { n.post(ctx, n.FailureURL, text) }

func (n *SlackNotifier) post(ctx context.Context, rawURL, text string) {
	if strings.TrimSpace(rawURL) == "" {
		return
	}
	if kind := n.send(ctx, rawURL, text); kind != "" {
		slog.Error("[SlackNotifier] post failed", "kind", kind)
	}
}

// send makes the request and returns a short, secret-free name for what went
// wrong, or "" on success.
func (n *SlackNotifier) send(ctx context.Context, rawURL, text string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "invalid_url"
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "unsupported_url"
	}
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return "encode"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return "request"
	}
	req.Header.Set("Content-Type", "application/json")
	hc := n.HTTPClient
	if hc == nil {
		hc = newHTTPClient()
	}
	resp, err := hc.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return "canceled"
		case errors.Is(err, context.DeadlineExceeded):
			return "timeout"
		}
		return "transport"
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	return ""
}
