package markdown

import (
	"strings"
	"testing"

	"github.com/yuin/goldmark/parser"
)

// Direct tests of the ported scanners. The expected values are comrak's
// (autolink.rs and cmark's smart punctuation); rules_test.go checks the same
// rules end to end against real Commonmarker output.

func TestDashes(t *testing.T) {
	// cmark handle_hyphen: n%3==0 em; n%2==0 en; n%3==2 one en + ems; else two en + ems.
	for n, w := range map[int]string{2: "–", 3: "—", 4: "––", 5: "—–", 6: "——", 7: "—––", 9: "———"} {
		if got := dashes(n); got != w {
			t.Errorf("dashes(%d) = %q, want %q", n, got, w)
		}
	}
}

func TestAutolinkDelim(t *testing.T) {
	// autolink_delim: trailing ? ! . , : * _ ~ ' " are dropped, a trailing
	// entity reference is dropped whole, a ")" only when unbalanced, and
	// everything from a "<" on is dropped.
	cases := []struct {
		data string
		end  int
		want int
	}{
		{"a.com.", 6, 5},
		{"a.com?!.,:*_~'\"", 15, 5},
		{"a.com/(b)", 9, 9},
		{"a.com/(b))", 10, 9},
		{"a.com/b)", 8, 7},
		{"a.com/b)))", 10, 7},
		{"a.com/&amp;", 11, 6},
		{"a.com/&amp", 10, 10},
		{"a.com/<b>", 9, 6},
		{"a.com/a;", 8, 7},
		{")", 1, 0},
	}
	for _, c := range cases {
		if got := autolinkDelim(c.data, c.end); got != c.want {
			t.Errorf("autolinkDelim(%q, %d) = %d, want %d", c.data, c.end, got, c.want)
		}
	}
}

func TestCheckDomain(t *testing.T) {
	// check_domain: scanning starts at index 1; underscores in the last two
	// labels reject the domain (unless there are more than 10 dots); a
	// non-ASCII character ends the scan; without allowShort at least one dot
	// is required.
	cases := []struct {
		data  string
		short bool
		want  bool
	}{
		{"/a.com", true, true},
		{"/a_b.com", true, false},
		{"/_a.com", true, false},
		{"/a.b_c", true, false},
		{"/a", true, true},
		{"www.a", false, true},
		{"www.", false, true}, // "www." alone links as "www"; the dot is trimmed later
		{"www", false, false},
		{"www.café.com", false, true},
		{"www.a\\_b.com", false, false},
	}
	for _, c := range cases {
		if _, got := checkDomain(c.data, c.short); got != c.want {
			t.Errorf("checkDomain(%q, %v) ok = %v, want %v", c.data, c.short, got, c.want)
		}
	}
}

func TestEmailMatch(t *testing.T) {
	cases := []struct {
		s          string
		at         int
		url        string
		rewind     int
		skip       int
		wantOK     bool
		descripton string
	}{
		{"a@b.com", 1, "mailto:a@b.com", 1, 7, true, "plain address"},
		{"x a.b+c_d-e@f.g.com y", 11, "mailto:a.b+c_d-e@f.g.com", 9, 17, true, "local part characters"},
		{"mailto:a@b.com", 8, "mailto:a@b.com", 8, 14, true, "mailto: prefix stays in the text, href unchanged"},
		{"xmailto:a@b.com", 9, "mailto:a@b.com", 1, 7, true, "a letter before the prefix word rejects the prefix"},
		{"xmpp:a@b.com/txt", 6, "xmpp:a@b.com/txt", 6, 16, true, "xmpp resource"},
		{"a@b", 1, "", 0, 0, false, "domain needs a dot"},
		{"@b.com", 0, "", 0, 0, false, "empty local part"},
		{"a@b@c.com", 1, "", 0, 0, false, "second @"},
		{"a@b.com", 1, "mailto:a@b.com", 1, 7, true, "end of text"},
	}
	for _, c := range cases {
		url, rewind, skip, ok := emailMatch(c.s, c.at)
		if ok != c.wantOK || (ok && (url != c.url || rewind != c.rewind || skip != c.skip)) {
			t.Errorf("%s: emailMatch(%q, %d) = %q %d %d %v, want %q %d %d %v", c.descripton, c.s, c.at, url, rewind, skip, ok, c.url, c.rewind, c.skip, c.wantOK)
		}
	}
}

func TestWWWAhead(t *testing.T) {
	cases := map[string]bool{
		"www.a": true, " www.a": true, "(www.a": true, "~www.a": true, "*www.a": true,
		"**www.a": true, "_www.a": true, "\\[www.a": true, "\\.www.a": false,
		"a www.a": false, "www": false, " ww.a": false, "x": false, "*": false, "\\": false,
	}
	for in, want := range cases {
		if got := wwwAhead([]byte(in)); got != want {
			t.Errorf("wwwAhead(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestQuoteFlanking(t *testing.T) {
	// comrak scan_delims for quotes, can_open / can_close.
	cases := []struct {
		before, after     rune
		canOpen, canClose bool
	}{
		{' ', 'a', true, false},
		{'a', ' ', false, true},
		{'a', 'b', false, true},
		{'(', '.', true, true},
		{'[', '.', true, true},
		{'.', '(', false, true},
		{')', 'a', false, false},
		{']', 'a', false, false},
		{' ', ' ', false, false},
	}
	for _, c := range cases {
		o, cl := quoteFlanking(c.before, c.after)
		if o != c.canOpen || cl != c.canClose {
			t.Errorf("quoteFlanking(%q, %q) = %v, %v, want %v, %v", c.before, c.after, o, cl, c.canOpen, c.canClose)
		}
	}
}

// TestHTMLBlockParserIsInstalled fails if blockParsers() stops replacing
// goldmark's HTML block parser (for example after an upgrade changes its
// priority): goldmark's own parser treats "</ div>" as a block and does not
// let a lone "</pre>" start one.
func TestHTMLBlockParserIsInstalled(t *testing.T) {
	found := 0
	for _, p := range blockParsers() {
		if _, ok := p.Value.(htmlBlockParser); ok {
			found++
		}
		if p.Value == parser.NewHTMLBlockParser() {
			t.Errorf("goldmark's default HTML block parser is still registered")
		}
	}
	if found != 1 {
		t.Fatalf("htmlBlockParser registered %d times, want 1", found)
	}
	out, _ := Render([]byte("</ div>\n"))
	if string(out) != "<p>&lt;/ div&gt;</p>\n" {
		t.Errorf("</ div> rendered as %q", out)
	}
	out, _ = Render([]byte("</pre>\n"))
	if string(out) != "</pre>\n" {
		t.Errorf("</pre> rendered as %q", out)
	}
	out, _ = Render([]byte("- a\n<b>\n"))
	if !strings.Contains(string(out), "</ul>\n<b>") {
		t.Errorf("lazy type 7 start not applied: %q", out)
	}
}
