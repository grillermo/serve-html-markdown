package execx_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"serve-html-markdown/internal/execx"
)

func TestRunCapturesOutputAndExit(t *testing.T) {
	out, errOut, err := execx.Run(context.Background(), []string{"sh", "-c", "printf hi; printf err >&2"}, execx.Options{})
	if err != nil || string(out) != "hi" || string(errOut) != "err" {
		t.Fatalf("out = %q, err output = %q, err = %v", out, errOut, err)
	}
	out, errOut, err = execx.Run(context.Background(), []string{"sh", "-c", "printf out; printf bad >&2; exit 3"}, execx.Options{})
	var exitErr *execx.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 3 || string(out) != "out" || string(errOut) != "bad" {
		t.Errorf("out = %q, err output = %q, err = %v", out, errOut, err)
	}
	if _, _, err = execx.Run(context.Background(), []string{"/nonexistent/binary"}, execx.Options{}); err == nil || errors.As(err, &exitErr) ||
		errors.Is(err, execx.ErrTimedOut) || errors.Is(err, execx.ErrOutputTooLarge) {
		t.Errorf("a missing binary must be a plain start error, got %v", err)
	}
	if _, _, err = execx.Run(context.Background(), nil, execx.Options{}); err == nil {
		t.Error("an empty argv must fail")
	}
}

// Argv is run directly, with no shell: metacharacters stay literal.
func TestRunHasNoShell(t *testing.T) {
	out, _, err := execx.Run(context.Background(), []string{"printf", "%s", "; echo pwned $(id) `id` | cat"}, execx.Options{})
	if err != nil || string(out) != "; echo pwned $(id) `id` | cat" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestRunHasNoStdin(t *testing.T) {
	out, _, err := execx.Run(context.Background(), []string{"sh", "-c", "cat | wc -c"}, execx.Options{})
	if err != nil || strings.TrimSpace(string(out)) != "0" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestSafeEnvAllowlist(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin", "HOME=/home/u", "USER=u", "LOGNAME=u", "SHELL=/bin/zsh", "LANG=en_US.UTF-8",
		"LC_ALL=C", "LC_CTYPE=UTF-8", "TERM=xterm", "TMPDIR=/tmp", "TZ=UTC", "XDG_CONFIG_HOME=/c", "XDG_CACHE_HOME=/d",
		"SSL_CERT_FILE=/f", "SSL_CERT_DIR=/d", "HTTP_PROXY=p", "HTTPS_PROXY=p", "NO_PROXY=n", "ALL_PROXY=p",
		"http_proxy=p", "https_proxy=p", "no_proxy=n", "all_proxy=p",
		"DATABASE_PASSWORD=s", "DATABASE_URL=u", "SECRET_KEY_BASE=s", "API_TOKEN=t", "SLACK_TOKEN=s",
		"YOUTUBE_CLIENT_SECRET=y", "RULINKY_TOKEN=r", "OPENAI_API_KEY=o", "EXPANSION_LLM_API_KEY=k", "ADMIN_PASSWORD=a",
		"PATHOLOGICAL=1", "LCX=1", "XDG=1", "malformed", "=novalue", "HOME_EXTRA=1", "path=lower",
	}
	got := execx.SafeEnv(environ)
	want := environ[:23]
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("env = %q\nwant  %q", got, want)
	}
	if execx.SafeEnv(nil) != nil {
		t.Error("nil environment should stay empty")
	}
}

func TestRunEnvironment(t *testing.T) {
	t.Setenv("SHM_SECRET_FOR_TEST", "topsecret")
	t.Setenv("DATABASE_PASSWORD", "topsecret")
	t.Setenv("LANG", "C")
	out, _, err := execx.Run(context.Background(), []string{"sh", "-c", `env`}, execx.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "topsecret") || !strings.Contains(string(out), "LANG=C") {
		t.Errorf("default env should hold the allowlist only, got:\n%s", out)
	}
	out, _, err = execx.Run(context.Background(), []string{"sh", "-c", `echo "$ONLY"; echo "x$LANG"`}, execx.Options{Env: []string{"ONLY=1", "PATH=/usr/bin:/bin"}})
	if err != nil || string(out) != "1\nx\n" {
		t.Errorf("explicit env: out = %q, err = %v", out, err)
	}
}

func TestRunWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	out, _, err := execx.Run(context.Background(), []string{"pwd", "-P"}, execx.Options{Dir: dir})
	want, _ := filepath.EvalSymlinks(dir)
	if err != nil || strings.TrimSpace(string(out)) != want {
		t.Errorf("pwd = %q, want %q (err %v)", out, want, err)
	}
	// Default: a fresh empty directory that is gone afterwards, never the caller's.
	here, _ := os.Getwd()
	out, _, err = execx.Run(context.Background(), []string{"sh", "-c", "pwd -P; ls -A | wc -l"}, execx.Options{})
	lines := strings.Fields(string(out))
	if err != nil || len(lines) != 2 || lines[1] != "0" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if realHere, _ := filepath.EvalSymlinks(here); lines[0] == here || lines[0] == realHere {
		t.Errorf("ran in the caller's directory %s", lines[0])
	}
	if _, err := os.Stat(lines[0]); !os.IsNotExist(err) {
		t.Errorf("the scratch directory %s was not removed (%v)", lines[0], err)
	}
}

// The timeout kills the whole process group, so a child of the process does not linger.
func TestRunTimeoutKillsTheProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	start := time.Now()
	_, _, err := execx.Run(context.Background(), []string{"sh", "-c", `sleep 30 & echo $! > "$1"; wait`, "sh", pidFile},
		execx.Options{Timeout: 300 * time.Millisecond})
	if !errors.Is(err, execx.ErrTimedOut) {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %v", elapsed)
	}
	waitForExit(t, pidFile)
}

func TestRunStopsWithTheContextAndKillsTheGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			if _, err := os.Stat(pidFile); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	start := time.Now()
	_, _, err := execx.Run(ctx, []string{"sh", "-c", `sleep 30 & echo $! > "$1.tmp"; mv "$1.tmp" "$1"; wait`, "sh", pidFile}, execx.Options{})
	if err == nil || errors.Is(err, execx.ErrTimedOut) || time.Since(start) > 10*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context should be visible in the error: %v", err)
	}
	waitForExit(t, pidFile)
}

func TestRunCapsStdout(t *testing.T) {
	_, _, err := execx.Run(context.Background(), []string{"sh", "-c", "yes | head -c 40000000"}, execx.Options{MaxStdout: 1 << 20})
	if !errors.Is(err, execx.ErrOutputTooLarge) {
		t.Fatalf("err = %v", err)
	}
	// Under the cap everything is returned.
	out, _, err := execx.Run(context.Background(), []string{"sh", "-c", "yes | head -c 100000"}, execx.Options{MaxStdout: 1 << 20})
	if err != nil || len(out) != 100000 {
		t.Errorf("len(out) = %d, err = %v", len(out), err)
	}
}

// Past its cap stderr is truncated, not fatal: the command's own outcome stands.
func TestRunTruncatesStderr(t *testing.T) {
	_, errOut, err := execx.Run(context.Background(), []string{"sh", "-c", "yes 2>&1 | head -c 5000000 >&2"}, execx.Options{MaxStderr: 1000})
	if err != nil || len(errOut) != 1000 {
		t.Errorf("len(stderr) = %d, err = %v", len(errOut), err)
	}
}

// An argument list the OS refuses (E2BIG) is a clean error, never a panic.
func TestRunArgumentListTooLong(t *testing.T) {
	_, _, err := execx.Run(context.Background(), []string{"true", strings.Repeat("x", 4<<20)}, execx.Options{})
	if !errors.Is(err, syscall.E2BIG) {
		t.Fatalf("err = %v", err)
	}
}

// A grandchild that keeps the pipes open must not hold Run forever once the
// process itself is done and killed.
func TestRunDoesNotWaitForLingeringPipeHolders(t *testing.T) {
	start := time.Now()
	out, _, err := execx.Run(context.Background(), []string{"sh", "-c", "(sleep 30 &) ; echo done"}, execx.Options{Timeout: 10 * time.Second})
	if err != nil || strings.TrimSpace(string(out)) != "done" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if time.Since(start) > 8*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
}

func waitForExit(t *testing.T, pidFile string) {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscan(string(raw), &pid); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("the child of the killed process is still running")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
