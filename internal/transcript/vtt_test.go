package transcript_test

import (
	"testing"

	"serve-html-markdown/internal/transcript"
)

// Ruby: VttTextTest "drops headers, timings and word tags, and collapses
// repeated rolling lines" (the heredoc, verbatim).
func TestVttTextDropsHeadersTimingsAndTags(t *testing.T) {
	vtt := "WEBVTT\n" +
		"Kind: captions\n" +
		"Language: en\n" +
		"\n" +
		"00:00:00.240 --> 00:00:02.150 align:start position:0%\n" +
		"\n" +
		"How<00:00:00.440><c> the</c><00:00:01.120><c> do</c>\n" +
		"\n" +
		"00:00:02.150 --> 00:00:02.160 align:start position:0%\n" +
		"How the do\n" +
		"\n" +
		"00:00:02.160 --> 00:00:03.630 align:start position:0%\n" +
		"How the do\n" +
		"Q&amp;A<00:00:02.400><c> time</c>\n"
	if got, want := transcript.VttText(vtt), "How the do\nQ&A time"; got != want {
		t.Errorf("VttText = %q, want %q", got, want)
	}
}

// Ruby: "passes plain text through", plus the corners of VttText.call.
func TestVttText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain text", "full transcript", "full transcript"},
		{"empty", "", ""},
		{"blank lines only", "\n\n  \n", ""},
		{"header words only at the start of a line", "WEBVTT - title\nKind: x\nLanguage: en\nNOTE a comment\nNOTE\nNOTES are kept\nWEBVTTX dropped", "NOTES are kept"},
		{"header after leading blanks is stripped before matching", "   WEBVTT\n\thello", "hello"},
		{"header in the middle of a line stays", "see Kind: this\nNot NOTE", "see Kind: this\nNot NOTE"},
		{"lowercase header is not a header", "webvtt\nnote x\nkind: y", "webvtt\nnote x\nkind: y"},
		{"cue numbers dropped", "1\n2\nsecond 3\n42\n", "second 3"},
		{"numbers with other characters kept", "3.5\n-1\n1 2\n", "3.5\n-1\n1 2"},
		{"timing lines dropped anywhere", "a --> b\nreal\n00:00:01.000 --> 00:00:02.000", "real"},
		{"tags removed", "<c.colorE5E5E5>Hi</c> <b>there</b>", "Hi there"},
		{"tag with newline-free attributes and angle in text", "a <b class=\"x\">b</b> c", "a b c"},
		{"unterminated tag stays", "a < b and c", "a < b and c"},
		{"entities decoded after tag removal", "&lt;b&gt;bold&lt;/b&gt;", "<b>bold</b>"},
		{"named entities not decoded", "a&nbsp;b &copy;", "a&nbsp;b &copy;"},
		{"numeric entities", "caf&#233; &#x1F600; &#39;q&#39;", "café 😀 'q'"},
		{"decoded amp not decoded twice", "&amp;amp; &amp;lt;", "&amp; &lt;"},
		{"nbsp entity result is not stripped", "a&#160;b", "a b"},
		{"runs of spaces squeezed", "a    b  c", "a b c"},
		{"tabs are not squeezed", "a\t\tb", "a\t\tb"},
		{"squeeze runs after tag removal", "a <c> </c> b", "a b"},
		{"line emptied by tags is dropped", "<c></c>\nx\n<00:00:01.000>", "x"},
		{"line emptied by entity-free whitespace after tags is dropped", "<c> </c>\nx", "x"},
		{"consecutive duplicates collapse", "a\na\na\nb\nb\na", "a\nb\na"},
		{"duplicates compared after cleaning", "x <c>y</c>\nx y\nx  y", "x y"},
		{"non consecutive duplicates stay", "a\nb\na", "a\nb\na"},
		{"crlf line endings", "WEBVTT\r\n\r\n1\r\n00:00:00.000 --> 00:00:01.000\r\nhello\r\nhello\r\nworld\r\n", "hello\nworld"},
		{"lone cr is not a line break", "a\rb", "a\rb"},
		{"strips whitespace around lines", "  padded  \n\tx\t", "padded\nx"},
		{"no trailing newline in output", "a\nb\n\n", "a\nb"},
		{"vertical tab and form feed are whitespace to strip", "\vhello\f\nworld", "hello\nworld"},
		{"unicode spaces are not stripped", " a ", " a "},
	}
	for _, c := range cases {
		if got := transcript.VttText(c.in); got != c.want {
			t.Errorf("%s: VttText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
