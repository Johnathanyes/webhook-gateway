package migrations

import "embed"

// FS embeds the goose migration files so the built binary can migrate
// itself on boot without the migrations/ directory present on disk
// (BR-29: single static binary, BR-30: automatic schema migrations).
//
//go:embed *.sql
var FS embed.FS
