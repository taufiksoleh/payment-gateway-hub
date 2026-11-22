// Package migrations embeds the SQL schema files so the binary can migrate
// the database on startup without extra tooling.
package migrations

import "embed"

// FS holds every *.sql file in this directory.
//
//go:embed *.sql
var FS embed.FS
