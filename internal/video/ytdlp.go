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

// YtdlpClient runs yt-dlp. The zero value is not usable; see NewYtdlpClient.
type YtdlpClient struct {
	// Bin is the yt-dlp executable (config YTDLP_BIN).
	Bin string
	// Runner starts the process; tests replace it. NewYtdlpClient sets execx.Run.
	Runner execx.Runner
	// DownloadTimeout and SubsTimeout bound each command.
	DownloadTimeout, SubsTimeout time.Duration
}

// NewYtdlpClient returns a client for the yt-dlp at bin that really runs it.
func NewYtdlpClient(bin string) *YtdlpClient {
	return &YtdlpClient{Bin: bin, Runner: execx.Run, DownloadTimeout: DefaultDownloadTimeout, SubsTimeout: DefaultSubsTimeout}
}

// Downloaded is what Download found out: the file yt-dlp wrote and the video's
// title ("" when yt-dlp printed none).
type Downloaded struct {
	Path  string
	Title string
}

// Download fetches the video at url into destDir (created, mode 0700, when
// missing) as "<id>.<ext>", preferring mp4, and returns the path yt-dlp printed
// and the title. It ports YtdlpClient#download, command line included.
func (c *YtdlpClient) Download(ctx context.Context, url, destDir string) (Downloaded, error) {
	if err := checkURL(url); err != nil {
		return Downloaded{}, err
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
	title, _ := parsePrefixed(stdout, "TW2WL_TITLE:")
	return Downloaded{Path: path, Title: title}, nil
}

// FetchAutoSubs fetches the English captions of the YouTube video at youtubeURL
// into destDir (no media; uploaded captions win over auto-captions) and returns
// the path of the first "*.vtt" in destDir by name, or "" when there is none.
// A non-empty cookies is the path of a Netscape cookies file for yt-dlp. It
// ports YtdlpClient#fetch_auto_subs.
func (c *YtdlpClient) FetchAutoSubs(ctx context.Context, youtubeURL, destDir, cookies string) (string, error) {
	if err := checkURL(youtubeURL); err != nil {
		return "", err
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

// checkURL refuses what yt-dlp would take for an option instead of the URL.
// (The command line stays exactly Ruby's, without a "--" separator.)
func checkURL(url string) error {
	if url == "" || strings.HasPrefix(url, "-") {
		return errors.New("yt-dlp: refusing an empty or option-like url")
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
