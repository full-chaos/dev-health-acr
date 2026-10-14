package main

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/stretchr/testify/require"
)

// The purge marker is read by every projector tick and cleared by discovery,
// both on the runtime DSN; only the purge writes it. This runs the Kubernetes
// path (migrate, then grant-runtime-acl) and connects as the runtime role.
func TestGrantRuntimeACL_runtimeRoleCanUseThePurgeMarkerTable(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_marker_runtime_test"})

	const org = "0f7c2d4e-8a31-4b6d-9c15-2e7a6b9d1f03"
	store, err := pgprojection.NewOrgPurgeMarkerStore(database.Runtime)
	require.NoError(t, err)

	t.Run("the projector tick reads the marker", func(t *testing.T) {
		purged, err := store.IsPurged(ctx, org)
		require.NoError(t, err)
		require.False(t, purged)
	})

	t.Run("the runtime role cannot write the marker: only the purge does", func(t *testing.T) {
		_, err := database.Runtime.ExecContext(ctx, `INSERT INTO acr.context_fabric_org_purge_markers (org_id, purged_at) VALUES ($1, clock_timestamp())`, org)
		require.ErrorContains(t, err, "42501")
		_, err = database.Runtime.ExecContext(ctx, `UPDATE acr.context_fabric_org_purge_markers SET purged_at = clock_timestamp() WHERE org_id = $1`, org)
		require.ErrorContains(t, err, "42501")
	})

	t.Run("discovery clears the marker from its snapshot", func(t *testing.T) {
		_, err := database.Owner.ExecContext(ctx, `INSERT INTO acr.context_fabric_org_purge_markers (org_id, purged_at) VALUES ($1, clock_timestamp()) ON CONFLICT (org_id) DO NOTHING`, org)
		require.NoError(t, err)
		clear, release, err := store.BeginDiscovery(ctx)
		require.NoError(t, err)
		defer release()
		_, err = database.Owner.ExecContext(ctx, `SELECT pg_sleep(0.05)`)
		require.NoError(t, err)
		cleared, err := clear(ctx, []string{org})
		require.NoError(t, err)
		require.Equal(t, []string{org}, cleared)
	})
}
