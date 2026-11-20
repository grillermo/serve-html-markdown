package web

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/files"
)

const fetchTimeout = 30 * time.Second

// leadingUUID is the id files.chiq.me puts before a file's own name.
var leadingUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}-`)

// WithFetchClient replaces the HTTP client GET /new downloads with (tests).
func WithFetchClient(c *http.Client) Option {
	return func(s *Server) { s.fetchClient = c }
}

// newFetchClient refuses to connect to loopback, private and link-local
// addresses, so /new cannot be pointed at the machine's own network.
func newFetchClient() *http.Client {
	d := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
				ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
				return errors.New("address not allowed")
			}
			return nil
		},
	}
	return &http.Client{
		Timeout:   fetchTimeout,
		Transport: &http.Transport{DialContext: d.DialContext, Proxy: nil},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

// sourceURL extracts the URL pasted after "/new?". The raw query is taken as
// is, so a pasted URL needs no escaping: its own "?", "&", "=" and "#" survive
// (Go's r.URL.Query() would split it at the first "&"). "?url=<URL>" works too,
// and a fully percent-encoded URL ("https%3A%2F%2F...") is decoded.
func sourceURL(r *http.Request) string {
	raw := r.URL.RawQuery
	if rest, ok := strings.CutPrefix(raw, "url="); ok {
		raw = rest
		if dec, err := url.QueryUnescape(raw); err == nil && strings.Contains(dec, "://") {
			raw = dec
		}
	}
	if !strings.Contains(raw, "://") {
		if dec, err := url.QueryUnescape(raw); err == nil {
			raw = dec
		}
	}
	return strings.TrimSpace(raw)
}

// newFromURL is GET /new?<url>: download the page and store it as a new file,
// then redirect to it. Allowed with a session or the bearer token.
func (s *Server) newFromURL(w http.ResponseWriter, r *http.Request) {
	if !auth.BearerOK(r, s.cfg.APIToken) {
		user, err := s.sessionUser(r)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if user == nil {
			s.unauthenticated(w, r)
			return
		}
	}
	extendDeadlines(w, 0, fetchTimeout+writeSlack)

	u, err := url.Parse(sourceURL(r))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		writeDetail(w, http.StatusBadRequest, "Usage: /new?https://host/path/file.html")
		return
	}
	// Name and type come from the URL's file name; the stored extension must be
	// one the server shows.
	base := leadingUUID.ReplaceAllString(path.Base(u.Path), "")
	ext, err := files.UploadExtension(base)
	if err == nil {
		_, err = files.Stem(base)
	}
	if err != nil {
		s.fileError(w, r, err)
		return
	}
	limit := s.maxRenderBytes
	if ext == ".html" {
		limit = s.maxHTMLBytes
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		writeDetail(w, http.StatusBadRequest, "Invalid URL.")
		return
	}
	resp, err := s.fetchClient.Do(req)
	if err != nil {
		writeDetail(w, http.StatusBadGateway, "Could not download that URL.")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeDetail(w, http.StatusBadGateway, fmt.Sprintf("The URL answered %d.", resp.StatusCode))
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		writeDetail(w, http.StatusBadGateway, "Could not download that URL.")
		return
	}
	if int64(len(data)) > limit {
		writeDetail(w, http.StatusRequestEntityTooLarge, msgFileTooLarge)
		return
	}
	if !utf8.Valid(data) {
		writeDetail(w, http.StatusBadRequest, msgNotUTF8)
		return
	}
	p, err := files.CreateUnique(s.cfg.FilesDir, base, ext, data)
	if err != nil {
		s.fileError(w, r, err)
		return
	}
	name := filepath.Base(p)
	if s.recordCreated(w, r, name) {
		http.Redirect(w, r, "/"+url.PathEscape(name), http.StatusSeeOther)
	}
}
