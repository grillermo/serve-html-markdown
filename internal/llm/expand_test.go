package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const expandHTML = "<!DOCTYPE html><html><body>answer</body></html>"

var expandArgs = Params{FileName: "notes.md", Document: "Alpha beta.", Selection: "beta", Question: "why?"}

type call struct{ argv []string }

// fakeRunner scripts the CLIs by command name and records every argv.
type fakeRunner struct {
	mu            sync.Mutex
	calls         []call
	claude, codex func(argv []string) ([]byte, error)
}

func (f *fakeRunner) run(_ context.Context, argv []string) ([]byte, []byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call{argv})
	f.mu.Unlock()
	h := f.claude
	if argv[0] == "codex" {
		h = f.codex
	}
	if h == nil {
		return nil, nil, errors.New("unexpected command " + argv[0])
	}
	out, err := h(argv)
	return out, nil, err
}

func (f *fakeRunner) names() []string {
	var n []string
	for _, c := range f.calls {
		n = append(n, c.argv[0])
	}
	return n
}

func claudeJSON(isError any, result any) func([]string) ([]byte, error) {
	return func([]string) ([]byte, error) {
		b, _ := json.Marshal(map[string]any{"is_error": isError, "result": result})
		return b, nil
	}
}

// codexWrites writes text to the -o file like the codex CLI.
func codexWrites(text string) func([]string) ([]byte, error) {
	return func(argv []string) ([]byte, error) {
		for i, a := range argv {
			if a == "-o" {
				return nil, os.WriteFile(argv[i+1], []byte(text), 0o600)
			}
		}
		return nil, errors.New("no -o")
	}
}

var errExit = &exec.ExitError{}

func fail([]string) ([]byte, error) { return nil, errExit }

type fakeRecorder struct {
	stages   []string
	provider string
	bytes    int
}

func (r *fakeRecorder) Stamp(stage string) { r.stages = append(r.stages, stage) }
func (r *fakeRecorder) RecordResponse(p string, n int) {
	r.provider, r.bytes = p, n
}

func serviceWith(f *fakeRunner) *ExpandService {
	return &ExpandService{Runner: f.run}
}

func argAfter(t *testing.T, argv []string, flag string) string {
	t.Helper()
	for i, a := range argv {
		if a == flag {
			return argv[i+1]
		}
	}
	t.Fatalf("%s missing from %q", flag, argv)
	return ""
}

// Ruby: "returns claude result on success".
func TestExpandReturnsClaudeResult(t *testing.T) {
	f := &fakeRunner{claude: claudeJSON(false, expandHTML)}
	got, err := serviceWith(f).Expand(context.Background(), expandArgs, nil)
	if err != nil || got != expandHTML {
		t.Fatalf("got %q, %v", got, err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %v", f.names())
	}
	argv := f.calls[0].argv
	prompt := argAfter(t, argv, "-p")
	want := []string{"claude", "-p", prompt, "--model", "sonnet", "--output-format", "json", "--tools", ""}
	if fmt.Sprint(argv) != fmt.Sprint(want) {
		t.Errorf("argv = %q, want %q", argv, want)
	}
	for _, s := range []string{"Alpha beta.", "<selection>\nbeta\n</selection>", "<question>\nwhy?\n</question>",
		`<document filename="notes.md">`} {
		if !strings.Contains(prompt, s) {
			t.Errorf("prompt lacks %q", s)
		}
	}
}

// Ruby: "CLAUDE_MODEL is overridable via EXPANSION_CLAUDE_MODEL" (the config
// passes the model in).
func TestExpandUsesTheConfiguredClaudeModel(t *testing.T) {
	f := &fakeRunner{claude: claudeJSON(false, expandHTML)}
	s := serviceWith(f)
	s.ClaudeModel = "haiku"
	if _, err := s.Expand(context.Background(), expandArgs, nil); err != nil {
		t.Fatal(err)
	}
	if got := argAfter(t, f.calls[0].argv, "--model"); got != "haiku" {
		t.Errorf("model = %q", got)
	}
}

// Ruby: "stamps llm_request_start and llm_response and records provider_used/html_bytes on claude success".
func TestExpandStampsAndRecordsClaude(t *testing.T) {
	f := &fakeRunner{claude: claudeJSON(false, expandHTML)}
	rec := &fakeRecorder{}
	if _, err := serviceWith(f).Expand(context.Background(), expandArgs, rec); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(rec.stages) != "[llm_request_start llm_response]" || rec.provider != "claude" || rec.bytes != len(expandHTML) {
		t.Errorf("rec = %+v", rec)
	}
}

// Ruby: "stamps llm_first_failure before falling back to codex".
func TestExpandStampsFirstFailureOnFallback(t *testing.T) {
	f := &fakeRunner{claude: fail, codex: codexWrites(expandHTML)}
	rec := &fakeRecorder{}
	if _, err := serviceWith(f).Expand(context.Background(), expandArgs, rec); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(rec.stages) != "[llm_request_start llm_first_failure llm_response]" || rec.provider != "codex" || rec.bytes != len(expandHTML) {
		t.Errorf("rec = %+v", rec)
	}
}

// Ruby: "works with no expansion given".
func TestExpandWorksWithoutARecorder(t *testing.T) {
	f := &fakeRunner{claude: claudeJSON(false, expandHTML)}
	if got, err := serviceWith(f).Expand(context.Background(), expandArgs, nil); err != nil || got != expandHTML {
		t.Fatalf("got %q, %v", got, err)
	}
}

// Ruby: "strips a wrapping markdown code fence".
func TestExpandStripsAFence(t *testing.T) {
	f := &fakeRunner{claude: claudeJSON(false, "```html\n"+expandHTML+"\n```")}
	got, err := serviceWith(f).Expand(context.Background(), expandArgs, nil)
	if err != nil || got != expandHTML {
		t.Fatalf("got %q, %v", got, err)
	}
}

// Ruby: "falls back to codex when claude fails", with the exact codex argv.
func TestExpandFallsBackToCodexWhenClaudeFails(t *testing.T) {
	f := &fakeRunner{claude: fail, codex: codexWrites(expandHTML)}
	got, err := serviceWith(f).Expand(context.Background(), expandArgs, nil)
	if err != nil || got != expandHTML {
		t.Fatalf("got %q, %v", got, err)
	}
	if fmt.Sprint(f.names()) != "[claude codex]" {
		t.Fatalf("calls = %v", f.names())
	}
	argv := f.calls[1].argv
	out := argAfter(t, argv, "-o")
	prompt := argv[len(argv)-1]
	want := []string{"codex", "exec", "-m", "earth", "-s", "read-only", "--skip-git-repo-check", "--color", "never", "-o", out, prompt}
	if fmt.Sprint(argv) != fmt.Sprint(want) || !strings.Contains(prompt, "Alpha beta.") {
		t.Errorf("argv = %q", argv)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("the codex output file was left behind: %v", err)
	}
}

// Ruby: "falls back to codex when claude reports is_error" (and any truthy value).
func TestExpandFallsBackWhenClaudeReportsIsError(t *testing.T) {
	for _, isErr := range []any{true, 0, "no"} {
		f := &fakeRunner{claude: claudeJSON(isErr, "refused"), codex: codexWrites(expandHTML)}
		got, err := serviceWith(f).Expand(context.Background(), expandArgs, nil)
		if err != nil || got != expandHTML {
			t.Errorf("is_error=%v: got %q, %v", isErr, got, err)
		}
	}
}

// Claude's JSON that is not an object, or not JSON at all, falls back too.
func TestExpandFallsBackOnUnparseableClaudeOutput(t *testing.T) {
	for _, out := range []string{"not json", "[1]", "null", ""} {
		f := &fakeRunner{claude: func([]string) ([]byte, error) { return []byte(out), nil }, codex: codexWrites(expandHTML)}
		if _, err := serviceWith(f).Expand(context.Background(), expandArgs, nil); err != nil {
			t.Errorf("%q: %v", out, err)
		}
	}
}

// Ruby: "falls back to codex when claude command cannot launch".
func TestExpandFallsBackWhenClaudeCannotLaunch(t *testing.T) {
	f := &fakeRunner{
		claude: func([]string) ([]byte, error) { return nil, &exec.Error{Name: "claude", Err: exec.ErrNotFound} },
		codex:  codexWrites(expandHTML),
	}
	if _, err := serviceWith(f).Expand(context.Background(), expandArgs, nil); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(f.names()) != "[claude codex]" {
		t.Errorf("calls = %v", f.names())
	}
}

func isExpandError(err error) bool {
	var e *ExpandError
	return errors.As(err, &e)
}

// Ruby: "raises service Error without launch exception content when both commands cannot launch".
func TestExpandErrorHasNoLaunchDetail(t *testing.T) {
	const secret = "TOP-SECRET-LAUNCH-DETAIL"
	launch := func([]string) ([]byte, error) {
		return nil, &os.PathError{Op: "fork/exec", Path: secret, Err: syscall.ENOENT}
	}
	f := &fakeRunner{claude: launch, codex: launch}
	_, err := serviceWith(f).Expand(context.Background(), expandArgs, nil)
	if !isExpandError(err) || strings.Contains(err.Error(), secret) {
		t.Fatalf("err = %v", err)
	}
}

// Ruby: "does not log Claude result content when falling back" (stderr is never read, the
// result is never logged or put in an error).
func TestExpandErrorHasNoClaudeResultOrStderr(t *testing.T) {
	const secret = "TOP-SECRET-STDERR"
	f := &fakeRunner{claude: claudeJSON(true, secret), codex: codexWrites("plain " + secret)}
	_, err := serviceWith(f).Expand(context.Background(), expandArgs, nil)
	if !isExpandError(err) || strings.Contains(err.Error(), secret) {
		t.Fatalf("err = %v", err)
	}
}

// Ruby: "raises Error when both CLIs fail".
func TestExpandFailsWhenBothCLIsFail(t *testing.T) {
	f := &fakeRunner{claude: fail, codex: fail}
	_, err := serviceWith(f).Expand(context.Background(), expandArgs, nil)
	if !isExpandError(err) || err.Error() != "codex CLI failed" {
		t.Fatalf("err = %v", err)
	}
}

// Ruby: "raises Error when output is not HTML" (from claude, then from codex).
func TestExpandRequiresHTML(t *testing.T) {
	f := &fakeRunner{claude: claudeJSON(false, "sorry, cannot help"), codex: codexWrites("plain text")}
	_, err := serviceWith(f).Expand(context.Background(), expandArgs, nil)
	if !isExpandError(err) || err.Error() != "output does not look like HTML" {
		t.Fatalf("err = %v", err)
	}
	if fmt.Sprint(f.names()) != "[claude codex]" {
		t.Errorf("a claude page that is not HTML should fall back: %v", f.names())
	}
}

func TestExpandHTMLCheckIsCaseInsensitive(t *testing.T) {
	f := &fakeRunner{claude: claudeJSON(false, "<!DOCTYPE html><HTML><body>x</body></HTML>")}
	if _, err := serviceWith(f).Expand(context.Background(), expandArgs, nil); err != nil {
		t.Fatal(err)
	}
}

func openAIService(t *testing.T, key string, status int, body string) (*ExpandService, *capture, *fakeRunner) {
	t.Helper()
	srv, got := fakeEndpoint(t, status, body)
	f := &fakeRunner{claude: func([]string) ([]byte, error) { t.Error("claude must not run"); return nil, errExit }}
	f.codex = f.claude
	s := serviceWith(f)
	s.OpenAI = clientFor(srv, key)
	return s, got, f
}

var openAIExpandResponse = `{"output":[{"type":"message","content":[{"type":"output_text","text":` +
	func() string { b, _ := json.Marshal(expandHTML); return string(b) }() + `}]}]}`

// Ruby: "calls openai when use_openai is true" and "records openai as provider_used".
func TestExpandCallsOpenAI(t *testing.T) {
	s, got, _ := openAIService(t, "test-key", 200, openAIExpandResponse)
	rec := &fakeRecorder{}
	p := expandArgs
	p.UseOpenAI = true
	out, err := s.Expand(context.Background(), p, rec)
	if err != nil || out != expandHTML {
		t.Fatalf("got %q, %v", out, err)
	}
	reasoning, _ := got.body["reasoning"].(map[string]any)
	if got.body["model"] != "gpt-5.6-terra" || reasoning["effort"] != "medium" ||
		!strings.Contains(got.body["input"].(string), "Alpha beta.") || got.auth != "Bearer test-key" {
		t.Errorf("request = %v %s", got.body, got.auth)
	}
	if fmt.Sprint(rec.stages) != "[llm_request_start llm_response]" || rec.provider != "openai" || rec.bytes != len(expandHTML) {
		t.Errorf("rec = %+v", rec)
	}
}

// Ruby: "raises without falling back to codex when openai fails".
func TestExpandOpenAIFailureDoesNotFallBack(t *testing.T) {
	s, _, f := openAIService(t, "test-key", 500, "sensitive upstream details")
	p := expandArgs
	p.UseOpenAI = true
	_, err := s.Expand(context.Background(), p, nil)
	if !isExpandError(err) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("CLIs ran: %v", f.names())
	}
}

// Ruby: "raises when EXPANSION_LLM_API_KEY is not configured".
func TestExpandOpenAIWithoutKey(t *testing.T) {
	for _, s := range []*ExpandService{{}, {OpenAI: NewClient("  ")}} {
		p := expandArgs
		p.UseOpenAI = true
		_, err := s.Expand(context.Background(), p, nil)
		if !isExpandError(err) || !errors.Is(err, ErrConfiguration) {
			t.Errorf("err = %v", err)
		}
	}
}

func TestExpandOpenAIOutputIsValidatedToo(t *testing.T) {
	body := `{"output":[{"type":"message","content":[{"type":"output_text","text":"no markup"}]}]}`
	s, _, _ := openAIService(t, "k", 200, body)
	p := expandArgs
	p.UseOpenAI = true
	if _, err := s.Expand(context.Background(), p, nil); err == nil || err.Error() != "output does not look like HTML" {
		t.Errorf("err = %v", err)
	}
}

// A cancelled context ends the call without trying the fallback.
func TestExpandStopsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeRunner{claude: func([]string) ([]byte, error) { cancel(); return nil, errors.New("killed") }, codex: codexWrites(expandHTML)}
	_, err := serviceWith(f).Expand(ctx, expandArgs, nil)
	if !isExpandError(err) || !errors.Is(err, context.Canceled) || len(f.calls) != 1 {
		t.Fatalf("err = %v, calls = %v", err, f.names())
	}
}

// Ruby: "rewrite returns markdown unchanged by the html check", and whitespace is kept.
func TestRewriteKeepsMarkdownAndWhitespace(t *testing.T) {
	markdown := "\n# Notes\n\nAlpha beta gamma.\n\n"
	f := &fakeRunner{claude: claudeJSON(false, markdown)}
	got, err := serviceWith(f).Rewrite(context.Background(), expandArgs, nil)
	if err != nil || got != markdown {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestRewriteFencedOutputIsStripped(t *testing.T) {
	f := &fakeRunner{claude: claudeJSON(false, "```markdown\n# Notes\n```\n")}
	got, err := serviceWith(f).Rewrite(context.Background(), expandArgs, nil)
	if err != nil || got != "# Notes" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// Ruby: "rewrite still requires html for an html source".
func TestRewriteHTMLSourceRequiresHTML(t *testing.T) {
	f := &fakeRunner{claude: claudeJSON(false, "not markup"), codex: codexWrites("still not")}
	p := expandArgs
	p.FileName = "NOTES.HTML"
	if _, err := serviceWith(f).Rewrite(context.Background(), p, nil); !isExpandError(err) {
		t.Fatalf("err = %v", err)
	}
	f = &fakeRunner{claude: claudeJSON(false, "<html></html>")}
	if _, err := serviceWith(f).Rewrite(context.Background(), p, nil); err != nil {
		t.Fatal(err)
	}
}

// Ruby: "rewrite rejects a blank document".
func TestRewriteRejectsABlankDocument(t *testing.T) {
	f := &fakeRunner{claude: claudeJSON(false, "   \n"), codex: codexWrites(" ")}
	_, err := serviceWith(f).Rewrite(context.Background(), expandArgs, nil)
	if !isExpandError(err) || err.Error() != "output was empty" {
		t.Fatalf("err = %v", err)
	}
}

// Ruby: "rewrite prompt asks for the anchor sentinel and the original format".
func TestRewritePrompt(t *testing.T) {
	f := &fakeRunner{claude: claudeJSON(false, "# ok\n")}
	if _, err := serviceWith(f).Rewrite(context.Background(), expandArgs, nil); err != nil {
		t.Fatal(err)
	}
	prompt := argAfter(t, f.calls[0].argv, "-p")
	for _, s := range []string{"⟦EXPANSION_ANCHOR⟧", "beta", "why?", "Markdown stays Markdown"} {
		if !strings.Contains(prompt, s) {
			t.Errorf("prompt lacks %q", s)
		}
	}
}

// Fields are filled in one pass over the template: a document that looks like a
// template field, or holds a "%", comes through verbatim.
func TestPromptEmbedsTextVerbatim(t *testing.T) {
	f := &fakeRunner{claude: claudeJSON(false, expandHTML)}
	p := Params{FileName: "a%d.md", Document: "100% %{selection} %s %%", Selection: "%{question}", Question: "50%"}
	if _, err := serviceWith(f).Expand(context.Background(), p, nil); err != nil {
		t.Fatal(err)
	}
	prompt := argAfter(t, f.calls[0].argv, "-p")
	for _, s := range []string{`filename="a%d.md"`, "\n100% %{selection} %s %%\n", "<selection>\n%{question}\n</selection>", "<question>\n50%\n</question>"} {
		if !strings.Contains(prompt, s) {
			t.Errorf("prompt lacks %q:\n%s", s, prompt)
		}
	}
}

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

// The runner passes the environment on and gives the process no stdin.
func TestExecRunnerEnvironmentAndStdin(t *testing.T) {
	t.Setenv("EXPAND_TEST_VAR", "inherited")
	out, _, err := execRunner(context.Background(), []string{"sh", "-c", `printf "%s" "$EXPAND_TEST_VAR"; cat | wc -c`})
	if err != nil || !strings.HasPrefix(string(out), "inherited") || !strings.Contains(string(out), "0") {
		t.Fatalf("out = %q, err = %v", out, err)
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

// --- the prompts ---

// Golden hashes of the two templates as the Ruby constants produce them, so the
// check survives the Ruby code's removal.
const (
	goldenExpandPromptSHA256  = "c8cf67481d76b9c27156fc6a4f559a380f19278d5d593d104ab5a0d86b3ff486"
	goldenRewritePromptSHA256 = "d2621f0449a34c937cf81129dfaae4d01f7d786725add22b132c643124e57f08"
)

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestExpandPromptGolden(t *testing.T) {
	if got := sha(ExpandPromptTemplate); got != goldenExpandPromptSHA256 {
		t.Errorf("ExpandPromptTemplate sha256 = %s, want %s", got, goldenExpandPromptSHA256)
	}
	if got := sha(RewritePromptTemplate); got != goldenRewritePromptSHA256 {
		t.Errorf("RewritePromptTemplate sha256 = %s, want %s", got, goldenRewritePromptSHA256)
	}
}

const expandServiceRuby = "claude_expand_service.rb"

func expandRubySource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "app", "services", expandServiceRuby))
	if os.IsNotExist(err) {
		t.Skip("the Ruby source is gone; TestExpandPromptGolden still guards the text")
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(src)
}

// heredoc returns the body of `NAME = <<~PROMPT ... PROMPT` as Ruby's squiggly
// heredoc makes it: the common indentation (4 spaces) removed.
func heredoc(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "  "+name+" = <<~PROMPT\n")
	if start < 0 {
		t.Fatalf("%s not found", name)
	}
	body := src[start:]
	body = body[strings.Index(body, "\n")+1:]
	body = body[:strings.Index(body, "\n  PROMPT\n")+1]
	var out strings.Builder
	for _, line := range strings.SplitAfter(body, "\n") {
		if strings.TrimSpace(line) == "" {
			out.WriteString(strings.TrimLeft(line, " "))
			continue
		}
		if !strings.HasPrefix(line, "    ") {
			t.Fatalf("%s: line %q is not indented by 4", name, line)
		}
		out.WriteString(line[4:])
	}
	return strings.ReplaceAll(out.String(), "#{ANCHOR_SENTINEL}", AnchorSentinel)
}

// TestPromptsMatchRubySource compares both templates with the Ruby heredocs
// byte for byte, parsed from the source independently of any interpreter.
func TestPromptsMatchRubySource(t *testing.T) {
	src := expandRubySource(t)
	if got := heredoc(t, src, "PROMPT_TEMPLATE"); got != ExpandPromptTemplate {
		t.Errorf("ExpandPromptTemplate differs from PROMPT_TEMPLATE:\n go:   %q\n ruby: %q", ExpandPromptTemplate, got)
	}
	if got := heredoc(t, src, "REWRITE_PROMPT_TEMPLATE"); got != RewritePromptTemplate {
		t.Errorf("RewritePromptTemplate differs from REWRITE_PROMPT_TEMPLATE:\n go:   %q\n ruby: %q", RewritePromptTemplate, got)
	}
	if !regexp.MustCompile(`ANCHOR_SENTINEL = "⟦EXPANSION_ANCHOR⟧"`).MatchString(src) {
		t.Error("ANCHOR_SENTINEL differs from the Ruby source")
	}
}

// TestPromptsMatchRubyInterpreter evaluates the Ruby heredocs themselves. It
// skips only when there is no ruby binary at all; a ruby that fails is a
// failure. rbenv shims need the project's version, tried second as RBENV_VERSION.
func TestPromptsMatchRubyInterpreter(t *testing.T) {
	expandRubySource(t)
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("no ruby binary on PATH; the source comparison and the golden hashes still check the prompts")
	}
	const script = `
src = File.read(ARGV[0])
ANCHOR_SENTINEL = "⟦EXPANSION_ANCHOR⟧"
which = ARGV[1] == "rewrite" ? "REWRITE_PROMPT_TEMPLATE" : "PROMPT_TEMPLATE"
eval(src[/  #{which} = <<~PROMPT\n.*?\n  PROMPT\n/m].sub(/\A  #{which}/, "$p"))
$stdout.binmode
print $p
`
	path := filepath.Join("..", "..", "app", "services", expandServiceRuby)
	run := func(which string, env ...string) ([]byte, error) {
		cmd := exec.Command(ruby, "-e", script, path, which)
		cmd.Env = append(os.Environ(), env...)
		return cmd.Output()
	}
	for which, want := range map[string]string{"expand": ExpandPromptTemplate, "rewrite": RewritePromptTemplate} {
		out, err := run(which)
		if err != nil {
			if v, rerr := os.ReadFile(filepath.Join("..", "..", ".ruby-version")); rerr == nil {
				out, err = run(which, "RBENV_VERSION="+strings.TrimSpace(string(v)))
			}
		}
		if err != nil {
			var stderr []byte
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				stderr = ee.Stderr
			}
			t.Fatalf("ruby (%s) failed: %v\n%s", ruby, err, stderr)
		}
		if string(out) != want {
			t.Errorf("%s prompt differs from Ruby's:\n go:   %q\n ruby: %q", which, want, out)
		}
	}
}
