package video_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"serve-html-markdown/internal/video"
)

type rulinkyHit struct {
	method, path, rawQuery, auth, contentType string
	body                                      map[string]any
}

func rulinkyServer(t *testing.T, status int, response string) (*httptest.Server, *rulinkyHit) {
	t.Helper()
	hit := &rulinkyHit{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hit.method, hit.path, hit.rawQuery = r.Method, r.URL.Path, r.URL.RawQuery
		hit.auth, hit.contentType = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		_ = json.Unmarshal(b, &hit.body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return srv, hit
}

func rulinkyFor(t *testing.T, srv *httptest.Server) *video.RulinkyClient {
	t.Helper()
	c, err := video.NewRulinkyClient(srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTPClient = srv.Client()
	return c
}

// Ruby: "creates a link and returns the id".
func TestRulinkyCreatesALinkAndReturnsTheID(t *testing.T) {
	srv, hit := rulinkyServer(t, 201, `{"id":"uuid-1"}`)
	id, err := rulinkyFor(t, srv).CreateLink(context.Background(), "https://h/x.html", "My Title")
	if err != nil || id != "uuid-1" {
		t.Fatalf("id = %q, err = %v", id, err)
	}
	if hit.method != "POST" || hit.path != "/api/links" || hit.auth != "Bearer tok" || hit.contentType != "application/json" {
		t.Errorf("request = %+v", hit)
	}
	if hit.body["link"] != "https://h/x.html" || hit.body["note"] != "My Title" || len(hit.body) != 2 {
		t.Errorf("body = %v", hit.body)
	}
}

// The id column is a string: a JSON number is kept as written.
func TestRulinkyKeepsANumericIDAsAString(t *testing.T) {
	for response, want := range map[string]string{
		`{"id":42}`:                    "42",
		`{"id":12345678901234567890}`:  "12345678901234567890",
		`{"id":"7","other":{"a":[1]}}`: "7",
		`{"id":null}`:                  "", // Ruby returned nil and the job stored NULL
		`{}`:                           "",
	} {
		srv, _ := rulinkyServer(t, 200, response)
		id, err := rulinkyFor(t, srv).CreateLink(context.Background(), "l", "n")
		if err != nil || id != want {
			t.Errorf("%s: id = %q, err = %v, want %q", response, id, err, want)
		}
	}
}

// Ruby: "raises on non-2xx" - and the generic message never holds the body.
func TestRulinkyRaisesOnNon2xxWithoutLeakingTheBody(t *testing.T) {
	for _, status := range []int{199, 301, 401, 500} {
		srv, _ := rulinkyServer(t, status, "secret upstream")
		_, err := rulinkyFor(t, srv).CreateLink(context.Background(), "l", "n")
		var e *video.RulinkyError
		if !errors.As(err, &e) || !errors.Is(err, video.ErrRulinky) || errors.Is(err, video.ErrRulinkyConfiguration) {
			t.Fatalf("%d: err = %v", status, err)
		}
		if err.Error() != "rulinky link creation failed." {
			t.Errorf("%d: message = %q", status, err.Error())
		}
	}
}

func TestRulinkyUnusableAnswersAreGeneric(t *testing.T) {
	for name, response := range map[string]string{
		"not json":     "<html>secret upstream",
		"array":        `["secret upstream"]`,
		"object id":    `{"id":{"secret":"upstream"}}`,
		"bool id":      `{"id":true}`,
		"empty":        ``,
		"huge":         `{"id":"` + strings.Repeat("a", 2<<20) + `"}`,
		"trailing":     `{"id":"a"} secret upstream`,
		"nested array": `{"id":["secret upstream"]}`,
	} {
		srv, _ := rulinkyServer(t, 200, response)
		_, err := rulinkyFor(t, srv).CreateLink(context.Background(), "l", "n")
		if !errors.Is(err, video.ErrRulinky) || strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// Ruby: "rejects blank config" (blank? covers whitespace too).
func TestRulinkyRejectsBlankConfig(t *testing.T) {
	for _, c := range [][2]string{{"", ""}, {"https://r.test", ""}, {"", "tok"}, {" ", "tok"}, {"https://r.test", "\n"}} {
		client, err := video.NewRulinkyClient(c[0], c[1])
		if client != nil || !errors.Is(err, video.ErrRulinkyConfiguration) || !errors.Is(err, video.ErrRulinky) {
			t.Errorf("%q: client = %v, err = %v", c, client, err)
		}
		if err.Error() != "RULINKY_HOST and RULINKY_API_TOKEN must be configured." {
			t.Errorf("message = %q", err.Error())
		}
	}
}

// URI.join(host, "/api/links"): the path of the host is replaced, and a host
// that is not an http(s) URL is refused up front.
func TestRulinkyEndpointIsHostPlusAPILinks(t *testing.T) {
	srv, hit := rulinkyServer(t, 200, `{"id":"a"}`)
	c, err := video.NewRulinkyClient(srv.URL+"/some/path/?q=1#f", "tok")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTPClient = srv.Client()
	if _, err := c.CreateLink(context.Background(), "l", "n"); err != nil {
		t.Fatal(err)
	}
	if hit.path != "/api/links" || hit.rawQuery != "" {
		t.Errorf("request = %s?%s", hit.path, hit.rawQuery)
	}
	for _, bad := range []string{"rulinky.test", "ftp://r.test", "https://", "://x", "https://u:p@r.test"} {
		if _, err := video.NewRulinkyClient(bad, "tok"); !errors.Is(err, video.ErrRulinkyConfiguration) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

// The bearer token must not follow a redirect anywhere.
func TestRulinkyDoesNotFollowRedirectsWithTheToken(t *testing.T) {
	var leaked atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Add(1)
		}
		_, _ = io.WriteString(w, `{"id":"x"}`)
	}))
	t.Cleanup(elsewhere.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/api/links", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	c, _ := video.NewRulinkyClient(srv.URL, "tok")
	_, err := c.CreateLink(context.Background(), "l", "n")
	if !errors.Is(err, video.ErrRulinky) || leaked.Load() != 0 {
		t.Errorf("err = %v, leaked = %d", err, leaked.Load())
	}
}

func TestRulinkyTransportErrorsAreGenericAndKeepCancellation(t *testing.T) {
	srv, _ := rulinkyServer(t, 200, `{"id":"a"}`)
	c := rulinkyFor(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.CreateLink(ctx, "l", "n")
	if !errors.Is(err, video.ErrRulinky) || !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	srv.Close()
	_, err = c.CreateLink(context.Background(), "l", "n")
	if !errors.Is(err, video.ErrRulinky) || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "tok") {
		t.Errorf("err = %v", err)
	}
}

func TestRulinkyBadTokenCharactersNeverEchoTheToken(t *testing.T) {
	srv, _ := rulinkyServer(t, 200, `{"id":"a"}`)
	c, _ := video.NewRulinkyClient(srv.URL, "tok\nsecret-token")
	c.HTTPClient = srv.Client()
	_, err := c.CreateLink(context.Background(), "l", "n")
	if !errors.Is(err, video.ErrRulinky) || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("err = %v", err)
	}
}

func TestRulinkyDefaultClientHasTheNetHTTPTimeout(t *testing.T) {
	c, _ := video.NewRulinkyClient("https://r.test", "t")
	if c.HTTPClient.Timeout != video.DefaultHTTPTimeout {
		t.Errorf("timeout = %v", c.HTTPClient.Timeout)
	}
}
