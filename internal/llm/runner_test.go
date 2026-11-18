package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// --- the real runner ---

func TestExecRunnerCapturesOutputAndExit(t *testing.T) {
	out, _, err := execRunner(context.Background(), []string{"sh", "-c", "printf hi; printf err >&2"})
	if err != nil || string(out) != "hi" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	_, _, err = execRunner(context.Background(), []string{"sh", "-c", "exit 3"})
	var ee *exec.ExitError
	if !errors.As(err, &ee) || commandError("x", err).Error() != "x CLI failed" {
		t.Errorf("err = %v", err)
	}
	_, _, err = execRunner(context.Background(), []string{"/nonexistent/binary"})
	if err == nil || commandError("claude", err).Error() != "claude CLI could not be started" {
		t.Errorf("err = %v / %v", err, commandError("claude", err))
	}
}

// The process gets no stdin.
func TestExecRunnerHasNoStdin(t *testing.T) {
	out, _, err := execRunner(context.Background(), []string{"sh", "-c", "cat | wc -c"})
	if err != nil || strings.TrimSpace(string(out)) != "0" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestSubprocessEnvAllowlist(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin", "HOME=/home/u", "USER=u", "LOGNAME=u", "SHELL=/bin/zsh", "LANG=en_US.UTF-8",
		"LC_ALL=C", "LC_CTYPE=UTF-8", "TERM=xterm", "TMPDIR=/tmp", "TZ=UTC", "XDG_CONFIG_HOME=/c", "XDG_DATA_DIRS=/d",
		"SSL_CERT_FILE=/f", "SSL_CERT_DIR=/d", "HTTP_PROXY=p", "HTTPS_PROXY=p", "NO_PROXY=n",
		"http_proxy=p", "https_proxy=p", "no_proxy=n",
		"CLAUDE_CONFIG_DIR=/x", "ANTHROPIC_API_KEY=a", "CODEX_HOME=/h", "OPENAI_API_KEY=o",
		"DATABASE_PASSWORD=s", "DATABASE_URL=u", "SECRET_KEY_BASE=s", "API_TOKEN=t", "EXPANSION_LLM_API_KEY=k",
		"EXPANSION_CLAUDE_MODEL=m", "SLACK_TOKEN=s", "YOUTUBE_API_KEY=y", "RULINKY_TOKEN=r", "ADMIN_PASSWORD=a",
		"PATHOLOGICAL=1", "LCX=1", "XDG=1", "malformed", "=novalue", "HOME_EXTRA=1", "path=lower",
	}
	got := subprocessEnv(environ)
	want := environ[:25]
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("env = %q\nwant  %q", got, want)
	}
	if subprocessEnv(nil) != nil {
		t.Error("nil environment should stay empty")
	}
}

// The real process sees the allowlist and nothing else, and runs in a fresh
// directory of its own that is gone afterwards.
func TestExecRunnerRunsWithTheAllowlistedEnvironmentInAFreshDirectory(t *testing.T) {
	env, err := exec.LookPath("env")
	if err != nil {
		t.Skip("no env binary")
	}
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("HOME", "/home/expansion-test")
	t.Setenv("DATABASE_PASSWORD", "TOP-SECRET-DB")
	t.Setenv("SECRET_KEY_BASE", "TOP-SECRET-BASE")
	t.Setenv("EXPANSION_LLM_API_KEY", "TOP-SECRET-KEY")
	t.Setenv("ANTHROPIC_API_KEY", "claude-login")
	out, _, err := execRunner(context.Background(), []string{env})
	if err != nil {
		t.Fatal(err)
	}
	vars := string(out)
	for _, want := range []string{"PATH=" + os.Getenv("PATH"), "HOME=/home/expansion-test", "ANTHROPIC_API_KEY=claude-login"} {
		if !strings.Contains("\n"+vars, "\n"+want+"\n") {
			t.Errorf("env lacks %q:\n%s", want, vars)
		}
	}
	for _, secret := range []string{"DATABASE_PASSWORD", "SECRET_KEY_BASE", "EXPANSION_LLM_API_KEY", "TOP-SECRET"} {
		if strings.Contains(vars, secret) {
			t.Errorf("env leaks %s:\n%s", secret, vars)
		}
	}

	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	out, _, err = execRunner(context.Background(), []string{"sh", "-c", "pwd -P; ls -A | wc -l"})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(out))
	if len(lines) != 2 || lines[1] != "0" {
		t.Fatalf("out = %q, want a path and an empty directory", out)
	}
	realHere, _ := filepath.EvalSymlinks(here)
	if lines[0] == here || lines[0] == realHere {
		t.Errorf("ran in the server's directory %s", lines[0])
	}
	if _, err := os.Stat(lines[0]); !os.IsNotExist(err) {
		t.Errorf("the working directory %s was not removed (%v)", lines[0], err)
	}
}

// Ruby: the 120 s timeout kills the process. Here with a short timeout; the
// whole process group goes, so a child of the process does not linger.
func TestExecRunnerTimeoutKillsTheProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	run := newExecRunner(300 * time.Millisecond)
	start := time.Now()
	_, _, err := run(context.Background(), []string{"sh", "-c", `sleep 30 & echo $! > "$1"; wait`, "sh", pidFile})
	if !errors.Is(err, errTimedOut) {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %v", elapsed)
	}
	if got := commandError("claude", err).Error(); got != "claude timed out after 120s" {
		t.Errorf("message = %q", got)
	}
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
			break
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("the child of the killed process is still running")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestExecRunnerStopsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := execRunner(ctx, []string{"sleep", "30"})
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestExecRunnerCapsStdout(t *testing.T) {
	_, _, err := execRunner(context.Background(), []string{"sh", "-c", "yes | head -c 40000000"})
	if !errors.Is(err, errOutputTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if got := commandError("claude", err).Error(); got != "claude output was too large" {
		t.Errorf("message = %q", got)
	}
}

// An argument list the OS refuses (E2BIG) is a clean ExpandError, never a panic.
func TestExecRunnerArgumentListTooLong(t *testing.T) {
	huge := strings.Repeat("x", 4<<20)
	_, _, err := execRunner(context.Background(), []string{"true", huge})
	if !errors.Is(err, syscall.E2BIG) {
		t.Fatalf("err = %v", err)
	}
	f := &fakeRunner{claude: func([]string) ([]byte, error) { return execErr(huge) }, codex: func([]string) ([]byte, error) { return execErr(huge) }}
	_, err = serviceWith(f).Expand(context.Background(), expandArgs, nil)
	if !isExpandError(err) || !strings.Contains(err.Error(), "prompt is too large") {
		t.Errorf("err = %v", err)
	}
}

func execErr(huge string) ([]byte, error) {
	_, _, err := execRunner(context.Background(), []string{"true", huge})
	return nil, err
}
