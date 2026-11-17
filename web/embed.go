// Package webfs embeds the static files served by the Go binary.
package webfs

import "embed"

// FS holds the public/ directory (error pages, icons, robots.txt). Later tasks
// add assets/ and templates/ embeds once those directories exist: an
// //go:embed pattern that matches no files fails the build.
//
//go:embed all:public
var FS embed.FS
