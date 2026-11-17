// Package webfs embeds the static files, assets and templates served by the Go binary.
package webfs

import "embed"

// FS holds public/ (error pages, icons, robots.txt), assets/ (the stylesheets
// and script that get digested names under /assets/) and templates/
// (html/template pages).
//
//go:embed all:public all:templates all:assets
var FS embed.FS
