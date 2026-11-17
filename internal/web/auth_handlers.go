package web

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/store"
)

const (
	// sessionCookieName is the signed session cookie.
	sessionCookieName = "_shm_session"
	// sessionLifetime is Devise's remember_for default (2 weeks). It is the
	// signed expiry of every session; with "remember me" it is also the cookie's
	// Max-Age, without it the cookie ends with the browser session.
	sessionLifetime = 14 * 24 * time.Hour
	// maxFormBytes bounds the sign-in form body.
	maxFormBytes = 1 << 16

	// Devise's en locale (config/locales/devise.en.yml).
	msgUnauthenticated = "You need to sign in or sign up before continuing."
	msgInvalid         = "Invalid Email or password."
)

// signInPage is the data of the sign_in template.
type signInPage struct {
	Alert    string
	Email    string
	ReturnTo string
}

// signInForm is GET /users/sign_in. A signed-in visitor goes home, like
// Devise's require_no_authentication. return_to arrives from the guard's
// redirect, which in Rails also set the "You need to sign in..." flash, so the
// alert shows exactly then.
func (s *Server) signInForm(w http.ResponseWriter, r *http.Request) {
	user, err := s.sessionUser(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	returnTo := safeReturnTo(r.URL.Query().Get("return_to"))
	if user != nil {
		redirect(w, orRoot(returnTo), http.StatusFound)
		return
	}
	page := signInPage{ReturnTo: returnTo}
	if returnTo != "" {
		page.Alert = msgUnauthenticated
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, http.StatusOK, "sign_in", page)
}

// signIn is POST /users/sign_in with the Devise form fields user[email],
// user[password] and user[remember_me], plus return_to. Unknown email and wrong
// password are the same 422, and both spend a bcrypt compare.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	email := r.PostFormValue("user[email]")
	password := r.PostFormValue("user[password]")
	// The form sends a hidden user[remember_me]=0 before the checkbox's =1, and
	// Rails uses the last value.
	remember := false
	if vals := r.PostForm["user[remember_me]"]; len(vals) > 0 {
		remember = vals[len(vals)-1] == "1"
	}
	returnTo := safeReturnTo(r.PostFormValue("return_to"))

	if user, err := s.sessionUser(r); err != nil {
		s.serverError(w, r, err)
		return
	} else if user != nil {
		redirect(w, orRoot(returnTo), http.StatusFound)
		return
	}

	var user *store.User
	if strings.TrimSpace(email) != "" {
		var err error
		if user, err = store.FindUserByEmail(r.Context(), s.pool, email); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	hash := ""
	if user != nil {
		hash = user.EncryptedPassword
	}
	if !auth.CheckPasswordConstantTime(hash, password) || user == nil {
		w.Header().Set("Cache-Control", "no-store")
		s.render(w, http.StatusUnprocessableEntity, "sign_in", signInPage{
			Alert: msgInvalid, Email: email, ReturnTo: returnTo,
		})
		return
	}

	expires := s.Clock().Add(sessionLifetime)
	value := s.codec.EncodeSession(auth.Session{
		UserID: user.ID, Salt: auth.SaltToken(user.EncryptedPassword), ExpiresAt: expires,
	})
	if value == "" {
		s.serverError(w, r, errSealSession)
		return
	}
	c := s.sessionCookie(value)
	if remember {
		c.MaxAge = int(sessionLifetime / time.Second)
	}
	http.SetCookie(w, c)
	redirect(w, orRoot(returnTo), http.StatusSeeOther)
}

// signOut is DELETE /users/sign_out. Sessions are stateless, so it only clears
// the cookie (see auth.Authenticate). It works for a signed-out visitor too.
func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	c := s.sessionCookie("")
	c.MaxAge = -1
	c.Expires = time.Unix(0, 0)
	http.SetCookie(w, c)
	redirect(w, "/", http.StatusSeeOther)
}

// sessionCookie builds the session cookie. Secure is always set: production is
// behind HTTPS (Rails' force_ssl with assume_ssl).
func (s *Server) sessionCookie(value string) *http.Cookie {
	return &http.Cookie{
		Name: sessionCookieName, Value: value, Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	}
}

// sessionUser returns the user of the request's session cookie, or nil.
func (s *Server) sessionUser(r *http.Request) (*store.User, error) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, nil
	}
	return auth.Authenticate(r.Context(), s.pool, s.codec, c.Value)
}

// requireUser is Devise's authenticate_user!. It stores the user in the request
// context (auth.UserFrom). An anonymous request is answered the way Devise's
// failure app does: a 401 for non-navigational formats (JSON, XHR), otherwise a
// 302 to the sign-in page, remembering a GET/HEAD location in return_to.
func (s *Server) requireUser(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := s.sessionUser(r)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if user == nil {
			s.unauthenticated(w, r)
			return
		}
		next(w, r.WithContext(auth.WithUser(r.Context(), user)))
	})
}

func (s *Server) unauthenticated(w http.ResponseWriter, r *http.Request) {
	if !wantsHTML(r) {
		if firstMediaType(r.Header.Get("Accept")) == "application/json" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			body, _ := json.Marshal(map[string]string{"error": msgUnauthenticated})
			_, _ = w.Write(body)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(msgUnauthenticated))
		return
	}
	target := "/users/sign_in"
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		target += "?return_to=" + url.QueryEscape(r.URL.RequestURI())
	}
	redirect(w, target, http.StatusFound)
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

type constError string

func (e constError) Error() string { return string(e) }

const errSealSession = constError("web: could not seal the session cookie")

// browserLikeAccept is Rails' BROWSER_LIKE_ACCEPTS: a list that includes
// "*/*" is treated as a browser request, whatever it lists first.
var browserLikeAccept = regexp.MustCompile(`,\s*\*/\*|\*/\*\s*,`)

// wantsHTML mirrors Devise's navigational_formats ("*/*", html, turbo_stream)
// together with Rails' request.format choice: the first listed media type
// decides, unless the header is browser-like. An XHR is never navigational.
func wantsHTML(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("X-Requested-With"), "XMLHttpRequest") {
		return false
	}
	accept := r.Header.Get("Accept")
	if strings.TrimSpace(accept) == "" || browserLikeAccept.MatchString(accept) {
		return true
	}
	switch firstMediaType(accept) {
	case "text/html", "application/xhtml+xml", "text/vnd.turbo-stream.html", "*/*":
		return true
	}
	return false
}

// firstMediaType is the lower-cased first media type of an Accept header,
// without parameters.
func firstMediaType(accept string) string {
	first, _, _ := strings.Cut(accept, ",")
	first, _, _ = strings.Cut(first, ";")
	return strings.ToLower(strings.TrimSpace(first))
}

// maxReturnToBytes caps a return_to value.
const maxReturnToBytes = 2048

// safeReturnTo returns the cleaned local path to redirect to, or "" when v is
// not one. It keeps return_to from becoming an open redirect:
//
//   - the raw value has no backslash, control character, whitespace or non-ASCII
//     byte and is at most 2048 bytes; after one round of percent-decoding it has
//     no backslash or control character either (browsers read "/\x" as "//x");
//   - it must parse with no scheme, host or userinfo;
//   - the path is cleaned the way http.Redirect would (dot segments and repeated
//     slashes collapse), and the CLEANED value is returned and used as the
//     Location, so what was validated is what is sent. The result starts with
//     exactly one "/" and its decoded path does not start with "//".
func safeReturnTo(v string) string {
	if v == "" || len(v) > maxReturnToBytes || v[0] != '/' {
		return ""
	}
	if !plainASCII(v) {
		return ""
	}
	decoded, err := url.PathUnescape(v)
	if err != nil || !noBackslashOrControl(decoded) {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return ""
	}
	// Split off the query/fragment, clean only the path.
	rawPath, rest := v, ""
	if i := strings.IndexAny(v, "?#"); i >= 0 {
		rawPath, rest = v[:i], v[i:]
	}
	clean := path.Clean(rawPath)
	if strings.HasSuffix(rawPath, "/") && !strings.HasSuffix(clean, "/") {
		clean += "/"
	}
	out := clean + rest
	dec, err := url.PathUnescape(out)
	if err != nil || out[0] != '/' || strings.HasPrefix(out, "//") || strings.HasPrefix(dec, "//") || !noBackslashOrControl(dec) {
		return ""
	}
	return out
}

// plainASCII reports whether s has no backslash, control character, space or
// byte outside 7-bit ASCII.
func plainASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= 0x20 || c >= 0x7f || c == '\\' {
			return false
		}
	}
	return true
}

// redirect sends a redirect with exactly the given Location. Callers pass
// either a constant or a value from safeReturnTo, which is already clean, so
// nothing rewrites it on the way out (http.Redirect would path.Clean it again).
func redirect(w http.ResponseWriter, target string, status int) {
	w.Header().Set("Location", target)
	w.WriteHeader(status)
}

// noBackslashOrControl is for percent-decoded text, where spaces and UTF-8
// (from file names such as "my%20file.md") are legitimate.
func noBackslashOrControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f || c == '\\' {
			return false
		}
	}
	return true
}

func orRoot(path string) string {
	if path == "" {
		return "/"
	}
	return path
}
