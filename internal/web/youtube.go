package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"

	"serve-html-markdown/internal/store"
	"serve-html-markdown/internal/video"
)

const (
	// oauthCookieName is the signed cookie that carries the OAuth state (and the
	// stalled video's id) from /youtube/reauth to /youtube/callback.
	oauthCookieName = "_shm_oauth"
	// oauthCookiePath keeps the cookie off every other route.
	oauthCookiePath = "/youtube"
	// oauthLifetime is how long a visitor has to approve access at Google.
	oauthLifetime = 10 * time.Minute
	// oauthCallbackTimeout bounds the exchange with Google and the writes after it.
	oauthCallbackTimeout = time.Minute
	// maxVideoIDDigits keeps a video id inside int64.
	maxVideoIDDigits = 18

	msgOAuthStateMismatch = "Authorization state did not match. Start again from the Slack link."
	msgOAuthDenied        = "Google denied the request"
	msgOAuthFailed        = "The authorization could not be completed."
	msgOAuthStoreError    = "Could not store the new refresh token."
	msgRequeueFailed      = "The refresh token is stored, but the video could not be queued again."
)

// YoutubeAuthorizer is the browser half of the YouTube OAuth flow
// (*video.YoutubeAuthorization is the real one). WithYoutubeAuthorizer replaces
// it in tests.
type YoutubeAuthorizer interface {
	ConsentURL(state string) (string, error)
	Exchange(ctx context.Context, code string) (string, error)
}

// WithYoutubeAuthorizer replaces the authorizer built from the YouTube
// settings for each request. It is meant for tests.
func WithYoutubeAuthorizer(a YoutubeAuthorizer) Option {
	return func(s *Server) { s.youtubeAuth = func() (YoutubeAuthorizer, error) { return a, nil } }
}

// youtubeSettings are the raw YouTube values: host, redirect and reauth
// overrides come from configuration, never from the request, because the
// tunnel forwards plain HTTP and a request-derived URI would not match the one
// registered on the Google client.
func (s *Server) youtubeSettings() video.YoutubeSettings {
	return video.YoutubeSettings{
		Host: s.cfg.YoutubeHost, RedirectURI: s.cfg.YoutubeRedirectURI, ReauthURL: s.cfg.YoutubeReauthURL,
		ClientID: s.cfg.YoutubeClientID, ClientSecret: s.cfg.YoutubeClientSecret,
	}
}

// defaultYoutubeAuthorizer is YoutubeAuthorization.build: it fails with a
// configuration error while the client id or secret is not set.
func (s *Server) defaultYoutubeAuthorizer() (YoutubeAuthorizer, error) {
	set := s.youtubeSettings()
	a, err := video.NewYoutubeAuthorization(set.ClientID, set.ClientSecret, video.YoutubeRedirectURI(set.RedirectURI, set.Host))
	if err != nil {
		return nil, err
	}
	return a, nil
}

// oauthState is the payload of the _shm_oauth cookie. The cookie is signed,
// not encrypted: it holds nothing secret (the state is single use and only
// useful together with a code Google issues to this client).
type oauthState struct {
	State   string `json:"s"`
	VideoID string `json:"v,omitempty"`
}

// problemPage is the data of the youtube_problem template.
type problemPage struct{ Message string }

// createdPage is the data of the youtube_create template; VideoID is 0 when no
// video was waiting.
type createdPage struct{ VideoID int64 }

// oauthCookie builds the state cookie. It is SameSite=Lax, not Strict, so it
// comes back with Google's top-level redirect to the callback.
func oauthCookie(value string, maxAge int) *http.Cookie {
	c := &http.Cookie{
		Name: oauthCookieName, Value: value, Path: oauthCookiePath, MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	}
	if maxAge < 0 {
		c.Expires = time.Unix(0, 0)
	}
	return c
}

// problem answers with the failure page.
func (s *Server) problem(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, status, "youtube_problem", problemPage{Message: message})
}

// validVideoID accepts a video id of digits only, or "" for anything else.
func validVideoID(v string) string {
	if v == "" || len(v) > maxVideoIDDigits {
		return ""
	}
	for i := 0; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return ""
		}
	}
	return v
}

// youtubeReauth is GET /youtube/reauth (YoutubeAuthorizationsController#new):
// remember a fresh state and the stalled video in a signed cookie and send the
// signed-in visitor to Google's consent page. A missing client id or secret is
// a 500 page that says which.
func (s *Server) youtubeReauth(w http.ResponseWriter, r *http.Request) {
	authorizer, err := s.youtubeAuth()
	if err != nil {
		s.oauthFailure(w, err)
		return
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		s.serverError(w, r, err)
		return
	}
	state := hex.EncodeToString(raw)
	consent, err := authorizer.ConsentURL(state)
	if err != nil {
		s.oauthFailure(w, err)
		return
	}
	value, err := s.oauthCodec.Seal(oauthState{State: state, VideoID: validVideoID(r.URL.Query().Get("video_id"))}, s.Clock().Add(oauthLifetime))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.SetCookie(w, oauthCookie(value, int(oauthLifetime/time.Second)))
	w.Header().Set("Cache-Control", "no-store")
	redirect(w, consent, http.StatusFound)
}

// oauthFailure answers a failed authorizer: a configuration error is a 500
// with its message, an error of Google's side a 502 with its message (both are
// safe to show: they never hold a secret), and anything else a 500 with a
// fixed text.
func (s *Server) oauthFailure(w http.ResponseWriter, err error) {
	var ae *video.AuthorizationError
	switch {
	case errors.Is(err, video.ErrAuthorizationConfiguration):
		s.problem(w, http.StatusInternalServerError, err.Error())
	case errors.As(err, &ae):
		s.problem(w, http.StatusBadGateway, ae.Message)
	default:
		slog.Error("youtube authorization failed", "err", err)
		s.problem(w, http.StatusInternalServerError, msgOAuthFailed)
	}
}

// oauthErrorCode is the part of Google's "error" parameter that is shown: a
// short snake_case code such as access_denied.
var oauthErrorCode = regexp.MustCompile(`\A[a-z_]{1,40}\z`)

// youtubeCallback is GET /youtube/callback (YoutubeAuthorizationsController#create),
// where Google sends the visitor back. The state in the query must equal the one
// in the signed cookie (single use: the cookie is cleared whatever the outcome).
// Then the code is traded for a refresh token, which is stored, and the video
// that was waiting for it (the id in the cookie, never one from the query) is
// queued again per the retry contract of retryVideo.
//
// Failures render the problem page: Google's own "error" and a state mismatch
// 400, a configuration error 500, any other failure of the exchange 502.
func (s *Server) youtubeCallback(w http.ResponseWriter, r *http.Request) {
	var saved oauthState
	if c, err := r.Cookie(oauthCookieName); err != nil || !s.oauthCodec.Open(c.Value, &saved) {
		saved = oauthState{}
	}
	// Single use, on every path, success or not.
	http.SetCookie(w, oauthCookie("", -1))

	query := r.URL.Query()
	if denied := query.Get("error"); denied != "" {
		message := msgOAuthDenied + "."
		if oauthErrorCode.MatchString(denied) {
			message = msgOAuthDenied + ": " + denied + "."
		}
		s.problem(w, http.StatusBadRequest, message)
		return
	}
	given := query.Get("state")
	if saved.State == "" || subtle.ConstantTimeCompare([]byte(given), []byte(saved.State)) != 1 {
		s.problem(w, http.StatusBadRequest, msgOAuthStateMismatch)
		return
	}

	authorizer, err := s.youtubeAuth()
	if err != nil {
		s.oauthFailure(w, err)
		return
	}
	// The token is Google's answer to a code that works once: store it and
	// queue the retry even if the visitor closes the tab now.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), oauthCallbackTimeout)
	defer cancel()
	token, err := authorizer.Exchange(ctx, query.Get("code"))
	if err != nil {
		s.oauthFailure(w, err)
		return
	}
	if err := store.StoreYoutubeCredential(ctx, s.pool, token); err != nil {
		slog.Error("could not store the YouTube refresh token", "err", err)
		s.problem(w, http.StatusInternalServerError, msgOAuthStoreError)
		return
	}
	id, err := s.retryStalledVideo(ctx, saved.VideoID)
	if err != nil {
		// The token is stored: the retry can be started again from the Slack link.
		slog.Error("could not queue the stalled video after re-authorization", "video_id", saved.VideoID, "err", err)
		s.problem(w, http.StatusInternalServerError, msgRequeueFailed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, http.StatusOK, "youtube_create", createdPage{VideoID: id})
}

// retryStalledVideo queues the video whose id the cookie carried, when it
// exists and is waiting on a job (see retryVideo). It returns its id, or 0 when
// nothing was waiting.
func (s *Server) retryStalledVideo(ctx context.Context, videoID string) (int64, error) {
	id, err := strconv.ParseInt(validVideoID(videoID), 10, 64)
	if err != nil {
		return 0, nil
	}
	v, err := store.FindTwitterVideo(ctx, s.pool, id)
	if err != nil || v == nil {
		return 0, err
	}
	_, queued, err := s.retryVideo(ctx, v)
	if err != nil || !queued {
		return 0, err
	}
	return v.ID, nil
}

// youtubeDebug is GET /youtube/debug: what THIS process computes, so a
// redirect_uri_mismatch can be compared byte for byte with the URI registered
// on the Google client. Values are quoted, so stray whitespace shows. The client
// secret is never printed, only its length.
func (s *Server) youtubeDebug(w http.ResponseWriter, r *http.Request) {
	scheme := "https" // like Rails' assume_ssl: TLS ends at the tunnel
	report := fmt.Sprintf("pid: %d\nAPP_ENV: %s\napp root: %s\n%s\nrequest.base_url: %s://%s\nX-Forwarded-Proto: %q",
		os.Getpid(), s.cfg.AppEnv, s.cfg.Root, s.youtubeSettings().DebugReport(), scheme, r.Host,
		r.Header.Get("X-Forwarded-Proto"))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(report))
}
