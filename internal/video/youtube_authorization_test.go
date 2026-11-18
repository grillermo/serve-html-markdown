package video_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"serve-html-markdown/internal/video"
)

const testRedirectURI = "https://serve.chiq.me/youtube/callback"

func newAuth(t *testing.T) *video.YoutubeAuthorization {
	t.Helper()
	a, err := video.NewYoutubeAuthorization("cid", "sec", testRedirectURI)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Ruby: "consent url asks for offline access and a fresh consent".
func TestConsentURLAsksForOfflineAccessAndAFreshConsent(t *testing.T) {
	raw, err := newAuth(t).ConsentURL("st4te")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != "https://accounts.google.com/o/oauth2/v2/auth" {
		t.Errorf("base = %q", got)
	}
	q := u.Query()
	for key, want := range map[string]string{
		"access_type":   "offline",
		"prompt":        "consent",
		"state":         "st4te",
		"response_type": "code",
		"client_id":     "cid",
		"redirect_uri":  testRedirectURI,
		"scope":         "https://www.googleapis.com/auth/youtube.upload",
	} {
		if got := q.Get(key); got != want || len(q[key]) != 1 {
			t.Errorf("%s = %q (%d values), want %q", key, got, len(q[key]), want)
		}
	}
	if strings.Contains(raw, "sec") && strings.Contains(raw, "client_secret") {
		t.Errorf("the consent URL carries the client secret: %s", raw)
	}
}

func TestConsentURLNeedsAState(t *testing.T) {
	for _, state := range []string{"", "  "} {
		if raw, err := newAuth(t).ConsentURL(state); err == nil || raw != "" || !errors.Is(err, video.ErrAuthorization) {
			t.Errorf("state %q: %q, %v", state, raw, err)
		}
	}
}

// tokenServer answers the token endpoint and remembers what it was asked.
type tokenServer struct {
	*httptest.Server
	form   url.Values
	header http.Header
	calls  atomic.Int32
}

func newTokenServer(t *testing.T, status int, body string) *tokenServer {
	t.Helper()
	ts := &tokenServer{}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		ts.form, _ = url.ParseQuery(string(b))
		ts.header = r.Header.Clone()
		if r.Method != http.MethodPost || r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func authAt(t *testing.T, ts *tokenServer) *video.YoutubeAuthorization {
	t.Helper()
	a := newAuth(t)
	a.TokenURL = ts.URL + "/token"
	a.HTTPClient = ts.Client()
	return a
}

// Ruby: "exchange! hands google the code and returns the refresh token".
func TestExchangeHandsGoogleTheCodeAndReturnsTheRefreshToken(t *testing.T) {
	ts := newTokenServer(t, 200, `{"access_token":"at","token_type":"Bearer","expires_in":3600,"refresh_token":"1//refresh"}`)
	got, err := authAt(t, ts).Exchange(context.Background(), "abc")
	if err != nil || got != "1//refresh" {
		t.Fatalf("got %q, %v", got, err)
	}
	for key, want := range map[string]string{
		"grant_type": "authorization_code", "code": "abc", "redirect_uri": testRedirectURI,
		"client_id": "cid", "client_secret": "sec",
	} {
		if ts.form.Get(key) != want {
			t.Errorf("form %s = %q, want %q", key, ts.form.Get(key), want)
		}
	}
	if ts.calls.Load() != 1 {
		t.Errorf("token endpoint called %d times", ts.calls.Load())
	}
	if ts.header.Get("Authorization") != "" {
		t.Error("credentials went in a header; they belong in the body")
	}
}

// Ruby: "exchange! raises when google sends no refresh token".
func TestExchangeRaisesWhenGoogleSendsNoRefreshToken(t *testing.T) {
	for _, body := range []string{
		`{"access_token":"at","token_type":"Bearer","expires_in":3600}`,
		`{"access_token":"at","token_type":"Bearer","expires_in":3600,"refresh_token":"  "}`,
	} {
		ts := newTokenServer(t, 200, body)
		_, err := authAt(t, ts).Exchange(context.Background(), "abc")
		if !errors.Is(err, video.ErrAuthorization) || errors.Is(err, video.ErrAuthorizationConfiguration) {
			t.Fatalf("err = %v", err)
		}
		if err.Error() != "Google returned no refresh token for this code." {
			t.Errorf("message = %q", err.Error())
		}
	}
}

// The code is the one thing the user's browser handed over: a blank one never
// reaches Google, and nothing Google says is repeated back except its error code.
func TestExchangeFailuresAreSafeToShow(t *testing.T) {
	ts := newTokenServer(t, 200, `{}`)
	if _, err := authAt(t, ts).Exchange(context.Background(), " "); !errors.Is(err, video.ErrAuthorization) || ts.calls.Load() != 0 {
		t.Errorf("blank code: err = %v, calls = %d", err, ts.calls.Load())
	}
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"invalid grant": {400, `{"error":"invalid_grant","error_description":"Bad Request secret-detail"}`, "Google rejected the authorization code: invalid_grant."},
		"odd code":      {400, `{"error":"<script>secret-detail</script>"}`, "Google rejected the authorization code."},
		"html 502":      {502, `<html>secret-detail</html>`, "Google rejected the authorization code."},
		"garbage 200":   {200, `secret-detail`, "Google rejected the authorization code."},
	} {
		ts := newTokenServer(t, tc.status, tc.body)
		_, err := authAt(t, ts).Exchange(context.Background(), "abc")
		if !errors.Is(err, video.ErrAuthorization) || err.Error() != tc.want || strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	dead := newTokenServer(t, 200, `{}`)
	a := authAt(t, dead)
	dead.Close()
	_, err := a.Exchange(context.Background(), "abc")
	if !errors.Is(err, video.ErrAuthorization) || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "sec") {
		t.Errorf("transport: err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := authAt(t, ts).Exchange(ctx, "abc"); !errors.Is(err, context.Canceled) || !errors.Is(err, video.ErrAuthorization) {
		t.Errorf("cancelled: err = %v", err)
	}
}

func TestExchangeDoesNotFollowRedirects(t *testing.T) {
	var hit atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		_, _ = io.WriteString(w, `{"access_token":"x","refresh_token":"stolen"}`)
	}))
	t.Cleanup(other.Close)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redir.Close)
	a := newAuth(t)
	a.TokenURL = redir.URL + "/token"
	if _, err := a.Exchange(context.Background(), "abc"); !errors.Is(err, video.ErrAuthorization) || hit.Load() != 0 {
		t.Errorf("err = %v, hits = %d", err, hit.Load())
	}
}

// Ruby: "missing client credentials name the absent variable".
func TestMissingClientCredentialsNameTheAbsentVariable(t *testing.T) {
	for _, tc := range []struct{ id, secret, want string }{
		{"", "sec", "YOUTUBE_CLIENT_ID is not set."},
		{"  ", "sec", "YOUTUBE_CLIENT_ID is not set."},
		{"cid", "", "YOUTUBE_CLIENT_SECRET is not set."},
		{"", "", "YOUTUBE_CLIENT_ID is not set."},
	} {
		a, err := video.NewYoutubeAuthorization(tc.id, tc.secret, testRedirectURI)
		if a != nil || !errors.Is(err, video.ErrAuthorizationConfiguration) || !errors.Is(err, video.ErrAuthorization) || err.Error() != tc.want {
			t.Errorf("%q/%q: %v, %v", tc.id, tc.secret, a, err)
		}
	}
}

// Ruby: "reauth_url appends the video id when given".
func TestReauthURLAppendsTheVideoID(t *testing.T) {
	const base = "https://serve.chiq.me/youtube/reauth"
	if got := video.YoutubeReauthURL(base, "ignored.example", ""); got != base {
		t.Errorf("got %q", got)
	}
	if got := video.YoutubeReauthURL(base, "ignored.example", "7"); got != base+"?video_id=7" {
		t.Errorf("got %q", got)
	}
	if got := video.YoutubeReauthURL(base, "", "a b&c"); got != base+"?video_id=a+b%26c" {
		t.Errorf("a video id is query-escaped: %q", got)
	}
}

// Ruby: "reauth_url falls back to the default when YOUTUBE_REAUTH_URL is present but blank".
func TestReauthURLFallsBackToTheDefaultWhenBlank(t *testing.T) {
	for _, override := range []string{"", "  "} {
		if got := video.YoutubeReauthURL(override, "serve.chiq.me", ""); got != "https://serve.chiq.me/youtube/reauth" {
			t.Errorf("%q: got %q", override, got)
		}
		if got := video.YoutubeReauthURL(override, "serve.chiq.me", "7"); got != "https://serve.chiq.me/youtube/reauth?video_id=7" {
			t.Errorf("%q: got %q", override, got)
		}
	}
	if got := video.YoutubeReauthURL("", "", ""); got != "https://localhost:8009/youtube/reauth" {
		t.Errorf("no host: got %q", got)
	}
}

// Ruby: "redirect_uri falls back to the default when YOUTUBE_REDIRECT_URI is present but blank"
// - and the override wins as it is, never rewritten.
func TestRedirectURIIsTheOverrideOrDerivedFromHost(t *testing.T) {
	for _, override := range []string{"", " \t"} {
		if got := video.YoutubeRedirectURI(override, "serve.chiq.me"); got != "https://serve.chiq.me/youtube/callback" {
			t.Errorf("%q: got %q", override, got)
		}
	}
	if got := video.YoutubeRedirectURI("http://localhost:3000/cb", "serve.chiq.me"); got != "http://localhost:3000/cb" {
		t.Errorf("override: got %q", got)
	}
	if got := video.YoutubeRedirectURI("", ""); got != "https://localhost:8009/youtube/callback" {
		t.Errorf("no host: got %q", got)
	}
}

func TestDebugReportShowsWhatGoogleWouldReceiveWithoutSecrets(t *testing.T) {
	s := video.YoutubeSettings{
		Host: "serve.chiq.me", RedirectURI: "", ReauthURL: " ", ClientID: "cid.apps.example", ClientSecret: "super-secret-value",
	}
	report := s.DebugReport()
	for _, want := range []string{
		`ENV['HOST']: "serve.chiq.me"`,
		`ENV['YOUTUBE_REDIRECT_URI']: ""`,
		`ENV['YOUTUBE_REAUTH_URL']: " "`,
		`ENV['YOUTUBE_CLIENT_ID']: "cid.apps.example"`,
		"ENV['YOUTUBE_CLIENT_SECRET'] length: 18",
		`YoutubeAuthorization.redirect_uri: "https://serve.chiq.me/youtube/callback"`,
		`YoutubeAuthorization.reauth_url: "https://serve.chiq.me/youtube/reauth"`,
		`redirect_uri Google receives: "https://serve.chiq.me/youtube/callback"`,
		"consent_url: https://accounts.google.com/o/oauth2/v2/auth?",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "super-secret-value") {
		t.Errorf("the report prints the client secret:\n%s", report)
	}
	// Unconfigured: it says why instead of failing.
	bad := video.YoutubeSettings{Host: "h"}.DebugReport()
	if !strings.Contains(bad, "YOUTUBE_CLIENT_ID is not set.") || !strings.Contains(bad, "length: 0") {
		t.Errorf("report:\n%s", bad)
	}
}

func TestRedirectURIAndStateAreNeverRequestDerived(t *testing.T) {
	// The authorization has no way to take a request: the redirect URI is fixed
	// at construction and shows up unchanged in every consent URL.
	a := newAuth(t)
	if a.RedirectURI() != testRedirectURI {
		t.Errorf("RedirectURI() = %q", a.RedirectURI())
	}
	raw, _ := a.ConsentURL("s")
	if !strings.Contains(raw, url.QueryEscape(testRedirectURI)) {
		t.Errorf("consent URL: %s", raw)
	}
}

func TestNewAuthorizationRejectsABlankOrInvalidRedirectURI(t *testing.T) {
	for _, uri := range []string{"", "  ", "serve.chiq.me/youtube/callback", "/youtube/callback", "ftp://serve.chiq.me/cb",
		"https://", "javascript:alert(1)", " https://serve.chiq.me/cb", "https://serve.chiq.me/cb\n", "://x"} {
		a, err := video.NewYoutubeAuthorization("cid", "sec", uri)
		if a != nil || !errors.Is(err, video.ErrAuthorizationConfiguration) {
			t.Errorf("%q: %v, %v", uri, a, err)
		}
	}
	for _, uri := range []string{"https://serve.chiq.me/youtube/callback", "http://localhost:8009/youtube/callback"} {
		if _, err := video.NewYoutubeAuthorization("cid", "sec", uri); err != nil {
			t.Errorf("%q: %v", uri, err)
		}
	}
	// What YoutubeRedirectURI makes is always acceptable.
	if _, err := video.NewYoutubeAuthorization("cid", "sec", video.YoutubeRedirectURI("", "")); err != nil {
		t.Error(err)
	}
}
