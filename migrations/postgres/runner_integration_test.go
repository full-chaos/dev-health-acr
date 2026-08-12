package postgres

import (
	"context"
	"database/sql"
	"io/fs"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedRunner_appliesMigrationsInOrder_whenDatabaseIsFresh(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	runner, err := Embedded()
	require.NoError(t, err)

	// When
	err = runner.Up(ctx, db)

	// Then
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 3, 4, 5, 6, 7, 8}, migrationVersions(t, ctx, runner, db))
	requireRotatedAtColumn(t, ctx, db)
	requireDeviceAuthorizationsTable(t, ctx, db)
	requireDeviceAuthorizationHintColumns(t, ctx, db)
	requireContextFabricProjectionCheckpointsTable(t, ctx, db)
	requireContextFabricProjectionRebuildMarkersTable(t, ctx, db)
	requireAgentEpisodesUpdatedAtColumn(t, ctx, db)
}

func TestRunner_isIdempotent_whenMigrationsAreAlreadyApplied(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	runner, err := Embedded()
	require.NoError(t, err)
	require.NoError(t, runner.Up(ctx, db))

	// When
	err = runner.Up(ctx, db)

	// Then
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 3, 4, 5, 6, 7, 8}, migrationVersions(t, ctx, runner, db))
	requireRotatedAtColumn(t, ctx, db)
	requireDeviceAuthorizationsTable(t, ctx, db)
	requireDeviceAuthorizationHintColumns(t, ctx, db)
}

func TestRunner_statusDoesNotCreateHistory_whenDatabaseIsFresh(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	runner, err := Embedded()
	require.NoError(t, err)

	// When
	status, err := runner.Status(ctx, db)

	// Then
	require.NoError(t, err)
	require.Empty(t, status)
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'acr')").Scan(&exists))
	require.False(t, exists)
}

func TestRunner_upgradesInOrder_whenDatabaseMatchesReleasedMain(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	released, err := NewRunner(fstest.MapFS{
		"0001_acr_core.sql": {Data: mustReadFile(t, "0001_acr_core.sql")},
		"0002_episode_repository_scoped_idempotency.sql": {Data: mustReadFile(t, "0002_episode_repository_scoped_idempotency.sql")},
		"0003_credential_rotation_marker.sql":            {Data: mustReadFile(t, "0003_credential_rotation_marker.sql")},
	})
	require.NoError(t, err)
	require.NoError(t, released.Up(ctx, db))
	latest, err := Embedded()
	require.NoError(t, err)

	// When
	err = latest.Up(ctx, db)

	// Then
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 3, 4, 5, 6, 7, 8}, migrationVersions(t, ctx, latest, db))
	requireRotatedAtColumn(t, ctx, db)
	requireDeviceAuthorizationsTable(t, ctx, db)
	requireDeviceAuthorizationHintColumns(t, ctx, db)
}

func TestRunner_serializesConcurrentUp_calls(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	runner, err := Embedded()
	require.NoError(t, err)
	var group sync.WaitGroup
	errs := make(chan error, 2)

	// When
	for range 2 {
		group.Go(func() {
			errs <- runner.Up(ctx, db)
		})
	}
	group.Wait()
	close(errs)

	// Then
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, []int64{1, 2, 3, 4, 5, 6, 7, 8}, migrationVersions(t, ctx, runner, db))
}

func TestRunner_rollsBackFailedMigration_withoutHistoryRow(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	runner, err := NewRunner(fstest.MapFS{
		"0001_failing.sql": {Data: []byte("CREATE TABLE acr.rollback_probe (id integer); INVALID SQL;")},
	})
	require.NoError(t, err)

	// When
	err = runner.Up(ctx, db)

	// Then
	require.Error(t, err)
	require.Empty(t, migrationVersions(t, ctx, runner, db))
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_tables WHERE schemaname = 'acr' AND tablename = 'rollback_probe')").Scan(&exists))
	require.False(t, exists)
}

func TestRunner_rejectsForgedHistoryName_beforeApplyingMigrations(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	runner, err := Embedded()
	require.NoError(t, err)
	seedMigrationHistory(t, ctx, db, 1, "0001_forged.sql", nil)

	// When
	err = runner.Up(ctx, db)

	// Then
	require.ErrorIs(t, err, ErrInvalidMigration)
	require.Equal(t, []int64{1}, storedMigrationVersions(t, ctx, db))
}

func TestRunner_rejectsStaleHistoryChecksum_beforeApplyingMigrations(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	runner, err := Embedded()
	require.NoError(t, err)
	seedMigrationHistory(t, ctx, db, 1, "0001_acr_core.sql", pointer("forged-checksum"))

	// When
	err = runner.Up(ctx, db)

	// Then
	require.ErrorIs(t, err, ErrInvalidMigration)
	require.Equal(t, []int64{1}, storedMigrationVersions(t, ctx, db))
}

func TestRunner_rejectsUnknownHistoryVersion_beforeApplyingMigrations(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	runner, err := Embedded()
	require.NoError(t, err)
	seedMigrationHistory(t, ctx, db, 99, "0099_forged.sql", pointer("forged-checksum"))

	// When
	err = runner.Up(ctx, db)

	// Then
	require.ErrorIs(t, err, ErrInvalidMigration)
	require.Equal(t, []int64{99}, storedMigrationVersions(t, ctx, db))
}

func TestRunner_rejectsNonPrefixHistory_beforeApplyingMigrations(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	runner, err := Embedded()
	require.NoError(t, err)
	seedMigrationHistory(t, ctx, db, 2, "0002_episode_repository_scoped_idempotency.sql", nil)

	// When
	err = runner.Up(ctx, db)

	// Then
	require.ErrorIs(t, err, ErrInvalidMigration)
	require.Equal(t, []int64{2}, storedMigrationVersions(t, ctx, db))
}

func TestRunner_backfillsLegacyChecksum_afterCanonicalHistoryValidation(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	first, err := NewRunner(fstest.MapFS{
		"0001_acr_core.sql": {Data: mustReadFile(t, "0001_acr_core.sql")},
	})
	require.NoError(t, err)
	require.NoError(t, first.Up(ctx, db))
	_, err = db.ExecContext(ctx, "UPDATE acr.schema_migrations SET checksum = NULL WHERE version = 1")
	require.NoError(t, err)
	runner, err := Embedded()
	require.NoError(t, err)

	// When
	err = runner.Up(ctx, db)

	// Then
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 3, 4, 5, 6, 7, 8}, migrationVersions(t, ctx, runner, db))
	var checksum string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT checksum FROM acr.schema_migrations WHERE version = 1").Scan(&checksum))
	require.NotEmpty(t, checksum)
}

func migrationVersions(t *testing.T, ctx context.Context, runner *Runner, db *sql.DB) []int64 {
	t.Helper()
	status, err := runner.Status(ctx, db)
	require.NoError(t, err)
	versions := make([]int64, len(status))
	for index, migration := range status {
		versions[index] = migration.Version
	}
	return versions
}

func requireRotatedAtColumn(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = 'acr' AND table_name = 'client_credentials' AND column_name = 'rotated_at'
	)`).Scan(&exists))
	require.True(t, exists)
}

func requireDeviceAuthorizationsTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables
		WHERE table_schema = 'acr' AND table_name = 'device_authorizations'
	)`).Scan(&exists))
	require.True(t, exists)
}

func requireContextFabricProjectionCheckpointsTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables
		WHERE table_schema = 'acr' AND table_name = 'context_fabric_projection_checkpoints'
	)`).Scan(&exists))
	require.True(t, exists)
}

func requireContextFabricProjectionRebuildMarkersTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables
		WHERE table_schema = 'acr' AND table_name = 'context_fabric_projection_rebuild_markers'
	)`).Scan(&exists))
	require.True(t, exists)
}

func requireAgentEpisodesUpdatedAtColumn(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = 'acr' AND table_name = 'agent_episodes' AND column_name = 'updated_at'
	)`).Scan(&exists))
	require.True(t, exists)
}

func requireDeviceAuthorizationHintColumns(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, column := range []string{"organization_id_hint", "repository_hints"} {
		var exists bool
		require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'acr' AND table_name = 'device_authorizations' AND column_name = $1
		)`, column).Scan(&exists))
		require.True(t, exists, "missing device authorization hint column %s", column)
	}
}

func mustReadFile(t *testing.T, name string) []byte {
	t.Helper()
	contents, err := fs.ReadFile(Files, name)
	require.NoError(t, err)
	return contents
}

func seedMigrationHistory(t *testing.T, ctx context.Context, db *sql.DB, version int64, name string, checksum *string) {
	t.Helper()
	_, err := db.ExecContext(ctx, "CREATE SCHEMA acr; CREATE TABLE acr.schema_migrations (version BIGINT PRIMARY KEY, name TEXT NOT NULL, checksum TEXT, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW())")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "INSERT INTO acr.schema_migrations (version, name, checksum) VALUES ($1, $2, $3)", version, name, checksum)
	require.NoError(t, err)
}

func pointer(value string) *string {
	return &value
}

func storedMigrationVersions(t *testing.T, ctx context.Context, db *sql.DB) []int64 {
	t.Helper()
	rows, err := db.QueryContext(ctx, "SELECT version FROM acr.schema_migrations ORDER BY version")
	require.NoError(t, err)
	defer rows.Close()
	var versions []int64
	for rows.Next() {
		var version int64
		require.NoError(t, rows.Scan(&version))
		versions = append(versions, version)
	}
	require.NoError(t, rows.Err())
	return versions
}
