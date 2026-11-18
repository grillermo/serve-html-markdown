package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Messages of RulinkyError (Ruby: RulinkyClient).
const (
	msgRulinkyNotConfigured = "RULINKY_HOST and RULINKY_API_TOKEN must be configured."
	msgRulinkyFailed        = "rulinky link creation failed."
	msgRulinkyUnreachable   = "rulinky request could not be completed."
)

// RulinkyError is a failed Rulinky call. Classify it with errors.Is:
// ErrRulinkyConfiguration for missing or invalid settings, ErrRulinky for any
// RulinkyError (a refused, unusable or unreachable Rulinky), and
// context.Canceled or context.DeadlineExceeded when the caller's ctx ended the
// call (the cause is kept); errors.As(err, &*RulinkyError) reaches the message. Message is generic and safe to show or
// log: it never holds the upstream body, the host or the token. Cause (when
// set) is the underlying transport error, reachable with errors.Is/As but not
// printed by Error.
type RulinkyError struct {
	Message string
	// Config marks the not-configured error (Ruby's ConfigurationError, a
	// subclass of Error).
	Config bool
	cause  error
}

func (e *RulinkyError) Error() string { return e.Message }
func (e *RulinkyError) Unwrap() error { return e.cause }

// Is lets errors.Is(err, ErrRulinky) match every RulinkyError and
// errors.Is(err, ErrRulinkyConfiguration) only the configuration ones.
func (e *RulinkyError) Is(target error) bool {
	t, ok := target.(*RulinkyError)
	if !ok {
		return false
	}
	return !t.Config || e.Config
}

// Sentinels for errors.Is. They are shared values: never return or modify them
// as a real error.
var (
	ErrRulinky              = &RulinkyError{Message: msgRulinkyFailed}
	ErrRulinkyConfiguration = &RulinkyError{Message: msgRulinkyNotConfigured, Config: true}
)

// RulinkyClient pushes a link to Rulinky (the user's link inbox) with its API.
type RulinkyClient struct {
	// HTTPClient defaults to a client with Net::HTTP's 60 s timeout that
	// follows no redirects, so the bearer token never leaves the configured host.
	HTTPClient *http.Client

	endpoint string
	token    string
}

// NewRulinkyClient returns a client for the Rulinky at host (RULINKY_HOST, an
// http(s) URL) with the API token. A blank host or token is
// ErrRulinkyConfiguration with Ruby's message; so is a host that is not an
// http(s) URL without credentials, which Ruby only discovered at the first
// request. As with URI.join(host, "/api/links"), only scheme and host:port of
// the host are used.
func NewRulinkyClient(host, token string) (*RulinkyClient, error) {
	if strings.TrimSpace(host) == "" || strings.TrimSpace(token) == "" {
		return nil, &RulinkyError{Message: msgRulinkyNotConfigured, Config: true}
	}
	u, err := url.Parse(strings.TrimSpace(host))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, &RulinkyError{Message: "RULINKY_HOST must be an http(s) URL.", Config: true}
	}
	endpoint := (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/api/links"}).String()
	return &RulinkyClient{HTTPClient: newHTTPClient(), endpoint: endpoint, token: token}, nil
}

// CreateLink posts link with note and returns the id Rulinky gave it, as a
// string (a JSON number is kept as written). An answer without an id gives ""
// and no error, as Ruby gave nil. Any non-2xx answer, or one that is not a JSON
// object with a string, number or null id, is a RulinkyError with the generic
// message "rulinky link creation failed."; a transport failure is one with
// "rulinky request could not be completed." (errors.Is still sees a cancelled
// context). The token is only ever sent in the Authorization header.
func (c *RulinkyClient) CreateLink(ctx context.Context, link, note string) (string, error) {
	payload, err := json.Marshal(map[string]string{"link": link, "note": note})
	if err != nil {
		return "", &RulinkyError{Message: msgRulinkyFailed}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", &RulinkyError{Message: msgRulinkyFailed}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	hc := c.HTTPClient
	if hc == nil {
		hc = newHTTPClient()
	}
	resp, err := hc.Do(req)
	if err != nil {
		// A url.Error names the endpoint; keep it out of the message.
		return "", &RulinkyError{Message: msgRulinkyUnreachable, cause: errors.Unwrap(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return "", &RulinkyError{Message: msgRulinkyFailed}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", &RulinkyError{Message: msgRulinkyUnreachable, cause: err}
	}
	if len(body) > maxResponseBytes {
		return "", &RulinkyError{Message: msgRulinkyFailed}
	}
	return parseLinkID(body)
}

func parseLinkID(body []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var parsed map[string]any
	if err := dec.Decode(&parsed); err != nil || parsed == nil || dec.More() {
		return "", &RulinkyError{Message: msgRulinkyFailed}
	}
	switch id := parsed["id"].(type) {
	case nil:
		return "", nil
	case string:
		return id, nil
	case json.Number:
		return id.String(), nil
	}
	return "", &RulinkyError{Message: msgRulinkyFailed}
}
