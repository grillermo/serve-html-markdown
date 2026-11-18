package video

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	// GoogleConsentURL is where the browser is sent to approve access.
	GoogleConsentURL = "https://accounts.google.com/o/oauth2/v2/auth"
	// GoogleTokenURL is Google's OAuth token endpoint (Ruby: OAUTH_TOKEN_URL).
	GoogleTokenURL = "https://oauth2.googleapis.com/token"
	// YoutubeScope is the only scope asked for: upload videos.
	YoutubeScope = "https://www.googleapis.com/auth/youtube.upload"

	// DefaultYoutubeHost is Ruby's ENV.fetch("HOST", "localhost:8009") default,
	// used when the host is blank.
	DefaultYoutubeHost = "localhost:8009"

	// defaultOAuthTimeout bounds one call to the token endpoint.
	defaultOAuthTimeout = 30 * time.Second
)

// Messages of AuthorizationError.
const (
	msgNoRefreshToken    = "Google returned no refresh token for this code."
	msgCodeRejected      = "Google rejected the authorization code."
	msgGoogleUnreachable = "Could not reach Google to exchange the authorization code."
	msgCodeMissing       = "The authorization code is missing."
	msgStateMissing      = "An OAuth state is required."
)

// AuthorizationError is a failure of the browser half of YouTube OAuth
// (Ruby: YoutubeAuthorization::Error). Message is safe to show the user: it
// never holds the client secret, the code or anything Google said except its
// short error code. Cause (when set) is the underlying transport error,
// reachable with errors.Is/As but not printed by Error.
type AuthorizationError struct {
	Message string
	// Config marks a missing credential (Ruby's ConfigurationError, a subclass
	// of Error), which the web layer answers with 500 instead of 502.
	Config bool
	cause  error
}

func (e *AuthorizationError) Error() string { return e.Message }
func (e *AuthorizationError) Unwrap() error { return e.cause }

// Is lets errors.Is(err, ErrAuthorization) match every AuthorizationError and
// errors.Is(err, ErrAuthorizationConfiguration) only the configuration ones.
func (e *AuthorizationError) Is(target error) bool {
	t, ok := target.(*AuthorizationError)
	if !ok {
		return false
	}
	return !t.Config || e.Config
}

// Sentinels for errors.Is. They are shared values: never return or modify them
// as a real error.
var (
	ErrAuthorization              = &AuthorizationError{Message: msgCodeRejected}
	ErrAuthorizationConfiguration = &AuthorizationError{Message: "YouTube OAuth credentials are not configured.", Config: true}
)

// defaultHost returns host, or DefaultYoutubeHost when it is blank.
func defaultHost(host string) string {
	if strings.TrimSpace(host) == "" {
		return DefaultYoutubeHost
	}
	return host
}

// YoutubeRedirectURI is the redirect URI registered on the Google web client:
// override (YOUTUBE_REDIRECT_URI) when it is not blank, else
// https://<host>/youtube/callback. It must match the registered one
// byte for byte and is derived from configuration, never from a request: the
// public tunnel forwards plain HTTP, so a request-derived URI would be http://
// and Google would reject it.
func YoutubeRedirectURI(override, host string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	return "https://" + defaultHost(host) + "/youtube/callback"
}

// YoutubeReauthURL is where a human re-authorizes uploads: override
// (YOUTUBE_REAUTH_URL) when it is not blank, else https://<host>/youtube/reauth,
// with ?video_id=<videoID> appended when videoID is not blank.
func YoutubeReauthURL(override, host, videoID string) string {
	base := override
	if strings.TrimSpace(base) == "" {
		base = "https://" + defaultHost(host) + "/youtube/reauth"
	}
	if strings.TrimSpace(videoID) == "" {
		return base
	}
	return base + "?video_id=" + url.QueryEscape(videoID)
}

// YoutubeAuthorization is the browser half of YouTube OAuth: build a consent
// URL, then trade the code Google hands back for a refresh token. It knows
// nothing about requests or controllers, so the URL shape is testable without a
// browser; storing the token is the caller's job.
type YoutubeAuthorization struct {
	// TokenURL defaults to GoogleTokenURL; tests point it at an httptest server.
	TokenURL string
	// HTTPClient is used for the token exchange. It defaults to a client with a
	// 30 s timeout that follows no redirects.
	HTTPClient *http.Client

	clientID, clientSecret, redirectURI string
}

// NewYoutubeAuthorization returns an authorization for the Google web client.
// A blank client id or secret is an AuthorizationError (Config) that names the
// missing variable, like Ruby's ConfigurationError.
func NewYoutubeAuthorization(clientID, clientSecret, redirectURI string) (*YoutubeAuthorization, error) {
	if strings.TrimSpace(clientID) == "" {
		return nil, &AuthorizationError{Message: "YOUTUBE_CLIENT_ID is not set.", Config: true}
	}
	if strings.TrimSpace(clientSecret) == "" {
		return nil, &AuthorizationError{Message: "YOUTUBE_CLIENT_SECRET is not set.", Config: true}
	}
	return &YoutubeAuthorization{
		TokenURL: GoogleTokenURL, HTTPClient: newOAuthHTTPClient(),
		clientID: clientID, clientSecret: clientSecret, redirectURI: redirectURI,
	}, nil
}

// newOAuthHTTPClient never follows redirects: the client secret and the code
// go to the token endpoint and nowhere else.
func newOAuthHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       defaultOAuthTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// RedirectURI is the redirect URI sent to Google.
func (a *YoutubeAuthorization) RedirectURI() string { return a.redirectURI }

func (a *YoutubeAuthorization) config() *oauth2.Config {
	tokenURL := a.TokenURL
	if tokenURL == "" {
		tokenURL = GoogleTokenURL
	}
	return &oauth2.Config{
		ClientID: a.clientID, ClientSecret: a.clientSecret, RedirectURL: a.redirectURI,
		Scopes: []string{YoutubeScope},
		Endpoint: oauth2.Endpoint{
			AuthURL: GoogleConsentURL, TokenURL: tokenURL,
			// Credentials in the body, like Signet: one request, no probing.
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

// ConsentURL is the URL to send the browser to. state is the caller's CSRF
// token (a blank one is an error: the callback could not check it). Google only
// returns a refresh token when prompt=consent is requested, which is why that
// parameter is not optional.
func (a *YoutubeAuthorization) ConsentURL(state string) (string, error) {
	if strings.TrimSpace(state) == "" {
		return "", &AuthorizationError{Message: msgStateMissing}
	}
	return a.config().AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent")), nil
}

// errorCode is what of Google's own error text is shown: a short snake_case code.
var errorCode = regexp.MustCompile(`\A[a-z_]{1,40}\z`)

// Exchange trades the code from the callback for a refresh token. It fails
// with an AuthorizationError whose message is safe to show: a blank code, a
// code Google refused (with Google's error code when it is a plain one), a
// Google that cannot be reached (errors.Is still sees a cancelled context) and
// "Google returned no refresh token for this code." when the grant carries none.
func (a *YoutubeAuthorization) Exchange(ctx context.Context, code string) (string, error) {
	if strings.TrimSpace(code) == "" {
		return "", &AuthorizationError{Message: msgCodeMissing}
	}
	hc := a.HTTPClient
	if hc == nil {
		hc = newOAuthHTTPClient()
	}
	tok, err := a.config().Exchange(context.WithValue(ctx, oauth2.HTTPClient, hc), code)
	if err != nil {
		return "", exchangeError(err)
	}
	if strings.TrimSpace(tok.RefreshToken) == "" {
		return "", &AuthorizationError{Message: msgNoRefreshToken}
	}
	return tok.RefreshToken, nil
}

func exchangeError(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		if errorCode.MatchString(re.ErrorCode) {
			return &AuthorizationError{Message: "Google rejected the authorization code: " + re.ErrorCode + "."}
		}
		return &AuthorizationError{Message: msgCodeRejected}
	}
	var ue *url.Error
	if errors.As(err, &ue) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// A url.Error names the token endpoint; keep it out of the message.
		cause := err
		if ue != nil {
			cause = ue.Unwrap()
		}
		return &AuthorizationError{Message: msgGoogleUnreachable, cause: cause}
	}
	return &AuthorizationError{Message: msgCodeRejected}
}

// YoutubeSettings are the raw configuration values the OAuth flow derives its
// URLs from (HOST, YOUTUBE_REDIRECT_URI, YOUTUBE_REAUTH_URL, YOUTUBE_CLIENT_ID,
// YOUTUBE_CLIENT_SECRET).
type YoutubeSettings struct {
	Host, RedirectURI, ReauthURL, ClientID, ClientSecret string
}

// DebugReport reports what THIS process computes, so a redirect_uri_mismatch can
// be compared byte for byte with the URI registered on the Google client.
// Values are quoted, so stray whitespace is visible. The client secret is
// never printed, only its length. The caller adds what only it knows (the
// request's base URL and X-Forwarded-Proto, process and environment).
func (s YoutubeSettings) DebugReport() string {
	consent, sent := "", ""
	auth, err := NewYoutubeAuthorization(s.ClientID, s.ClientSecret, YoutubeRedirectURI(s.RedirectURI, s.Host))
	if err == nil {
		consent, err = auth.ConsentURL("debug-state")
	}
	if err != nil {
		consent = "(" + err.Error() + ")"
	} else if u, perr := url.Parse(consent); perr == nil {
		sent = u.Query().Get("redirect_uri")
	}
	lines := [][2]string{
		{"ENV['HOST']", fmt.Sprintf("%q", s.Host)},
		{"ENV['YOUTUBE_REDIRECT_URI']", fmt.Sprintf("%q", s.RedirectURI)},
		{"ENV['YOUTUBE_REAUTH_URL']", fmt.Sprintf("%q", s.ReauthURL)},
		{"ENV['YOUTUBE_CLIENT_ID']", fmt.Sprintf("%q", s.ClientID)},
		{"ENV['YOUTUBE_CLIENT_SECRET'] length", fmt.Sprint(len(s.ClientSecret))},
		{"YoutubeAuthorization.redirect_uri", fmt.Sprintf("%q", YoutubeRedirectURI(s.RedirectURI, s.Host))},
		{"YoutubeAuthorization.reauth_url", fmt.Sprintf("%q", YoutubeReauthURL(s.ReauthURL, s.Host, ""))},
		{"redirect_uri Google receives", fmt.Sprintf("%q", sent)},
		{"consent_url", consent},
	}
	var b strings.Builder
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l[0] + ": " + l[1])
	}
	return b.String()
}
