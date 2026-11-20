package llm

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// attrsString renders Diagnostics' key/value pairs as "k=v k=v" for matching.
func attrsString(attrs []any) string {
	var b strings.Builder
	for i := 0; i+1 < len(attrs); i += 2 {
		fmt.Fprintf(&b, "%v=%v ", attrs[i], attrs[i+1])
	}
	return b.String()
}

func TestDiagnosticsOfARejectedSotaLLMsRequest(t *testing.T) {
	srv, _ := fakeEndpoint(t, 429, `{"error":"rate limited"}`)
	s := &ExpandService{SotaLLMs: clientFor(srv, "sk-secret-key")}
	_, err := s.Expand(context.Background(), Params{FileName: "a.md", UseSotaLLMs: true}, nil)
	if err == nil || err.Error() != "sota-llms request failed" {
		t.Fatalf("err = %v", err)
	}
	got := attrsString(Diagnostics(err))
	for _, want := range []string{"http_status=429", `http_body={"error":"rate limited"}`, "model=" + SotaLLMsExpandModel, "chain="} {
		if !strings.Contains(got, want) {
			t.Errorf("diagnostics %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "sk-secret-key") {
		t.Errorf("diagnostics leak the key: %q", got)
	}
}

func TestDiagnosticsOfAClaudeErrorAndARejectedCodexOutput(t *testing.T) {
	f := &fakeRunner{
		claude: func([]string) ([]byte, error) {
			return []byte(`{"is_error":true,"subtype":"error_during_execution","result":"Usage limit reached"}`), nil
		},
		codex: codexWrites("Sorry, I cannot help with that."),
	}
	var claudeErr error
	f2 := &fakeRunner{claude: f.claude}
	_, claudeErr = serviceWith(f2).runClaude(context.Background(), "p")
	got := attrsString(Diagnostics(claudeErr))
	for _, want := range []string{"subtype=error_during_execution", "result=Usage limit reached"} {
		if !strings.Contains(got, want) {
			t.Errorf("claude diagnostics %q missing %q", got, want)
		}
	}

	_, err := serviceWith(f).Expand(context.Background(), expandArgs, nil)
	if err == nil || err.Error() != "output does not look like HTML" {
		t.Fatalf("err = %v", err)
	}
	got = attrsString(Diagnostics(err))
	for _, want := range []string{"provider=codex", "output_head=Sorry, I cannot help with that.", "output_bytes=31"} {
		if !strings.Contains(got, want) {
			t.Errorf("codex diagnostics %q missing %q", got, want)
		}
	}
}

func TestCommandErrorKeepsStderrAndExitCode(t *testing.T) {
	_, _, err := newExecRunner(CommandTimeout)(context.Background(), []string{"sh", "-c", "echo boom >&2; exit 3"})
	got := attrsString(Diagnostics(commandError("claude", err, nil, []byte("boom\n"))))
	for _, want := range []string{"exit_code=3", "stderr=boom", "command=claude"} {
		if !strings.Contains(got, want) {
			t.Errorf("diagnostics %q missing %q", got, want)
		}
	}
}

func TestHeadAndTailCutOnRuneBoundaries(t *testing.T) {
	if got := head("ñandú", 2); got != "ñ…" {
		t.Errorf("head = %q", got)
	}
	if got := tail([]byte("ñandú"), 2); got != "…ú" {
		t.Errorf("tail = %q", got)
	}
}
