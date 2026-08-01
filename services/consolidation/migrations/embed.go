package migrations

import "embed"

// FS contains the ordered, schema-qualified consolidation migrations.
//
//go:embed *.sql
var FS embed.FS
