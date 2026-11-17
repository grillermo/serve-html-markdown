package markdown

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// The limits below are deliberately generous (the fixed code takes a few
// milliseconds to a few hundred) so the tests catch algorithmic regressions
// (seconds to minutes) without being flaky. Run them without -race: the race
// detector slows everything down by an order of magnitude, so they skip there.

func renderWithin(t *testing.T, name, src string, limit time.Duration) string {
	t.Helper()
	if raceEnabled {
		t.Skip("timing bounds are only meaningful without -race")
	}
	start := time.Now()
	out, err := Render([]byte(src))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	t.Logf("%s: %d bytes in, %d bytes out, %v", name, len(src), len(out), elapsed)
	if elapsed > limit {
		t.Errorf("%s took %v, limit %v", name, elapsed, limit)
	}
	return string(out)
}

func TestDoSHighlighting(t *testing.T) {
	cases := map[string]string{
		"yaml [":                 "```yaml\n" + strings.Repeat("[", 30000) + "\n```\n",
		"yaml '":                 "```yaml\n" + strings.Repeat("'", 30000) + "\n```\n",
		"cpp [":                  "```cpp\n" + strings.Repeat("[", 30000) + "\n```\n",
		"yaml [ 2000-byte lines": "```yaml\n" + strings.Repeat(strings.Repeat("[", 2000)+"\n", 15) + "```\n",
		"cpp [ 2000-byte lines":  "```cpp\n" + strings.Repeat(strings.Repeat("[", 2000)+"\n", 15) + "```\n",
		"yaml [ per line":        "```yaml\n" + strings.Repeat("[\n", 15000) + "```\n",
		"cpp ( per line":         "```cpp\n" + strings.Repeat("(\n", 15000) + "```\n",
		"json [ per line":        "```json\n" + strings.Repeat("[\n", 15000) + "```\n",
		"html <?php":             "```html\n" + strings.Repeat("<?php ", 5000) + "\n```\n",
	}
	start := time.Now()
	for name, src := range cases {
		out := renderWithin(t, name, src, time.Second)
		if !strings.Contains(out, `<pre style="background-color:#2b303b;"><code class="language-`) {
			t.Errorf("%s: wrapper missing: %.200s", name, out)
		}
	}
	if !raceEnabled && time.Since(start) > time.Second {
		t.Errorf("all highlighting repros took %v, want < 1s", time.Since(start))
	}
}

func TestHighlightTimeoutFallsBackToPlain(t *testing.T) {
	old := highlightTimeout
	highlightTimeout = time.Nanosecond
	defer func() { highlightTimeout = old }()
	src := "```go\n" + strings.Repeat("func main() { println(\"x\") }\n", 200) + "```\n"
	out, err := Render([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	// Plain text has exactly one colour: the foreground.
	if strings.Contains(string(out), "#b48ead") {
		t.Errorf("block was highlighted despite an expired deadline")
	}
	if !strings.Contains(string(out), `<code class="language-go">`) {
		t.Errorf("wrapper missing")
	}
}

func TestLongLineIsNotHighlighted(t *testing.T) {
	src := "```go\n" + strings.Repeat("a", maxHighlightLine+1) + "\n```\n"
	out, _ := Render([]byte(src))
	if strings.Count(string(out), "<span") != 1 {
		t.Errorf("expected one plain span, got %.300s", out)
	}
	short := "```go\nfunc main() {}\n```\n"
	out, _ = Render([]byte(short))
	if strings.Count(string(out), "<span") < 2 {
		t.Errorf("short block should still be highlighted: %s", out)
	}
}

func TestDoSInline(t *testing.T) {
	const n = 128 << 10
	cases := map[string]string{
		"www trigger spaces":  strings.Repeat("a ", n/2),
		"www trigger parens":  strings.Repeat("(a", n/2),
		"url repeated":        strings.Repeat("http://a ", n/9),
		"www repeated":        strings.Repeat("www.a.com ", n/10),
		"emails at-at":        strings.Repeat("a@", n/2),
		"trailing parens":     "www.a.com" + strings.Repeat(")", n),
		"url trailing parens": "http://a.com/" + strings.Repeat(")", n),
		"email many dots":     strings.Repeat("a.", n/2) + "@b.com",
		"brackets":            strings.Repeat("[", n/2),
		"quotes":              strings.Repeat("'a ", n/3),
	}
	for name, src := range cases {
		renderWithin(t, name, src, 2*time.Second)
	}
}

func TestDoSDuplicateHeadings(t *testing.T) {
	const n = 20000
	src := strings.Repeat("# same\n\n", n)
	out := renderWithin(t, "20000 duplicate headings", src, 2*time.Second)
	for _, id := range []string{`id="same"`, `id="same-1"`, fmt.Sprintf(`id="same-%d"`, n-1)} {
		if !strings.Contains(out, id) {
			t.Errorf("missing %s", id)
		}
	}
}

func TestDuplicateIDsCollideWithExplicit(t *testing.T) {
	// comrak's taken set only grows: a generated "a-1" is skipped when an
	// earlier heading already produced it.
	az := newAnchorizer()
	var got []string
	for _, h := range []string{"a", "a-1", "a", "a", "a-1", "A"} {
		got = append(got, az.anchorize(h))
	}
	want := []string{"a", "a-1", "a-2", "a-3", "a-1-1", "a-4"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}
