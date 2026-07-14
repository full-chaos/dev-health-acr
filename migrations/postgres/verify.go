package postgres

import (
	"context"
	"database/sql"
)

// VerifyCurrent confirms every migration this Runner embeds has been
// applied, in order, at the expected position, with a matching checksum. A
// rolling deployment may run an older binary against a schema that a newer
// binary already advanced further: additional migrations applied after the
// full required prefix are tolerated so readiness does not flap mid-rollout
// and the schema stays rollback-compatible. Missing, reordered, or
// checksum-mismatched required entries are rejected.
func (r *Runner) VerifyCurrent(ctx context.Context, db *sql.DB) error {
	var history sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT to_regclass('acr.schema_migrations')").Scan(&history); err != nil || !history.Valid {
		return ErrInvalidMigration
	}
	applied, err := appliedMigrations(ctx, db)
	if err != nil {
		return err
	}
	if len(applied) < len(r.migrations) {
		return ErrInvalidMigration
	}
	for index, expected := range r.migrations {
		entry := applied[index]
		if entry.Version != expected.Version || entry.Name != expected.Name {
			return ErrInvalidMigration
		}
		if !entry.Checksum.Valid || entry.Checksum.String != expected.Checksum {
			return ErrInvalidMigration
		}
	}
	return nil
}
