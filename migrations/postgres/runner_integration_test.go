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
// Held in ONE place rather than repeated across the assertions below, so
// adding a migration is a single-line edit that cannot be applied to some
// assertions and missed by others. cmd/acr-migrate/cli_test.go asserts the
// COUNT of the same set and has to move with it.
//
// Contiguous again as of the CHAOS-3781 rebase onto CHAOS-3786: 0012 is
// 3786's reuse-epoch cutover and 0013 is 3781's time-axis reuse key. (The
// runner sorts by version and rejects only duplicates, so a gap applies
// cleanly -- but there is no gap to tolerate here now.) 0014 is
// CHAOS-3833's embed-retrieval reuse-key columns. 0015 is CHAOS-3862's
// prompt-version and version-authority reuse-key columns. 0016 is
// CHAOS-3859's clarification-selection capture table. 0017 is CHAOS-3889's
// model-execution-receipt request_id column. 0018 is CHAOS-3884's
// identity-normalization-version reuse-key column (renumbered from 0017 to
// 0018 during the rebase onto origin/main: both CHAOS-3889 and this
// branch's own CHAOS-3884 work independently claimed version 17 as separate
// features landed in parallel; CHAOS-3889 merged to main first, so this
// branch's migration moved to the next free version rather than the other
// way around). 0019 and 0020 are CHAOS-3898 S2a's graph lifecycle row
// (+ retire records + build source progress) and the projection checkpoint
// re-key to (org, epoch, source), respectively. 0021 is CHAOS-3898 S2's
// §2.3 graph_epoch reuse-key dimension on context_fabric_investigation_results.
// 0022 is CHAOS-3900 W1's window_inference_version reuse-key dimension on
// the same table. 0023 is CHAOS-3927 P4's structure-offer supersession
// atomicity table. 0024 is CHAOS-3927 P4's structure_selections capture
// table (StructureSelectionEvent). 0025 is CHAOS-3927 P4's codex-review
// backfill of pre-0023 confirmed-structure rows. 0026 is CHAOS-3860 P6's
// precondition fix: StructureSelectionEvent gains the ConsensusEvidence
// column the design brief's own §4 P6 row names as a P4 dependency, which
// P4 shipped without (discovered while activating P6, DP5(b)). 0027 is
// that same P6 fix's codex-review follow-up: a panel-size CHECK
// constraint requiring >=2 distinct panel model identities, added as its
// own migration rather than editing the already-pushed 0026 in place
// (this checksum-pinning discipline is exactly why). 0028 is CHAOS-3977
// P5's structure-prior store (versioned snapshots + active-version
// pointer + pointer history + per-entry revocations) -- renumbered from
// 0026 during the rebase onto origin/main: both CHAOS-3860 P6 and this
// branch's own CHAOS-3977 work independently claimed version 26 as
// separate features landed in parallel; CHAOS-3860 P6 merged to main
// first, so this branch's migrations moved to the next free versions
// rather than the other way around (the SAME renumbering precedent 0018's
// own comment above already documents). 0029 is that same P5 ticket's own
// one-time cleanup: clear reuse-key columns on pre-existing structure-
// bearing rows a pre-P5 binary wrote before reuseColumnsFor's own source-
// ineligibility fix existed. 0030 is CHAOS-4013's RFC 8693 workload token
// exchange: acr.workload_bindings plus client_credentials.workload_binding_id.
// 0031 is CHAOS-4085's commit_gate_version reuse-key dimension. 0032 is
// CHAOS-4305's projection-checkpoint rows_applied column, written
// atomically with the cursor by the checkpoint's own CAS statement so the
// build-drain's row-count accumulator can no longer diverge from it.
var expectedMigrationVersions = []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}

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

// TestRunner_upgradeTo15AddsPromptAndVersionAuthorityReuseKeyColumns is
// CHAOS-3862's sol round-2 F5: a real 0014->0015 upgrade, mirroring
// TestRunner_upgradesInOrder_whenDatabaseMatchesReleasedMain's
// released-main-fixture pattern (a runner built from a FIXED file subset,
// simulating a database already at a prior released schema, upgraded by
// the full embedded set) rather than always starting from an empty
// database as every other Up() test in this file does. Asserts the
// migration's complete, positive effect: all five new columns exist, all
// five length CHECK constraints exist by their (deliberately abbreviated,
// see 0015's own header comment on the 63-byte identifier limit) names,
// the new v4 index exists, AND -- the replace-don't-stack half of 0015's
// contract that a purely additive check would miss entirely -- the old v3
// index is actually gone, not left stacked beside the new one.
func TestRunner_upgradeTo15AddsPromptAndVersionAuthorityReuseKeyColumns(t *testing.T) {
	// Given a database at the released main schema through migration 0014
	// (everything CHAOS-3862 builds on top of)...
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	preReuseKeyVersionAuthorityFiles := fstest.MapFS{}
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
		"0012_context_fabric_reuse_fallback_identity_cutover.sql",
		"0013_context_fabric_time_axis_reuse_key.sql",
		"0014_context_fabric_embed_retrieval_reuse_key.sql",
	} {
		preReuseKeyVersionAuthorityFiles[name] = &fstest.MapFile{Data: mustReadFile(t, name)}
	}
	released, err := NewRunner(preReuseKeyVersionAuthorityFiles)
	require.NoError(t, err)
	require.NoError(t, released.Up(ctx, db))
	requireIndexExists(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v3")

	// When upgrading to the full (post-CHAOS-3862) migration set.
	latest, err := Embedded()
	require.NoError(t, err)

	// Then
	require.NoError(t, latest.Up(ctx, db))
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, latest, db))

	for _, column := range []string{
		"interpretation_prompt_version", "synthesis_prompt_version",
		"query_version", "canonical_service_version", "model_output_schema_version",
	} {
		requireContextFabricInvestigationResultsColumn(t, ctx, db, column)
	}
	for _, constraint := range []string{
		"ck_acr_cf_investigation_results_interp_prompt_version_length",
		"ck_acr_cf_investigation_results_synth_prompt_version_length",
		"ck_acr_cf_investigation_results_query_version_length",
		"ck_acr_cf_investigation_results_canon_svc_version_length",
		"ck_acr_cf_investigation_results_model_output_schema_ver_length",
	} {
		requireConstraintExists(t, ctx, db, constraint)
	}
	// v4 (0015's own reuse-key index) is itself superseded by 0018's v5 in
	// the SAME "latest" run this test upgrades to -- see
	// TestRunner_upgradeTo18AddsIdentityNormalizationReuseKeyColumn for the
	// dedicated 0016->0018 proof (the v4->v5 replacement, mirroring this
	// test's own v3->v4 check below). This test's own job stops at proving
	// 0015's COLUMNS/CONSTRAINTS survive to the final schema; the index's
	// FINAL name is that later test's responsibility, not this one's, to
	// avoid this test silently going stale every time a LATER migration
	// touches the same index the way this update just did.
	requireIndexAbsent(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v3")
}

// TestRunner_upgradeTo16CreatesClarificationSelectionsTable is CHAOS-3859's
// 0015->0016 upgrade proof, mirroring
// TestRunner_upgradeTo15AddsPromptAndVersionAuthorityReuseKeyColumns's
// released-main-fixture pattern exactly: a runner built from a FIXED file
// subset (everything through 0015), upgraded by the full embedded set,
// then asserted against the NEW table's complete shape -- not just "the
// table exists," but every column and constraint 0016 adds, plus both
// indexes, so a partially-applied or partially-idempotent migration would
// fail this test even if the table itself came into existence.
func TestRunner_upgradeTo16CreatesClarificationSelectionsTable(t *testing.T) {
	// Given a database at the released main schema through migration 0015
	// (everything CHAOS-3859 builds on top of)...
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	preClarificationCaptureFiles := fstest.MapFS{}
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
		"0012_context_fabric_reuse_fallback_identity_cutover.sql",
		"0013_context_fabric_time_axis_reuse_key.sql",
		"0014_context_fabric_embed_retrieval_reuse_key.sql",
		"0015_context_fabric_prompt_version_reuse_key.sql",
	} {
		preClarificationCaptureFiles[name] = &fstest.MapFile{Data: mustReadFile(t, name)}
	}
	released, err := NewRunner(preClarificationCaptureFiles)
	require.NoError(t, err)
	require.NoError(t, released.Up(ctx, db))
	requireTableAbsent(t, ctx, db, "context_fabric_clarification_selections")

	// When upgrading to the full (post-CHAOS-3859) migration set.
	latest, err := Embedded()
	require.NoError(t, err)

	// Then
	require.NoError(t, latest.Up(ctx, db))
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, latest, db))

	requireTableExists(t, ctx, db, "context_fabric_clarification_selections")
	for _, column := range []string{
		"selection_id", "org_id", "captured_at", "question_hash", "prior_result_id",
		"selected_receipt_id", "selected_subject_kind", "selected_subject_canonical_id",
		"selection_provenance", "offered_candidates", "pipeline_context", "created_at",
	} {
		requireColumnExists(t, ctx, db, "context_fabric_clarification_selections", column)
	}
	for _, constraint := range []string{
		"ck_acr_cf_clarification_selections_selection_id_length",
		"ck_acr_cf_clarification_selections_org_id_length",
		"ck_acr_cf_clarification_selections_question_hash_length",
		"ck_acr_cf_clarification_selections_prior_result_id_length",
		"ck_acr_cf_clarification_selections_receipt_id_length",
		"ck_acr_cf_clarification_selections_subject_kind_length",
		"ck_acr_cf_clarification_selections_subject_id_length",
		"ck_acr_cf_clarification_selections_provenance_vocabulary",
	} {
		requireConstraintExists(t, ctx, db, constraint)
	}
	requireIndexExists(t, ctx, db, "ix_acr_cf_clarification_selections_org_captured")
	requireIndexExists(t, ctx, db, "ix_acr_cf_clarification_selections_org_question")
}

// TestRunner_upgradeTo16IsIdempotentOnRetry is CHAOS-3862's F3 lesson
// applied to THIS migration: 0016 must survive being applied twice without
// erroring (a real retry-after-partial-failure proxy -- see 0016's own
// header comment on why every constraint is DROP-IF-EXISTS-then-ADD and
// why there is no inline BEGIN/COMMIT).
func TestRunner_upgradeTo16IsIdempotentOnRetry(t *testing.T) {
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	runner, err := Embedded()
	require.NoError(t, err)
	require.NoError(t, runner.Up(ctx, db))
	require.NoError(t, runner.Up(ctx, db), "a second Up() over an already-migrated database must not error")
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, runner, db))
}

// TestRunner_upgradeTo18AddsIdentityNormalizationReuseKeyColumn is
// CHAOS-3884's 0016->0018 upgrade proof (renumbered from 0017 during the
// rebase onto origin/main -- CHAOS-3889 independently claimed 0017 first;
// see expectedMigrationVersions' own comment), mirroring
// TestRunner_upgradeTo16CreatesClarificationSelectionsTable's
// released-main-fixture pattern exactly: a runner built from a FIXED file
// subset (everything through 0016), upgraded by the full embedded set
// (which now applies 0017 -- CHAOS-3889's unrelated model-receipts
// request_id column, not asserted on here -- on the way to 0018), then
// asserted against 0018's complete shape -- including the v4->v5 reuse-key
// index replacement TestRunner_upgradeTo15's own tail assertion used to
// (incorrectly, once this migration landed) claim as final state.
func TestRunner_upgradeTo18AddsIdentityNormalizationReuseKeyColumn(t *testing.T) {
	// Given a database at the released main schema through migration 0016
	// (everything CHAOS-3884 builds on top of)...
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	preIdentityNormalizationFiles := fstest.MapFS{}
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
		"0012_context_fabric_reuse_fallback_identity_cutover.sql",
		"0013_context_fabric_time_axis_reuse_key.sql",
		"0014_context_fabric_embed_retrieval_reuse_key.sql",
		"0015_context_fabric_prompt_version_reuse_key.sql",
		"0016_context_fabric_clarification_selections.sql",
	} {
		preIdentityNormalizationFiles[name] = &fstest.MapFile{Data: mustReadFile(t, name)}
	}
	released, err := NewRunner(preIdentityNormalizationFiles)
	require.NoError(t, err)
	require.NoError(t, released.Up(ctx, db))
	requireIndexExists(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v4")

	// When upgrading to the full (post-CHAOS-3884) migration set.
	latest, err := Embedded()
	require.NoError(t, err)

	// Then
	require.NoError(t, latest.Up(ctx, db))
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, latest, db))

	requireContextFabricInvestigationResultsColumn(t, ctx, db, "identity_normalization_version")
	requireConstraintExists(t, ctx, db, "ck_acr_cf_investigation_results_identity_norm_version_length")
	// CHAOS-3898 §2.3's migration 0021 replaces v5 with v6 (one more reuse-
	// key dimension, graph_epoch), CHAOS-3900 W1's migration 0022 in turn
	// replaces v6 with v7 (window_inference_version), and CHAOS-4085's
	// migration 0031 replaces v7 with v8 (commit_gate_version) -- "latest"
	// here includes all three, so the index this 0018 upgrade itself created
	// is no longer the CURRENT one; see
	// TestRunner_upgradeTo21AddsGraphEpochReuseKeyColumn,
	// TestRunner_upgradeTo22AddsWindowInferenceReuseKeyColumn and
	// TestRunner_upgradeTo31AddsCommitGateReuseKeyColumn for the dedicated
	// boundary proofs this same replace-don't-stack pattern needs.
	requireIndexExists(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v8")
	requireIndexAbsent(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v7")
	requireIndexAbsent(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v6")
	requireIndexAbsent(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v5")
	requireIndexAbsent(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v4")
}

// TestRunner_upgradeTo21AddsGraphEpochReuseKeyColumn mirrors
// TestRunner_upgradeTo18AddsIdentityNormalizationReuseKeyColumn (CHAOS-3898
// §2.3): 0021 adds graph_epoch as one more reuse-key dimension on top of a
// database already at 0018-through-0020 (S2a's own lifecycle-row and
// checkpoint-epoch migrations, which do not touch
// context_fabric_investigation_results), proving both halves of the
// replace-don't-stack pattern -- the new column/constraint/index exist, and
// the OLD v5 index is actually dropped, not left stacked beside v6.
func TestRunner_upgradeTo21AddsGraphEpochReuseKeyColumn(t *testing.T) {
	// Given a database at the released main schema through migration 0020
	// (everything CHAOS-3898 S2a builds on top of)...
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	preGraphEpochFiles := fstest.MapFS{}
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
		"0012_context_fabric_reuse_fallback_identity_cutover.sql",
		"0013_context_fabric_time_axis_reuse_key.sql",
		"0014_context_fabric_embed_retrieval_reuse_key.sql",
		"0015_context_fabric_prompt_version_reuse_key.sql",
		"0016_context_fabric_clarification_selections.sql",
		"0017_context_fabric_model_receipts_request_id.sql",
		"0018_context_fabric_identity_normalization_reuse_key.sql",
		"0019_context_fabric_graph_lifecycle.sql",
		"0020_context_fabric_projection_checkpoints_epoch.sql",
	} {
		preGraphEpochFiles[name] = &fstest.MapFile{Data: mustReadFile(t, name)}
	}
	released, err := NewRunner(preGraphEpochFiles)
	require.NoError(t, err)
	require.NoError(t, released.Up(ctx, db))
	requireIndexExists(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v5")

	// When upgrading to the full (post-CHAOS-3898-S2) migration set.
	latest, err := Embedded()
	require.NoError(t, err)

	// Then
	require.NoError(t, latest.Up(ctx, db))
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, latest, db))

	requireContextFabricInvestigationResultsColumn(t, ctx, db, "graph_epoch")
	requireConstraintExists(t, ctx, db, "ck_acr_cf_investigation_results_graph_epoch_nonneg")
	// CHAOS-3900 W1's migration 0022 replaces v6 with v7 (one more reuse-key
	// dimension, window_inference_version) and CHAOS-4085's migration 0031
	// replaces v7 with v8 (commit_gate_version) -- "latest" here includes
	// both, so the index this 0021 upgrade itself created is no longer the
	// CURRENT one; see TestRunner_upgradeTo22AddsWindowInferenceReuseKeyColumn
	// and TestRunner_upgradeTo31AddsCommitGateReuseKeyColumn for the
	// dedicated boundary proofs this same replace-don't-stack pattern needs.
	requireIndexExists(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v8")
	requireIndexAbsent(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v7")
	requireIndexAbsent(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v6")
	requireIndexAbsent(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v5")
}

// TestRunner_upgradeTo22AddsWindowInferenceReuseKeyColumn mirrors
// TestRunner_upgradeTo21AddsGraphEpochReuseKeyColumn (CHAOS-3900 W1): 0022
// adds window_inference_version as one more reuse-key dimension on top of a
// database already at 0021 (CHAOS-3898 §2.3's graph_epoch column, which
// 0022 itself extends), proving both halves of the replace-don't-stack
// pattern -- the new column/constraint/index exist, and the OLD v6 index is
// actually dropped, not left stacked beside v7.
func TestRunner_upgradeTo22AddsWindowInferenceReuseKeyColumn(t *testing.T) {
	// Given a database at the released main schema through migration 0021
	// (everything CHAOS-3900 W1 builds on top of)...
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	preWindowInferenceFiles := fstest.MapFS{}
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
		"0012_context_fabric_reuse_fallback_identity_cutover.sql",
		"0013_context_fabric_time_axis_reuse_key.sql",
		"0014_context_fabric_embed_retrieval_reuse_key.sql",
		"0015_context_fabric_prompt_version_reuse_key.sql",
		"0016_context_fabric_clarification_selections.sql",
		"0017_context_fabric_model_receipts_request_id.sql",
		"0018_context_fabric_identity_normalization_reuse_key.sql",
		"0019_context_fabric_graph_lifecycle.sql",
		"0020_context_fabric_projection_checkpoints_epoch.sql",
		"0021_context_fabric_graph_epoch_reuse_key.sql",
	} {
		preWindowInferenceFiles[name] = &fstest.MapFile{Data: mustReadFile(t, name)}
	}
	released, err := NewRunner(preWindowInferenceFiles)
	require.NoError(t, err)
	require.NoError(t, released.Up(ctx, db))
	requireIndexExists(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v6")

	// When upgrading to the full (post-CHAOS-3900-W1) migration set.
	latest, err := Embedded()
	require.NoError(t, err)

	// Then
	require.NoError(t, latest.Up(ctx, db))
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, latest, db))

	requireContextFabricInvestigationResultsColumn(t, ctx, db, "window_inference_version")
	requireConstraintExists(t, ctx, db, "ck_acr_cf_investigation_results_window_inference_version_length")
	// CHAOS-4085's migration 0031 replaces v7 with v8 (commit_gate_version)
	// -- "latest" here includes it, so the index this 0022 upgrade itself
	// created is no longer the CURRENT one; see
	// TestRunner_upgradeTo31AddsCommitGateReuseKeyColumn for the dedicated
	// 0022->0031-boundary proof.
	requireIndexExists(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v8")
	requireIndexAbsent(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v7")
	requireIndexAbsent(t, ctx, db, "ix_acr_cf_investigation_results_reuse_key_v6")
}

// TestRunner_upgradeTo22IsIdempotentOnRetry mirrors
// TestRunner_upgradeTo21IsIdempotentOnRetry: 0022 must survive being
// applied twice without erroring.
func TestRunner_upgradeTo22IsIdempotentOnRetry(t *testing.T) {
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	runner, err := Embedded()
	require.NoError(t, err)
	require.NoError(t, runner.Up(ctx, db))
	require.NoError(t, runner.Up(ctx, db), "a second Up() over an already-migrated database must not error")
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, runner, db))
}

// TestRunner_upgradeTo21IsIdempotentOnRetry mirrors
// TestRunner_upgradeTo18IsIdempotentOnRetry: 0021 must survive being
// applied twice without erroring.
func TestRunner_upgradeTo21IsIdempotentOnRetry(t *testing.T) {
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	runner, err := Embedded()
	require.NoError(t, err)
	require.NoError(t, runner.Up(ctx, db))
	require.NoError(t, runner.Up(ctx, db), "a second Up() over an already-migrated database must not error")
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, runner, db))
}

// TestRunner_upgradeTo18IsIdempotentOnRetry mirrors
// TestRunner_upgradeTo16IsIdempotentOnRetry: 0018 (renumbered from 0017
// during the rebase onto origin/main -- see expectedMigrationVersions' own
// comment) must survive being applied twice without erroring.
func TestRunner_upgradeTo18IsIdempotentOnRetry(t *testing.T) {
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	runner, err := Embedded()
	require.NoError(t, err)
	require.NoError(t, runner.Up(ctx, db))
	require.NoError(t, runner.Up(ctx, db), "a second Up() over an already-migrated database must not error")
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, runner, db))
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

// TestRunner_upgradeTo27ToleratesPreExistingLegacyConsensusRow is CHAOS-3860
// P6's own codex round-3 finding (session 01a01efe-0778-7b11-8725-4e8050c4d7c3,
// P1), proved directly: a database that already applied migration 0026 (the
// consensus_evidence column, with no panel-size CHECK yet) and picked up a
// single-member legacy row -- the exact shape 0027's CHECK now forbids for
// NEW rows -- must still be able to upgrade cleanly to 0027 and beyond.
// 0027's ADD CONSTRAINT ... NOT VALID is what makes this possible: without
// it, ADD CONSTRAINT would validate every existing row and this exact
// legacy row would roll the whole migration back, wedging that database
// below head permanently. Mirrors TestRunner_upgradeTo16CreatesClarification
// SelectionsTable's own "released fixture through migration 0026, then
// upgrade with the full embedded set" pattern.
func TestRunner_upgradeTo27ToleratesPreExistingLegacyConsensusRow(t *testing.T) {
	// Given a database at the released schema through migration 0026 (the
	// consensus_evidence column exists; 0027's panel-size CHECK does not
	// yet)...
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	pre0027Files := fstest.MapFS{}
	for version := 1; version <= 26; version++ {
		name := migrationFileNameByVersion(t, int64(version))
		pre0027Files[name] = &fstest.MapFile{Data: mustReadFile(t, name)}
	}
	released, err := NewRunner(pre0027Files)
	require.NoError(t, err)
	require.NoError(t, released.Up(ctx, db))

	// ...and a legacy single-member consensus_evidence row already landed
	// on it (0026 alone had no panel-size CHECK to stop this -- a
	// hand-inserted or test-only row is the only way one could exist,
	// since no production code path writes this column, but the upgrade
	// must tolerate it regardless).
	_, err = db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_structure_selections
    (selection_id, org_id, captured_at, question_hash, prior_result_id, member, selected_receipt_id, selected_applied_value, accepted, selection_mode, selection_provenance, offered, pipeline_context, consensus_evidence)
VALUES ('legacy-consensus-row-0001', 'org-legacy-upgrade', now(), repeat('a', 64), 'result-legacy-0001', 'expected_kind', 'kindr_x', 'pull_request', true, 'agent_receipt', 'credential_mcp', '[]'::jsonb, '{}'::jsonb, '{"panel_model_identities": ["anthropic/sol-max"], "agreement_bits": [true]}'::jsonb)`)
	require.NoError(t, err)

	// When upgrading to the full (post-CHAOS-3860-P6) migration set.
	latest, err := Embedded()
	require.NoError(t, err)

	// Then the upgrade succeeds -- NOT VALID skipped the existing-row scan
	// that would otherwise have rejected this exact row...
	require.NoError(t, latest.Up(ctx, db))
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, latest, db))

	// ...the legacy row is grandfathered, untouched...
	var legacyStillPresent bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM acr.context_fabric_structure_selections WHERE selection_id = 'legacy-consensus-row-0001')`).Scan(&legacyStillPresent))
	require.True(t, legacyStillPresent, "a legacy row NOT VALID chose not to validate must survive the upgrade untouched")

	// ...and the constraint is nonetheless ENFORCED for every new row from
	// this point forward (NOT VALID exempts existing rows from the
	// one-time scan; it does not weaken the constraint for new writes).
	_, err = db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_structure_selections
    (selection_id, org_id, captured_at, question_hash, prior_result_id, member, selected_receipt_id, selected_applied_value, accepted, selection_mode, selection_provenance, offered, pipeline_context, consensus_evidence)
VALUES ('post-upgrade-consensus-row-0001', 'org-legacy-upgrade', now(), repeat('a', 64), 'result-legacy-0002', 'expected_kind', 'kindr_x', 'pull_request', true, 'agent_receipt', 'credential_mcp', '[]'::jsonb, '{}'::jsonb, '{"panel_model_identities": ["anthropic/sol-max"], "agreement_bits": [true]}'::jsonb)`)
	require.Error(t, err, "a NEW single-member row must still be rejected after the upgrade -- NOT VALID is not a permanently weaker constraint")
}

// migrationFileNameByVersion resolves the embedded migration file whose
// numeric version prefix matches version, for tests that need a
// released-fixture file SET by version number rather than by hand-typed
// filename (a hand-typed list drifts the moment a migration is renamed;
// this cannot).
func migrationFileNameByVersion(t *testing.T, version int64) string {
	t.Helper()
	runner, err := Embedded()
	require.NoError(t, err)
	for _, migration := range runner.migrations {
		if migration.Version == version {
			return migration.Name
		}
	}
	t.Fatalf("no embedded migration with version %d", version)
	return ""
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

// TestRunner_upgradeTo32BackfillsRowsAppliedForAlreadyOpenBuilds is
// CHAOS-4305's own migration-rollout proof, closing codex R1's Medium
// finding: 0032 adds context_fabric_projection_checkpoints.rows_applied at
// DEFAULT 0, but an organization with an ALREADY-OPEN build epoch when this
// migration lands has a checkpoint row whose cursor already advanced under
// the pre-CHAOS-4305 binary -- rows_applied never existed to track that
// history, only cf_build_source_progress.rows_projected did (the very
// table CHAOS-4305 stops trusting for future-tick accumulation). Left at
// the bare DEFAULT, runBuildPair's new checkpoint-derived total would
// UNDERCOUNT that in-flight build's remaining ticks -- reintroducing, once,
// during this migration's own rollout window, exactly the kind of
// undercount CHAOS-4305 exists to close. 0032's own backfill UPDATE must
// recover it from cf_build_source_progress instead.
func TestRunner_upgradeTo32BackfillsRowsAppliedForAlreadyOpenBuilds(t *testing.T) {
	// Given a database at the released main schema through migration 0031
	// (everything before CHAOS-4305's own 0032)...
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	pre0032Files := fstest.MapFS{}
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
		"0012_context_fabric_reuse_fallback_identity_cutover.sql",
		"0013_context_fabric_time_axis_reuse_key.sql",
		"0014_context_fabric_embed_retrieval_reuse_key.sql",
		"0015_context_fabric_prompt_version_reuse_key.sql",
		"0016_context_fabric_clarification_selections.sql",
		"0017_context_fabric_model_receipts_request_id.sql",
		"0018_context_fabric_identity_normalization_reuse_key.sql",
		"0019_context_fabric_graph_lifecycle.sql",
		"0020_context_fabric_projection_checkpoints_epoch.sql",
		"0021_context_fabric_graph_epoch_reuse_key.sql",
		"0022_context_fabric_window_inference_reuse_key.sql",
		"0023_context_fabric_structure_supersession_claims.sql",
		"0024_context_fabric_structure_selections.sql",
		"0025_context_fabric_structure_supersession_backfill.sql",
		"0026_context_fabric_structure_selections_consensus_evidence.sql",
		"0027_context_fabric_structure_selections_consensus_panel_size.sql",
		"0028_context_fabric_structure_priors.sql",
		"0029_context_fabric_structure_bearing_reuse_cleanup.sql",
		"0030_workload_token_exchange.sql",
		"0031_commit_gate_version.sql",
	} {
		pre0032Files[name] = &fstest.MapFile{Data: mustReadFile(t, name)}
	}
	released, err := NewRunner(pre0032Files)
	require.NoError(t, err)
	require.NoError(t, released.Up(ctx, db))

	// Simulate an organization with an already-open build epoch at the
	// moment this migration lands: a checkpoint row that already advanced
	// (a real cursor, no rows_applied column to have recorded it in) beside
	// the durable cf_build_source_progress row the OLD code was tracking
	// the true total in.
	_, err = db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_projection_checkpoints (org_id, source, epoch, cursor, source_version, backend_watermark, updated_at)
VALUES ('org-1', 'source-a', 1, 'cursor-mid-build', 'v1', 'watermark-1', now())`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_graph_build_source_progress (org_id, epoch, source, completion_mode, rows_projected, updated_at)
VALUES ('org-1', 1, 'source-a', 'pending', 137, now())`)
	require.NoError(t, err)
	// A checkpoint with nothing ever applied to it (a genuinely fresh
	// source, or the legacy epoch-0 row) must stay at 0 -- the backfill's
	// `bsp.rows_projected > 0` guard, proven here rather than assumed.
	_, err = db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_projection_checkpoints (org_id, source, epoch, cursor, source_version, backend_watermark, updated_at)
VALUES ('org-1', 'source-b', 1, '', '', '', now())`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_graph_build_source_progress (org_id, epoch, source, completion_mode, rows_projected, updated_at)
VALUES ('org-1', 1, 'source-b', 'empty_first_tick', 0, now())`)
	require.NoError(t, err)

	// When upgrading to the full (post-CHAOS-4305) migration set.
	latest, err := Embedded()
	require.NoError(t, err)

	// Then
	require.NoError(t, latest.Up(ctx, db))
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, latest, db))
	requireColumnExists(t, ctx, db, "context_fabric_projection_checkpoints", "rows_applied")
	requireConstraintExists(t, ctx, db, "ck_acr_cf_projection_checkpoints_rows_applied_nonneg")

	var rowsAppliedA, rowsAppliedB int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT rows_applied FROM acr.context_fabric_projection_checkpoints WHERE org_id = 'org-1' AND epoch = 1 AND source = 'source-a'`).Scan(&rowsAppliedA))
	require.Equal(t, int64(137), rowsAppliedA,
		"the backfill must recover an in-flight build's true prior total from cf_build_source_progress, not leave it at the migration's own DEFAULT 0")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT rows_applied FROM acr.context_fabric_projection_checkpoints WHERE org_id = 'org-1' AND epoch = 1 AND source = 'source-b'`).Scan(&rowsAppliedB))
	require.Zero(t, rowsAppliedB, "a source with nothing genuinely applied (rows_projected=0) must stay at 0, not be touched by the backfill")
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

// requireContextFabricInvestigationResultsColumn is the generic column-
// existence check CHAOS-3862's upgrade test uses for all five new reuse-key
// columns, rather than five near-duplicate single-purpose functions.
func requireContextFabricInvestigationResultsColumn(t *testing.T, ctx context.Context, db *sql.DB, column string) {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = 'acr' AND table_name = 'context_fabric_investigation_results' AND column_name = $1
	)`, column).Scan(&exists))
	require.True(t, exists, "missing context_fabric_investigation_results column %s", column)
}

// requireTableExists/requireTableAbsent/requireColumnExists are the
// generic (table-parameterized) versions of the table-specific
// require<Table>Column helpers above -- CHAOS-3859's 0016 upgrade proof
// needs a brand-NEW table's existence checked both before (absent) and
// after (present) the upgrade, which none of the earlier single-table
// helpers were built to express.
func requireTableExists(t *testing.T, ctx context.Context, db *sql.DB, table string) {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables WHERE table_schema = 'acr' AND table_name = $1
	)`, table).Scan(&exists))
	require.True(t, exists, "missing table %s", table)
}

func requireTableAbsent(t *testing.T, ctx context.Context, db *sql.DB, table string) {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables WHERE table_schema = 'acr' AND table_name = $1
	)`, table).Scan(&exists))
	require.False(t, exists, "table %s should not exist yet at this migration level", table)
}

func requireColumnExists(t *testing.T, ctx context.Context, db *sql.DB, table, column string) {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = 'acr' AND table_name = $1 AND column_name = $2
	)`, table, column).Scan(&exists))
	require.True(t, exists, "missing %s column %s", table, column)
}

// requireConstraintExists/requireIndexExists/requireIndexAbsent back
// CHAOS-3862's 0014->0015 upgrade proof: the migration's positive effect
// (new columns, new constraints, the new index) AND its negative effect
// (the OLD index actually dropped, not left stacked beside the new one --
// the half of a replace-don't-stack migration a purely additive check
// would never catch).
func requireConstraintExists(t *testing.T, ctx context.Context, db *sql.DB, name string) {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_constraint c
		JOIN pg_namespace n ON n.oid = c.connamespace
		WHERE n.nspname = 'acr' AND c.conname = $1
	)`, name).Scan(&exists))
	require.True(t, exists, "missing constraint %s", name)
}

func requireIndexExists(t *testing.T, ctx context.Context, db *sql.DB, name string) {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_indexes WHERE schemaname = 'acr' AND indexname = $1
	)`, name).Scan(&exists))
	require.True(t, exists, "missing index %s", name)
}

func requireIndexAbsent(t *testing.T, ctx context.Context, db *sql.DB, name string) {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_indexes WHERE schemaname = 'acr' AND indexname = $1
	)`, name).Scan(&exists))
	require.False(t, exists, "index %s should have been dropped by the replacing migration", name)
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
