package markdown

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestRenderConcurrent(t *testing.T) {
	inputs, _ := filepath.Glob("../../testdata/golden/markdown/*.md")
	if len(inputs) == 0 {
		t.Fatal("no inputs")
	}
	type doc struct {
		src, want []byte
	}
	var docs []doc
	for _, in := range inputs {
		src, err := os.ReadFile(in)
		if err != nil {
			t.Fatal(err)
		}
		want, err := Render(src)
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, doc{src, want})
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range docs {
				d := docs[(i+g)%len(docs)]
				got, err := Render(d.src)
				// Under -race the lexers can hit the per-block time cap and fall back
				// to plain text, which only changes the <span>s inside <pre>.
				if err != nil || stripCodeSpans(string(got)) != stripCodeSpans(string(d.want)) {
					t.Errorf("concurrent render differs (err=%v)", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestRenderInvalidUTF8(t *testing.T) {
	_, err := Render([]byte("a\xffb"))
	if !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("err = %v, want ErrInvalidUTF8", err)
	}
}

func TestRenderNormalisation(t *testing.T) {
	cases := map[string]string{
		"x\x00y":               "<p>x\uFFFDy</p>\n",
		"\ufeff# h":            "<h1><a href=\"#h\" aria-hidden=\"true\" class=\"anchor\" id=\"h\"></a>h</h1>\n",
		"a\r\nb\rc":            "<p>a\nb\nc</p>\n",
		"":                     "",
		"  \n\t\n":             "",
		"<div>\nno newline":    "<div>\nno newline\n",
		"```\nx\r\n```\r\n":    "<pre style=\"background-color:#2b303b;\"><code><span style=\"color:#c0c5ce;\">x\n</span></code></pre>\n",
		"\"a\" 'b' don't -- …": "<p>“a” ‘b’ don’t – …</p>\n",
	}
	for in, want := range cases {
		got, err := Render([]byte(in))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if string(got) != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestHeadingIDs(t *testing.T) {
	src := "## a\n\n## a-1\n\n## a\n\n## a\n\n# 🎉\n\n# 🎉\n\n# ΟΔΥΣΣΕΥΣ\n\n# İ\n"
	got, err := Render([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{`id="a"`, `id="a-1"`, `id="a-2"`, `id="a-3"`, `id=""`, `id="-1"`, `id="οδυσσευς"`, "id=\"i\u0307\""} {
		if !strings.Contains(string(got), id) {
			t.Errorf("missing %s in\n%s", id, got)
		}
	}
}

// BenchmarkRenderLargest renders the biggest golden document (about 200 KB).
func BenchmarkRenderLargest(b *testing.B) {
	src, err := os.ReadFile("../../testdata/golden/markdown/2026-08-22-git-sync.md")
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Render(src); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRenderNoLexer renders the same document with every code fence's
// language removed, so no chroma lexer runs.
func BenchmarkRenderNoLexer(b *testing.B) {
	src, err := os.ReadFile("../../testdata/golden/markdown/2026-08-22-git-sync.md")
	if err != nil {
		b.Fatal(err)
	}
	src = regexp.MustCompile("(?m)^```.+$").ReplaceAll(src, []byte("```")) // no language: plain text, no lexer
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Render(src); err != nil {
			b.Fatal(err)
		}
	}
}
