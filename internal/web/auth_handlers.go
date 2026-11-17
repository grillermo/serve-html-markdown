package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
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
		http.Redirect(w, r, orRoot(returnTo), http.StatusFound)
		return
	}
	page := signInPage{ReturnTo: returnTo}
	if returnTo != "" {
		page.Alert = msgUnauthenticated
	}
	s.render(w, http.StatusOK, "sign_in", page)
}

// signIn is POST /users/sign_in with the Devise form fields user[email],
// user[password] and user[remember_me], plus return_to. Unknown email and wrong
// password are the same 422, and both spend a bcrypt compare.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	email := r.PostFormValue("user[email]")
	password := r.PostFormValue("user[password]")
	remember := r.PostFormValue("user[remember_me]") == "1"
	returnTo := safeReturnTo(r.PostFormValue("return_to"))

	if user, err := s.sessionUser(r); err != nil {
		s.serverError(w, r, err)
		return
	} else if user != nil {
		http.Redirect(w, r, orRoot(returnTo), http.StatusFound)
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
	http.Redirect(w, r, orRoot(returnTo), http.StatusSeeOther)
}

// signOut is DELETE /users/sign_out. Sessions are stateless, so it only clears
// the cookie (see auth.Authenticate). It works for a signed-out visitor too.
func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	c := s.sessionCookie("")
	c.MaxAge = -1
	c.Expires = time.Unix(0, 0)
	http.SetCookie(w, c)
	http.Redirect(w, r, "/", http.StatusSeeOther)
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
	http.Redirect(w, r, target, http.StatusFound)
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

// safeReturnTo returns v when it is a local path (one leading "/", not "//" or
// "/\", no control characters), else "". It keeps return_to from becoming an
// open redirect.
func safeReturnTo(v string) string {
	if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.HasPrefix(v, `/\`) {
		return ""
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] == 0x7f {
			return ""
		}
	}
	return v
}

func orRoot(path string) string {
	if path == "" {
		return "/"
	}
	return path
}
