// Package migrations embeds the SQL migrations of the Go application.
//
// These are separate from Rails' db/migrate: the Go binary only owns the
// tables it adds (go_jobs); everything else comes from the Rails schema.
package migrations

import "embed"

// FS holds every *.sql migration file, named <14-digit version>_<name>.sql.
//
//go:embed *.sql
var FS embed.FS
