package video

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

const (
	// DefaultUploadChunkSize is the size of one resumable-upload chunk, which is
	// all the memory an upload holds of the file (Ruby sent 100 MB chunks). The
	// Google client rounds any chunk size up to a multiple of 256 KiB, so this
	// is one.
	DefaultUploadChunkSize = 16 << 20
	// MaxErrorBodyBytes caps the Google response body carried in an upload
	// error, which ends up in Slack messages and logs.
	MaxErrorBodyBytes = 2048

	// uploadContentType is what the Ruby uploader declared for every video.
	uploadContentType = "video/*"
	// privacyStatus: every upload is unlisted, never public.
	privacyStatus = "unlisted"

	// maxScrubInput bounds the text scrubbed for an error message.
	maxScrubInput = 64 << 10
	redacted      = "[REDACTED]"
	// minSecretLen is the shortest known secret that is searched for literally.
	minSecretLen = 4
)

// Messages of YoutubeError.
const (
	msgYoutubeNotConfigured = "YouTube OAuth credentials are not configured."
	msgNoStoredToken        = "No YouTube refresh token is stored."
)

// YoutubeError is a failed YouTube upload (Ruby: YoutubeUploader::Error).
// Classify it with errors.Is: ErrAuthorizationExpired for a dead or missing
// grant (offer YoutubeReauthURL), ErrYoutube for any YoutubeError, and
// context.Canceled or context.DeadlineExceeded when the caller's ctx ended the
// call (the cause is kept); errors.As(err, &*YoutubeError) reaches the message.
// Message is safe to show and log: it never holds the client secret, a token,
// the URL of an upload session, or more than MaxErrorBodyBytes of Google's
// response. Cause (when set) is the underlying error, reachable with
// errors.Is/As but not printed by Error.
type YoutubeError struct {
	Message string
	// Expired marks a dead or missing refresh token (Ruby's AuthorizationExpired,
	// a subclass of Error): recoverable by re-running the browser OAuth flow, so
	// callers can offer a link (YoutubeReauthURL) instead of a stack trace.
	Expired bool
	cause   error
}

func (e *YoutubeError) Error() string { return e.Message }
func (e *YoutubeError) Unwrap() error { return e.cause }

// Is lets errors.Is(err, ErrYoutube) match every YoutubeError and
// errors.Is(err, ErrAuthorizationExpired) only the expired ones.
func (e *YoutubeError) Is(target error) bool {
	t, ok := target.(*YoutubeError)
	if !ok {
		return false
	}
	return !t.Expired || e.Expired
}

// Sentinels for errors.Is. They are shared values: never return or modify them
// as a real error.
var (
	ErrYoutube              = &YoutubeError{Message: "YouTube upload failed."}
	ErrAuthorizationExpired = &YoutubeError{Message: msgNoStoredToken, Expired: true}
)

// YoutubeUploader uploads a video file to the user's channel, unlisted.
type YoutubeUploader struct {
	// TokenURL defaults to GoogleTokenURL; tests point it at an httptest server.
	TokenURL string
	// APIEndpoint overrides the YouTube API's base URL (tests); "" is Google's.
	APIEndpoint string
	// ChunkSize defaults to DefaultUploadChunkSize.
	ChunkSize int
	// HTTPClient, when set, is used for the token endpoint and as the base
	// transport of the upload (tests). Unset, the token client has a 30 s
	// timeout and the upload transport bounds dialing, TLS and response
	// headers; the upload as a whole is bounded by the caller's ctx only,
	// because a large file takes as long as it takes. Neither follows
	// redirects, so a bearer token never travels to another host.
	HTTPClient *http.Client

	clientID, clientSecret, refreshToken string
}

// NewYoutubeUploader returns an uploader for the OAuth client and the stored
// refresh token. A blank client id or secret is a YoutubeError "YouTube OAuth
// credentials are not configured."; a blank refresh token is
// ErrAuthorizationExpired ("No YouTube refresh token is stored.").
func NewYoutubeUploader(clientID, clientSecret, refreshToken string) (*YoutubeUploader, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return nil, &YoutubeError{Message: msgYoutubeNotConfigured}
	}
	if strings.TrimSpace(refreshToken) == "" {
		return nil, &YoutubeError{Message: msgNoStoredToken, Expired: true}
	}
	return &YoutubeUploader{
		TokenURL: GoogleTokenURL, ChunkSize: DefaultUploadChunkSize,
		clientID: clientID, clientSecret: clientSecret, refreshToken: refreshToken,
	}, nil
}

// Upload sends the video at filePath to YouTube as an unlisted video titled
// title and returns its id. The refresh token is exchanged first, so a dead
// grant is reported (ErrAuthorizationExpired) before any of the file is read;
// a grant that dies during a long upload is reported the same way. The file is
// streamed one chunk at a time, must be a non-empty regular file, and is closed
// on every path; ctx ends the upload. Every other failure is a YoutubeError
// (see there) whose message for an API error is
// "YouTube upload failed: <status> <message> body=<body>".
//
// Caveats for the caller (the upload job):
//
//   - Every call starts a NEW resumable session. If Google accepted the video
//     but the final response was lost (the call fails after the last chunk went
//     out), the video exists on the channel even though Upload returned an
//     error, and a blind retry uploads it a second time. Persist the returned
//     id the moment Upload returns, and never call Upload again for the same
//     video without checking first (for instance that no id is stored).
//   - Upload has no deadline of its own: a large file takes as long as it
//     takes. Wrap the call in a context with the job's overall deadline.
//   - A connection the server closes in the middle of a chunk fails the upload;
//     it is not retried or resumed here (only HTTP-level retries of the Google
//     client apply).
//   - Classify the error with errors.Is(err, ErrAuthorizationExpired) (send the
//     user to YoutubeReauthURL), errors.Is(err, ErrYoutube) for any other
//     failure of this uploader and errors.Is(err, context.Canceled) or
//     context.DeadlineExceeded when ctx ended it; errors.As(err, &*YoutubeError)
//     gives the message, which is safe to log and to post to Slack.
func (u *YoutubeUploader) Upload(ctx context.Context, filePath, title, description string) (string, error) {
	tokenURL := u.TokenURL
	if tokenURL == "" {
		tokenURL = GoogleTokenURL
	}
	cfg := &oauth2.Config{
		ClientID: u.clientID, ClientSecret: u.clientSecret, Scopes: []string{YoutubeScope},
		Endpoint: oauth2.Endpoint{TokenURL: tokenURL, AuthStyle: oauth2.AuthStyleInParams},
	}
	tokenClient, base := u.HTTPClient, http.RoundTripper(nil)
	if tokenClient == nil {
		tokenClient, base = newOAuthHTTPClient(), newUploadTransport()
	} else {
		base = tokenClient.Transport
	}
	ts := cfg.TokenSource(context.WithValue(ctx, oauth2.HTTPClient, tokenClient), &oauth2.Token{RefreshToken: u.refreshToken})
	// Forced before anything is opened or sent, so an expired grant is found
	// while it is still cheap.
	tok, err := ts.Token()
	if err != nil {
		return "", u.tokenError(ctx, err, nil)
	}
	secrets := []string{tok.AccessToken}

	f, err := openRegular(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	client := &http.Client{
		Transport:     &oauth2.Transport{Source: ts, Base: base},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	opts := []option.ClientOption{option.WithHTTPClient(client)}
	if u.APIEndpoint != "" {
		opts = append(opts, option.WithEndpoint(strings.TrimRight(u.APIEndpoint, "/")+"/"))
	}
	svc, err := youtube.NewService(ctx, opts...)
	if err != nil {
		return "", &YoutubeError{Message: "YouTube upload failed: the client could not be built.", cause: err}
	}
	chunk := u.ChunkSize
	if chunk <= 0 {
		chunk = DefaultUploadChunkSize
	}
	video := &youtube.Video{
		Snippet: &youtube.VideoSnippet{Title: title, Description: description},
		Status:  &youtube.VideoStatus{PrivacyStatus: privacyStatus},
	}
	created, err := svc.Videos.Insert([]string{"snippet", "status"}, video).
		Media(f, googleapi.ContentType(uploadContentType), googleapi.ChunkSize(chunk)).
		Context(ctx).Do()
	if err != nil {
		return "", u.uploadError(ctx, err, secrets)
	}
	return created.Id, nil
}

// openRegular opens path for reading and checks, on the open file, that it is a
// regular file. O_NONBLOCK keeps a FIFO from blocking the open.
func openRegular(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		// The os error names the path; keep it out of the message.
		return nil, &YoutubeError{Message: "YouTube upload failed: the video file could not be opened.", cause: err}
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, &YoutubeError{Message: "YouTube upload failed: the video is not a regular file.", cause: err}
	}
	if info.Size() == 0 {
		f.Close()
		return nil, &YoutubeError{Message: "YouTube upload failed: the video file is empty."}
	}
	return f, nil
}

// newUploadTransport is a transport that bounds every phase of a request but
// the body, so a stalled connection fails while a slow, large upload still
// finishes.
func newUploadTransport() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 2 * time.Minute
	return t
}

// tokenError turns a failed token fetch into a YoutubeError. invalid_grant, and
// only that, is ErrAuthorizationExpired: a wrong client or a broken Google is
// not fixed by sending the user through the consent screen again.
func (u *YoutubeUploader) tokenError(ctx context.Context, err error, secrets []string) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		if re.ErrorCode == "invalid_grant" {
			msg := "YouTube authorization expired: invalid_grant"
			if d := u.scrub(re.ErrorDescription, secrets); d != "" {
				msg += " (" + capText(d, 200) + ")"
			}
			return &YoutubeError{Message: msg, Expired: true, cause: err}
		}
		if errorCode.MatchString(re.ErrorCode) {
			return &YoutubeError{Message: "YouTube token request failed: " + re.ErrorCode, cause: err}
		}
		status := 0
		if re.Response != nil {
			status = re.Response.StatusCode
		}
		return &YoutubeError{Message: fmt.Sprintf("YouTube token request failed: status %d", status), cause: err}
	}
	return &YoutubeError{Message: "YouTube token request failed.", cause: unwrapTransport(ctx, err)}
}

// uploadError turns a failed Do into a YoutubeError.
func (u *YoutubeUploader) uploadError(ctx context.Context, err error, secrets []string) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) { // the access token ran out mid-upload and the refresh failed
		return u.tokenError(ctx, err, secrets)
	}
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		parts := []string{"YouTube upload failed:", fmt.Sprint(ge.Code)}
		if m := u.scrub(ge.Message, secrets); m != "" {
			parts = append(parts, capText(m, MaxErrorBodyBytes))
		}
		parts = append(parts, "body="+capText(u.scrub(ge.Body, secrets), MaxErrorBodyBytes))
		return &YoutubeError{Message: strings.Join(parts, " "), cause: err}
	}
	// A transport error names the URL, which holds the upload session's id.
	return &YoutubeError{Message: "YouTube upload failed: the request could not be completed.", cause: unwrapTransport(ctx, err)}
}

// unwrapTransport returns the cause worth keeping: ctx's error when ctx is
// done, else err without the url.Error layer, which names the URL (for the
// upload, the secret id of the upload session).
func unwrapTransport(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Unwrap()
	}
	return err
}

// credentialKeys are the names whose values are redacted wherever they show up
// in Google's error text, whatever the value looks like.
const credentialKeys = `access_token|refresh_token|id_token|client_secret|client_id|upload_id|api_key|apikey|key|signature|sig|token`

// quotedValue is a JSON-style value, up to the closing quote (an escaped
// character inside it, such as \/, belongs to it).
const quotedValue = `(?:\\[^"']|[^"'\\])*`

var (
	// Shapes of Google credentials, scrubbed even when not known to this process.
	accessTokenShape  = regexp.MustCompile(`ya29\.(?:[A-Za-z0-9._~+=-]|\\?/)+`)
	refreshTokenShape = regexp.MustCompile(`1(?:\\?/){2}(?:[A-Za-z0-9._~+=-]|\\?/)+`)
	bearerShape       = regexp.MustCompile(`(?i)\bbearer[\s\p{Z}]+(?:[A-Za-z0-9._~+=-]|\\?/)+`)
	// key=value (query or "k=v, k=v" style): the value runs to a separator,
	// whitespace or quote.
	keyEqualsValue = regexp.MustCompile(`(?i)\b(` + credentialKeys + `)\b(\\*["']?\s*=\s*(?:\\*["'])?)(?:\\[^"']|[^"'\\\s&,;])*`)
	// "key":"value", 'key': 'value' and the same escaped inside another JSON
	// string (\"key\":\"value\"): the value runs to the closing quote.
	keyColonValue = regexp.MustCompile(`(?i)\b(` + credentialKeys + `)\b(\\*["']?\s*:\s*\\*["'])` + quotedValue)
)

// withoutBackslashes undoes JSON escaping crudely (\/ -> /, \" -> "), so a
// secret that was escaped when Google embedded it in a string still compares equal.
func withoutBackslashes(s string) string { return strings.ReplaceAll(s, `\`, "") }

// scrub makes s one line and replaces every secret in it: the ones this
// uploader holds (and the access token it was given in secrets) wherever they
// appear, JSON-escaped or not; the value of anything named like a credential
// (access_token, upload_id, key, ...) in URL or JSON style; and anything
// shaped like a Google token or an Authorization value.
func (u *YoutubeUploader) scrub(s string, secrets []string) string {
	if len(s) > maxScrubInput {
		s = s[:maxScrubInput]
	}
	s = strings.ToValidUTF8(s, "")
	known := make([]string, 0, 2+len(secrets))
	for _, secret := range append([]string{u.refreshToken, u.clientSecret}, secrets...) {
		if len(secret) >= minSecretLen {
			known = append(known, secret)
		}
	}
	replaceKnown := func(s string) string {
		for _, secret := range known {
			s = strings.ReplaceAll(s, secret, redacted)
		}
		return s
	}
	s = replaceKnown(s)
	// A secret hidden by JSON escapes only shows once they are removed.
	if plain := withoutBackslashes(s); plain != s {
		if replaced := replaceKnown(plain); replaced != plain {
			s = replaced
		}
	}
	s = keyEqualsValue.ReplaceAllString(s, "${1}${2}"+redacted)
	s = keyColonValue.ReplaceAllString(s, "${1}${2}"+redacted)
	s = accessTokenShape.ReplaceAllString(s, redacted)
	s = refreshTokenShape.ReplaceAllString(s, redacted)
	s = bearerShape.ReplaceAllString(s, redacted)
	return strings.Join(strings.Fields(s), " ")
}

// capText cuts s to at most max bytes (on a rune boundary), saying so.
func capText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "...[truncated]"
}
