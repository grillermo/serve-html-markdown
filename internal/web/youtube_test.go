package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"serve-html-markdown/internal/config"
	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/jobs"
	"serve-html-markdown/internal/store"
	"serve-html-markdown/internal/video"
)

// Ports of test/controllers/youtube_authorizations_controller_test.rb ("Ruby:
// ..." names the test), plus the cookie-state cases the Rails session gave for free.

// fakeAuthorizer stands in for Google.
type fakeAuthorizer struct {
	token        string
	exchangeErr  error
	consentErr   error
	state, code  string
	exchangeCall int
}

func (f *fakeAuthorizer) ConsentURL(state string) (string, error) {
	f.state = state
	if f.consentErr != nil {
		return "", f.consentErr
	}
	return "https://accounts.google.com/o/oauth2/auth?state=" + state, nil
}

func (f *fakeAuthorizer) Exchange(_ context.Context, code string) (string, error) {
	f.exchangeCall++
	f.code = code
	return f.token, f.exchangeErr
}

type ytEnv struct {
	*authEnv
	enq  *fakeEnqueuer
	auth *fakeAuthorizer
	h    map[string]string
}

// ytSetup varies newYtEnv: real uses the pool-backed enqueuer, realAuth the
// authorizer built from the configuration instead of the fake, cfg edits the config.
type ytSetup struct {
	real, realAuth bool
	cfg            func(*config.Config)
}

func newYtEnv(t *testing.T, setup ytSetup) *ytEnv {
	t.Helper()
	enq := &fakeEnqueuer{}
	fa := &fakeAuthorizer{token: "1//new-token"}
	env := &authEnv{now: new(time.Time)}
	*env.now = fixedNow
	cfg := testConfig(t)
	cfg.YoutubeClientID, cfg.YoutubeClientSecret, cfg.YoutubeHost = "client-id", "client-secret-value", "reader.example"
	cfg.AppEnv, cfg.Root = "test", "/srv/app"
	if setup.cfg != nil {
		setup.cfg(&cfg)
	}
	opts := []Option{WithClock(func() time.Time { return *env.now })}
	if !setup.realAuth {
		opts = append(opts, WithYoutubeAuthorizer(fa))
	}
	if !setup.real {
		opts = append(opts, WithEnqueuer(enq))
	}
	env.srv = New(cfg, testdb.New(t), opts...)
	env.user = env.addUser(t, viewerEmail, viewerPassword)
	return &ytEnv{authEnv: env, enq: enq, auth: fa, h: map[string]string{"Cookie": env.cookieFor(env.user)}}
}

func (e *ytEnv) get(path string, cookies ...string) *httptest.ResponseRecorder {
	cookie := e.h["Cookie"]
	for _, c := range cookies {
		cookie += "; " + c
	}
	return do(e.srv, "GET", path, map[string]string{"Cookie": cookie})
}

var stateRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// oauthCookieOf is the _shm_oauth cookie a response sets.
func oauthCookieOf(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == oauthCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", oauthCookieName, rec.Header()["Set-Cookie"])
	return nil
}

// reauth runs GET /youtube/reauth and returns the state and the cookie header.
func (e *ytEnv) reauth(t *testing.T, query string) (state, cookie string) {
	t.Helper()
	rec := e.get("/youtube/reauth" + query)
	if rec.Code != 302 {
		t.Fatalf("reauth: got %d %s", rec.Code, rec.Body)
	}
	c := oauthCookieOf(t, rec)
	return e.auth.state, c.Name + "=" + c.Value
}

func (e *ytEnv) token(t *testing.T) string {
	t.Helper()
	tok, err := store.YoutubeRefreshToken(context.Background(), e.srv.pool, "")
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (e *ytEnv) video(t *testing.T, status string, youtubeID *string) *store.TwitterVideo {
	t.Helper()
	v, err := store.CreateTwitterVideo(context.Background(), e.srv.pool, store.NewTwitterVideo{SourceURL: "https://x.com/foo/status/9", Status: status})
	if err != nil {
		t.Fatal(err)
	}
	if youtubeID != nil {
		if err := store.UpdateTwitterVideo(context.Background(), e.srv.pool, v.ID, store.TwitterVideoChanges{YoutubeID: youtubeID, ErrorDetail: new("boom")}); err != nil {
			t.Fatal(err)
		}
	}
	return v
}

func (e *ytEnv) wantJobs(t *testing.T, want ...enqueued) {
	t.Helper()
	e.enq.mu.Lock()
	defer e.enq.mu.Unlock()
	if len(e.enq.got) != len(want) {
		t.Fatalf("enqueued %+v, want %+v", e.enq.got, want)
	}
	for i := range want {
		if e.enq.got[i] != want[i] {
			t.Fatalf("enqueued %+v, want %+v", e.enq.got, want)
		}
	}
}

func wantCleared(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	c := oauthCookieOf(t, rec)
	if c.Value != "" || c.MaxAge >= 0 || c.Path != "/youtube" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("the state cookie was not cleared: %+v", c)
	}
}

// Ruby: "requires a signed-in user".
func TestYoutubeRoutesRequireSignIn(t *testing.T) {
	e := newYtEnv(t, ytSetup{})
	for _, p := range []string{"/youtube/reauth", "/youtube/callback?code=x&state=y", "/youtube/debug"} {
		rec := do(e.srv, "GET", p, nil)
		if rec.Code != 302 || !strings.HasPrefix(rec.Header().Get("Location"), "/users/sign_in") {
			t.Errorf("%s: got %d %q", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	if e.auth.exchangeCall != 0 || e.auth.state != "" {
		t.Error("Google was contacted for an anonymous visitor")
	}
}

// Ruby: "redirects to google with a state parameter".
func TestYoutubeReauthRedirectsToGoogleWithState(t *testing.T) {
	e := newYtEnv(t, ytSetup{})
	v := e.video(t, "failed", nil)
	rec := e.get("/youtube/reauth?video_id=" + itoa(v.ID))
	if rec.Code != 302 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if !stateRE.MatchString(e.auth.state) {
		t.Fatalf("state %q is not 16 random bytes in hex", e.auth.state)
	}
	if got, want := rec.Header().Get("Location"), "https://accounts.google.com/o/oauth2/auth?state="+e.auth.state; got != want {
		t.Errorf("Location %q, want %q", got, want)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
	c := oauthCookieOf(t, rec)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/youtube" || c.MaxAge != 600 {
		t.Errorf("cookie attributes: %+v", c)
	}
	var saved oauthState
	if !e.srv.oauthCodec.Open(c.Value, &saved) || saved.State != e.auth.state || saved.VideoID != itoa(v.ID) {
		t.Errorf("cookie payload %+v", saved)
	}
	// The cookie is signed under its own purpose: the session codec does not open it.
	if e.srv.codec.Open(c.Value, &saved) {
		t.Error("the session codec opened the oauth cookie")
	}
	// Each visit starts a new state.
	first := e.auth.state
	e.get("/youtube/reauth")
	if e.auth.state == first {
		t.Error("state repeated")
	}
}

func TestYoutubeReauthKeepsOnlyADigitVideoID(t *testing.T) {
	e := newYtEnv(t, ytSetup{})
	for _, q := range []string{"?video_id=abc", "?video_id=1%3Cscript%3E", "?video_id=-5", "?video_id=1234567890123456789", "?video_id=", ""} {
		rec := e.get("/youtube/reauth" + q)
		var saved oauthState
		if rec.Code != 302 || !e.srv.oauthCodec.Open(oauthCookieOf(t, rec).Value, &saved) || saved.VideoID != "" {
			t.Errorf("%q: %d %+v", q, rec.Code, saved)
		}
	}
}

// With the real authorizer the consent URL is built from configuration, never
// from the request's Host.
func TestYoutubeReauthRealAuthorizerUsesConfiguredRedirect(t *testing.T) {
	e := newYtEnv(t, ytSetup{realAuth: true})
	req := httptest.NewRequest("GET", "http://evil.example/youtube/reauth", nil)
	req.Header.Set("Cookie", e.h["Cookie"])
	req.Header.Set("X-Forwarded-Host", "evil.example")
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != 302 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || loc.Host != "accounts.google.com" {
		t.Fatalf("Location %q: %v", rec.Header().Get("Location"), err)
	}
	q := loc.Query()
	if q.Get("redirect_uri") != "https://reader.example/youtube/callback" || q.Get("client_id") != "client-id" ||
		q.Get("prompt") != "consent" || q.Get("access_type") != "offline" || !stateRE.MatchString(q.Get("state")) {
		t.Errorf("query %v", q)
	}
	var saved oauthState
	if !e.srv.oauthCodec.Open(oauthCookieOf(t, rec).Value, &saved) || saved.State != q.Get("state") {
		t.Errorf("cookie state %+v vs %s", saved, q.Get("state"))
	}
}

// A missing client id or secret is a 500 page that says which, and no cookie is set.
func TestYoutubeReauthConfigurationError(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*config.Config)
		want string
	}{
		{"no client id", func(c *config.Config) { c.YoutubeClientID = "" }, "YOUTUBE_CLIENT_ID is not set."},
		{"no secret", func(c *config.Config) { c.YoutubeClientSecret = "" }, "YOUTUBE_CLIENT_SECRET is not set."},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newYtEnv(t, ytSetup{realAuth: true, cfg: c.edit})
			rec := e.get("/youtube/reauth")
			if rec.Code != 500 || !strings.Contains(rec.Body.String(), c.want) || strings.Contains(rec.Body.String(), "client-secret-value") {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
			if len(rec.Result().Cookies()) != 0 || rec.Header().Get("Location") != "" {
				t.Errorf("cookie or redirect on a configuration error")
			}
			if !strings.Contains(rec.Body.String(), `href="/youtube/reauth"`) || rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("problem page: %s / %q", rec.Body, rec.Header().Get("Cache-Control"))
			}
		})
	}
}

// Ruby: "stores the token and re-queues the stalled video".
func TestYoutubeCallbackStoresTokenAndRequeuesStalledVideo(t *testing.T) {
	e := newYtEnv(t, ytSetup{})
	v := e.video(t, "failed", nil)
	state, cookie := e.reauth(t, "?video_id="+itoa(v.ID))

	rec := e.get("/youtube/callback?code=auth-code&state="+state, cookie)
	if rec.Code != 200 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if e.auth.code != "auth-code" || e.auth.exchangeCall != 1 {
		t.Errorf("exchanged %q %d times", e.auth.code, e.auth.exchangeCall)
	}
	if got := e.token(t); got != "1//new-token" {
		t.Errorf("stored token %q", got)
	}
	e.wantJobs(t, ingest(v.ID))
	if body := rec.Body.String(); !strings.Contains(body, "YouTube re-authorized") || !strings.Contains(body, "Re-queued twitter video #"+itoa(v.ID)) {
		t.Errorf("page: %s", body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
	wantCleared(t, rec)
	// The failed row was not written (ingest leases it through updated_at).
	after, _ := store.FindTwitterVideo(context.Background(), e.srv.pool, v.ID)
	if !after.UpdatedAt.Equal(v.UpdatedAt) || after.Status != "failed" {
		t.Errorf("row written before its job: %+v", after)
	}
}

// The retry contract applies to the callback too: a failed row that has a
// YouTube id goes back to awaiting captions, one with a finished run is left alone.
func TestYoutubeCallbackRetryContract(t *testing.T) {
	id := new("KpILHX2D-AA")
	for _, c := range []struct {
		name, status string
		youtube      *string
		want         func(int64) []enqueued
		requeued     bool
	}{
		{"failed with id", "failed", id, func(i int64) []enqueued { return []enqueued{captions(i)} }, true},
		{"awaiting captions", "awaiting_captions", id, func(i int64) []enqueued { return []enqueued{captions(i)} }, true},
		{"downloading", "downloading", nil, func(i int64) []enqueued { return []enqueued{ingest(i)} }, true},
		{"done", "done", id, func(int64) []enqueued { return nil }, false},
		{"publishing", "publishing", id, func(int64) []enqueued { return nil }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newYtEnv(t, ytSetup{})
			v := e.video(t, c.status, c.youtube)
			state, cookie := e.reauth(t, "?video_id="+itoa(v.ID))
			rec := e.get("/youtube/callback?code=c&state="+state, cookie)
			if rec.Code != 200 {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
			e.wantJobs(t, c.want(v.ID)...)
			body := rec.Body.String()
			if c.requeued != strings.Contains(body, "Re-queued twitter video #"+itoa(v.ID)) ||
				c.requeued == strings.Contains(body, "No video was waiting") {
				t.Errorf("page: %s", body)
			}
			if got := e.token(t); got != "1//new-token" {
				t.Errorf("token %q", got)
			}
		})
	}
}

// Ruby: "stores the token even when no video was waiting".
func TestYoutubeCallbackWithoutVideo(t *testing.T) {
	e := newYtEnv(t, ytSetup{})
	state, cookie := e.reauth(t, "")
	rec := e.get("/youtube/callback?code=auth-code&state="+state, cookie)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "No video was waiting on this") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	e.wantJobs(t)
	if e.token(t) != "1//new-token" {
		t.Error("token not stored")
	}
	// A video id that matches no row queues nothing either.
	state, cookie = e.reauth(t, "?video_id=424242")
	rec = e.get("/youtube/callback?code=auth-code&state="+state, cookie)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "No video was waiting on this") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	e.wantJobs(t)
}

// The video to retry comes from the signed cookie, never from the callback's query.
func TestYoutubeCallbackIgnoresVideoIDInTheQuery(t *testing.T) {
	e := newYtEnv(t, ytSetup{})
	v := e.video(t, "failed", nil)
	state, cookie := e.reauth(t, "")
	rec := e.get("/youtube/callback?code=c&video_id="+itoa(v.ID)+"&state="+state, cookie)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	e.wantJobs(t)
}

// Ruby: "rejects a mismatched state without storing anything".
func TestYoutubeCallbackRejectsStateMismatch(t *testing.T) {
	e := newYtEnv(t, ytSetup{})
	v := e.video(t, "failed", nil)
	state, cookie := e.reauth(t, "?video_id="+itoa(v.ID))

	for name, query := range map[string]string{
		"forged":      "code=auth-code&state=forged",
		"missing":     "code=auth-code",
		"empty":       "code=auth-code&state=",
		"prefix":      "code=auth-code&state=" + state[:31],
		"longer":      "code=auth-code&state=" + state + "0",
		"upper-cased": "code=auth-code&state=" + strings.ToUpper(state),
	} {
		rec := e.get("/youtube/callback?"+query, cookie)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "Authorization state did not match. Start again from the Slack link.") {
			t.Errorf("%s: got %d %s", name, rec.Code, rec.Body)
		}
		wantCleared(t, rec)
	}
	if e.token(t) != "" || e.auth.exchangeCall != 0 {
		t.Error("something was stored or exchanged")
	}
	e.wantJobs(t)
}

// A callback with no usable cookie cannot match any state: missing, tampered,
// expired, from another purpose, or of another shape.
func TestYoutubeCallbackRejectsAnUnusableCookie(t *testing.T) {
	e := newYtEnv(t, ytSetup{})
	state, cookie := e.reauth(t, "")
	value := strings.TrimPrefix(cookie, oauthCookieName+"=")
	session := e.srv.codec
	wrongPurpose, _ := session.Seal(oauthState{State: state}, e.now.Add(time.Minute))
	otherShape, _ := e.srv.oauthCodec.Seal(map[string]string{"state": state}, e.now.Add(time.Minute))
	blankState, _ := e.srv.oauthCodec.Seal(oauthState{}, e.now.Add(time.Minute))

	cases := map[string]string{
		"no cookie":     "",
		"tampered":      oauthCookieName + "=" + value[:len(value)-2] + "xx",
		"garbage":       oauthCookieName + "=garbage",
		"wrong purpose": oauthCookieName + "=" + wrongPurpose,
		"other shape":   oauthCookieName + "=" + otherShape,
		"blank state":   oauthCookieName + "=" + blankState,
	}
	for name, c := range cases {
		var rec *httptest.ResponseRecorder
		if c == "" {
			rec = e.get("/youtube/callback?code=c&state=" + state)
		} else {
			rec = e.get("/youtube/callback?code=c&state="+state, c)
		}
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "state did not match") {
			t.Errorf("%s: got %d %s", name, rec.Code, rec.Body)
		}
	}
	// And even a blank state in the query does not match a blank saved one.
	if rec := e.get("/youtube/callback?code=c", oauthCookieName+"="+blankState); rec.Code != 400 {
		t.Errorf("blank = blank: got %d", rec.Code)
	}
	// Expired: ten minutes pass at Google's consent page.
	*e.now = e.now.Add(oauthLifetime + time.Second)
	e.h["Cookie"] = e.cookieFor(e.user)
	if rec := e.get("/youtube/callback?code=c&state="+state, cookie); rec.Code != 400 || !strings.Contains(rec.Body.String(), "state did not match") {
		t.Errorf("expired: got %d %s", rec.Code, rec.Body)
	}
	if e.token(t) != "" || e.auth.exchangeCall != 0 {
		t.Error("something was stored or exchanged")
	}
}

// Just before it expires the cookie still works.
func TestYoutubeCallbackCookieLivesTenMinutes(t *testing.T) {
	e := newYtEnv(t, ytSetup{})
	state, cookie := e.reauth(t, "")
	*e.now = e.now.Add(oauthLifetime - time.Second)
	e.h["Cookie"] = e.cookieFor(e.user)
	if rec := e.get("/youtube/callback?code=c&state="+state, cookie); rec.Code != 200 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

// Ruby: "reports a denied consent without storing anything".
func TestYoutubeCallbackReportsDeniedConsent(t *testing.T) {
	e := newYtEnv(t, ytSetup{})
	state, cookie := e.reauth(t, "")
	rec := e.get("/youtube/callback?error=access_denied&state="+state, cookie)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "Google denied the request: access_denied.") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if e.token(t) != "" || e.auth.exchangeCall != 0 {
		t.Error("something was stored or exchanged")
	}
	wantCleared(t, rec)

	// Anything that is not a short code is not echoed at all.
	for _, bad := range []string{"<script>alert(1)</script>", "Access Denied", strings.Repeat("a", 41)} {
		rec = e.get("/youtube/callback?error="+url.QueryEscape(bad)+"&state="+state, cookie)
		if rec.Code != 400 || strings.Contains(rec.Body.String(), "<script>") || strings.Contains(rec.Body.String(), "Access Denied") ||
			!strings.Contains(rec.Body.String(), "Google denied the request.") {
			t.Errorf("%q: got %d %s", bad, rec.Code, rec.Body)
		}
	}
}

// Ruby: "reports a configuration error during exchange as an internal server
// error, not a bad gateway"; an error of Google's is a 502.
func TestYoutubeCallbackExchangeErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		code int
		want string
	}{
		{"configuration", &video.AuthorizationError{Message: "YOUTUBE_CLIENT_SECRET is not set.", Config: true}, 500, "YOUTUBE_CLIENT_SECRET is not set."},
		{"google", video.ErrAuthorization, 502, "Google rejected the authorization code."},
		{"no refresh token", &video.AuthorizationError{Message: "Google returned no refresh token for this code."}, 502, "Google returned no refresh token for this code."},
		{"unexpected", errors.New("dial tcp 1.2.3.4: secret internals"), 500, "The authorization could not be completed."},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newYtEnv(t, ytSetup{})
			v := e.video(t, "failed", nil)
			state, cookie := e.reauth(t, "?video_id="+itoa(v.ID))
			e.auth.exchangeErr = c.err
			rec := e.get("/youtube/callback?code=c&state="+state, cookie)
			if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.want) || strings.Contains(rec.Body.String(), "secret internals") {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
			if e.token(t) != "" {
				t.Error("a token was stored")
			}
			e.wantJobs(t)
			wantCleared(t, rec)
		})
	}
}

// The token is stored even when queueing the video fails afterwards; the page
// says so and the visitor can start again from the Slack link.
func TestYoutubeCallbackEnqueueFailure(t *testing.T) {
	e := newYtEnv(t, ytSetup{})
	v := e.video(t, "failed", nil)
	state, cookie := e.reauth(t, "?video_id="+itoa(v.ID))
	e.enq.err = errors.New("queue down: secret internals")
	rec := e.get("/youtube/callback?code=c&state="+state, cookie)
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "refresh token is stored") || strings.Contains(rec.Body.String(), "secret internals") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if e.token(t) != "1//new-token" {
		t.Error("token not stored")
	}
	wantCleared(t, rec)
}

// One case with the real queue: the first callback queues ingest, a second one
// while it is still queued does not queue another.
func TestYoutubeCallbackWithTheRealQueueDoesNotDuplicate(t *testing.T) {
	e := newYtEnv(t, ytSetup{real: true})
	v := e.video(t, "failed", nil)
	for i := 0; i < 2; i++ {
		state, cookie := e.reauth(t, "?video_id="+itoa(v.ID))
		if rec := e.get("/youtube/callback?code=c&state="+state, cookie); rec.Code != 200 {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
	}
	var n int
	if err := e.srv.pool.QueryRow(context.Background(), `SELECT count(*) FROM go_jobs WHERE kind = $1 AND args @> $2::jsonb`,
		jobs.KindTwitterVideoIngest, `{"id": `+itoa(v.ID)+`}`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d ingest jobs (%v), want 1", n, err)
	}
	if ok, err := store.ClaimTwitterVideoForIngest(context.Background(), e.srv.pool, v.ID, 10*time.Minute); err != nil || !ok {
		t.Errorf("the ingest job cannot claim the row: %v %v", ok, err)
	}
}

// Ruby: "debug reports the computed oauth values to a signed-in user".
func TestYoutubeDebugReport(t *testing.T) {
	e := newYtEnv(t, ytSetup{cfg: func(c *config.Config) { c.YoutubeRedirectURI = " https://reader.example/cb " }})
	if rec := do(e.srv, "GET", "/youtube/debug", nil); rec.Code != 302 {
		t.Fatalf("anonymous: got %d", rec.Code)
	}
	rec := e.get("/youtube/debug")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("got %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
	}
	body := rec.Body.String()
	for _, want := range []string{
		"pid: ", "APP_ENV: test", "app root: /srv/app",
		"ENV['HOST']: \"reader.example\"", `ENV['YOUTUBE_REDIRECT_URI']: " https://reader.example/cb "`,
		`ENV['YOUTUBE_CLIENT_ID']: "client-id"`, "ENV['YOUTUBE_CLIENT_SECRET'] length: 19",
		"YoutubeAuthorization.redirect_uri", "YoutubeAuthorization.reauth_url", "redirect_uri Google receives", "consent_url: ",
		"request.base_url: https://example.com", "X-Forwarded-Proto: \"\"",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("report lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "client-secret-value") {
		t.Error("the client secret is in the report")
	}
}
