package markdown

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// knownDifferences lists golden files that are allowed to differ outside code
// blocks, with the reason. Each entry must also be described in
// testdata/golden/markdown/DIFFERENCES.txt. It is intentionally empty unless a
// difference was explicitly approved.
var knownDifferences = map[string]string{}

var (
	preBlock = regexp.MustCompile(`(?s)<pre\b.*?</pre>`)
	spanTag  = regexp.MustCompile(`</?span\b[^>]*>`)
)

// stripCodeSpans removes the <span ...> / </span> tags inside <pre>…</pre>
// blocks. syntect (Commonmarker) and chroma tokenise differently, so the
// per-token spans of highlighted code are not comparable byte for byte; the
// wrappers and the escaped text still are.
func stripCodeSpans(s string) string {
	return preBlock.ReplaceAllStringFunc(s, func(block string) string {
		return spanTag.ReplaceAllString(block, "")
	})
}

func TestGolden(t *testing.T) {
	inputs, err := filepath.Glob("../../testdata/golden/markdown/*.md")
	if err != nil || len(inputs) == 0 {
		t.Fatalf("no golden inputs found: %v", err)
	}
	for _, in := range inputs {
		name := filepath.Base(in)
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(strings.TrimSuffix(in, ".md") + ".html")
			if err != nil {
				t.Fatal(err)
			}
			got, err := Render(src)
			if err != nil {
				t.Fatal(err)
			}
			w, g := stripCodeSpans(string(want)), stripCodeSpans(string(got))
			if w == g {
				if reason, ok := knownDifferences[name]; ok {
					t.Errorf("%s is listed in knownDifferences (%s) but now matches; remove the entry", name, reason)
				}
				return
			}
			if reason, ok := knownDifferences[name]; ok {
				t.Skipf("known difference: %s", reason)
			}
			t.Errorf("mismatch: %s", firstDiff(w, g))
		})
	}
}

// firstDiff describes the first differing line.
func firstDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var a, b string
		if i < len(wl) {
			a = wl[i]
		}
		if i < len(gl) {
			b = gl[i]
		}
		if a != b {
			return fmt.Sprintf("first difference at line %d\n--- want\n%s\n--- got\n%s", i+1, clip(a), clip(b))
		}
	}
	return "identical"
}

func clip(s string) string {
	if len(s) > 800 {
		return s[:800] + "..."
	}
	return s
}
