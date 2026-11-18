package textutil

import "testing"

func TestCGIUnescape(t *testing.T) {
	// Expected values were produced by Ruby 3.4.8's CGI.unescapeHTML.
	tests := []struct{ in, want string }{
		{"&amp;&nbsp;&#233;&#xE9;&bogus;", "&&nbsp;éé&bogus;"},
		{"&amp; &lt; &gt; &quot; &apos; &#39;", `& < > " ' '`},
		{"&AMP; &QUOT; &Amp;", "&AMP; &QUOT; &Amp;"},
		{"&#0;", "\x00"},
		{"&#x;", "&#x;"},
		{"&#;", "&#;"},
		{"&#X41;&#x41;&#65;", "AAA"},
		{"&#xx41;", "&#xx41;"},
		{"&#-1;&#+65;&# 65;", "&#-1;&#+65;&# 65;"},
		{"&#00000000000000000065;&#x0000000000000041;", "AA"},
		{"&#99999999999999999999;&#xFFFFFFFFFFFF;", "&#99999999999999999999;&#xFFFFFFFFFFFF;"},
		{"&#1114110;", "\U0010FFFE"},
		{"&#1114111;&#x10FFFF;&#1114112;&#x110000;", "&#1114111;&#x10FFFF;&#1114112;&#x110000;"},
		{"&#xD800;", "\xED\xA0\x80"}, // surrogates are emitted raw, as invalid UTF-8
		{"&#55296;", "\xED\xA0\x80"},
		{"&#128512;&#x1F600;", "😀😀"},
		{"&amp;amp;", "&amp;"}, // single pass
		{"&&amp;", "&&"},
		{"&amp", "&amp"},
		{"no entities", "no entities"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := CGIUnescape(tc.in); got != tc.want {
			t.Errorf("CGIUnescape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
