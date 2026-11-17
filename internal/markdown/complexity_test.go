package markdown

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTooComplexNesting(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{"plain", "# Title\n\ntext\n", false},
		{"64 quotes", strings.Repeat(">", 64) + " x\n", false},
		{"65 quotes", strings.Repeat(">", 65) + " x\n", true},
		{"spaced quotes", strings.Repeat("> ", 65) + "x\n", true},
		{"64 bullets", strings.Repeat("- ", 64) + "x\n", false},
		{"65 bullets", strings.Repeat("- ", 65) + "x\n", true},
		{"mixed", strings.Repeat("> - ", 33) + "x\n", true},
		{"numbered", strings.Repeat("1. ", 65) + "x\n", true},
		{"numbered paren", strings.Repeat("12) ", 65) + "x\n", true},
		{"deep indent with a bullet", strings.Repeat(" ", 200) + "- x\n", true},
		{"deep indent without one", strings.Repeat(" ", 5000) + "x\n", false},
		{"thematic break", "---\n***\n- - -\n", false},
		{"hr not a bullet", strings.Repeat("-", 5000) + "\n", false},
		{"quote chars in text", "a " + strings.Repeat(">", 5000) + "\n", false},
		{"ten-digit number is text", strings.Repeat("1234567890. ", 100) + "\n", false},
		{"late line", strings.Repeat("ok\n", 1000) + strings.Repeat(">", 100) + "\n", true},
		{"no trailing newline", "text\n" + strings.Repeat(">", 100), true},
	} {
		if got := tooComplex([]byte(tc.src)); got != tc.want {
			t.Errorf("%s: tooComplex = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestTooComplexReferenceDefinitions(t *testing.T) {
	refs := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString("[r" + strconv.Itoa(i) + "]: http://example.com\n")
		}
		return b.String()
	}
	if tooComplex([]byte(refs(maxReferenceDefinitions))) {
		t.Error("exactly the limit is rejected")
	}
	if !tooComplex([]byte(refs(maxReferenceDefinitions + 1))) {
		t.Error("over the limit is accepted")
	}
	for _, s := range []string{"    [a]: b\n", "[a] b\n", "[]: b\n", "text [a]: b\n", "[a]\n", "["} {
		if isReferenceDefinition([]byte(s)) {
			t.Errorf("%q looks like a definition", s)
		}
	}
	for _, s := range []string{"[a]: b", "   [a]: b", "[a b]:c"} {
		if !isReferenceDefinition([]byte(s)) {
			t.Errorf("%q is not seen as a definition", s)
		}
	}
}

// The limits only reject; a document just inside them renders as before.
func TestRenderAtTheLimits(t *testing.T) {
	out, err := Render([]byte(strings.Repeat("> ", 32) + "deep\n"))
	if err != nil || !strings.Contains(string(out), "deep") {
		t.Errorf("32 nested quotes: %v %q", err, out)
	}
	var b strings.Builder
	for i := 0; i < maxReferenceDefinitions; i++ {
		b.WriteString("[r" + strconv.Itoa(i) + "]: /x\n")
	}
	b.WriteString("[r42]\n")
	out, err = Render([]byte(b.String()))
	if err != nil || !strings.Contains(string(out), `<a href="/x">r42</a>`) {
		t.Errorf("10000 definitions: %v", err)
	}
}

// The adversarial documents that used to take 2 s (80 KiB of ">") to minutes
// (a few MiB of them, or of reference definitions) are rejected up front.
func TestRenderRejectsQuadraticInput(t *testing.T) {
	for name, src := range map[string]string{
		"5 MiB of >":          strings.Repeat(">", 5<<20),
		"2 MiB of >":          strings.Repeat(">", 2<<20),
		"5 MiB of [a]: b":     strings.Repeat("[a]: b\n", 5<<20/7),
		"2 MiB of [a]: b":     strings.Repeat("[a]: b\n", 2<<20/7),
		"5 MiB of > > > ...":  strings.Repeat("> ", 2<<20) + "x\n",
		"deep nested bullets": strings.Repeat("- ", 1<<20) + "x\n",
	} {
		start := time.Now()
		_, err := Render([]byte(src))
		elapsed := time.Since(start)
		if !errors.Is(err, ErrTooComplex) {
			t.Errorf("%s: err = %v, want ErrTooComplex", name, err)
		}
		if elapsed > time.Second {
			t.Errorf("%s: took %v, want well under a second", name, elapsed)
		}
	}
}
