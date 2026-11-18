package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"serve-html-markdown/internal/execx"
)

const (
	// DefaultDownloadTimeout bounds one video download. Ruby had no limit at all,
	// so a stuck yt-dlp held the job forever.
	DefaultDownloadTimeout = 30 * time.Minute
	// DefaultSubsTimeout bounds one caption fetch (no media is downloaded).
	DefaultSubsTimeout = 3 * time.Minute

	// maxYtdlpStdout caps what is kept of yt-dlp's stdout (two --print lines).
	maxYtdlpStdout = 1 << 20
	// maxYtdlpStderr caps what is kept of its stderr.
	maxYtdlpStderr = 1 << 20
	// maxFailureReason bounds the yt-dlp text carried into an error message.
	maxFailureReason = 4000
)

// YtdlpError is Ruby's YtdlpClient::Error: yt-dlp ran (or was stopped) and did
// not deliver: it exited non-zero, produced no file, timed out or wrote too
// much. The captions job treats it as "not ready yet". A binary that cannot be
// started, or a cancelled context, is a plain error instead, like Ruby's
// Errno::ENOENT. The message never holds cookie contents: it is built from
// yt-dlp's ERROR lines and fixed text only. Err is the underlying cause.
type YtdlpError struct {
	Message string
	Err     error
}

func (e *YtdlpError) Error() string { return e.Message }
func (e *YtdlpError) Unwrap() error { return e.Err }

// ErrInvalidURL is returned, before anything runs, for a URL yt-dlp must not be
// given: not a plain http(s) link, or not one this service ever downloads (see
// YtdlpClient.Download and FetchAutoSubs). Its text is fixed; it never repeats
// the URL. It is a shared value: never modify it.
var ErrInvalidURL = errors.New("yt-dlp: url not allowed")

// YtdlpClient runs yt-dlp. The zero value is not usable; see NewYtdlpClient.
type YtdlpClient struct {
	// Bin is the yt-dlp executable (config YTDLP_BIN).
	Bin string
	// Runner starts the process; tests replace it. NewYtdlpClient sets execx.Run.
	Runner execx.Runner
	// DownloadTimeout and SubsTimeout bound each command.
	DownloadTimeout, SubsTimeout time.Duration
	// ValidateURL is an extra check every URL passes before the shape rules of
	// Download and FetchAutoSubs; nil skips it. NewYtdlpClient sets it to
	// HTTPURLOnly. The http(s)-only rule and those shape rules are always applied.
	ValidateURL func(string) error
}

// NewYtdlpClient returns a client for the yt-dlp at bin that really runs it.
func NewYtdlpClient(bin string) *YtdlpClient {
	return &YtdlpClient{Bin: bin, Runner: execx.Run, DownloadTimeout: DefaultDownloadTimeout, SubsTimeout: DefaultSubsTimeout, ValidateURL: HTTPURLOnly}
}

// HTTPURLOnly accepts only plain "http://" and "https://" URLs (lowercase
// scheme) made of printable ASCII without spaces, so no "ytsearch:" prefix, no
// file:// or other scheme, no option, no control character. It returns
// ErrInvalidURL otherwise.
func HTTPURLOnly(url string) error {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return ErrInvalidURL
	}
	for i := 0; i < len(url); i++ {
		if url[i] <= ' ' || url[i] >= 0x7f {
			return ErrInvalidURL
		}
	}
	return nil
}

// Downloaded is what Download found out: the file yt-dlp wrote and the video's
// title ("" when yt-dlp printed none).
type Downloaded struct {
	Path  string
	Title string
}

// Download fetches the video at url into destDir (created, mode 0700, when
// missing) as "<id>.<ext>", preferring mp4, and returns the absolute path yt-dlp
// printed and the title. It ports YtdlpClient#download, command line included.
// destDir may be relative: it is made absolute first (yt-dlp runs inside it).
//
// The URL must be what the ingest job passes in Ruby, a tweet in its canonical
// form (what NormalizeTwitterURL returns for it), or else a YouTube link
// (YoutubeVideoID finds an id in it); anything else, ytsearch: prefixes and
// other schemes included, is refused with ErrInvalidURL before yt-dlp starts.
func (c *YtdlpClient) Download(ctx context.Context, url, destDir string) (Downloaded, error) {
	if err := c.checkURL(url, func(u string) bool {
		canonical, err := NormalizeTwitterURL(u)
		return (err == nil && canonical == u) || YoutubeVideoID(u) != ""
	}); err != nil {
		return Downloaded{}, err
	}
	destDir, err := filepath.Abs(destDir)
	if err != nil {
		return Downloaded{}, fmt.Errorf("yt-dlp download: %w", err)
	}
	argv := []string{c.Bin,
		"-f", "best[ext=mp4]/best",
		"--no-playlist",
		"-o", outputTemplate(destDir),
		"--print", "after_move:TW2WL_FILE:%(filepath)s",
		"--print", "after_move:TW2WL_TITLE:%(title)s",
		"--newline", url}
	stdout, _, err := c.run(ctx, "download", argv, destDir, c.DownloadTimeout)
	if err != nil {
		return Downloaded{}, err
	}
	path, ok := parsePrefixed(stdout, "TW2WL_FILE:")
	if !ok || path == "" {
		return Downloaded{}, &YtdlpError{Message: "yt-dlp produced no file"}
	}
	if !filepath.IsAbs(path) { // relative to where yt-dlp ran
		path = filepath.Join(destDir, path)
	}
	title, _ := parsePrefixed(stdout, "TW2WL_TITLE:")
	return Downloaded{Path: path, Title: title}, nil
}

// FetchAutoSubs fetches the English captions of the YouTube video at youtubeURL
// into destDir (no media; uploaded captions win over auto-captions) and returns
// the absolute path of the first "*.vtt" in destDir by name, or "" when there
// is none. A non-empty cookies is the path of a Netscape cookies file for
// yt-dlp. destDir and cookies may be relative: both are made absolute first
// (yt-dlp runs inside destDir). It ports YtdlpClient#fetch_auto_subs.
//
// The URL must be the one the captions job passes in Ruby, the watch URL of a
// video id (YoutubeWatchURL), exactly; anything else is refused with
// ErrInvalidURL before yt-dlp starts.
func (c *YtdlpClient) FetchAutoSubs(ctx context.Context, youtubeURL, destDir, cookies string) (string, error) {
	if err := c.checkURL(youtubeURL, func(u string) bool {
		id := YoutubeVideoID(u)
		return id != "" && YoutubeWatchURL(id) == u
	}); err != nil {
		return "", err
	}
	destDir, err := filepath.Abs(destDir)
	if err != nil {
		return "", fmt.Errorf("yt-dlp subs fetch: %w", err)
	}
	if cookies != "" {
		if cookies, err = filepath.Abs(cookies); err != nil {
			return "", fmt.Errorf("yt-dlp subs fetch: %w", err)
		}
	}
	argv := []string{c.Bin,
		"--write-subs",
		"--write-auto-subs",
		"--sub-langs", "en.*",
		"--sub-format", "vtt",
		"--skip-download",
		// Signed in, YouTube can offer no playable format; that must not stop the subs.
		"--ignore-no-formats-error",
		"--no-playlist",
		"-o", outputTemplate(destDir)}
	if cookies != "" {
		argv = append(argv, "--cookies", cookies)
	}
	argv = append(argv, youtubeURL)
	if _, _, err := c.run(ctx, "subs fetch", argv, destDir, c.SubsTimeout); err != nil {
		return "", err
	}
	return firstVtt(destDir)
}

// run starts argv in dir and maps the outcome: nil when yt-dlp succeeded, a
// *YtdlpError when it failed, timed out or wrote too much, a plain error when
// it could not be started or the context ended.
func (c *YtdlpClient) run(ctx context.Context, what string, argv []string, dir string, timeout time.Duration) (stdout, stderr []byte, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("yt-dlp %s: %w", what, err)
	}
	run := c.Runner
	if run == nil {
		run = execx.Run
	}
	stdout, stderr, err = run(ctx, argv, execx.Options{Timeout: timeout, Dir: dir, MaxStdout: maxYtdlpStdout, MaxStderr: maxYtdlpStderr})
	var exitErr *execx.ExitError
	switch {
	case err == nil:
		return stdout, stderr, nil
	case errors.As(err, &exitErr):
		return nil, nil, &YtdlpError{Message: failureMessage("yt-dlp "+what+" failed", stderr), Err: err}
	case errors.Is(err, execx.ErrTimedOut):
		return nil, nil, &YtdlpError{Message: "yt-dlp " + what + " timed out", Err: err}
	case errors.Is(err, execx.ErrOutputTooLarge):
		return nil, nil, &YtdlpError{Message: "yt-dlp " + what + " output was too large", Err: err}
	}
	return nil, nil, fmt.Errorf("yt-dlp %s: %w", what, err)
}

// checkURL refuses what yt-dlp would take for an option instead of the URL
// (the command line stays exactly Ruby's, without a "--" separator), then
// applies ValidateURL, the always-on http(s)-only rule and the method's shape rule.
func (c *YtdlpClient) checkURL(url string, shape func(string) bool) error {
	if url == "" || strings.HasPrefix(url, "-") {
		return ErrInvalidURL
	}
	if c.ValidateURL != nil {
		if err := c.ValidateURL(url); err != nil {
			return ErrInvalidURL
		}
	}
	if HTTPURLOnly(url) != nil || !shape(url) {
		return ErrInvalidURL
	}
	return nil
}

// outputTemplate is the -o value: "<dir>/%(id)s.%(ext)s". A "%" in the
// directory is doubled, or yt-dlp would read it as a template field.
func outputTemplate(destDir string) string {
	return filepath.Join(strings.ReplaceAll(destDir, "%", "%%"), "%(id)s.%(ext)s")
}

// failureMessage explains a failure with yt-dlp's own words: its "ERROR:" lines
// joined by " | ", else the last three non-empty lines of stderr.
func failureMessage(summary string, stderr []byte) string {
	var lines, errorLines []string
	for line := range strings.SplitSeq(strings.ToValidUTF8(string(stderr), "�"), "\n") {
		if line = rubyStrip(line); line == "" {
			continue
		}
		lines = append(lines, line)
		if strings.HasPrefix(line, "ERROR:") {
			errorLines = append(errorLines, line)
		}
	}
	reason := errorLines
	if len(reason) == 0 {
		reason = lines[max(0, len(lines)-3):]
	}
	if len(reason) == 0 {
		return summary
	}
	return summary + ": " + truncate(strings.Join(reason, " | "), maxFailureReason)
}

// truncate cuts s to at most n bytes on a rune boundary, marking the cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// parsePrefixed is the stripped rest of the first stdout line starting with prefix.
func parsePrefixed(stdout []byte, prefix string) (string, bool) {
	for line := range strings.SplitSeq(string(stdout), "\n") {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return rubyStrip(rest), true
		}
	}
	return "", false
}

// firstVtt is the first "*.vtt" file of dir by name ("" when none). A name
// starting with "." does not count, as in Ruby's glob.
func firstVtt(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries { // sorted by name
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".vtt") || e.IsDir() {
			continue
		}
		return filepath.Join(dir, name), nil
	}
	return "", nil
}
