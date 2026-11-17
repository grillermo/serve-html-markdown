// Package webfs embeds the static files and templates served by the Go binary.
package webfs

import "embed"

// FS holds public/ (error pages, icons, robots.txt) and templates/ (html/template
// pages). A later task adds assets/ once that directory exists: an //go:embed
// pattern that matches no files fails the build.
//
//go:embed all:public all:templates
var FS embed.FS
