package postgres

import "embed"

// Files is the authoritative, ordered ACR PostgreSQL migration set: only the
// numbered files. reconcile_client_class.sql is not a migration.
//
//go:embed [0-9]*.sql
var Files embed.FS

// reconcileClientClassSQL is the idempotent relabel step Apply runs last.
//
//go:embed reconcile_client_class.sql
var reconcileClientClassSQL string
