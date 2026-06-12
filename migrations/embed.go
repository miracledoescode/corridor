// Package migrations embeds the goose SQL files into the binary so
// corridord can migrate itself at boot — no separate migration step can
// be forgotten in a deploy.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
