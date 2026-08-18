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

// TestAcrDbInit_RuntimeRoleCanInsertIntoClarificationSelections is CHAOS-3859
// sol review F1's grant proof: runs the REAL acr-db-init.sh in both modes
// (roles, then runtime-acl, with migrations applied as the migration role
// in between -- byte-for-byte the sequence acr.compose.yml's acr-db-init,
// acr-migrate, and acr-db-acl services run), then connects AS the runtime
// role and proves an INSERT into acr.context_fabric_clarification_selections
// succeeds. Also asserts the negative control: a SELECT on the SAME table
// as the SAME role fails -- this table is INSERT-only for the runtime role
// (mirroring audit_events), so a passing INSERT test alone would not catch
// an accidentally-too-permissive grant.
func TestAcrDbInit_RuntimeRoleCanInsertIntoClarificationSelections(t *testing.T) {
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

	// Stage 2, mirroring acr-migrate: apply every migration (through 0016)
	// AS THE MIGRATION ROLE -- table ownership must be the migration
	// role's for the runtime-acl stage's ALTER TABLE OWNER / REVOKE/GRANT
	// sequence to mean what it means in real deployments, not a
	// superuser's ownership standing in for it.
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

	// Now connect as the RUNTIME role -- the same role
	// internal/runtime/hosted wires pgclarification.Sink's *sql.DB
	// through in a real deployment -- and prove the grant this fix added.
	runtimeDSN := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", runtimeUser, runtimePassword, host, port, acrDBName)
	runtimeDB, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: runtimeDSN})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtimeDB.Close()) })

	_, err = runtimeDB.ExecContext(ctx, `
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
