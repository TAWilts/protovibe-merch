// Package migrations embeds the versioned SQL schema files.
//
// The SQL is the single source of truth for the schema; the GORM models in
// internal/models only map onto it. That keeps CHECK constraints, collations
// and composite unique keys explicit instead of inferred.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
