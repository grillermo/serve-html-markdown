package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	// CommandTimeout bounds each CLI call (claude, then codex).
	CommandTimeout = 120 * time.Second
	// maxCommandOutput caps the stdout and the codex output file that are read,
	// so a runaway process cannot exhaust memory.
	maxCommandOutput = 16 << 20
	// maxCommandStderr caps what is kept of stderr (which is never used).
	maxCommandStderr = 1 << 20
)

// ErrPromptTooLarge is returned, before anything is started, for a prompt that
// would not fit in the CLIs' argument list. The OpenAI path has no such limit.
// It is a shared value: never modify it. The processor turns it into a
// user-facing error.
var ErrPromptTooLarge error = &ExpandError{Message: "Document is too large to expand."}

// MaxPromptArgBytes is the largest prompt passed to claude or codex as an
// argument. The OS limits the whole argument list plus environment: macOS to
// 1 MiB (about 1,042,000 bytes of one argument measured with a 5 KB environment),
// Linux to 128 KiB per argument. The limit is set well under those.
var MaxPromptArgBytes = promptArgLimit(runtime.GOOS)

func promptArgLimit(goos string) int {
	if goos == "darwin" {
		return 900_000
	}
	return 120_000
}

// Runner starts argv (no shell) and returns what the process wrote. A non-nil
// error means it failed: it could not start, exited non-zero, timed out or
// wrote too much. The error text is never shown to users.
type Runner func(ctx context.Context, argv []string) (stdout, stderr []byte, err error)

// commandError turns a Runner error into the generic ExpandError of the
// command; the error text itself (which may hold paths or output) is dropped.
func commandError(name string, err error) error {
	var exitErr *exec.ExitError
	var execErr *exec.Error
	var pathErr *fs.PathError
	var errno syscall.Errno
	switch {
	case errors.Is(err, errTimedOut):
		return expandErr(fmt.Sprintf("%s timed out after %ds", name, int(CommandTimeout/time.Second)), err)
	case errors.Is(err, errOutputTooLarge):
		return expandErr(name+" output was too large", err)
	case errors.Is(err, syscall.E2BIG):
		return expandErr(name+" CLI could not be started: the prompt is too large", err)
	case errors.As(err, &exitErr):
		return expandErr(name+" CLI failed", err)
	case errors.As(err, &execErr), errors.As(err, &pathErr), errors.As(err, &errno):
		return expandErr(name+" CLI could not be started", err)
	}
	return expandErr(name+" CLI failed", err)
}

var (
	errTimedOut       = errors.New("command timed out")
	errOutputTooLarge = errors.New("command output too large")
)

var execRunner = newExecRunner(CommandTimeout)

// newExecRunner returns the real Runner: it runs argv directly (no shell) with
// no stdin, in its own process group, and kills that whole group when timeout
// passes, the context ends or stdout exceeds maxCommandOutput, so claude's and
// codex's children never linger.
//
// The process gets the allowlisted environment of subprocessEnv, not the
// server's whole one (which holds the database password, the session secret
// and the API tokens), and runs in a fresh empty temporary directory, removed
// afterwards, not in the server's working directory (which holds its dotenv
// file). The OpenAI path starts no process, so none of this applies to it.
func newExecRunner(timeout time.Duration) Runner {
	return func(ctx context.Context, argv []string) ([]byte, []byte, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		workDir, err := os.MkdirTemp("", "expansion-run-")
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", argv[0], err)
		}
		defer os.RemoveAll(workDir)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdin = nil
		cmd.Dir = workDir
		cmd.Env = subprocessEnv(os.Environ())
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
		out := &capWriter{limit: maxCommandOutput, fail: true, cancel: cancel}
		errb := &capWriter{limit: maxCommandStderr}
		cmd.Stdout, cmd.Stderr = out, errb
		err = cmd.Run()
		switch {
		case out.exceeded:
			return nil, nil, fmt.Errorf("%s: %w", argv[0], errOutputTooLarge)
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, nil, fmt.Errorf("%s: %w", argv[0], errTimedOut)
		}
		return out.buf.Bytes(), errb.buf.Bytes(), err
	}
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
		return 0, errOutputTooLarge
	}
	return len(p), nil
}

// envNames and envPrefixes are what a CLI subprocess may inherit: locale,
// terminal, proxy and certificate settings, plus the variables claude and codex
// read for their own login (CLAUDE_*, ANTHROPIC_*, CODEX_*, OPENAI_*).
// Everything else (DATABASE_*, SECRET_KEY_BASE, API_TOKEN, EXPANSION_LLM_API_KEY,
// SLACK_*, YOUTUBE_*, RULINKY_*, ADMIN_* and the rest) is left out.
var (
	envNames = map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "LANG": true,
		"TERM": true, "TMPDIR": true, "TZ": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
		"http_proxy": true, "https_proxy": true, "no_proxy": true,
	}
	envPrefixes = []string{"LC_", "XDG_", "CLAUDE_", "ANTHROPIC_", "CODEX_", "OPENAI_"}
)

// subprocessEnv filters environ (KEY=value entries, as os.Environ returns
// them) down to the allowlist, keeping their order.
func subprocessEnv(environ []string) []string {
	var env []string
	for _, kv := range environ {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			continue
		}
		if envNames[key] {
			env = append(env, kv)
			continue
		}
		for _, prefix := range envPrefixes {
			if strings.HasPrefix(key, prefix) {
				env = append(env, kv)
				break
			}
		}
	}
	return env
}
