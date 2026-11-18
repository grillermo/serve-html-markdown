package video

import (
	"net/http"
	"time"
)

// DefaultHTTPTimeout is Net::HTTP's default open and read timeout (60 s), which
// the Ruby Slack and Rulinky clients used. Here it bounds the whole call.
const DefaultHTTPTimeout = 60 * time.Second

// maxResponseBytes caps how much of any response these clients read.
const maxResponseBytes = 1 << 20

// newHTTPClient never follows redirects (Net::HTTP does not), so a credential
// (a webhook path, a bearer token) is never sent anywhere but where it was
// configured.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       DefaultHTTPTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
