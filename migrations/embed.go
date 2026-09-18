// Package migrations embeds the SQL files in this directory into the
// compiled binary, so the running application (and its Docker image) never
// depends on the migrations/ folder existing on disk at runtime - it only
// matters for local, manual `migrate` CLI usage during development.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
