package markdown

import (
	"strings"
	"testing"
)

func TestHardBreakDropsAllTrailingSpaces(t *testing.T) {
	for _, n := range []int{2, 3, 5, 79} {
		src := "a\n❯ /resume" + strings.Repeat(" ", n) + "\nnext\n"
		out, err := Render([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		if want := "<p>a\n❯ /resume<br />\nnext</p>\n"; string(out) != want {
			t.Errorf("%d spaces: got %q, want %q", n, out, want)
		}
	}
	// A hard break after inline markup keeps the markup's own text.
	out, _ := Render([]byte("**bold**" + strings.Repeat(" ", 10) + "\nnext\n"))
	if want := "<p><strong>bold</strong><br />\nnext</p>\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}
