package linker

import (
	"strings"
	"testing"
)

var (
	benchMarkdown = strings.Repeat("# Title\nSome *emphasis* and a [link](/u) with `code` &amp; text.\n\n- item one\n- item two\n\n", 1500) + "\nzzz\n"
	benchHTML     = strings.Repeat("<h2>Title</h2><p>Some <em>emphasis</em> and a <a href=\"/u\">link</a> with <code>code</code> &amp; text.</p><ul><li>one</li></ul>\n", 1200) + "<p>zzz</p>"
)

func BenchmarkLinkMarkdown(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Link(benchMarkdown, ".md", "zzz", 0, "/u"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLinkHTML(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Link(benchHTML, ".html", "zzz", 0, "/u"); err != nil {
			b.Fatal(err)
		}
	}
}
