// Package migrations embeds the schema history shipped with svc-core.
package migrations

import "embed"

// Files is consumed during service startup, so each image carries the schema
// history it requires.
//
//go:embed migrations/*.sql
var Files embed.FS
