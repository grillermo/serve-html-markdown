package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/store"
)

const (
	viewerEmail    = "viewer@example.com"
	viewerPassword = "s3cretpass"
	fourteenDays   = 14 * 24 * time.Hour
)

var fixedNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

// authEnv is a server on the test database with one user (viewer@example.com)
// and a clock the test can move.
type authEnv struct {
	srv  *Server
	user *store.User
	now  *time.Time
}

func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	env := &authEnv{now: new(time.Time)}
	*env.now = fixedNow
	env.srv = New(testConfig(t), testdb.New(t), WithClock(func() time.Time { return *env.now }))
	env.user = env.addUser(t, viewerEmail, viewerPassword)
	return env
}

// addUser stores a user with a minimum-cost bcrypt hash (auth.HashPassword
// would spend about a second at cost 12).
func (e *authEnv) addUser(t *testing.T, email, password string) *store.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u, err := store.UpsertUser(context.Background(), e.srv.pool, email, string(hash))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// cookieFor is a valid session cookie header value for the user, as sign-in issues it.
func (e *authEnv) cookieFor(u *store.User) string {
	v := e.srv.codec.EncodeSession(auth.Session{
		UserID: u.ID, Salt: auth.SaltToken(u.EncryptedPassword), ExpiresAt: e.now.Add(fourteenDays),
	})
	return sessionCookieName + "=" + v
}

func postForm(srv http.Handler, method, path string, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func signInForm(email, password string) url.Values {
	return url.Values{"user[email]": {email}, "user[password]": {password}}
}

// setCookie returns the response's session cookie, or nil.
func setCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	return nil
}

// --- test/controllers/sessions_test.rb: "renders the sign-in form" ---

func TestSignInFormRenders(t *testing.T) {
	srv := newTestServerNoDB(t)
	rec := do(srv, "GET", "/users/sign_in", nil)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type %q", got)
	}
	body := rec.Body.String()
	for _, re := range []string{
		`<main class="signin">`,
		`<form[^>]*action="/users/sign_in"[^>]*method="post"`,
		`<input[^>]*required[^>]*name="user\[email\]"`,
		`<input[^>]*required[^>]*name="user\[password\]"`,
		`<input[^>]*type="checkbox"[^>]*name="user\[remember_me\]"`,
		`<input[^>]*type="submit"[^>]*value="Sign in"`,
		`<title>Sign in</title>`,
		`<link rel="stylesheet" href="/assets/application.css">`,
	} {
		if !regexp.MustCompile(re).MatchString(body) {
			t.Errorf("body lacks %s\n%s", re, body)
		}
	}
	if strings.Contains(body, "signin-alert") {
		t.Error("alert shown on a plain visit")
	}
}

func TestSignInFormKeepsReturnToInHiddenField(t *testing.T) {
	srv := newTestServerNoDB(t)
	rec := do(srv, "GET", "/users/sign_in?return_to=%2Findex%3Fq%3D%22x", nil)
	body := rec.Body.String()
	if !strings.Contains(body, `name="return_to" value="/index?q=&#34;x"`) {
		t.Errorf("hidden return_to missing or unescaped:\n%s", body)
	}
	if !strings.Contains(body, "You need to sign in or sign up before continuing.") {
		t.Error("guard redirect should show Devise's alert")
	}
	// An unsafe return_to is dropped.
	rec = do(srv, "GET", "/users/sign_in?return_to=https://evil.test/", nil)
	if strings.Contains(rec.Body.String(), "evil.test") {
		t.Error("unsafe return_to echoed")
	}
}

// --- guard ---

func TestGuardRedirectsHTMLToSignIn(t *testing.T) {
	srv := newTestServerNoDB(t)
	rec := do(srv, "GET", "/index", nil)
	if rec.Code != 302 || rec.Header().Get("Location") != "/users/sign_in?return_to=%2Findex" {
		t.Errorf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = do(srv, "GET", "/anything.md?a=b&c=d", nil)
	if rec.Header().Get("Location") != "/users/sign_in?return_to=%2Fanything.md%3Fa%3Db%26c%3Dd" {
		t.Errorf("query not preserved: %q", rec.Header().Get("Location"))
	}
	// A browser Accept header is still HTML.
	rec = do(srv, "GET", "/index", map[string]string{"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"})
	if rec.Code != 302 {
		t.Errorf("browser accept: got %d", rec.Code)
	}
	rec = do(srv, "GET", "/index", map[string]string{"Accept": "application/json, text/plain, */*"})
	if rec.Code != 302 {
		t.Errorf("browser-like json accept: got %d, want 302 (Rails treats it as html)", rec.Code)
	}
}

func TestGuardOtherVerbsRedirectWithoutReturnTo(t *testing.T) {
	srv := newTestServerNoDB(t)
	for _, m := range []string{"POST", "PATCH"} {
		rec := do(srv, m, "/scroll_position", nil)
		if rec.Code != 302 || rec.Header().Get("Location") != "/users/sign_in" {
			t.Errorf("%s: got %d %q", m, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestGuardAnswers401JSON(t *testing.T) {
	srv := newTestServerNoDB(t)
	rec := do(srv, "GET", "/expansions/1", map[string]string{"Accept": "application/json"})
	if rec.Code != 401 {
		t.Fatalf("got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type %q", got)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body) != 1 ||
		body["error"] != "You need to sign in or sign up before continuing." {
		t.Errorf("body %q (%v)", rec.Body.String(), err)
	}
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Error("Devise sends no WWW-Authenticate for database auth")
	}
	// XHR and other non-navigational formats also get 401.
	if rec := do(srv, "GET", "/index", map[string]string{"X-Requested-With": "XMLHttpRequest"}); rec.Code != 401 {
		t.Errorf("xhr: got %d", rec.Code)
	}
	rec = do(srv, "POST", "/expansions", map[string]string{"Accept": "text/plain"})
	if rec.Code != 401 || strings.TrimSpace(rec.Body.String()) != "You need to sign in or sign up before continuing." {
		t.Errorf("text/plain: %d %q", rec.Code, rec.Body.String())
	}
}

func TestGuardRejectsBadCookies(t *testing.T) {
	srv := newTestServerNoDB(t)
	for _, c := range []string{sessionCookieName + "=garbage", sessionCookieName + "=", "other=1"} {
		rec := do(srv, "GET", "/index", map[string]string{"Cookie": c})
		if rec.Code != 302 {
			t.Errorf("cookie %q: got %d", c, rec.Code)
		}
	}
}

func TestGuardAdmitsSignedInUserAndPutsItInContext(t *testing.T) {
	env := newAuthEnv(t)
	var got *store.User
	h := env.srv.requireUser(func(w http.ResponseWriter, r *http.Request) {
		got = auth.UserFrom(r.Context())
		w.WriteHeader(204)
	})
	req := httptest.NewRequest("GET", "/index", nil)
	req.Header.Set("Cookie", env.cookieFor(env.user))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 || got == nil || got.ID != env.user.ID || got.Email != viewerEmail {
		t.Fatalf("code %d user %+v", rec.Code, got)
	}
	// Through the real route table the (not yet ported) handler answers 501.
	if rec := do(env.srv, "GET", "/index", map[string]string{"Cookie": env.cookieFor(env.user)}); rec.Code != 501 {
		t.Errorf("GET /index with cookie: got %d, want 501", rec.Code)
	}
}

func TestSessionWithStaleSaltIsSignedOut(t *testing.T) {
	env := newAuthEnv(t)
	cookie := env.cookieFor(env.user)
	if rec := do(env.srv, "GET", "/index", map[string]string{"Cookie": cookie}); rec.Code != 501 {
		t.Fatalf("before password change: got %d", rec.Code)
	}
	env.addUser(t, viewerEmail, "a-new-password") // same user, new hash
	if rec := do(env.srv, "GET", "/index", map[string]string{"Cookie": cookie}); rec.Code != 302 {
		t.Errorf("after password change: got %d, want 302", rec.Code)
	}
}

func TestSessionOfDeletedUserOrExpiredIsSignedOut(t *testing.T) {
	env := newAuthEnv(t)
	cookie := env.cookieFor(env.user)
	*env.now = env.now.Add(fourteenDays + time.Second)
	if rec := do(env.srv, "GET", "/index", map[string]string{"Cookie": cookie}); rec.Code != 302 {
		t.Errorf("expired: got %d", rec.Code)
	}
	*env.now = fixedNow
	if _, err := env.srv.pool.Exec(context.Background(), `DELETE FROM users`); err != nil {
		t.Fatal(err)
	}
	if rec := do(env.srv, "GET", "/index", map[string]string{"Cookie": cookie}); rec.Code != 302 {
		t.Errorf("deleted user: got %d", rec.Code)
	}
}

func TestSessionWithEmptySaltIsRejected(t *testing.T) {
	env := newAuthEnv(t)
	v := env.srv.codec.EncodeSession(auth.Session{UserID: env.user.ID, Salt: "", ExpiresAt: env.now.Add(time.Hour)})
	if rec := do(env.srv, "GET", "/index", map[string]string{"Cookie": sessionCookieName + "=" + v}); rec.Code != 302 {
		t.Errorf("got %d", rec.Code)
	}
}

// --- test/controllers/sessions_test.rb: "signs in with valid credentials and remembers the user" ---

func TestSignInSetsRememberedCookie(t *testing.T) {
	env := newAuthEnv(t)
	form := signInForm(viewerEmail, viewerPassword)
	form.Set("user[remember_me]", "1")
	rec := postForm(env.srv, "POST", "/users/sign_in", form, nil)
	if rec.Code != 303 || rec.Header().Get("Location") != "/" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	c := setCookie(rec)
	if c == nil || c.Value == "" {
		t.Fatal("no session cookie")
	}
	if c.MaxAge != int(fourteenDays/time.Second) {
		t.Errorf("Max-Age %d", c.MaxAge)
	}
	for _, attr := range []string{"HttpOnly", "Secure", "SameSite=Lax", "Path=/"} {
		if !strings.Contains(rec.Header().Get("Set-Cookie"), attr) {
			t.Errorf("Set-Cookie %q lacks %s", rec.Header().Get("Set-Cookie"), attr)
		}
	}
	// The cookie opens the guard.
	if rec := do(env.srv, "GET", "/index", map[string]string{"Cookie": sessionCookieName + "=" + c.Value}); rec.Code != 501 {
		t.Errorf("issued cookie rejected: %d", rec.Code)
	}
}

func TestSignInWithoutRememberMeIsASessionCookie(t *testing.T) {
	env := newAuthEnv(t)
	rec := postForm(env.srv, "POST", "/users/sign_in", signInForm(viewerEmail, viewerPassword), nil)
	c := setCookie(rec)
	if rec.Code != 303 || c == nil {
		t.Fatalf("got %d, cookie %v", rec.Code, c)
	}
	if c.MaxAge != 0 || !c.Expires.IsZero() {
		t.Errorf("not a browser-session cookie: %q", rec.Header().Get("Set-Cookie"))
	}
	s, ok := env.srv.codec.DecodeSession(c.Value)
	if !ok || s.UserID != env.user.ID || !s.ExpiresAt.Equal(fixedNow.Add(fourteenDays)) {
		t.Errorf("session %+v ok=%v", s, ok)
	}
	if s.Salt != auth.SaltToken(env.user.EncryptedPassword) || strings.Contains(c.Value, env.user.EncryptedPassword[:29]) {
		t.Error("salt token wrong or raw bcrypt salt leaked")
	}
}

func TestSignInNormalizesEmail(t *testing.T) {
	env := newAuthEnv(t)
	rec := postForm(env.srv, "POST", "/users/sign_in", signInForm("  Viewer@Example.COM ", viewerPassword), nil)
	if rec.Code != 303 {
		t.Errorf("got %d", rec.Code)
	}
}

func TestSignInRedirectsToReturnTo(t *testing.T) {
	env := newAuthEnv(t)
	cases := []struct{ returnTo, want string }{
		{"/index", "/index"},
		{"/anything.md?a=b", "/anything.md?a=b"},
		{"", "/"},
		{"//evil.test", "/"},
		{"/\\evil.test", "/"},
		{"https://evil.test/", "/"},
		{"evil", "/"},
		{"/ok\nbad", "/"},
		{"/ok\x7fbad", "/"},
	}
	for _, c := range cases {
		form := signInForm(viewerEmail, viewerPassword)
		form.Set("return_to", c.returnTo)
		rec := postForm(env.srv, "POST", "/users/sign_in", form, nil)
		if rec.Code != 303 || rec.Header().Get("Location") != c.want {
			t.Errorf("return_to %q: got %d %q, want %q", c.returnTo, rec.Code, rec.Header().Get("Location"), c.want)
		}
	}
}

// --- "rejects invalid credentials" ---

func TestSignInRejectsInvalidCredentials(t *testing.T) {
	env := newAuthEnv(t)
	// An unknown email costs a cost-12 dummy bcrypt compare, which is slow
	// under -race, so there is only one such case.
	bodies := map[string]string{}
	cases := map[string]url.Values{
		"wrong password": signInForm(viewerEmail, "wrong"),
		"unknown email":  signInForm("nobody@example.com", "wrong"),
	}
	for name, form := range cases {
		form.Set("return_to", "/index")
		rec := postForm(env.srv, "POST", "/users/sign_in", form, nil)
		if rec.Code != 422 {
			t.Errorf("%s: got %d", name, rec.Code)
		}
		if c := setCookie(rec); c != nil {
			t.Errorf("%s: cookie set: %v", name, c)
		}
		b := rec.Body.String()
		if !strings.Contains(b, `<p class="signin-alert">Invalid Email or password.</p>`) {
			t.Errorf("%s: body lacks the alert:\n%s", name, b)
		}
		if !strings.Contains(b, `value="/index"`) {
			t.Errorf("%s: return_to lost", name)
		}
		bodies[name] = strings.ReplaceAll(strings.ReplaceAll(b, viewerEmail, "EMAIL"), "nobody@example.com", "EMAIL")
	}
	// Unknown email and wrong password are indistinguishable.
	if bodies["wrong password"] != bodies["unknown email"] {
		t.Error("responses differ between known and unknown email")
	}
}

func TestSignInPageEscapesEmailAndReturnTo(t *testing.T) {
	srv := newTestServerNoDB(t)
	rec := httptest.NewRecorder()
	srv.render(rec, 422, "sign_in", signInPage{Email: `"><script>x</script>`, ReturnTo: `/a"><script>y`})
	if rec.Code != 422 || strings.Contains(rec.Body.String(), "<script>") {
		t.Errorf("got %d; not escaped:\n%s", rec.Code, rec.Body.String())
	}
}

func TestSignInWhenAlreadySignedInRedirectsHome(t *testing.T) {
	env := newAuthEnv(t)
	h := map[string]string{"Cookie": env.cookieFor(env.user)}
	if rec := do(env.srv, "GET", "/users/sign_in", h); rec.Code != 302 || rec.Header().Get("Location") != "/" {
		t.Errorf("GET: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// --- sign out ---

func TestSignOutClearsCookie(t *testing.T) {
	env := newAuthEnv(t)
	rec := do(env.srv, "DELETE", "/users/sign_out", map[string]string{"Cookie": env.cookieFor(env.user)})
	if rec.Code != 303 || rec.Header().Get("Location") != "/" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	c := setCookie(rec)
	if c == nil || c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("cookie not cleared: %q", rec.Header().Get("Set-Cookie"))
	}
	for _, attr := range []string{"HttpOnly", "Secure", "SameSite=Lax", "Path=/"} {
		if !strings.Contains(rec.Header().Get("Set-Cookie"), attr) {
			t.Errorf("clearing cookie lacks %s: %q", attr, rec.Header().Get("Set-Cookie"))
		}
	}
	// Signing out when signed out is fine too (Devise: "Already signed out.").
	if rec := do(newTestServerNoDB(t), "DELETE", "/users/sign_out", nil); rec.Code != 303 {
		t.Errorf("signed out: got %d", rec.Code)
	}
}

func TestNewRequiresSecretKeyBase(t *testing.T) {
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "SECRET_KEY_BASE") {
			t.Fatalf("expected a clear panic, got %v", r)
		}
	}()
	cfg := testConfig(t)
	cfg.SecretKeyBase = ""
	New(cfg, nil)
}

func TestWantsHTML(t *testing.T) {
	cases := []struct {
		accept, xhr string
		want        bool
	}{
		{"", "", true},
		{"*/*", "", true},
		{"text/html", "", true},
		{"text/vnd.turbo-stream.html, text/html, application/xhtml+xml", "", true},
		{"application/json", "", false},
		{"application/json;q=0.9, text/html", "", false},
		{"text/plain", "", false},
		{"application/json, text/javascript, */*; q=0.01", "", true},
		{"text/html", "XMLHttpRequest", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		if c.accept != "" {
			r.Header.Set("Accept", c.accept)
		}
		if c.xhr != "" {
			r.Header.Set("X-Requested-With", c.xhr)
		}
		if got := wantsHTML(r); got != c.want {
			t.Errorf("Accept %q xhr %q: got %v, want %v", c.accept, c.xhr, got, c.want)
		}
	}
}

func TestSafeReturnTo(t *testing.T) {
	for in, want := range map[string]string{
		"/": "/", "/a/b?c=d#e": "/a/b?c=d#e", "": "", "//x": "", "/\\x": "", "x": "", "http://x": "",
		"/a\tb": "", "/a\x00b": "", "javascript:alert(1)": "",
	} {
		if got := safeReturnTo(in); got != want {
			t.Errorf("safeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}
