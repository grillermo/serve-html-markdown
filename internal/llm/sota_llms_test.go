package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// capture is a fake Responses endpoint that records the request it got.
type capture struct {
	method, path, auth, contentType string
	body                            map[string]any
	rawBody                         string
}

func fakeEndpoint(t *testing.T, status int, response string) (*httptest.Server, *capture) {
	t.Helper()
	c := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.method, c.path = r.Method, r.URL.Path
		c.auth, c.contentType = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		c.rawBody = string(b)
		_ = json.Unmarshal(b, &c.body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func clientFor(srv *httptest.Server, key string) *Client {
	c := NewClient(key)
	c.Endpoint = srv.URL + "/v1/chat/completions"
	c.HTTPClient = srv.Client()
	return c
}

const okResponse = `{"choices":[{"index":0,"message":{"role":"assistant","content":"formatted markdown"},"finish_reason":"stop"}]}`

// Ruby: "formats content through the configured model" (request half).
func TestGeneratePostsModelAndInputWithBearerKey(t *testing.T) {
	srv, got := fakeEndpoint(t, 200, okResponse)
	text, err := clientFor(srv, "sota-llms-key").Generate(context.Background(), Request{Model: "agy:gemini-3.8-flash-medium", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if text != "formatted markdown" {
		t.Errorf("text = %q", text)
	}
	if got.method != "POST" || got.path != "/v1/chat/completions" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	if got.auth != "Bearer sota-llms-key" {
		t.Errorf("Authorization = %q", got.auth)
	}
	if got.contentType != "application/json" {
		t.Errorf("Content-Type = %q", got.contentType)
	}
	want := `{"messages":[{"content":"hello","role":"user"}],"model":"agy:gemini-3.8-flash-medium"}`
	b, _ := json.Marshal(got.body)
	if string(b) != want {
		t.Errorf("body = %s, want %s ", b, want)
	}
}

func TestGenerateDefaultsToTheFormatterModel(t *testing.T) {
	srv, got := fakeEndpoint(t, 200, okResponse)
	if _, err := clientFor(srv, "k").Generate(context.Background(), Request{Input: "x"}); err != nil {
		t.Fatal(err)
	}
	if got.body["model"] != "agy:gemini-3.8-flash-medium" {
		t.Errorf("model = %v", got.body["model"])
	}
}

func TestGenerateUsesTheFirstChoice(t *testing.T) {
	srv, _ := fakeEndpoint(t, 200, `{"choices":[{"message":{"content":"first"}},{"message":{"content":"second"}}]}`)
	text, err := clientFor(srv, "k").Generate(context.Background(), Request{Input: "x"})
	if err != nil || text != "first" {
		t.Errorf("text = %q, err = %v", text, err)
	}
}

// Ruby: "raises a generic error when sota-llms rejects the request".
func TestGenerateNon2xxIsAGenericErrorWithoutTheBody(t *testing.T) {
	const secret = "sensitive upstream details"
	for _, status := range []int{400, 401, 429, 500, 302} {
		srv, _ := fakeEndpoint(t, status, secret)
		_, err := clientFor(srv, "sk-secret-key").Generate(context.Background(), Request{Input: "x"})
		var e *Error
		if !errors.As(err, &e) || e.Message != "sota-llms request failed." {
			t.Fatalf("status %d: err = %v", status, err)
		}
		for _, leak := range []string{secret, "sk-secret-key"} {
			if strings.Contains(err.Error(), leak) || strings.Contains(e.Message, leak) {
				t.Errorf("status %d: error leaks %q: %v", status, leak, err)
			}
		}
	}
}

// Ruby: "raises a generic error when sota-llms returns a malformed body".
func TestGenerateUnparseableBody(t *testing.T) {
	for _, body := range []string{"not json sensitive", "", `[1,2]`, `{"choices":`} {
		srv, _ := fakeEndpoint(t, 200, body)
		_, err := clientFor(srv, "k").Generate(context.Background(), Request{Input: "x"})
		var e *Error
		if !errors.As(err, &e) || e.Message != "sota-llms returned an unparseable response." {
			t.Errorf("body %q: err = %v", body, err)
		}
		if strings.Contains(err.Error(), "sensitive") {
			t.Errorf("error leaks the body: %v", err)
		}
	}
}

func TestGenerateNoOutput(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"choices":null}`, `{"choices":[]}`, `{"choices":[{}]}`,
		`{"choices":[{"message":{"content":"  \n"}}]}`,
		`{"choices":[{"message":{"content":null}}]}`,
		`{"choices":[{"message":"oops"}]}`,
	} {
		srv, _ := fakeEndpoint(t, 200, body)
		_, err := clientFor(srv, "k").Generate(context.Background(), Request{Input: "x"})
		var e *Error
		if !errors.As(err, &e) || e.Message != "sota-llms returned no output." {
			t.Errorf("body %s: err = %v", body, err)
		}
	}
}

// Ruby: "rejects an empty API key before making a request".
func TestGenerateMissingKeyMakesNoRequest(t *testing.T) {
	srv, got := fakeEndpoint(t, 200, okResponse)
	for _, key := range []string{"", "  "} {
		_, err := clientFor(srv, key).Generate(context.Background(), Request{Input: "x"})
		if !errors.Is(err, ErrConfiguration) {
			t.Errorf("key %q: err = %v, want ErrConfiguration", key, err)
		}
		if err != nil && err.Error() != "SOTA_LLMS_API_KEY is not configured." {
			t.Errorf("message = %q", err.Error())
		}
	}
	if got.method != "" {
		t.Error("a request was made without a key")
	}
}

func TestGenerateTransportFailureIsGeneric(t *testing.T) {
	srv, _ := fakeEndpoint(t, 200, okResponse)
	c := clientFor(srv, "k")
	srv.Close()
	_, err := c.Generate(context.Background(), Request{Input: "x"})
	var e *Error
	if !errors.As(err, &e) || e.Message != "sota-llms request could not be completed." {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("error exposes the address: %v", err)
	}
}

func TestGenerateHonoursContextCancellation(t *testing.T) {
	arrived := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := clientFor(srv, "k").Generate(ctx, Request{Input: "x"})
		result <- err
	}()
	select {
	case <-arrived: // the request is in flight: cancel it now
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the server")
	}
	cancel()
	var err error
	select {
	case err = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("Generate did not return after the context was cancelled")
	}
	var e *Error
	if !errors.As(err, &e) || e.Message != "sota-llms request could not be completed." {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation should unwrap to context.Canceled: %v", err)
	}
}

func TestGenerateTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	c := clientFor(srv, "k")
	hc := *srv.Client()
	hc.Timeout = 100 * time.Millisecond
	c.HTTPClient = &hc
	_, err := c.Generate(context.Background(), Request{Input: "x"})
	var e *Error
	if !errors.As(err, &e) || e.Message != "sota-llms request could not be completed." {
		t.Fatalf("err = %v", err)
	}
}

func TestNewClientTimeoutMatchesRuby(t *testing.T) {
	// Net::HTTP's default open and read timeouts are 60 seconds.
	if got := NewClient("k").HTTPClient.Timeout; got != 60*time.Second {
		t.Errorf("timeout = %v, want 60s", got)
	}
	if got := NewClient("k").Endpoint; got != "https://sota-llms.chiq.me/v1/chat/completions" {
		t.Errorf("endpoint = %q", got)
	}
}

func TestGenerateCapsTheResponseSize(t *testing.T) {
	big := `{"choices":[{"message":{"content":"` + strings.Repeat("a", maxResponseBytes) + `"}}]}`
	srv, _ := fakeEndpoint(t, 200, big)
	_, err := clientFor(srv, "k").Generate(context.Background(), Request{Input: "x"})
	var e *Error
	if !errors.As(err, &e) || e.Message != "sota-llms returned an unparseable response." {
		t.Fatalf("err = %v", err)
	}
}
