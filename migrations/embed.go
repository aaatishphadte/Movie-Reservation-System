// Package migrations embeds the versioned SQL schema so the binary is self-contained.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
