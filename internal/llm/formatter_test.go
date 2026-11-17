package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Ruby: "formats content through the configured OpenAI model".
func TestFormatterSendsThePromptAndContent(t *testing.T) {
	srv, got := fakeEndpoint(t, 200, okResponse)
	out, err := NewFormatter(clientFor(srv, "openai-key")).Format(context.Background(), "source text")
	if err != nil {
		t.Fatal(err)
	}
	if out != "formatted markdown" {
		t.Errorf("out = %q", out)
	}
	if got.auth != "Bearer openai-key" || got.path != "/v1/responses" {
		t.Errorf("request = %s %s", got.path, got.auth)
	}
	if got.body["model"] != "gpt-6-luna" || got.body["input"] != FormatPrompt+"source text" {
		t.Errorf("body = %v", got.body)
	}
	if len(got.body) != 2 {
		t.Errorf("body has extra fields: %v", got.body)
	}
}

// Ruby: "rejects an empty API key before making a request".
func TestFormatterMissingKey(t *testing.T) {
	_, err := NewFormatter(NewClient("")).Format(context.Background(), "x")
	if !errors.Is(err, ErrConfiguration) || err.Error() != "OPENAI_API_KEY is not configured." {
		t.Fatalf("err = %v", err)
	}
}

// Ruby: "raises a generic error when OpenAI rejects the request" and
// "... returns a malformed body": every upstream failure is the same error.
func TestFormatterUpstreamFailuresAreGeneric(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"rejected":  {500, "sensitive upstream details"},
		"malformed": {200, "not json sensitive"},
		"no output": {200, `{"output":[]}`},
	} {
		srv, _ := fakeEndpoint(t, tc.status, tc.body)
		_, err := NewFormatter(clientFor(srv, "k")).Format(context.Background(), "x")
		var e *Error
		if !errors.As(err, &e) || e.Message != FormattingFailed {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		if strings.Contains(err.Error(), "sensitive") {
			t.Errorf("%s: error leaks the body", name)
		}
		if errors.Is(err, ErrConfiguration) {
			t.Errorf("%s: should not be a configuration error", name)
		}
	}
}

func TestFormatterKeepsCancellationVisible(t *testing.T) {
	srv, _ := fakeEndpoint(t, 200, okResponse)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewFormatter(clientFor(srv, "k")).Format(ctx, "x")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to unwrap to context.Canceled", err)
	}
}

// goldenPromptSHA256 is the SHA-256 of FORMAT_PROMPT in
// app/services/openai_formatter.rb, so the check survives the Ruby code's removal.
const goldenPromptSHA256 = "cdf419cc2e5d1947ee49f103048cf7e3782af89f220bdb762643921d6307e8f7"

func TestFormatPromptGolden(t *testing.T) {
	sum := sha256.Sum256([]byte(FormatPrompt))
	if got := hex.EncodeToString(sum[:]); got != goldenPromptSHA256 {
		t.Errorf("FormatPrompt sha256 = %s, want %s", got, goldenPromptSHA256)
	}
}

func rubySource(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "app", "services", "openai_formatter.rb")
	src, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skip("the Ruby source is gone; TestFormatPromptGolden still guards the text")
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(src)
}

// TestFormatPromptMatchesRubySource compares FormatPrompt with FORMAT_PROMPT
// byte for byte. The literal is read from the Ruby source (the quoted pieces
// between "FORMAT_PROMPT = (" and ")", their escapes decoded), independently of
// any Ruby interpreter.
func TestFormatPromptMatchesRubySource(t *testing.T) {
	src := rubySource(t)
	start := strings.Index(src, "FORMAT_PROMPT = (")
	if start < 0 {
		t.Fatal("FORMAT_PROMPT not found")
	}
	body := src[start+len("FORMAT_PROMPT = ("):]
	body = body[:strings.Index(body, "\n  )")]
	pieces := regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`).FindAllStringSubmatch(body, -1)
	if len(pieces) == 0 {
		t.Fatal("no string pieces found")
	}
	var want strings.Builder
	for _, p := range pieces {
		s, err := strconv.Unquote(`"` + p[1] + `"`) // only \n and \" appear in the source
		if err != nil {
			t.Fatal(err)
		}
		want.WriteString(s)
	}
	if FormatPrompt != want.String() {
		t.Errorf("FormatPrompt differs from the Ruby source:\n go:   %q\n ruby: %q", FormatPrompt, want.String())
	}
}

// TestFormatPromptMatchesRubyInterpreter evaluates the Ruby constant itself. It
// skips only when there is no ruby binary at all; a ruby that fails is a
// failure. rbenv shims need the project's version (.ruby-version), so it is
// passed as RBENV_VERSION on a second try.
func TestFormatPromptMatchesRubyInterpreter(t *testing.T) {
	rubySource(t) // skips when the source is gone
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("no ruby binary on PATH; TestFormatPromptMatchesRubySource and the golden hash still check the prompt")
	}
	const script = `
src = File.read(ARGV[0])
module OpenaiResponses; end
class OpenaiFormatter; end
eval(src[/FORMAT_PROMPT = \(.*?\n  \)/m].sub("FORMAT_PROMPT", "$p"))
$stdout.binmode
print $p
`
	path := filepath.Join("..", "..", "app", "services", "openai_formatter.rb")
	run := func(env ...string) ([]byte, error) {
		cmd := exec.Command(ruby, "-e", script, path)
		cmd.Env = append(os.Environ(), env...)
		return cmd.Output()
	}
	out, err := run()
	if err != nil {
		if v, rerr := os.ReadFile(filepath.Join("..", "..", ".ruby-version")); rerr == nil {
			out, err = run("RBENV_VERSION=" + strings.TrimSpace(string(v)))
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
	if string(out) != FormatPrompt {
		t.Errorf("FormatPrompt differs from Ruby's FORMAT_PROMPT:\n go:   %q\n ruby: %q", FormatPrompt, out)
	}
}
