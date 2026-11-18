package video_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"serve-html-markdown/internal/video"
)

// fakeGoogle is both Google endpoints the uploader talks to: the token endpoint
// (/token) and the YouTube upload endpoint (/upload/youtube/v3/videos, in its
// multipart and resumable forms).
type fakeGoogle struct {
	*httptest.Server

	mu sync.Mutex
	// How the token endpoint answers; tokenStatus 0 means 200 with a good token.
	tokenStatus  int
	tokenBody    string
	tokenExpires int // expires_in of good tokens; 0 = 3600
	// tokenReplies, when non-empty, answers the token calls one after the other.
	tokenReplies []tokenReply
	tokenForms   []url.Values

	// How the upload endpoint answers: uploadStatus 0 means success.
	uploadStatus int
	uploadBody   string
	// stallOnUpload makes the upload endpoint hang until the client goes away.
	stallOnUpload bool

	uploads []capturedUpload
}

type tokenReply struct {
	status int
	body   string
}

type capturedUpload struct {
	query       url.Values
	auth        string
	metadata    map[string]any
	media       []byte
	mediaType   string
	chunks      int
	contentType string
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	g := &fakeGoogle{}
	var session capturedUpload
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		switch {
		case r.URL.Path == "/token":
			g.serveToken(w, r)
		case r.URL.Path == "/upload/youtube/v3/videos" && r.Method == http.MethodPost:
			session = g.serveInit(w, r)
		case r.URL.Path == "/session" && (r.Method == http.MethodPut || r.Method == http.MethodPost):
			g.serveChunk(w, r, &session)
		default:
			t.Logf("fake google: unexpected %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *fakeGoogle) serveToken(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(b))
	g.tokenForms = append(g.tokenForms, form)
	w.Header().Set("Content-Type", "application/json")
	if len(g.tokenReplies) > 0 {
		reply := g.tokenReplies[0]
		g.tokenReplies = g.tokenReplies[1:]
		w.WriteHeader(reply.status)
		_, _ = io.WriteString(w, reply.body)
		return
	}
	if g.tokenStatus != 0 {
		w.WriteHeader(g.tokenStatus)
		_, _ = io.WriteString(w, g.tokenBody)
		return
	}
	exp := g.tokenExpires
	if exp == 0 {
		exp = 3600
	}
	fmt.Fprintf(w, `{"access_token":"at-%d","token_type":"Bearer","expires_in":%d}`, len(g.tokenForms), exp)
}

// stall holds the request open until the client goes away (or 5 s pass).
func stall(r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body) // so the server notices the client leaving
	select {
	case <-r.Context().Done():
	case <-time.After(5 * time.Second):
	}
}

func (g *fakeGoogle) fail(w http.ResponseWriter, r *http.Request) bool {
	if g.stallOnUpload {
		stall(r)
		return true
	}
	if g.uploadStatus != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(g.uploadStatus)
		_, _ = io.WriteString(w, g.uploadBody)
		return true
	}
	return false
}

// serveInit takes the first request of an upload: the whole thing for a
// multipart upload, the metadata only for a resumable one.
func (g *fakeGoogle) serveInit(w http.ResponseWriter, r *http.Request) capturedUpload {
	up := capturedUpload{query: r.URL.Query(), auth: r.Header.Get("Authorization")}
	body, _ := io.ReadAll(r.Body)
	switch up.query.Get("uploadType") {
	case "multipart":
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err == nil {
			mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
			if p, err := mr.NextPart(); err == nil {
				b, _ := io.ReadAll(p)
				_ = json.Unmarshal(b, &up.metadata)
			}
			if p, err := mr.NextPart(); err == nil {
				up.media, _ = io.ReadAll(p)
				up.mediaType = p.Header.Get("Content-Type")
			}
		}
		up.chunks = 1
		g.uploads = append(g.uploads, up)
		if g.fail(w, r) {
			return up
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"kind":"youtube#video","id":"YT123"}`)
	case "resumable":
		_ = json.Unmarshal(body, &up.metadata)
		up.mediaType = r.Header.Get("X-Upload-Content-Type")
		g.uploads = append(g.uploads, up)
		if g.fail(w, r) {
			return up
		}
		w.Header().Set("Location", "http://"+r.Host+"/session")
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "bad uploadType", http.StatusBadRequest)
	}
	return up
}

func (g *fakeGoogle) serveChunk(w http.ResponseWriter, r *http.Request, session *capturedUpload) {
	idx := len(g.uploads) - 1
	if idx < 0 {
		http.NotFound(w, r)
		return
	}
	if g.stallOnUpload {
		stall(r)
		return
	}
	chunk, _ := io.ReadAll(r.Body)
	up := &g.uploads[idx]
	up.chunks++
	up.media = append(up.media, chunk...)
	up.contentType = r.Header.Get("Content-Type")
	// Content-Range: bytes 0-262143/700000
	// "bytes 0-262143/*" for a chunk that is not the last, "bytes a-b/<total>" for the last.
	cr := r.Header.Get("Content-Range")
	total := 0
	if i := strings.LastIndex(cr, "/"); i >= 0 {
		total, _ = strconv.Atoi(cr[i+1:])
	}
	if strings.HasSuffix(cr, "/*") || len(up.media) < total {
		w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(up.media)-1))
		// 308 Resume Incomplete, as Google answers a client sending X-GUploader-No-308.
		w.Header().Set("X-Http-Status-Code-Override", "308")
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"kind":"youtube#video","id":"YT123"}`)
}

func (g *fakeGoogle) snapshot() (uploads []capturedUpload, tokenCalls int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]capturedUpload(nil), g.uploads...), len(g.tokenForms)
}

func (g *fakeGoogle) uploaderFor(t *testing.T) *video.YoutubeUploader {
	t.Helper()
	u, err := video.NewYoutubeUploader("cid", "client-secret-xyz", "1//refresh-token-abc")
	if err != nil {
		t.Fatal(err)
	}
	u.TokenURL = g.URL + "/token"
	u.APIEndpoint = g.URL
	u.HTTPClient = g.Client()
	return u
}

func writeVideo(t *testing.T, size int) (path string, content []byte) {
	t.Helper()
	content = make([]byte, size)
	for i := range content {
		content[i] = byte(i*7 + i/251)
	}
	path = filepath.Join(t.TempDir(), "x.mp4")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, content
}

func uploadError(t *testing.T, err error) *video.YoutubeError {
	t.Helper()
	var ye *video.YoutubeError
	if !errors.As(err, &ye) {
		t.Fatalf("err = %v (%T), want *video.YoutubeError", err, err)
	}
	return ye
}

// Ruby: "uploads unlisted and returns the video id".
func TestUploaderUploadsUnlistedAndReturnsTheVideoID(t *testing.T) {
	g := newFakeGoogle(t)
	path, content := writeVideo(t, 1000)

	id, err := g.uploaderFor(t).Upload(context.Background(), path, "Tweet by foo", "d")
	if err != nil || id != "YT123" {
		t.Fatalf("id = %q, err = %v", id, err)
	}
	uploads, tokenCalls := g.snapshot()
	if len(uploads) != 1 || tokenCalls != 1 {
		t.Fatalf("uploads = %d, token calls = %d", len(uploads), tokenCalls)
	}
	up := uploads[0]
	if strings.Join(up.query["part"], ",") != "snippet,status" {
		t.Errorf("part = %q", up.query.Get("part"))
	}
	if up.query.Has("notifySubscribers") {
		t.Errorf("notifySubscribers was set: %v", up.query)
	}
	snippet, _ := up.metadata["snippet"].(map[string]any)
	status, _ := up.metadata["status"].(map[string]any)
	if status["privacyStatus"] != "unlisted" || snippet["title"] != "Tweet by foo" || snippet["description"] != "d" {
		t.Errorf("metadata = %v", up.metadata)
	}
	if len(snippet) != 2 || len(status) != 1 || len(up.metadata) != 2 {
		t.Errorf("extra metadata fields: %v", up.metadata)
	}
	if !bytes.Equal(up.media, content) || up.mediaType != "video/*" {
		t.Errorf("media: %d bytes (want %d), type %q", len(up.media), len(content), up.mediaType)
	}
	if up.auth != "Bearer at-1" {
		t.Errorf("Authorization = %q", up.auth)
	}
}

func TestUploaderSendsTheRefreshGrantToTheTokenEndpoint(t *testing.T) {
	g := newFakeGoogle(t)
	path, _ := writeVideo(t, 10)
	if _, err := g.uploaderFor(t).Upload(context.Background(), path, "t", ""); err != nil {
		t.Fatal(err)
	}
	form := g.tokenForms[0]
	for key, want := range map[string]string{
		"grant_type": "refresh_token", "refresh_token": "1//refresh-token-abc",
		"client_id": "cid", "client_secret": "client-secret-xyz",
	} {
		if form.Get(key) != want {
			t.Errorf("token form %s = %q, want %q", key, form.Get(key), want)
		}
	}
}

// A file bigger than a chunk goes up as a resumable upload, chunk by chunk.
func TestUploaderStreamsALargeFileInChunks(t *testing.T) {
	g := newFakeGoogle(t)
	path, content := writeVideo(t, 700*1024)
	u := g.uploaderFor(t)
	u.ChunkSize = 256 * 1024

	id, err := u.Upload(context.Background(), path, "Big", "")
	if err != nil || id != "YT123" {
		t.Fatalf("id = %q, err = %v", id, err)
	}
	uploads, _ := g.snapshot()
	up := uploads[0]
	if up.chunks != 3 || !bytes.Equal(up.media, content) || up.mediaType != "video/*" {
		t.Errorf("chunks = %d, media %d bytes (want %d), type %q", up.chunks, len(up.media), len(content), up.mediaType)
	}
	if up.auth != "Bearer at-1" {
		t.Errorf("Authorization = %q", up.auth)
	}
}

// Ruby: "raises on blank config" and "a blank client id stays a plain
// configuration error".
func TestUploaderRejectsBlankClientCredentials(t *testing.T) {
	for _, c := range [][3]string{{"", "", ""}, {"", "s", "r"}, {"c", "", "r"}, {" ", "s", "r"}, {"c", "\n", "r"}} {
		u, err := video.NewYoutubeUploader(c[0], c[1], c[2])
		if u != nil || err == nil {
			t.Fatalf("%q: %v, %v", c, u, err)
		}
		ye := uploadError(t, err)
		if ye.Message != "YouTube OAuth credentials are not configured." || errors.Is(err, video.ErrAuthorizationExpired) {
			t.Errorf("%q: err = %v", c, err)
		}
		if !errors.Is(err, video.ErrYoutube) {
			t.Errorf("%q: not an ErrYoutube", c)
		}
	}
}

// Ruby: "a blank refresh token is an expired authorization, not a config error".
func TestUploaderBlankRefreshTokenIsAnExpiredAuthorization(t *testing.T) {
	for _, rt := range []string{"", "  \n"} {
		u, err := video.NewYoutubeUploader("c", "s", rt)
		if u != nil || !errors.Is(err, video.ErrAuthorizationExpired) || !errors.Is(err, video.ErrYoutube) {
			t.Fatalf("%q: %v, %v", rt, u, err)
		}
		if err.Error() != "No YouTube refresh token is stored." {
			t.Errorf("message = %q", err.Error())
		}
	}
}

// Ruby: "translates a dead refresh token into AuthorizationExpired" and
// "translates a bare Signet authorization error too". The upload endpoint is
// never reached, nor is the file opened (the path does not exist).
func TestUploaderTranslatesADeadRefreshTokenIntoAuthorizationExpired(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"described": {400, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`, "YouTube authorization expired: invalid_grant (Token has been expired or revoked.)"},
		"bare":      {401, `{"error":"invalid_grant"}`, "YouTube authorization expired: invalid_grant"},
	} {
		g := newFakeGoogle(t)
		g.tokenStatus, g.tokenBody = tc.status, tc.body
		_, err := g.uploaderFor(t).Upload(context.Background(), "/tmp/does-not-exist.mp4", "t", "")
		if !errors.Is(err, video.ErrAuthorizationExpired) || !errors.Is(err, video.ErrYoutube) {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		if err.Error() != tc.want {
			t.Errorf("%s: message = %q, want %q", name, err.Error(), tc.want)
		}
		if ups, _ := g.snapshot(); len(ups) != 0 {
			t.Errorf("%s: the upload endpoint was reached", name)
		}
	}
}

// Only a dead grant is "expired": a wrong client or a broken Google is not
// fixed by sending the user through the consent screen.
func TestUploaderOtherTokenFailuresAreNotAuthorizationExpired(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"invalid client": {401, `{"error":"invalid_client","error_description":"The OAuth client was not found."}`, "YouTube token request failed: invalid_client"},
		"html 502":       {502, `<html>client-secret-xyz</html>`, "YouTube token request failed: status 502"},
		"odd code":       {400, `{"error":"1//refresh-token-abc"}`, "YouTube token request failed: status 400"},
	} {
		g := newFakeGoogle(t)
		g.tokenStatus, g.tokenBody = tc.status, tc.body
		_, err := g.uploaderFor(t).Upload(context.Background(), "/tmp/x.mp4", "t", "")
		if errors.Is(err, video.ErrAuthorizationExpired) || !errors.Is(err, video.ErrYoutube) || err.Error() != tc.want {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
		for _, secret := range []string{"client-secret-xyz", "refresh-token-abc"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: error leaks %q", name, secret)
			}
		}
	}
}

func TestUploaderTokenEndpointTimeoutIsBoundedAndGeneric(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { stall(r) }))
	t.Cleanup(slow.Close)
	u, _ := video.NewYoutubeUploader("c", "s", "r")
	u.TokenURL = slow.URL + "/token"
	u.HTTPClient = &http.Client{Timeout: 100 * time.Millisecond}
	start := time.Now()
	_, err := u.Upload(context.Background(), "/tmp/x.mp4", "t", "")
	if time.Since(start) > 2*time.Second || !errors.Is(err, video.ErrYoutube) || errors.Is(err, video.ErrAuthorizationExpired) {
		t.Errorf("took %v, err = %v", time.Since(start), err)
	}
	if strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("error names the endpoint: %v", err)
	}
}

func TestUploaderDefaultsAreBounded(t *testing.T) {
	u, _ := video.NewYoutubeUploader("c", "s", "r")
	if u.TokenURL != video.GoogleTokenURL || u.ChunkSize != video.DefaultUploadChunkSize || u.ChunkSize%(256*1024) != 0 {
		t.Errorf("TokenURL = %q, ChunkSize = %d", u.TokenURL, u.ChunkSize)
	}
	if u.ChunkSize > 64<<20 {
		t.Errorf("a chunk is held in memory; %d is too large", u.ChunkSize)
	}
}

// A grant that dies during a long upload (the access token expires and the
// refresh fails) is an expired authorization too.
func TestUploaderAGrantDyingMidUploadIsAuthorizationExpired(t *testing.T) {
	g := newFakeGoogle(t)
	// The first access token is born expired, so every request refreshes; the
	// second refresh is refused.
	g.tokenReplies = []tokenReply{
		{200, `{"access_token":"at-1","token_type":"Bearer","expires_in":1}`},
		{400, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`},
	}
	path, _ := writeVideo(t, 10)
	_, err := g.uploaderFor(t).Upload(context.Background(), path, "t", "")
	if !errors.Is(err, video.ErrAuthorizationExpired) {
		t.Fatalf("err = %v", err)
	}
	if ups, _ := g.snapshot(); len(ups) != 0 {
		t.Errorf("an upload went through without a token: %d", len(ups))
	}
}

// Ruby: the Google::Apis::Error branch: "YouTube upload failed: ...".
func TestUploaderWrapsAPIErrorsWithStatusMessageAndBody(t *testing.T) {
	g := newFakeGoogle(t)
	g.uploadStatus = 403
	g.uploadBody = `{"error":{"code":403,"message":"The request cannot be completed because you have exceeded your quota.","errors":[{"reason":"quotaExceeded"}]}}`
	path, _ := writeVideo(t, 10)
	_, err := g.uploaderFor(t).Upload(context.Background(), path, "t", "")
	ye := uploadError(t, err)
	if errors.Is(err, video.ErrAuthorizationExpired) {
		t.Error("a 403 quota error is not an expired authorization")
	}
	const prefix = "YouTube upload failed: 403 The request cannot be completed because you have exceeded your quota. body="
	if !strings.HasPrefix(ye.Message, prefix) || !strings.Contains(ye.Message, `"reason":"quotaExceeded"`) {
		t.Errorf("message = %q", ye.Message)
	}
	if strings.ContainsAny(ye.Message, "\n\r") {
		t.Errorf("message spans lines: %q", ye.Message)
	}
}

// The body ends up in Slack and logs: capped, and no secret survives in it.
func TestUploaderErrorBodyIsCappedAndScrubbed(t *testing.T) {
	g := newFakeGoogle(t)
	g.uploadStatus = 400
	g.uploadBody = `{"error":{"code":400,"message":"bad token at-1 and 1//refresh-token-abc for client-secret-xyz",` +
		`"debug":"Authorization: Bearer ya29.A0AfH6SMBx-secret_tail"}}` + strings.Repeat(" padding", 1000)
	path, _ := writeVideo(t, 10)
	_, err := g.uploaderFor(t).Upload(context.Background(), path, "t", "")
	ye := uploadError(t, err)
	for _, secret := range []string{"at-1", "refresh-token-abc", "client-secret-xyz", "ya29.", "secret_tail"} {
		if strings.Contains(ye.Message, secret) {
			t.Errorf("message leaks %q: %s", secret, ye.Message)
		}
	}
	if !strings.Contains(ye.Message, "[REDACTED]") {
		t.Errorf("nothing was redacted: %s", ye.Message)
	}
	if i := strings.Index(ye.Message, "body="); i < 0 || len(ye.Message)-i > video.MaxErrorBodyBytes+len("body=")+len("...[truncated]") {
		t.Errorf("body not capped: %d bytes of message", len(ye.Message))
	}
	if !strings.HasSuffix(ye.Message, "[truncated]") {
		t.Errorf("a truncated body should say so: ...%s", ye.Message[len(ye.Message)-40:])
	}
}

// A refresh that fails mid-upload with something other than invalid_grant must
// not echo the URL of the (secret) upload session or the token.
func TestUploaderTransportErrorsAreGeneric(t *testing.T) {
	g := newFakeGoogle(t)
	u := g.uploaderFor(t)
	path, _ := writeVideo(t, 10)
	// Token works, then the API endpoint is gone.
	u.APIEndpoint = "http://127.0.0.1:1"
	_, err := u.Upload(context.Background(), path, "t", "")
	ye := uploadError(t, err)
	if !strings.HasPrefix(ye.Message, "YouTube upload failed") {
		t.Errorf("message = %q", ye.Message)
	}
	for _, leak := range []string{"127.0.0.1", "uploadType", "at-1", "refresh-token-abc", "client-secret-xyz"} {
		if strings.Contains(ye.Message, leak) {
			t.Errorf("message leaks %q: %s", leak, ye.Message)
		}
	}
}

// The upload honours ctx: cancelling it ends a stuck upload promptly and the
// cancellation stays visible.
func TestUploaderHonoursContextCancellation(t *testing.T) {
	g := newFakeGoogle(t)
	g.stallOnUpload = true
	path, _ := writeVideo(t, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := g.uploaderFor(t).Upload(ctx, path, "t", "")
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, video.ErrYoutube) {
		t.Errorf("err = %v", err)
	}
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if _, err := g.uploaderFor(t).Upload(cctx, path, "t", ""); !errors.Is(err, context.Canceled) {
		t.Errorf("pre-cancelled: err = %v", err)
	}
}

// Only a regular file is uploaded: not a directory, not a FIFO (which would
// block forever on open), and nothing is sent to YouTube for them.
func TestUploaderRefusesNonRegularFiles(t *testing.T) {
	g := newFakeGoogle(t)
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe.mp4")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no mkfifo: %v", err)
	}
	for name, path := range map[string]string{
		"directory": dir, "fifo": fifo, "missing": filepath.Join(dir, "nope.mp4"), "blank": "", "device": "/dev/null",
	} {
		done := make(chan error, 1)
		go func() {
			_, err := g.uploaderFor(t).Upload(context.Background(), path, "t", "")
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, video.ErrYoutube) || errors.Is(err, video.ErrAuthorizationExpired) {
				t.Errorf("%s: err = %v", name, err)
			}
			if strings.Contains(err.Error(), dir) {
				t.Errorf("%s: error names the path: %v", name, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s: Upload blocked", name)
		}
	}
	if ups, _ := g.snapshot(); len(ups) != 0 {
		t.Errorf("%d uploads went out for non-regular files", len(ups))
	}
}

// A symlink to a regular file is fine (the file is what is checked, after open).
func TestUploaderFollowsASymlinkToARegularFile(t *testing.T) {
	g := newFakeGoogle(t)
	path, _ := writeVideo(t, 100)
	link := filepath.Join(t.TempDir(), "link.mp4")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if id, err := g.uploaderFor(t).Upload(context.Background(), link, "t", ""); err != nil || id != "YT123" {
		t.Errorf("id = %q, err = %v", id, err)
	}
}

// The authenticated calls follow no redirect, so a bearer token never travels on.
func TestUploaderDoesNotFollowRedirects(t *testing.T) {
	hits := 0
	var mu sync.Mutex
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		_, _ = io.WriteString(w, `{"id":"stolen"}`)
	}))
	t.Cleanup(other.Close)
	g := newFakeGoogle(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(api.Close)
	u := g.uploaderFor(t)
	u.APIEndpoint = api.URL
	u.HTTPClient = nil // the default client: what runs in production
	u.TokenURL = g.URL + "/token"
	path, _ := writeVideo(t, 10)
	_, err := u.Upload(context.Background(), path, "t", "")
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Errorf("the redirect was followed (%d hits)", hits)
	}
	if !errors.Is(err, video.ErrYoutube) {
		t.Errorf("err = %v", err)
	}
}
