// Package compose_test proves acr-db-init.sh's runtime-acl grants against a
// REAL Postgres, driving the ACTUAL deployed shell script through the exact
// three-stage sequence acr.compose.yml runs in production (acr-db-init
// roles -> acr-migrate up -> acr-db-acl runtime-acl -> acr-api serve),
// rather than a hand-copied duplicate of its SQL that could silently drift
// from what is actually deployed.
//
// CHAOS-3859 sol review F1: hosted's clarification-selection capture sink
// (internal/contextfabric/pgclarification) writes through
// ACR_RUNTIME_DB_USER, the restricted role acr-db-init.sh's runtime-acl
// step grants privileges to -- NOT the migration/superuser role every
// OTHER testcontainers-backed integration test in this repo connects as.
// No existing Go test exercised that restricted role at all (grepped: no
// test references acr-db-init, runtime-acl, or ACR_RUNTIME_DB_USER), which
// is exactly how migration 0016's table shipped with no explicit grant for
// it at all -- production INSERTs from the real runtime role would have
// failed with permission-denied, converted by the sink's own fail-open
// design into an invisible, never-logged (on the caller path) dropped
// write. This file is "the compose acceptance/integration seam that runs
// acr-db-init," per the review's own instruction: the honest, narrowest
// place to prove the grant is real, without standing up the full
// fullstack-acceptance workflow (which needs a sibling private repository
// checkout and is not runnable in this sandbox).
package compose_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

// requirePostgresClientTools skips when psql/pg_isready are not on PATH in
// local dev, but FAILS in CI. acr-db-init.sh shells out to both, and
// .github/workflows/ci.yml's unit and race jobs now install postgresql-client
// specifically so this test genuinely runs there (CHAOS-3859 sol review
// F1 follow-up) -- a skip is how migration 0016's table shipped with no
// runtime grant at all the first time: a test that can quietly skip in the
// one environment meant to enforce it reads as passing when it never ran.
// Locally, a contributor without postgresql-client installed still gets
// the polite "absent means degrade" skip this codebase applies to every
// other optional local dependency; CI has no such excuse once the tool is
// installed by name in the workflow, so there a missing tool is a real
// regression (an accidental removal of that install step, a runner image
// change) and must fail loudly instead of silently no-op'ing the grant
// proof.
func requirePostgresClientTools(t *testing.T) {
	t.Helper()
	inCI := os.Getenv("CI") == "true" || os.Getenv("GITHUB_ACTIONS") == "true"
	for _, tool := range []string{"psql", "pg_isready", "sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			if inCI {
				t.Fatalf("%s not found on PATH in CI -- the unit and race jobs are expected to install postgresql-client before running Go tests; this test must not silently skip in the one environment meant to enforce acr-db-init.sh's runtime-acl grants", tool)
			}
			t.Skipf("%s not found on PATH -- skipping the real acr-db-init.sh integration proof (see this file's own package doc comment for why this is a skip, not a failure)", tool)
		}
	}
}

// acrDBInitHarness bundles the runtime- and migration-role connections
// bootstrapACRDBInit produces, sharing ONE Postgres testcontainer and ONE
// acr-db-init.sh run sequence across every test that needs to prove a
// runtime-role grant -- every context_fabric_* table's grant is set by the
// SAME runtime-acl script run, so a fresh container per table would be
// needlessly slow (testcontainers startup dominates this file's runtime)
// with no added coverage.
type acrDBInitHarness struct {
	ctx         context.Context
	runtimeDB   *sql.DB
	migrationDB *sql.DB
}

// bootstrapACRDBInit runs the REAL acr-db-init.sh in both modes (roles, then
// runtime-acl, with migrations applied as the migration role in between --
// byte-for-byte the sequence acr.compose.yml's acr-db-init, acr-migrate, and
// acr-db-acl services run in production), against a fresh Postgres
// testcontainer, and returns connections for both roles. Extracted from
// TestAcrDbInit_RuntimeRoleCanInsertIntoClarificationSelections (CHAOS-3859)
// so CHAOS-3876's per-table grant audit below reuses the exact same real
// bootstrap sequence rather than a second hand-copied one.
func bootstrapACRDBInit(t *testing.T) acrDBInitHarness {
	t.Helper()
	requirePostgresClientTools(t)
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.WithDatabase("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	adminDSN, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	adminURL, err := url.Parse(adminDSN)
	require.NoError(t, err)
	host := adminURL.Hostname()
	port := adminURL.Port()

	scriptPath, err := filepath.Abs("acr-db-init.sh")
	require.NoError(t, err)

	const (
		acrDBName         = "acr"
		runtimeUser       = "acr_runtime_test"
		runtimePassword   = "acr-runtime-test-password"
		migrationUser     = "acr_migration_test"
		migrationPassword = "acr-migration-test-password"
	)
	baseEnv := append(os.Environ(),
		"POSTGRES_HOST="+host, "POSTGRES_PORT="+port, "POSTGRES_USER=postgres", "POSTGRES_PASSWORD=postgres",
		"ACR_DB_NAME="+acrDBName,
		"ACR_RUNTIME_DB_USER="+runtimeUser, "ACR_RUNTIME_DB_PASSWORD="+runtimePassword,
		"ACR_MIGRATION_DB_USER="+migrationUser, "ACR_MIGRATION_DB_PASSWORD="+migrationPassword,
		"ACR_ENABLE_EPISODE_WRITEBACK=false",
	)

	runScript := func(mode string) {
		t.Helper()
		runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(runCtx, "sh", scriptPath, mode)
		cmd.Env = baseEnv
		output, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "acr-db-init.sh %s failed: %s", mode, output)
	}

	// Stage 1, mirroring acr.compose.yml's acr-db-init service: create the
	// roles and the acr database.
	runScript("roles")

	// Stage 2, mirroring acr-migrate: apply every migration AS THE
	// MIGRATION ROLE -- table ownership must be the migration role's for
	// the runtime-acl stage's ALTER TABLE OWNER / REVOKE/GRANT sequence to
	// mean what it means in real deployments, not a superuser's ownership
	// standing in for it.
	migrationDSN := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", migrationUser, migrationPassword, host, port, acrDBName)
	migrationDB, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: migrationDSN})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, migrationDB.Close()) })
	runner, err := migrations.Embedded()
	require.NoError(t, err)
	require.NoError(t, runner.Up(ctx, migrationDB))

	// Stage 3, mirroring acr-db-acl: transfer ownership to the migration
	// role and lock the runtime role down to the exact per-table grants
	// this script hard-codes.
	runScript("runtime-acl")

	// Now connect as the RUNTIME role -- the same role internal/runtime/
	// hosted and cmd/acr-projector wire every context_fabric_* store's
	// *sql.DB through in a real deployment.
	runtimeDSN := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", runtimeUser, runtimePassword, host, port, acrDBName)
	runtimeDB, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: runtimeDSN})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtimeDB.Close()) })

	return acrDBInitHarness{ctx: ctx, runtimeDB: runtimeDB, migrationDB: migrationDB}
}

// TestAcrDbInit_RuntimeRoleCanInsertIntoClarificationSelections is CHAOS-3859
// sol review F1's grant proof: proves an INSERT into
// acr.context_fabric_clarification_selections succeeds under the real
// runtime-acl grants. Also asserts the negative control: a SELECT on the
// SAME table as the SAME role fails -- this table is INSERT-only for the
// runtime role (mirroring audit_events), so a passing INSERT test alone
// would not catch an accidentally-too-permissive grant.
func TestAcrDbInit_RuntimeRoleCanInsertIntoClarificationSelections(t *testing.T) {
	h := bootstrapACRDBInit(t)
	ctx, runtimeDB, migrationDB := h.ctx, h.runtimeDB, h.migrationDB

	_, err := runtimeDB.ExecContext(ctx, `
INSERT INTO acr.context_fabric_clarification_selections
    (selection_id, org_id, captured_at, question_hash, prior_result_id, selected_receipt_id, selected_subject_kind, selected_subject_canonical_id, selection_provenance, offered_candidates, pipeline_context)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		"11111111-1111-4111-8111-111111111111", "org-acl-proof",
		time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
		// SHA-256 of the empty string -- a well-known, valid 64-hex-char
		// placeholder; this test only cares about the grant, not a real
		// question hash.
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"result_acl_proof_0001", "receipt_acl_proof_01", "project", "project-acl-proof",
		"web_assertion", `[]`, `{}`)
	require.NoError(t, err, "the runtime role must be able to INSERT into acr.context_fabric_clarification_selections (CHAOS-3859 sol review F1) -- if this fails with a permission error, acr-db-init.sh's runtime-acl grant list is missing this table again")

	// Negative control: this table is INSERT-only for the runtime role
	// (mirroring audit_events, migration 0016's own header comment) -- a
	// SELECT must fail. Without this half, a future edit that accidentally
	// granted SELECT too would pass the INSERT assertion above and this
	// test would never notice the privilege escalation.
	var count int
	selectErr := runtimeDB.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_clarification_selections`).Scan(&count)
	require.Error(t, selectErr, "the runtime role must NOT be able to SELECT from acr.context_fabric_clarification_selections -- this table is insert-only for the runtime role")

	// And the row genuinely landed -- confirmed via the MIGRATION role,
	// which does have full access to its own tables.
	var landedCount int
	require.NoError(t, migrationDB.QueryRowContext(ctx,
		`SELECT count(*) FROM acr.context_fabric_clarification_selections WHERE org_id = $1`, "org-acl-proof").Scan(&landedCount))
	require.Equal(t, 1, landedCount)
}

// contextFabricRuntimeGrantCase is one context_fabric_* table's runtime-role
// grant proof: an INSERT that must succeed under the real ACL, plus which
// direction the SELECT negative/positive control checks.
type contextFabricRuntimeGrantCase struct {
	// name identifies the subtest; table is the bare table name (no schema
	// prefix) used for both the INSERT's target and the SELECT checks.
	name  string
	table string
	// insertSQL is the full INSERT statement (schema-qualified), proving
	// the runtime role's grant. args are its positional parameters.
	insertSQL string
	args      []any
	// orgID is this row's unique org_id, tracked SEPARATELY from args
	// (rather than assumed to be args[0]) because several of these tables
	// list their OWN primary key column -- result_id, receipt_id,
	// selection_id -- before org_id in the INSERT's column list. It is
	// reused below to verify the row landed via the migration role.
	orgID string
	// selectGranted reports whether the runtime role should be able to
	// SELECT from this table (per acr-db-init.sh's runtime-acl grant list)
	// -- most of these tables grant SELECT (the hosted API reads several of
	// them back), but the insert-only sink tables (mirroring audit_events)
	// do not.
	selectGranted bool
}

// TestAcrDbInit_RuntimeRoleCanInsertIntoEveryOtherContextFabricTable is
// CHAOS-3876's audit proof: CHAOS-3859's fix (the test above) closed the
// runtime-acl grant gap for ONE context_fabric_* table
// (context_fabric_clarification_selections); this ticket found that NO
// OTHER context_fabric_* table appeared in acr-db-init.sh's explicit grant
// list either, even though every one of them is written (and several also
// read), IN THE PRODUCTION SERVING PATH, through ACR_RUNTIME_DB_USER -- the
// hosted API and acr-projector (including its "priors" operator
// subcommands, cmd/acr-projector/priors.go's openPriorsDB) each open
// exactly ONE Postgres connection, as that role. A table absent from the
// grant list is not merely under-privileged, it is completely unwritable
// there: every INSERT/UPDATE/SELECT against it fails permission-denied,
// which several of these write paths (pgclarification.Sink's own
// precedent) would swallow silently rather than surface to a caller.
//
// "In the production serving path" is deliberate scoping, not the whole
// story (codex round-1 M1): a handful of these tables also see DML from
// data-backfill migrations (e.g. 0012, 0025, 0029, 0032) and one from
// scripts/trial's pg_dump|psql graph_lifecycle copy -- both run as the
// MIGRATION role (or a superuser, for the trial script), never
// ACR_RUNTIME_DB_USER, and neither is production request-serving traffic.
// Those are the ONLY other writers found; acr-panel-harness, the one other
// context_fabric_structure_selections-adjacent binary, has no database
// access at all (cmd/acr-panel-harness/main.go's own doc comment).
//
// One INSERT per table, under the REAL acr-db-init.sh ACL, proves each
// grant genuinely works -- not just that acr-db-init.sh parses without
// error. Each row's SELECT check proves the grant is neither missing (a
// write that should work failing) nor too permissive (a read that should
// be denied succeeding); a later UPDATE-only table gaining SELECT by
// accident would fail this the same way CHAOS-3859's own negative control
// catches an accidental SELECT grant on an insert-only table.
func TestAcrDbInit_RuntimeRoleCanInsertIntoEveryOtherContextFabricTable(t *testing.T) {
	h := bootstrapACRDBInit(t)
	ctx, runtimeDB, migrationDB := h.ctx, h.runtimeDB, h.migrationDB

	// A well-known, valid 64-hex-char placeholder (SHA-256 of the empty
	// string) for the two tables whose question_hash column is
	// CHECK'd to exactly 64 hex characters -- these tests only care about
	// the grant, never a real question hash (matching the test above's own
	// convention).
	const questionHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

	cases := []contextFabricRuntimeGrantCase{
		{
			name:  "projection_checkpoints",
			table: "context_fabric_projection_checkpoints",
			insertSQL: `INSERT INTO acr.context_fabric_projection_checkpoints
    (org_id, source, updated_at) VALUES ($1, $2, $3)`,
			args:          []any{"org-acl-checkpoints", "github", now},
			orgID:         "org-acl-checkpoints",
			selectGranted: true,
		},
		{
			name:  "projection_rebuild_markers",
			table: "context_fabric_projection_rebuild_markers",
			insertSQL: `INSERT INTO acr.context_fabric_projection_rebuild_markers
    (org_id, started_at) VALUES ($1, $2)`,
			args:          []any{"org-acl-rebuild-markers", now},
			orgID:         "org-acl-rebuild-markers",
			selectGranted: true,
		},
		{
			name:  "investigation_results",
			table: "context_fabric_investigation_results",
			insertSQL: `INSERT INTO acr.context_fabric_investigation_results
    (result_id, org_id, payload, generated_at) VALUES ($1, $2, $3, $4)`,
			args:          []any{"result_acl_proof_investigation", "org-acl-investigation-results", `{}`, now},
			orgID:         "org-acl-investigation-results",
			selectGranted: true,
		},
		{
			name:  "org_model_config",
			table: "context_fabric_org_model_config",
			insertSQL: `INSERT INTO acr.context_fabric_org_model_config
    (org_id, provider, model, credential_ciphertext, credential_kid) VALUES ($1, $2, $3, $4, $5)`,
			args:          []any{"org-acl-model-config", "openai", "gpt-5.6-luna", []byte("sealed-ciphertext"), "kid-acl-proof"},
			orgID:         "org-acl-model-config",
			selectGranted: true,
		},
		{
			name:  "model_execution_receipts",
			table: "context_fabric_model_execution_receipts",
			insertSQL: `INSERT INTO acr.context_fabric_model_execution_receipts
    (receipt_id, org_id, operation, provider, outcome, fallback_used, payload, started_at, completed_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			args:          []any{"receipt_acl_proof_00000001", "org-acl-model-receipts", "interpret", "openai", "success", false, `{}`, now, now},
			orgID:         "org-acl-model-receipts",
			selectGranted: false, // insert-only sink, mirrors audit_events (package doc comment on pgmodelreceipts).
		},
		{
			name:  "reuse_invalidations",
			table: "context_fabric_reuse_invalidations",
			insertSQL: `INSERT INTO acr.context_fabric_reuse_invalidations
    (org_id, invalidated_at) VALUES ($1, $2)`,
			args:          []any{"org-acl-reuse-invalidations", now},
			orgID:         "org-acl-reuse-invalidations",
			selectGranted: true,
		},
		{
			name:  "graph_lifecycle",
			table: "context_fabric_graph_lifecycle",
			insertSQL: `INSERT INTO acr.context_fabric_graph_lifecycle
    (org_id, updated_at) VALUES ($1, $2)`,
			args:          []any{"org-acl-graph-lifecycle", now},
			orgID:         "org-acl-graph-lifecycle",
			selectGranted: true,
		},
		{
			name:  "graph_epoch_retirements",
			table: "context_fabric_graph_epoch_retirements",
			insertSQL: `INSERT INTO acr.context_fabric_graph_epoch_retirements
    (org_id, epoch, reason, drain_start, created_at, updated_at) VALUES ($1, 1, 'grace_expired', $2, $2, $2)`,
			args:          []any{"org-acl-epoch-retirements", now},
			orgID:         "org-acl-epoch-retirements",
			selectGranted: true,
		},
		{
			name:  "graph_build_source_progress",
			table: "context_fabric_graph_build_source_progress",
			insertSQL: `INSERT INTO acr.context_fabric_graph_build_source_progress
    (org_id, epoch, source, updated_at) VALUES ($1, 1, 'github', $2)`,
			args:          []any{"org-acl-build-source-progress", now},
			orgID:         "org-acl-build-source-progress",
			selectGranted: true,
		},
		{
			name:  "structure_supersession_claims",
			table: "context_fabric_structure_supersession_claims",
			insertSQL: `INSERT INTO acr.context_fabric_structure_supersession_claims
    (org_id, prior_result_id, member, claimed_by_result_id) VALUES ($1, $2, 'expected_kind', $3)`,
			args:          []any{"org-acl-supersession-claims", "result_acl_proof_prior", "result_acl_proof_winner"},
			orgID:         "org-acl-supersession-claims",
			selectGranted: true,
		},
		{
			name:  "structure_selections",
			table: "context_fabric_structure_selections",
			insertSQL: `INSERT INTO acr.context_fabric_structure_selections
    (selection_id, org_id, captured_at, question_hash, prior_result_id, member, selected_receipt_id, selected_applied_value, accepted, selection_mode, selection_provenance, offered, pipeline_context)
    VALUES ($1, $2, $3, $4, $5, 'expected_kind', $6, $7, true, 'agent_receipt', 'web_assertion', $8, $9)`,
			args:          []any{"selection_acl_proof_00000001", "org-acl-structure-selections", now, questionHash, "result_acl_proof_prior2", "receipt_acl_proof_02", "project", `[]`, `{}`},
			orgID:         "org-acl-structure-selections",
			selectGranted: true,
		},
		{
			name:  "structure_priors",
			table: "context_fabric_structure_priors",
			insertSQL: `INSERT INTO acr.context_fabric_structure_priors
    (org_id, version, entries, created_from_watermark, curation_rule_version) VALUES ($1, 1, $2, $3, $4)`,
			args:          []any{"org-acl-structure-priors", `[]`, "watermark-acl-proof", "rule-v1"},
			orgID:         "org-acl-structure-priors",
			selectGranted: true,
		},
		{
			name:  "structure_prior_pointer",
			table: "context_fabric_structure_prior_pointer",
			insertSQL: `INSERT INTO acr.context_fabric_structure_prior_pointer
    (org_id) VALUES ($1)`,
			args:          []any{"org-acl-prior-pointer"},
			orgID:         "org-acl-prior-pointer",
			selectGranted: true,
		},
		{
			name:  "structure_prior_pointer_history",
			table: "context_fabric_structure_prior_pointer_history",
			insertSQL: `INSERT INTO acr.context_fabric_structure_prior_pointer_history
    (org_id, ratified_by) VALUES ($1, $2)`,
			args:          []any{"org-acl-prior-pointer-history", "operator-acl-proof"},
			orgID:         "org-acl-prior-pointer-history",
			selectGranted: false, // insert-only audit trail; BIGSERIAL id needs its own sequence USAGE grant too.
		},
		{
			name:  "structure_prior_revocations",
			table: "context_fabric_structure_prior_revocations",
			insertSQL: `INSERT INTO acr.context_fabric_structure_prior_revocations
    (org_id, entry_id, revoked_by) VALUES ($1, $2, $3)`,
			args:          []any{"org-acl-prior-revocations", "entry-acl-proof", "operator-acl-proof"},
			orgID:         "org-acl-prior-revocations",
			selectGranted: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runtimeDB.ExecContext(ctx, tc.insertSQL, tc.args...)
			require.NoErrorf(t, err, "the runtime role must be able to INSERT into acr.%s -- if this fails with a permission error, acr-db-init.sh's runtime-acl grant list is missing this table (CHAOS-3876)", tc.table)

			var selectCount int
			selectErr := runtimeDB.QueryRowContext(ctx, "SELECT count(*) FROM acr."+tc.table).Scan(&selectCount)
			if tc.selectGranted {
				require.NoErrorf(t, selectErr, "the runtime role must be able to SELECT from acr.%s per acr-db-init.sh's runtime-acl grant list", tc.table)
			} else {
				require.Errorf(t, selectErr, "the runtime role must NOT be able to SELECT from acr.%s -- this table is insert-only for the runtime role", tc.table)
			}

			var landedCount int
			require.NoError(t, migrationDB.QueryRowContext(ctx,
				"SELECT count(*) FROM acr."+tc.table+" WHERE org_id = $1", tc.orgID).Scan(&landedCount))
			require.Equalf(t, 1, landedCount, "case %s: the INSERTed row must be visible via the migration role", tc.name)
		})
	}
}
