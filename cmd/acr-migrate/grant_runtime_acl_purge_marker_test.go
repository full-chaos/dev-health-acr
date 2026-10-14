package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// The purge marker is read by every projector tick and cleared by discovery,
// both on the runtime DSN; only the purge writes it. On Kubernetes the runtime
// role's table grants come from an out-of-band bootstrap plus
// grant-runtime-acl, never from acr-db-init.sh, so this runs the Kubernetes
// path (migrate, then grant-runtime-acl) and connects as the runtime role.
func TestGrantRuntimeACL_runtimeRoleCanUseThePurgeMarkerTable(t *testing.T) {
	ctx := context.Background()
	migrationDSN := newTestPostgresDSN(t, ctx)
	require.NoError(t, run(ctx, []string{"up"}, testMigrationEnvironment(migrationDSN), &bytes.Buffer{}))

	adminConfig, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	adminDB := stdlib.OpenDB(*adminConfig)
	t.Cleanup(func() { require.NoError(t, adminDB.Close()) })
	for _, statement := range []string{
		`CREATE ROLE acr_marker_runtime_test LOGIN PASSWORD 'runtime-test-password'`,
		`GRANT CONNECT ON DATABASE acr TO acr_marker_runtime_test`,
		`GRANT USAGE ON SCHEMA acr TO acr_marker_runtime_test`,
	} {
		_, err = adminDB.ExecContext(ctx, statement)
		require.NoError(t, err)
	}

	// A default privilege for the runtime role would hand it every table the
	// migrations create and hide a missing grant. This database is fresh, so
	// prove there is none.
	var defaultPrivileges int
	require.NoError(t, adminDB.QueryRowContext(ctx, `SELECT count(*) FROM pg_default_acl`).Scan(&defaultPrivileges))
	require.Zero(t, defaultPrivileges, "no default privileges: a missing grant must show up as permission denied")
	var markerPrivilegeBeforeGrant bool
	require.NoError(t, adminDB.QueryRowContext(ctx, `SELECT has_table_privilege('acr_marker_runtime_test', 'acr.context_fabric_org_purge_markers', 'SELECT')`).Scan(&markerPrivilegeBeforeGrant))
	require.False(t, markerPrivilegeBeforeGrant, "the runtime role starts with no privilege on the marker table")

	require.NoError(t, run(ctx, []string{"grant-runtime-acl"}, environment(map[string]string{
		migrationDSNEnvironment: migrationDSN,
		runtimeDSNEnvironment:   "postgres://acr_marker_runtime_test:unused@runtime-dsn-is-never-dialed.invalid:5432/acr",
	}), &bytes.Buffer{}))

	runtimeConfig, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	runtimeConfig.User, runtimeConfig.Password = "acr_marker_runtime_test", "runtime-test-password"
	runtimeDB := stdlib.OpenDB(*runtimeConfig)
	t.Cleanup(func() { require.NoError(t, runtimeDB.Close()) })

	const org = "0f7c2d4e-8a31-4b6d-9c15-2e7a6b9d1f03"
	store, err := pgprojection.NewOrgPurgeMarkerStore(runtimeDB)
	require.NoError(t, err)

	t.Run("the projector tick reads the marker", func(t *testing.T) {
		purged, err := store.IsPurged(ctx, org)
		require.NoError(t, err)
		require.False(t, purged)
	})

	t.Run("the runtime role cannot write the marker: only the purge does", func(t *testing.T) {
		_, err := runtimeDB.ExecContext(ctx, `INSERT INTO acr.context_fabric_org_purge_markers (org_id, purged_at) VALUES ($1, clock_timestamp())`, org)
		require.ErrorContains(t, err, "42501")
		_, err = runtimeDB.ExecContext(ctx, `UPDATE acr.context_fabric_org_purge_markers SET purged_at = clock_timestamp() WHERE org_id = $1`, org)
		require.ErrorContains(t, err, "42501")
	})

	t.Run("discovery clears the marker from its snapshot", func(t *testing.T) {
		_, err := adminDB.ExecContext(ctx, `INSERT INTO acr.context_fabric_org_purge_markers (org_id, purged_at) VALUES ($1, clock_timestamp()) ON CONFLICT (org_id) DO NOTHING`, org)
		require.NoError(t, err)
		clear, release, err := store.BeginDiscovery(ctx)
		require.NoError(t, err)
		defer release()
		_, err = adminDB.ExecContext(ctx, `SELECT pg_sleep(0.05)`)
		require.NoError(t, err)
		cleared, err := clear(ctx, []string{org})
		require.NoError(t, err)
		require.Equal(t, []string{org}, cleared)
	})
}
