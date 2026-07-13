package postgres

import "embed"

// Files is the authoritative, ordered ACR PostgreSQL migration set.
//
//go:embed *.sql
var Files embed.FS
