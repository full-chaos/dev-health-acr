package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// The write fence on a database built the way Kubernetes builds it: migrate,
// then grant-runtime-acl, with the writes made by the restricted runtime
// role. That path grants the runtime role nothing on the purge marker table,
// so a fence that read the marker with the writer's own privileges would fail
// every write of every organization with permission denied.
func TestWriteFenceWorksForARuntimeRoleWithNoGrantOnTheMarkerTable(t *testing.T) {
	ctx := context.Background()
	migrationDSN := newTestPostgresDSN(t, ctx)
	var output bytes.Buffer
	require.NoError(t, run(ctx, []string{"up"}, testMigrationEnvironment(migrationDSN), &output))

	ownerConfig, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	owner := stdlib.OpenDB(*ownerConfig)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	for _, statement := range []string{
		`CREATE ROLE acr_fence_runtime LOGIN PASSWORD 'runtime-test-password'`,
		`GRANT CONNECT ON DATABASE acr TO acr_fence_runtime`,
		`GRANT USAGE ON SCHEMA acr TO acr_fence_runtime`,
		// Stands for the table grants a deployment already holds; the fence adds none.
		`GRANT SELECT, INSERT ON TABLE acr.context_fabric_projection_rebuild_markers TO acr_fence_runtime`,
	} {
		_, err := owner.ExecContext(ctx, statement)
		require.NoError(t, err, statement)
	}
	require.NoError(t, run(ctx, []string{"grant-runtime-acl"}, environment(map[string]string{
		migrationDSNEnvironment: migrationDSN,
		runtimeDSNEnvironment:   "postgres://acr_fence_runtime:unused@runtime-dsn-is-never-dialed.invalid:5432/acr",
	}), &output))

	runtimeConfig, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	runtimeConfig.User, runtimeConfig.Password = "acr_fence_runtime", "runtime-test-password"
	runtime := stdlib.OpenDB(*runtimeConfig)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	state := func(err error) string {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return pgErr.Code
		}
		return ""
	}
	const (
		purged        = "aaaaaaaa-0000-4000-8000-000000000001"
		live          = "bbbbbbbb-0000-4000-8000-000000000002"
		insertRebuild = `INSERT INTO acr.context_fabric_projection_rebuild_markers (org_id, started_at) VALUES ($1, now())`
		denied        = "42501"
	)
	_, err = owner.ExecContext(ctx, `INSERT INTO acr.context_fabric_org_purge_markers (org_id, purged_at) VALUES ($1, now())`, purged)
	require.NoError(t, err)

	var mayReadMarkers, mayExecute, ownerIsMigrationRole bool
	require.NoError(t, owner.QueryRowContext(ctx, `
SELECT has_table_privilege('acr_fence_runtime', 'acr.context_fabric_org_purge_markers', 'SELECT'),
       has_function_privilege('acr_fence_runtime', 'acr.context_fabric_refuse_purged_org()', 'EXECUTE'),
       (SELECT pg_get_userbyid(proowner) = current_user FROM pg_proc WHERE oid = 'acr.context_fabric_refuse_purged_org()'::regprocedure)`).Scan(&mayReadMarkers, &mayExecute, &ownerIsMigrationRole))
	require.False(t, mayReadMarkers, "this path grants the runtime role nothing on the marker table")
	require.False(t, mayExecute, "the runtime role may not call the fence function")
	require.True(t, ownerIsMigrationRole, "the fence function is owned by the role that ran the migrations")

	_, err = runtime.ExecContext(ctx, insertRebuild, live)
	require.NoError(t, err, "a write for a live organization, by the runtime role")
	_, err = runtime.ExecContext(ctx, insertRebuild, purged)
	require.Equal(t, "ACR01", state(err), "a write for the purged organization, by the runtime role: %v", err)

	_, err = runtime.ExecContext(ctx, `SELECT acr.context_fabric_refuse_purged_org()`)
	require.Equal(t, denied, state(err), "the runtime role cannot call the fence function to learn whether an organization is marked: %v", err)
	_, err = runtime.ExecContext(ctx, `SELECT 1 FROM acr.context_fabric_org_purge_markers`)
	require.Equal(t, denied, state(err), "the runtime role cannot read the marker table: %v", err)

	// With the function run under the writer's privileges, the missing grant
	// fails every fenced write, for every organization.
	_, err = owner.ExecContext(ctx, `ALTER FUNCTION acr.context_fabric_refuse_purged_org() SECURITY INVOKER`)
	require.NoError(t, err)
	_, err = runtime.ExecContext(ctx, insertRebuild, "cccccccc-0000-4000-8000-000000000003")
	require.Equal(t, denied, state(err), "as security invoker the fence needs the marker grant: %v", err)
}
