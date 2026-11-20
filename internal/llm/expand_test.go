package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
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

var openAIExpandResponse = `{"choices":[{"message":{"content":` +
	func() string { b, _ := json.Marshal(expandHTML); return string(b) }() + `}}]}`

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
	if got.body["model"] != "agy:gemini-3.8-flash-medium" ||
		!strings.Contains(userContent(got.body), "Alpha beta.") || got.auth != "Bearer test-key" {
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
	body := `{"choices":[{"message":{"content":"no markup"}}]}`
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

func TestPromptArgLimit(t *testing.T) {
	if promptArgLimit("darwin") != 900_000 || promptArgLimit("linux") != 120_000 || promptArgLimit("freebsd") != 120_000 {
		t.Errorf("limits: %d %d %d", promptArgLimit("darwin"), promptArgLimit("linux"), promptArgLimit("freebsd"))
	}
	if MaxPromptArgBytes != promptArgLimit(runtime.GOOS) {
		t.Errorf("MaxPromptArgBytes = %d", MaxPromptArgBytes)
	}
}

// A prompt over the limit is refused before any process starts (so no useless
// codex retry), and nothing is stamped.
func TestExpandRefusesAPromptTheCLIsCannotTake(t *testing.T) {
	f := &fakeRunner{claude: claudeJSON(false, expandHTML), codex: codexWrites(expandHTML)}
	rec := &fakeRecorder{}
	for _, run := range []func(*ExpandService, context.Context, Params, Recorder) (string, error){
		(*ExpandService).Expand, (*ExpandService).Rewrite,
	} {
		p := expandArgs
		p.Document = strings.Repeat("x", MaxPromptArgBytes)
		_, err := run(serviceWith(f), context.Background(), p, rec)
		if !errors.Is(err, ErrPromptTooLarge) || err.Error() != "Document is too large to expand." || !isExpandError(err) {
			t.Fatalf("err = %v", err)
		}
	}
	if len(f.calls) != 0 || len(rec.stages) != 0 {
		t.Errorf("runner calls = %v, stages = %v", f.names(), rec.stages)
	}
}

// Just under the limit still goes through, and OpenAI has no limit.
func TestExpandPromptLimitBoundaryAndOpenAI(t *testing.T) {
	f := &fakeRunner{claude: claudeJSON(false, expandHTML)}
	p := expandArgs
	overhead := len(strings.NewReplacer("%{file_name}", p.FileName, "%{document}", "", "%{selection}", p.Selection,
		"%{question}", p.Question).Replace(ExpandPromptTemplate))
	p.Document = strings.Repeat("x", MaxPromptArgBytes-overhead)
	if _, err := serviceWith(f).Expand(context.Background(), p, nil); err != nil {
		t.Fatalf("exactly at the limit: %v", err)
	}
	p.Document += "x"
	if _, err := serviceWith(f).Expand(context.Background(), p, nil); !errors.Is(err, ErrPromptTooLarge) {
		t.Fatalf("one byte over: %v", err)
	}
	s, _, _ := openAIService(t, "k", 200, openAIExpandResponse)
	p.UseOpenAI = true
	if _, err := s.Expand(context.Background(), p, nil); err != nil {
		t.Fatalf("openai: %v", err)
	}
}
