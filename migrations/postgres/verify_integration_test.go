package postgres

import (
	"context"
	"database/sql"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestRunner_VerifyCurrentRejectsMigrationHistoryDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(context.Context, *sql.DB) error
	}{
		{name: "checksum", mutate: func(ctx context.Context, db *sql.DB) error {
			_, err := db.ExecContext(ctx, "UPDATE acr.schema_migrations SET checksum = 'tampered' WHERE version = 2")
			return err
		}},
		{name: "missing required migration", mutate: func(ctx context.Context, db *sql.DB) error {
			_, err := db.ExecContext(ctx, "DELETE FROM acr.schema_migrations WHERE version = 5")
			return err
		}},
		{name: "required migration name drift", mutate: func(ctx context.Context, db *sql.DB) error {
			_, err := db.ExecContext(ctx, "UPDATE acr.schema_migrations SET name = 'renamed.sql' WHERE version = 2")
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db := newTestDatabase(t, ctx)
			runner, err := Embedded()
			require.NoError(t, err)
			_, err = runner.Apply(ctx, db)
			require.NoError(t, err)
			require.NoError(t, runner.VerifyCurrent(ctx, db))
			require.NoError(t, test.mutate(ctx, db))

			err = runner.VerifyCurrent(ctx, db)

			require.ErrorIs(t, err, ErrInvalidMigration)
		})
	}
}

func TestRunner_VerifyCurrentRejectsReorderedRequiredMigrationHistory(t *testing.T) {
	// Given: a complete required prefix with its expected version, name, and
	// checksum entries.
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	runner, err := Embedded()
	require.NoError(t, err)
	_, err = runner.Apply(ctx, db)
	require.NoError(t, err)
	require.NoError(t, runner.VerifyCurrent(ctx, db))

	// When: the first two complete history entries exchange positions. The
	// temporary version keeps the primary key unique while each entry's name and
	// checksum travel with its original migration.
	_, err = db.ExecContext(ctx, "UPDATE acr.schema_migrations SET version = 0 WHERE version = 1")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "UPDATE acr.schema_migrations SET version = 1 WHERE version = 2")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "UPDATE acr.schema_migrations SET version = 2 WHERE version = 0")
	require.NoError(t, err)

	// Then: an ordered-prefix check rejects the reordered history, independently
	// of the name-drift coverage above.
	err = runner.VerifyCurrent(ctx, db)
	require.ErrorIs(t, err, ErrInvalidMigration)
}

func TestRunner_VerifyCurrentAllowsAdditionalLaterAppliedMigrations(t *testing.T) {
	// Given: an older binary (embedding migrations 1 through 3) checks
	// readiness against a schema a newer binary already advanced to include
	// migration 4.
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	latest, err := Embedded()
	require.NoError(t, err)
	_, err = latest.Apply(ctx, db)
	require.NoError(t, err)
	older, err := NewRunner(fstest.MapFS{
		"0001_acr_core.sql": {Data: mustReadFile(t, "0001_acr_core.sql")},
		"0002_episode_repository_scoped_idempotency.sql": {Data: mustReadFile(t, "0002_episode_repository_scoped_idempotency.sql")},
		"0003_credential_rotation_marker.sql":            {Data: mustReadFile(t, "0003_credential_rotation_marker.sql")},
	})
	require.NoError(t, err)

	// When
	err = older.VerifyCurrent(ctx, db)

	// Then: the required prefix (1 through 3) matches in order with correct
	// checksums, so the additional migration 4 does not fail readiness.
	require.NoError(t, err)
}

func TestRunner_VerifyCurrentRejectsMissingRequiredMigration_whenSchemaIsBehind(t *testing.T) {
	// Given: a newer binary (embedding migrations 1 through 4) checks
	// readiness against a schema that has only applied migrations 1 through 3.
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	older, err := NewRunner(fstest.MapFS{
		"0001_acr_core.sql": {Data: mustReadFile(t, "0001_acr_core.sql")},
		"0002_episode_repository_scoped_idempotency.sql": {Data: mustReadFile(t, "0002_episode_repository_scoped_idempotency.sql")},
		"0003_credential_rotation_marker.sql":            {Data: mustReadFile(t, "0003_credential_rotation_marker.sql")},
	})
	require.NoError(t, err)
	_, err = older.Apply(ctx, db)
	require.NoError(t, err)
	latest, err := Embedded()
	require.NoError(t, err)

	// When
	err = latest.VerifyCurrent(ctx, db)

	// Then: required migration 4 is missing from the applied history.
	require.ErrorIs(t, err, ErrInvalidMigration)
}
