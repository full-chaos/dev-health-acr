// Package orgfence names the database's refusal to write a row for a purged
// organization (the trigger function of migration 0052) as a typed error.
package orgfence

import (
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// PurgedSQLState is raised when the organization carries a purge marker.
	PurgedSQLState = "ACR01"
	// IsolationSQLState is raised when a fenced table is written outside
	// READ COMMITTED, where the marker read could be older than the lock.
	IsolationSQLState = "ACR02"
)

// Refused reports whether err is, or wraps, the write fence's refusal of a
// purged organization.
func Refused(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, storage.ErrOrgPurged) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == PurgedSQLState
}

// Classify returns storage.ErrOrgPurged for a refusal and err unchanged
// otherwise. Every database error of a writer of a fenced table goes through
// it before any other classification, so the refusal is never reported as an
// unavailable dependency.
func Classify(err error) error {
	if Refused(err) {
		return storage.ErrOrgPurged
	}
	return err
}
