package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"serve-html-markdown/internal/store"
)

const stubExpandJS = "/assets/expand-DIGEST.js"

// rubyBootstrapCases mirror internal/web/testdata/expand_bootstrap_ruby.rb.
func rubyBootstrapCases() map[string]ExpandBootstrap {
	nasty1 := "a b&c<d>\".md"
	return map[string]ExpandBootstrap{
		"plain_empty": {ExpansionMode: "create_new", ExpandJS: stubExpandJS},
		"anchor_versions": {
			ScrollAnchor: "section-2", ExpansionMode: "edit_in_place", ExpandJS: stubExpandJS,
			FileVersions: []FileVersionEntry{
				NewFileVersionEntry("notes.md", 1, false),
				NewFileVersionEntry("notes-v2.md", 2, true),
			},
		},
		"nasty": {
			ScrollAnchor: "a\"b</script><!--&'\u00e9\u2028\u2029\U0001F600", ExpansionMode: "create_new", ExpandJS: stubExpandJS,
			FileVersions: []FileVersionEntry{
				NewFileVersionEntry(nasty1, 1, true),
				NewFileVersionEntry("é.md", 2, false),
			},
		},
	}
}

// The expected strings are what ExpandBootstrapHelper#expand_bootstrap_tags
// emitted (generated with bin/rails runner; see the .rb next to the JSON). The
// only thing Go leaves out is the <meta name="csrf-token"> tag. The scripts
// are byte for byte the same, including Rails' < > & escapes.
func TestExpandBootstrapMatchesRubyHelper(t *testing.T) {
	raw, err := os.ReadFile("testdata/expand_bootstrap_ruby.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	metaRe := regexp.MustCompile(`^<meta name="csrf-token" content="[^"]*">`)
	cases := rubyBootstrapCases()
	if len(want) != len(cases) {
		t.Fatalf("%d Ruby cases, %d Go cases", len(want), len(cases))
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			exp, ok := want[name]
			if !ok || !metaRe.MatchString(exp) {
				t.Fatalf("Ruby output for %q missing or without the csrf meta: %q", name, exp)
			}
			exp = metaRe.ReplaceAllString(exp, "")
			// The one known difference: Rails leaves U+2028/U+2029 raw inside
			// the string, Go writes the equivalent \u2028/\u2029 escapes
			// (older JS engines treat the raw characters as line breaks).
			exp = strings.NewReplacer("\u2028", `\u2028`, "\u2029", `\u2029`).Replace(exp)
			got, err := ExpandBootstrapHTML(b)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != exp {
				t.Errorf("got\n%s\nwant\n%s", got, exp)
			}
		})
	}
}

func TestExpandBootstrapHTMLIsSafeAgainstScriptInjection(t *testing.T) {
	got, err := ExpandBootstrapHTML(ExpandBootstrap{
		ScrollAnchor:  `</script><script>alert(1)</script>`,
		ExpansionMode: `"</SCRIPT><!--`,
		ExpandJS:      stubExpandJS,
		FileVersions:  []FileVersionEntry{NewFileVersionEntry("</script>.md", 1, true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Exactly two <script> elements, the bootstrap and expand.js.
	if n := strings.Count(strings.ToLower(string(got)), "</script>"); n != 2 {
		t.Errorf("%d </script> in output:\n%s", n, got)
	}
	if strings.Contains(string(got), "alert(1)</script>") || strings.Contains(string(got), "<!--") {
		t.Errorf("unescaped payload:\n%s", got)
	}
}

func TestExpandBootstrapNilVersionsIsEmptyArray(t *testing.T) {
	got, _ := ExpandBootstrapHTML(ExpandBootstrap{ExpansionMode: "create_new", ExpandJS: stubExpandJS})
	if !strings.Contains(string(got), "window.__fileVersions = [];") {
		t.Errorf("got %s", got)
	}
}

func TestNewExpandBootstrapUsesDigestedExpandJS(t *testing.T) {
	srv := newTestServerNoDB(t)
	b := srv.NewExpandBootstrap("", nil, "edit_in_place")
	if b.ExpandJS != srv.AssetPath("expand.js") || !regexp.MustCompile(`^/assets/expand-[0-9a-f]{8}\.js$`).MatchString(b.ExpandJS) {
		t.Errorf("ExpandJS %q", b.ExpandJS)
	}
	html, _ := ExpandBootstrapHTML(b)
	if !strings.Contains(string(html), `<script src="`+b.ExpandJS+`" defer></script>`) {
		t.Errorf("got %s", html)
	}
}

func TestURLEncodeMatchesErbUtil(t *testing.T) {
	// Expected values from Ruby's ERB::Util.url_encode.
	for in, want := range map[string]string{
		"notes.md":     "notes.md",
		"a b.md":       "a%20b.md",
		"a+b&c=d.md":   "a%2Bb%26c%3Dd.md",
		"é ü.md":       "%C3%A9%20%C3%BC.md",
		"x/y?z#w%.md":  "x%2Fy%3Fz%23w%25.md",
		"a~b_c-d.md":   "a~b_c-d.md",
		"$,:;@!*'().m": "%24%2C%3A%3B%40%21%2A%27%28%29.m",
		"😀.md":         "%F0%9F%98%80.md",
	} {
		if got := URLEncode(in); got != want {
			t.Errorf("URLEncode(%q) = %q, want %q", in, got, want)
		}
	}
}

func renderMarkdown(t *testing.T, srv *Server, title, rendered string, b ExpandBootstrap) (*httptest.ResponseRecorder, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.renderMarkdownPage(rec, title, rendered, b)
	return rec, rec.Body.String()
}

func TestRenderMarkdownPage(t *testing.T) {
	srv := newTestServerNoDB(t)
	versions := []FileVersionEntry{NewFileVersionEntry("notes.md", 1, false), NewFileVersionEntry("notes-v2.md", 2, true)}
	rec, body := renderMarkdown(t, srv, "notes-v2.md", `<h1 id="hi">Hi</h1><p>a &amp; b</p>`, srv.NewExpandBootstrap("hi", versions, "create_new"))
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != defaultCacheControl {
		t.Errorf("Cache-Control %q", got)
	}
	for _, want := range []string{
		"<!DOCTYPE html>\n<html lang=\"en\">",
		`<meta charset="UTF-8">`,
		`<meta name="viewport" content="width=device-width, initial-scale=1.0">`,
		`<title>notes-v2.md</title>`,
		`<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Bookerly:ital,wght@0,400;0,700;1,400;1,700&display=swap">`,
		`<link rel="stylesheet" href="` + srv.AssetPath("markdown.css") + `">`,
		`<script>window.__scrollAnchor = "hi";window.__fileVersions = [{"name":"notes.md","url":"/notes.md","version":1,"current":false},{"name":"notes-v2.md","url":"/notes-v2.md","version":2,"current":true}];window.__expansionMode = "create_new";</script>`,
		`<script src="` + srv.AssetPath("expand.js") + `" defer></script>`,
		"<body>\n    <h1 id=\"hi\">Hi</h1><p>a &amp; b</p>\n  </body>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %s\n%s", want, body)
		}
	}
	// Order: stylesheet and bootstrap in <head>, the document in <body>.
	if !(strings.Index(body, "markdown-") < strings.Index(body, "__fileVersions") && strings.Index(body, "</head>") < strings.Index(body, `id="hi"`)) {
		t.Error("unexpected element order")
	}
	if strings.Contains(body, "application") {
		t.Error("markdown page must not load application.css")
	}
}

func TestRenderMarkdownPageEscapesTitleAndNoCSRF(t *testing.T) {
	srv := newTestServerNoDB(t)
	_, body := renderMarkdown(t, srv, `"><script>alert(1)</script> & é.md`, "<p>x</p>", srv.NewExpandBootstrap("", nil, "create_new"))
	if !strings.Contains(body, `<title>&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt; &amp; é.md</title>`) {
		t.Errorf("title not escaped:\n%s", body)
	}
	if strings.Contains(body, "alert(1)</script>") {
		t.Errorf("title injected a script:\n%s", body)
	}
	for _, bad := range []string{"csrf", "authenticity_token", "window.__scrollAnchor"} {
		if strings.Contains(body, bad) {
			t.Errorf("body contains %q", bad)
		}
	}
	if !strings.Contains(body, "window.__fileVersions = [];") {
		t.Error("nil versions must render as []")
	}
}

func TestRenderMarkdownPageScrollAnchorWithScriptCloser(t *testing.T) {
	srv := newTestServerNoDB(t)
	_, body := renderMarkdown(t, srv, "a.md", "<p>x</p>", srv.NewExpandBootstrap("x</script><b>", nil, "create_new"))
	if strings.Count(body, "</script>") != 2 || !strings.Contains(body, `window.__scrollAnchor = "x\u003c/script\u003e\u003cb\u003e";`) {
		t.Errorf("anchor not neutralised:\n%s", body)
	}
}

func TestRenderedAssetLinksResolve(t *testing.T) {
	// Every asset URL a page links must be served by the asset handler.
	srv := newTestServerNoDB(t)
	_, md := renderMarkdown(t, srv, "a.md", "", srv.NewExpandBootstrap("", nil, "create_new"))
	idx := httptest.NewRecorder()
	srv.renderIndex(idx, nil)
	signIn := do(srv, "GET", "/users/sign_in", nil).Body.String()
	re := regexp.MustCompile(`(?:href|src)="(/assets/[^"]+)"`)
	seen := 0
	for _, body := range []string{md, idx.Body.String(), signIn} {
		for _, m := range re.FindAllStringSubmatch(body, -1) {
			seen++
			if rec := do(srv, "GET", m[1], nil); rec.Code != 200 {
				t.Errorf("GET %s: %d", m[1], rec.Code)
			}
		}
	}
	if seen < 5 {
		t.Errorf("only %d asset links found", seen)
	}
}

func utc(s string) time.Time {
	tm, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		panic(err)
	}
	return tm
}

func TestRenderIndex(t *testing.T) {
	srv := newTestServerNoDB(t)
	files := []store.ServedFile{
		{Name: "notes-v2.md", UpdatedAt: utc("2024-03-05 18:30:00")},
		{Name: `a "b" <c> & d é.md`, UpdatedAt: utc("2024-01-02 00:05:00")},
	}
	rec := httptest.NewRecorder()
	srv.renderIndex(rec, files)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		`<title>Files</title>`,
		`<th>Name</th>`, `<th>Updated at</th>`,
		`<td><a href="/notes-v2.md">notes-v2.md</a></td>`,
		`<td>Mar 5, 2024 12:30 PM</td>`,
		`<td><a href="/a%20%22b%22%20%3Cc%3E%20%26%20d%20%C3%A9.md">a &#34;b&#34; &lt;c&gt; &amp; d é.md</a></td>`,
		`<td>Jan 1, 2024 6:05 PM</td>`,
		`<link rel="stylesheet" href="` + srv.AssetPath("application.css") + `">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %s\n%s", want, body)
		}
	}
	if strings.Index(body, "notes-v2.md") > strings.Index(body, "Jan 1, 2024") {
		t.Error("rows must keep the order given")
	}
	for _, bad := range []string{"csrf", "authenticity_token", "<form"} {
		if strings.Contains(body, bad) {
			t.Errorf("body contains %q", bad)
		}
	}
}

func TestRenderIndexEmptyList(t *testing.T) {
	srv := newTestServerNoDB(t)
	rec := httptest.NewRecorder()
	srv.renderIndex(rec, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<tbody>") || strings.Contains(rec.Body.String(), "<td>") {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}

// Expected strings from Ruby: in_time_zone("America/Mexico_City").strftime("%b %-d, %Y %-I:%M %p").
func TestIndexTimestampsMexicoCityIncludingDST(t *testing.T) {
	srv := newTestServerNoDB(t)
	for in, want := range map[string]string{
		"2022-04-03 07:59:00": "Apr 3, 2022 1:59 AM",  // last minute before DST began
		"2022-04-03 08:00:00": "Apr 3, 2022 3:00 AM",  // clocks jump 2:00 -> 3:00
		"2022-10-30 06:59:00": "Oct 30, 2022 1:59 AM", // still CDT
		"2022-10-30 07:00:00": "Oct 30, 2022 1:00 AM", // clocks fall back: repeated hour, CST
		"2023-07-01 12:00:00": "Jul 1, 2023 6:00 AM",  // DST abolished: CST all year
		"2024-12-31 23:59:00": "Dec 31, 2024 5:59 PM",
		"2024-01-02 00:05:00": "Jan 1, 2024 6:05 PM", // previous day, single-digit day and hour
		"2024-03-05 18:30:00": "Mar 5, 2024 12:30 PM",
	} {
		rec := httptest.NewRecorder()
		srv.renderIndex(rec, []store.ServedFile{{Name: "f.md", UpdatedAt: utc(in)}})
		if !strings.Contains(rec.Body.String(), "<td>"+want+"</td>") {
			t.Errorf("%s UTC: want %q in\n%s", in, want, rec.Body.String())
		}
	}
}

func TestNewPanicsWhenAssetMissing(t *testing.T) {
	// Guard for the requiredAssets list: every name must be embedded.
	srv := newTestServerNoDB(t)
	for _, name := range requiredAssets {
		if _, ok := srv.digests.public[name]; !ok {
			t.Errorf("%s not embedded", name)
		}
	}
}
