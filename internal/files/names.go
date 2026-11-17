package files

import (
	"strings"

	"serve-html-markdown/internal/store"
)

// rubyBasename is File.basename for a "/"-separated path: trailing slashes are
// ignored, "" stays "" and "/" stays "/".
func rubyBasename(s string) string {
	if s == "" {
		return ""
	}
	s = strings.TrimRight(s, "/")
	if s == "" {
		return "/"
	}
	return s[strings.LastIndex(s, "/")+1:]
}

// splitName is the Ruby base name, its extension (File.extname) and the base
// name without that extension (File.basename(name, ext)).
func splitName(name string) (base, ext, stem string) {
	base = rubyBasename(name)
	ext = store.Ext(base)
	stem = strings.TrimSuffix(base, ext)
	return base, ext, stem
}
