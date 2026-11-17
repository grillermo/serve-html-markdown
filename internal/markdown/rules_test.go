package markdown

// Generated from real Commonmarker output: each table row is an input and the
// HTML Commonmarker 1.1.5 produces for it with the app options. They pin the
// ported comrak/cmark rules (cited above each table) so that a goldmark or
// chroma upgrade cannot silently change them.

import "testing"

type ruleCase struct{ in, want string }

func runRuleCases(t *testing.T, cases []ruleCase) {
	t.Helper()
	for _, c := range cases {
		got, err := Render([]byte(c.in))
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// TestRulesURLAutolink
// Rule: comrak autolink.rs url_match: http/https/ftp only, case-sensitive, scanned on raw source, trailing punctuation trimmed by autolink_delim; never inside an open [.
func TestRulesURLAutolink(t *testing.T) {
	cases := []ruleCase{
		{"http://a.com", "<p><a href=\"http://a.com\">http://a.com</a></p>\n"},
		{"https://a.com/x?y=1&z=2", "<p><a href=\"https://a.com/x?y=1&amp;z=2\">https://a.com/x?y=1&amp;z=2</a></p>\n"},
		{"ftp://a.com", "<p><a href=\"ftp://a.com\">ftp://a.com</a></p>\n"},
		{"HTTP://a.com", "<p>HTTP://a.com</p>\n"},
		{"xhttp://a.com", "<p>xhttp://a.com</p>\n"},
		{"http://a.com/a--b", "<p><a href=\"http://a.com/a--b\">http://a.com/a--b</a></p>\n"},
		{"http://a.com.", "<p><a href=\"http://a.com\">http://a.com</a>.</p>\n"},
		{"http://a.com/(b)", "<p><a href=\"http://a.com/(b)\">http://a.com/(b)</a></p>\n"},
		{"(http://a.com)", "<p>(<a href=\"http://a.com\">http://a.com</a>)</p>\n"},
		{"http://a.com/&amp;", "<p><a href=\"http://a.com/\">http://a.com/</a>&amp;</p>\n"},
		{"http://a_b.com", "<p>http://a_b.com</p>\n"},
		{"http://_a.com", "<p>http://_a.com</p>\n"},
		{"[http://a.com]", "<p>[http://a.com]</p>\n"},
		{"[a http://b.com", "<p>[a http://b.com</p>\n"},
		{"[a] http://b.com", "<p>[a] <a href=\"http://b.com\">http://b.com</a></p>\n"},
		{"[a](http://b.com)", "<p><a href=\"http://b.com\">a</a></p>\n"},
		{"\"http://a.com\"", "<p>“<a href=\"http://a.com\">http://a.com</a>”</p>\n"},
		{"http://a.com/\\_b", "<p><a href=\"http://a.com/%5C_b\">http://a.com/\\_b</a></p>\n"},
		{"http://a.com/<b>x", "<p><a href=\"http://a.com/\">http://a.com/</a><b>x</p>\n"},
	}
	runRuleCases(t, cases)
}

// TestRulesWWWAutolink
// Rule: comrak autolink.rs www_match: needs whitespace, start of line or one of ( * _ ~ before www., http:// is prepended to the href, never inside an open [.
func TestRulesWWWAutolink(t *testing.T) {
	cases := []ruleCase{
		{"www.a.com", "<p><a href=\"http://www.a.com\">www.a.com</a></p>\n"},
		{"x www.a.com", "<p>x <a href=\"http://www.a.com\">www.a.com</a></p>\n"},
		{"xwww.a.com", "<p>xwww.a.com</p>\n"},
		{"(www.a.com)", "<p>(<a href=\"http://www.a.com\">www.a.com</a>)</p>\n"},
		{"*www.a.com*", "<p><em><a href=\"http://www.a.com\">www.a.com</a></em></p>\n"},
		{"_www.a.com_", "<p><em>www.a.com</em></p>\n"},
		{"~www.a.com", "<p>~<a href=\"http://www.a.com\">www.a.com</a></p>\n"},
		{"\"www.a.com\"", "<p>“www.a.com”</p>\n"},
		{"www.a.com/a--b", "<p><a href=\"http://www.a.com/a--b\">www.a.com/a--b</a></p>\n"},
		{"www.a_b.com", "<p>www.a_b.com</p>\n"},
		{"\\_www.a.com", "<p>_<a href=\"http://www.a.com\">www.a.com</a></p>\n"},
		{"\\[www.a.com", "<p>[<a href=\"http://www.a.com\">www.a.com</a></p>\n"},
		{"[www.a.com]", "<p>[www.a.com]</p>\n"},
		{"www.", "<p><a href=\"http://www\">www</a>.</p>\n"},
		{"www.a", "<p><a href=\"http://www.a\">www.a</a></p>\n"},
		{"a--bwww.a.com", "<p>a–bwww.a.com</p>\n"},
		{"www.a.com\nwww.b.com", "<p><a href=\"http://www.a.com\">www.a.com</a>\n<a href=\"http://www.b.com\">www.b.com</a></p>\n"},
	}
	runRuleCases(t, cases)
}

// TestRulesEmailAutolink
// Rule: comrak autolink.rs email_match: local part alnum . + - _, one @, domain with a dot, optional mailto:/xmpp: prefix kept in the text; skipped inside [ ] depth > 0; works on text after smart punctuation.
func TestRulesEmailAutolink(t *testing.T) {
	cases := []ruleCase{
		{"a@b.com", "<p><a href=\"mailto:a@b.com\">a@b.com</a></p>\n"},
		{"a.b+c_d-e@f.g.com", "<p><a href=\"mailto:a.b+c_d-e@f.g.com\">a.b+c_d-e@f.g.com</a></p>\n"},
		{"@b.com", "<p>@b.com</p>\n"},
		{"a@b", "<p>a@b</p>\n"},
		{"a@b.", "<p>a@b.</p>\n"},
		{"a@@b.com", "<p>a@@b.com</p>\n"},
		{"a@b@c.com", "<p>a@<a href=\"mailto:b@c.com\">b@c.com</a></p>\n"},
		{"mailto:a@b.com", "<p><a href=\"mailto:a@b.com\">mailto:a@b.com</a></p>\n"},
		{"xmailto:a@b.com", "<p>xmailto:<a href=\"mailto:a@b.com\">a@b.com</a></p>\n"},
		{"1mailto:a@b.com", "<p><a href=\"1mailto:a@b.com\">1mailto:a@b.com</a></p>\n"},
		{"xmpp:a@b.com/txt", "<p><a href=\"xmpp:a@b.com/txt\">xmpp:a@b.com/txt</a></p>\n"},
		{"xmpp:a@b.com/", "<p>xmpp:a@b.com/</p>\n"},
		{"foo:a@b.com", "<p>foo:<a href=\"mailto:a@b.com\">a@b.com</a></p>\n"},
		{"a@b.com.", "<p><a href=\"mailto:a@b.com\">a@b.com</a>.</p>\n"},
		{"[a@b.com]", "<p>[a@b.com]</p>\n"},
		{"[a b@c.com", "<p>[a b@c.com</p>\n"},
		{"[a] b@c.com", "<p>[a] <a href=\"mailto:b@c.com\">b@c.com</a></p>\n"},
		{"] [ a@b.com", "<p>] [ <a href=\"mailto:a@b.com\">a@b.com</a></p>\n"},
		{"[[ ] a@b.com", "<p>[[ ] a@b.com</p>\n"},
		{"a--b@c.com", "<p>a–<a href=\"mailto:b@c.com\">b@c.com</a></p>\n"},
		{"a@b--c.com", "<p>a@b–c.com</p>\n"},
		{"(a@b.com)", "<p>(<a href=\"mailto:a@b.com\">a@b.com</a>)</p>\n"},
		{"/a@b.com", "<p>/<a href=\"mailto:a@b.com\">a@b.com</a></p>\n"},
	}
	runRuleCases(t, cases)
}

// TestRulesSmartQuotes
// Rule: comrak scan_delims for quotes: can_open = left-flanking and (not right-flanking or after ( or [) and not after ) or ]; unmatched ' is ’, unmatched " is “ or ” by can_close; a closer pairs with the nearest unmatched opener of the same character.
func TestRulesSmartQuotes(t *testing.T) {
	cases := []ruleCase{
		{"\"a\" 'b'", "<p>“a” ‘b’</p>\n"},
		{"it's", "<p>it’s</p>\n"},
		{"'tis", "<p>’tis</p>\n"},
		{"('a')", "<p>(‘a’)</p>\n"},
		{"[\"a\"]", "<p>[“a”]</p>\n"},
		{"\"\" x", "<p>“” x</p>\n"},
		{"[\"\" x", "<p>[“” x</p>\n"},
		{"'a' 'b'", "<p>‘a’ ‘b’</p>\n"},
		{"'a 'b' c'", "<p>‘a ‘b’ c’</p>\n"},
		{"\"a 'b\" c'", "<p>“a ‘b” c’</p>\n"},
		{"a\"b", "<p>a”b</p>\n"},
		{"\"", "<p>“</p>\n"},
		{"'", "<p>’</p>\n"},
		{"(\"a\")", "<p>(“a”)</p>\n"},
		{")'a", "<p>)’a</p>\n"},
		{"x']'y", "<p>x’]’y</p>\n"},
	}
	runRuleCases(t, cases)
}

// TestRulesSmartDashesAndDots
// Rule: cmark/comrak handle_hyphen and handle_period: runs of hyphens n%3==0 em, n%2==0 en, else en/em mix with ems first; ... is an ellipsis, .. is not.
func TestRulesSmartDashesAndDots(t *testing.T) {
	cases := []ruleCase{
		{"a - b", "<p>a - b</p>\n"},
		{"a -- b", "<p>a – b</p>\n"},
		{"a --- b", "<p>a — b</p>\n"},
		{"a ---- b", "<p>a –– b</p>\n"},
		{"a ----- b", "<p>a —– b</p>\n"},
		{"a ------ b", "<p>a —— b</p>\n"},
		{"a ------- b", "<p>a —–– b</p>\n"},
		{"a -------- b", "<p>a –––– b</p>\n"},
		{"a... b", "<p>a… b</p>\n"},
		{"a.. b", "<p>a.. b</p>\n"},
		{"a.... b", "<p>a…. b</p>\n"},
		{"a...... b", "<p>a…… b</p>\n"},
	}
	runRuleCases(t, cases)
}

// TestRulesHTMLBlockStarts
// Rule: cmark html block starts: type 7 (a complete tag alone on a line, tab allowed after it) may start on a lazy continuation line; closing tags are </name with no space; </pre> </script> </style> alone on a line are type 7 blocks.
func TestRulesHTMLBlockStarts(t *testing.T) {
	cases := []ruleCase{
		{"</ div>", "<p>&lt;/ div&gt;</p>\n"},
		{"</pre>", "</pre>\n"},
		{"</script>\ntext", "</script>\ntext\n"},
		{"<b>\t", "<b>\t\n"},
		{"<b> \ntext", "<b> \ntext\n"},
		{"- a\n<b>", "<ul>\n<li>a</li>\n</ul>\n<b>\n"},
		{"- a\n<div>", "<ul>\n<li>a</li>\n</ul>\n<div>\n"},
		{"> a\n<span>", "<blockquote>\n<p>a</p>\n</blockquote>\n<span>\n"},
		{"a\n<b>", "<p>a\n<b></p>\n"},
		{"<div\tclass=x>\ntext", "<div\tclass=x>\ntext\n"},
		{"</div x>", "</div x>\n"},
		{"<a href='x'>\n\ntext", "<a href='x'>\n<p>text</p>\n"},
		{"- a\n</pre>", "<ul>\n<li>a</li>\n</ul>\n</pre>\n"},
	}
	runRuleCases(t, cases)
}
