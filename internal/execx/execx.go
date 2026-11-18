// Package execx runs external commands for the services that shell out
// (yt-dlp, the ego cookie export): argv only (never a shell), no stdin, its own
// process group that is killed whole on timeout or cancellation, capped
// output, an allowlisted environment and a scratch working directory.
//
// It is a leaf package. internal/llm keeps its own, older copy of this logic
// (its tests are tied to its unexported names); the two are kept in step by hand.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const (
	// DefaultMaxStdout caps captured stdout unless Options.MaxStdout says otherwise.
	DefaultMaxStdout = 16 << 20
	// DefaultMaxStderr caps captured stderr unless Options.MaxStderr says otherwise.
	DefaultMaxStderr = 1 << 20
)

var (
	// ErrTimedOut is wrapped by Run's error when Options.Timeout passed.
	ErrTimedOut = errors.New("command timed out")
	// ErrOutputTooLarge is wrapped by Run's error when stdout passed its cap.
	ErrOutputTooLarge = errors.New("command output too large")
)

// ExitError means the process ran and exited non-zero (or was killed by a
// signal, then Code is -1). Its text carries the code only, never any output.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// Options tunes one Run.
type Options struct {
	// Timeout bounds the whole command; zero means only the context bounds it.
	Timeout time.Duration
	// Dir is the working directory. Empty means a fresh empty temporary
	// directory that is removed afterwards (never the caller's own directory,
	// which for the server holds its dotenv file).
	Dir string
	// Env is the whole environment. Nil means SafeEnv(os.Environ(), ExtraEnv...).
	Env []string
	// ExtraEnv widens the default allowlist for this command (it is ignored
	// when Env is set): each entry is an exact variable name or, ending in "*",
	// a name prefix ("FOO_*"). A bare "*" is ignored: passing the whole
	// environment on is what Env is for, and it must be spelled out.
	ExtraEnv []string
	// MaxStdout and MaxStderr cap what is kept; zero means the defaults.
	// Stdout past its cap kills the command and fails with ErrOutputTooLarge;
	// stderr past its cap is dropped silently.
	MaxStdout, MaxStderr int
}

// Runner is the seam services use so tests can fake the process: it starts
// argv and returns what the process wrote. A non-nil error means the command
// did not succeed: *ExitError for a non-zero exit (stdout and stderr are
// still returned), ErrTimedOut, ErrOutputTooLarge, a context error, or a start
// failure (not found, not executable, E2BIG...).
type Runner func(ctx context.Context, argv []string, opts Options) (stdout, stderr []byte, err error)

// Run is the real Runner. It runs argv directly (no shell) with no stdin, in
// its own process group, and kills that whole group when Options.Timeout
// passes, the context ends or stdout exceeds its cap, so children of the
// command never linger.
func Run(parent context.Context, argv []string, opts Options) ([]byte, []byte, error) {
	if len(argv) == 0 || argv[0] == "" {
		return nil, nil, errors.New("execx: empty command")
	}
	ctx := parent
	if opts.Timeout > 0 {
		var cancelTimeout context.CancelFunc
		ctx, cancelTimeout = context.WithTimeout(ctx, opts.Timeout)
		defer cancelTimeout()
	}
	timeoutCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	dir := opts.Dir
	if dir == "" {
		scratch, err := os.MkdirTemp("", "execx-")
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", argv[0], err)
		}
		defer os.RemoveAll(scratch)
		dir = scratch
	}
	env := opts.Env
	if env == nil {
		env = SafeEnv(os.Environ(), opts.ExtraEnv...)
	}
	maxOut, maxErr := opts.MaxStdout, opts.MaxStderr
	if maxOut <= 0 {
		maxOut = DefaultMaxStdout
	}
	if maxErr <= 0 {
		maxErr = DefaultMaxStderr
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = nil
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// The leader has not been reaped yet, so its pid is still the group's.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
	// A grandchild that keeps the pipes open must not hold Wait forever.
	cmd.WaitDelay = 2 * time.Second
	out := &capWriter{limit: maxOut, fail: true, cancel: cancel}
	errb := &capWriter{limit: maxErr}
	cmd.Stdout, cmd.Stderr = out, errb

	err := cmd.Run()
	killLeftovers(cmd)
	switch {
	case out.exceeded:
		return nil, nil, fmt.Errorf("%s: %w", argv[0], ErrOutputTooLarge)
	case err != nil && parent.Err() != nil:
		return nil, nil, fmt.Errorf("%s: %w", argv[0], parent.Err())
	case err != nil && opts.Timeout > 0 && timeoutCtx.Err() != nil:
		return nil, nil, fmt.Errorf("%s: %w", argv[0], ErrTimedOut)
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		// The process itself succeeded; a grandchild still held the pipes open.
		err = nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		err = &ExitError{Code: exitErr.ExitCode()}
	}
	return out.buf.Bytes(), errb.buf.Bytes(), err
}

// capWriter keeps at most limit bytes. Past it, a failing writer cancels the
// command and errors (the process gets SIGPIPE/SIGKILL), a plain one discards.
type capWriter struct {
	buf      bytes.Buffer
	limit    int
	fail     bool
	cancel   context.CancelFunc
	exceeded bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.limit - w.buf.Len(); len(p) <= room {
		return w.buf.Write(p)
	} else if room > 0 {
		w.buf.Write(p[:room])
	}
	if w.fail {
		w.exceeded = true
		w.cancel()
		return 0, ErrOutputTooLarge
	}
	return len(p), nil
}

// killLeftovers kills whatever is still in the command's process group once it
// has been waited for (yt-dlp must not leave an ffmpeg or a daemon behind). A
// group that is already empty answers ESRCH, which is fine. The leader is
// already reaped here, so in theory its pid could have been handed to a new
// process that made itself a group leader in the same instant; the window is a
// few microseconds and the signal then lands in a process of this server's own.
func killLeftovers(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// The environment a command may inherit: locale, terminal, home and cache
// locations, proxy and certificate settings, and what yt-dlp and the Python
// and Deno runtimes behind it read. Everything else (DATABASE_*,
// SECRET_KEY_BASE, API_TOKEN, *_API_KEY, SLACK_*, YOUTUBE_*, RULINKY_*, ADMIN_*
// and the rest of the server's secrets) is left out unless a caller asks for it
// with Options.ExtraEnv.
var (
	envNames = map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "LANG": true,
		"TERM": true, "TMPDIR": true, "TZ": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
		"http_proxy": true, "https_proxy": true, "no_proxy": true, "all_proxy": true,
		"REQUESTS_CA_BUNDLE": true, "CURL_CA_BUNDLE": true, "NODE_EXTRA_CA_CERTS": true,
		"DENO_DIR": true, "DENO_INSTALL": true, "VIRTUAL_ENV": true,
	}
	envPrefixes = []string{"LC_", "XDG_", "YTDLP_", "YT_DLP_", "PYTHON"}
)

// SafeEnv filters environ (KEY=value entries, as os.Environ returns them) down
// to the allowlist plus extra (exact names, or prefixes written with a trailing
// "*"), keeping their order.
func SafeEnv(environ []string, extra ...string) []string {
	exact := map[string]bool{}
	prefixes := append([]string(nil), envPrefixes...)
	for _, e := range extra {
		switch prefix, isPrefix := strings.CutSuffix(e, "*"); {
		case e == "" || e == "*":
		case isPrefix:
			prefixes = append(prefixes, prefix)
		default:
			exact[e] = true
		}
	}
	var env []string
	for _, kv := range environ {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			continue
		}
		if envNames[key] || exact[key] {
			env = append(env, kv)
			continue
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(key, prefix) {
				env = append(env, kv)
				break
			}
		}
	}
	return env
}
