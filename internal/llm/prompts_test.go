package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// --- the prompts ---

// Golden hashes of the two templates, so any change to their wording is a
// deliberate one: update the hash together with the text.
const (
	goldenExpandPromptSHA256  = "c6edb3c13fd6178a4bc1c68a4682a94bf783d55c4135c3f36fea825a815d590b"
	goldenRewritePromptSHA256 = "0ecf0635329477ce40c442e5df5cf9d1c53ec18e4ff39b0cf26ecd7e4bc4bdfc"
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

// TestPromptsAskForHighlightableCode: the server colors <pre><code
// class="language-x"> with highlight.js (see web/templates/expand_bootstrap.html),
// so both prompts must ask the model for that markup, and the expand prompt
// must keep the model from shipping its own highlighter.
func TestPromptsAskForHighlightableCode(t *testing.T) {
	for name, tmpl := range map[string]string{"expand": ExpandPromptTemplate, "rewrite": RewritePromptTemplate} {
		for _, want := range []string{`<pre><code class="language-`, "highlight.js"} {
			if !strings.Contains(tmpl, want) {
				t.Errorf("%s prompt does not mention %q", name, want)
			}
		}
	}
	if !strings.Contains(ExpandPromptTemplate, "do not include any syntax highlighting library") {
		t.Error("expand prompt does not tell the model to leave the highlighter to the server")
	}
}

func TestPromptsKeepAnchorSentinel(t *testing.T) {
	if strings.Count(RewritePromptTemplate, AnchorSentinel) != 1 {
		t.Errorf("rewrite prompt should name %s exactly once", AnchorSentinel)
	}
}
