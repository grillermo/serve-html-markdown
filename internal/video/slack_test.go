package video_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"serve-html-markdown/internal/video"
)

type slackHit struct {
	path, contentType, method string
	tls                       bool
	body                      map[string]any
}

func slackServer(t *testing.T, status int, tls bool) (*httptest.Server, *[]slackHit) {
	t.Helper()
	var hits []slackHit
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hit := slackHit{path: r.URL.Path, contentType: r.Header.Get("Content-Type"), method: r.Method, tls: r.TLS != nil}
		_ = json.Unmarshal(b, &hit.body)
		hits = append(hits, hit)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "ok")
	})
	var srv *httptest.Server
	if tls {
		srv = httptest.NewTLSServer(h)
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	return srv, &hits
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// Ruby: "posts success text to the success webhook" (and the failure twin).
func TestSlackPostsToTheMatchingWebhookOverHTTPS(t *testing.T) {
	srv, hits := slackServer(t, 200, true)
	n := video.NewSlackNotifier(srv.URL+"/S", srv.URL+"/F")
	n.HTTPClient = srv.Client()

	n.Success(context.Background(), "stage ok")
	n.Failure(context.Background(), "stage <bad> & \"worse\"")

	if len(*hits) != 2 {
		t.Fatalf("hits = %d", len(*hits))
	}
	first, second := (*hits)[0], (*hits)[1]
	if first.path != "/S" || second.path != "/F" {
		t.Errorf("paths = %q, %q", first.path, second.path)
	}
	if !first.tls || first.method != "POST" || first.contentType != "application/json" {
		t.Errorf("request = %+v", first)
	}
	if first.body["text"] != "stage ok" || len(first.body) != 1 {
		t.Errorf("body = %v", first.body)
	}
	if second.body["text"] != "stage <bad> & \"worse\"" {
		t.Errorf("body = %v", second.body)
	}
}

// Ruby: "no-op when webhook url is blank" (blank? also covers whitespace).
func TestSlackBlankURLMakesNoRequest(t *testing.T) {
	srv, hits := slackServer(t, 200, false)
	for _, url := range []string{"", "  \n"} {
		n := video.NewSlackNotifier(url, url)
		n.HTTPClient = srv.Client()
		n.Success(context.Background(), "x")
		n.Failure(context.Background(), "x")
	}
	if len(*hits) != 0 {
		t.Errorf("hits = %d, want 0", len(*hits))
	}
	// Only the blank one is skipped.
	n := video.NewSlackNotifier("", srv.URL+"/F")
	n.Success(context.Background(), "x")
	n.Failure(context.Background(), "x")
	if len(*hits) != 1 || (*hits)[0].path != "/F" {
		t.Errorf("hits = %+v", *hits)
	}
}

// Ruby: "swallows connection errors", and the log never names the webhook URL
// (it is a secret) nor the text of the error.
func TestSlackSwallowsErrorsAndNeverLogsTheURL(t *testing.T) {
	logs := captureLogs(t)
	srv, _ := slackServer(t, 200, false)
	secret := srv.URL + "/services/T000/B000/s3cr3t"
	srv.Close() // connection refused

	for _, url := range []string{secret, "http://[::1", "ftp://hooks.example/s3cr3t", "F"} {
		n := video.NewSlackNotifier(url, url)
		n.Success(context.Background(), "x")
		n.Failure(context.Background(), "x")
	}
	out := logs.String()
	if !strings.Contains(out, "[SlackNotifier] post failed") {
		t.Errorf("failure not logged: %q", out)
	}
	for _, leak := range []string{"s3cr3t", "127.0.0.1", "hooks.example", "T000"} {
		if strings.Contains(out, leak) {
			t.Errorf("log leaks %q: %s", leak, out)
		}
	}
}

func TestSlackSwallowsACancelledContext(t *testing.T) {
	srv, hits := slackServer(t, 200, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	video.NewSlackNotifier(srv.URL+"/S", "").Success(ctx, "x")
	if len(*hits) != 0 {
		t.Error("a cancelled context still posted")
	}
}

// Ruby ignores the status of the answer; so does this, without logging the body.
func TestSlackIgnoresNon2xxAnswers(t *testing.T) {
	logs := captureLogs(t)
	srv, hits := slackServer(t, 500, false)
	video.NewSlackNotifier(srv.URL+"/S", "").Success(context.Background(), "x")
	if len(*hits) != 1 {
		t.Fatalf("hits = %d", len(*hits))
	}
	if strings.Contains(logs.String(), "/S") || strings.Contains(logs.String(), srv.URL) {
		t.Errorf("log names the URL: %s", logs.String())
	}
}

// A redirect is never followed: the webhook URL is the only place the text goes.
func TestSlackDoesNotFollowRedirects(t *testing.T) {
	var other atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { other.Add(1) }))
	t.Cleanup(elsewhere.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	video.NewSlackNotifier(srv.URL+"/S", "").Success(context.Background(), "x")
	if other.Load() != 0 {
		t.Error("the redirect was followed")
	}
}

func TestSlackDefaultClientHasTheNetHTTPTimeout(t *testing.T) {
	n := video.NewSlackNotifier("a", "b")
	if n.HTTPClient.Timeout != video.DefaultHTTPTimeout || video.DefaultHTTPTimeout.Seconds() != 60 {
		t.Errorf("timeout = %v", n.HTTPClient.Timeout)
	}
}
