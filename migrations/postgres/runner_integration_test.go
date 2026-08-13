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

// expectedMigrationVersions pins the exact applied-migration sequence.
//
// NOTE FOR THE CHAOS-3786 REBASE: this branch (CHAOS-3781) adds migration
// 0013, and 3786 adds 0012 and merges FIRST. Until that merge lands here,
// this worktree legitimately has a GAP at 12 -- the runner sorts by
// version and rejects only duplicates, so a gap applies cleanly and this
// branch stays green standalone.
//
// After rebasing onto 3786, 0012 is present and this becomes the
// contiguous {1..13}. Change this ONE line; the five assertions below all
// read from it.
var expectedMigrationVersions = []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 13}

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
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, runner, db))
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
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, runner, db))
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
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, latest, db))
	requireRotatedAtColumn(t, ctx, db)
	requireDeviceAuthorizationsTable(t, ctx, db)
	requireDeviceAuthorizationHintColumns(t, ctx, db)
}

// TestRunner_upgradeTo12QuarantinesPreExistingInvestigationResults is the
// codex round-1 P1(a) probe for CHAOS-3786's one-time reuse-epoch cutover
// (migration 0012): an organization with an investigation result already
// on disk BEFORE migration 12 runs -- simulating a fallback-produced
// result that CHAOS-3786's genkitruntime fix was not yet in place to label
// correctly -- must have its reuse-invalidation epoch bumped to 1 by the
// migration itself, exactly as if Store.InvalidateOrganizationReuse had
// been called for it. An organization with no investigation result at all
// must NOT gain an invalidations row it never needed.
func TestRunner_upgradeTo12QuarantinesPreExistingInvestigationResults(t *testing.T) {
	// Given a database at the pre-3786 released migration set (1..11)...
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	preCutoverFiles := fstest.MapFS{}
	for _, name := range []string{
		"0001_acr_core.sql",
		"0002_episode_repository_scoped_idempotency.sql",
		"0003_credential_rotation_marker.sql",
		"0004_device_authorization.sql",
		"0005_device_authorization_hints.sql",
		"0006_context_fabric_projection_checkpoints.sql",
		"0007_context_fabric_projection_rebuild_markers.sql",
		"0008_agent_episodes_updated_at.sql",
		"0009_context_fabric_investigation_results.sql",
		"0010_context_fabric_org_model_config.sql",
		"0011_context_fabric_answer_reuse.sql",
	} {
		preCutoverFiles[name] = &fstest.MapFile{Data: mustReadFile(t, name)}
	}
	preCutover, err := NewRunner(preCutoverFiles)
	require.NoError(t, err)
	require.NoError(t, preCutover.Up(ctx, db))

	// ...with one organization's investigation result already saved
	// (result_id/org_id/payload/generated_at are the only NOT NULL
	// columns migration 0009 requires; the reuse columns migration 0011
	// added are left NULL here on purpose -- this row predates
	// CHAOS-3782's own reuse wiring too, the more conservative case).
	const quarantinedOrg = "org-precutover-mislabeled"
	_, err = db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_investigation_results (result_id, org_id, payload, generated_at)
VALUES ('result_precutover_00001', $1, '{}'::jsonb, now())`, quarantinedOrg)
	require.NoError(t, err)

	// And a THIRD organization with TWO investigation results (migration
	// 0009 permits more than one result per organization) -- codex round-2
	// P1: a naive `SELECT DISTINCT org_id, clock_timestamp(), 1` dedupes
	// on the whole row, and clock_timestamp() is volatile, so this
	// organization would emit TWO source rows for the cutover INSERT,
	// which then hits the same ON CONFLICT target twice in one statement
	// -- a cardinality violation that aborts the ENTIRE migration, not
	// just this organization's row. This is the case that must not
	// regress.
	const multiResultOrg = "org-precutover-multi-result"
	for _, resultID := range []string{"result_precutover_multi01", "result_precutover_multi02"} {
		_, err = db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_investigation_results (result_id, org_id, payload, generated_at)
VALUES ($1, $2, '{}'::jsonb, now())`, resultID, multiResultOrg)
		require.NoError(t, err)
	}

	// And a fourth organization with NO investigation result at all.
	const untouchedOrg = "org-precutover-empty"

	// When upgrading to the full (post-CHAOS-3786) migration set.
	latest, err := Embedded()
	require.NoError(t, err)
	require.NoError(t, latest.Up(ctx, db))

	// Then the organization with a pre-cutover result is quarantined --
	// its epoch is bumped to 1, matching what
	// Store.InvalidateOrganizationReuse's first-ever call for an
	// organization would produce.
	var epoch int64
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT epoch FROM acr.context_fabric_reuse_invalidations WHERE org_id = $1`, quarantinedOrg).Scan(&epoch))
	require.Equal(t, int64(1), epoch, "pre-cutover organization must be quarantined by migration 0012")

	// The organization with TWO investigation results must ALSO be
	// quarantined exactly once -- proving the migration itself completed
	// (did not abort on a cardinality violation) and did not double-count
	// the organization.
	var multiResultEpoch int64
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT epoch FROM acr.context_fabric_reuse_invalidations WHERE org_id = $1`, multiResultOrg).Scan(&multiResultEpoch))
	require.Equal(t, int64(1), multiResultEpoch, "an organization with multiple investigation results must still be quarantined exactly once")

	// And the organization with nothing to quarantine gets no row at all.
	var untouchedRows int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM acr.context_fabric_reuse_invalidations WHERE org_id = $1`, untouchedOrg).Scan(&untouchedRows))
	require.Zero(t, untouchedRows, "an organization with no investigation result must not gain an invalidations row")
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
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, runner, db))
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
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, runner, db))
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
