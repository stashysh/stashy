// Package migrations embeds the per-dialect SQL migrations. The db package
// points goose at the subdirectory matching the connected database.
package migrations

import "embed"

//go:embed postgres/*.sql sqlite/*.sql
var FS embed.FS
