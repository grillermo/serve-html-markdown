// Package cutover embeds the one-off SQL of the Rails to Go switch.
package cutover

import _ "embed"

// MoveSolidQueueJobs is move_solid_queue_jobs.sql: BEGIN, the statements, COMMIT.
//
//go:embed move_solid_queue_jobs.sql
var MoveSolidQueueJobs string
