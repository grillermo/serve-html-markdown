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

// uploadExtensions maps the extension of an uploaded file name to the one the
// stored file gets.
var uploadExtensions = map[string]string{
	".html":     ".html",
	".htm":      ".html",
	".md":       ".md",
	".markdown": ".md",
}

// UploadExtension is the extension a file uploaded as filename is stored with:
// ".html" for .html and .htm, ".md" for .md and .markdown, in any case. Any
// other extension (or none) is BadRequest with the supported-types message.
// Like Stem it treats backslashes as separators and looks at the base name.
func UploadExtension(filename string) (string, error) {
	_, ext, _ := splitName(strings.ReplaceAll(filename, `\`, "/"))
	if stored, ok := uploadExtensions[strings.ToLower(ext)]; ok {
		return stored, nil
	}
	return "", BadRequest(ErrUnsupported.Message)
}
