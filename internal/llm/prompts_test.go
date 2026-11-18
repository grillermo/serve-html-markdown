package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

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
