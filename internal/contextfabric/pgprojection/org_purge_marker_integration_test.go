package pgprojection_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/stretchr/testify/require"
)

const (
	purgeMarkerOrg     = "0f7c2d4e-8a31-4b6d-9c15-2e7a6b9d1f03"
	purgeMarkerOrgLate = "9b3e1f20-5c44-4d7a-8e61-0a2d7c5f3b18"
)

func TestOrgPurgeMarkerStore_clearsOnlyMarkedOrganizationsInAnyUUIDSpelling(t *testing.T) {
	ctx := context.Background()
	db := newCheckpointTestDatabase(t, ctx)
	store, err := pgprojection.NewOrgPurgeMarkerStore(db)
	require.NoError(t, err)

	purged, err := store.IsPurged(ctx, purgeMarkerOrg)
	require.NoError(t, err)
	require.False(t, purged)

	_, err = db.ExecContext(ctx, `INSERT INTO acr.context_fabric_org_purge_markers (org_id, purged_at) VALUES ($1, now())`, purgeMarkerOrg)
	require.NoError(t, err)

	purged, err = store.IsPurged(ctx, strings.ToUpper(purgeMarkerOrg))
	require.NoError(t, err)
	require.True(t, purged, "the check reads any spelling of the id")
	purged, err = store.IsPurged(ctx, "org-not-a-uuid")
	require.NoError(t, err)
	require.False(t, purged)

	started, err := store.DiscoveryClock(ctx)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO acr.context_fabric_org_purge_markers (org_id, purged_at) VALUES ($1, clock_timestamp())`, purgeMarkerOrgLate)
	require.NoError(t, err)

	cleared, err := store.ClearPurged(ctx, []string{"org-not-a-uuid", "6a1b2c3d-0000-4000-8000-000000000001", strings.ToUpper(purgeMarkerOrg), purgeMarkerOrgLate}, started)
	require.NoError(t, err)
	require.Equal(t, []string{purgeMarkerOrg}, cleared, "only markers written before the discovery began are cleared")
	purged, err = store.IsPurged(ctx, purgeMarkerOrg)
	require.NoError(t, err)
	require.False(t, purged)
	purged, err = store.IsPurged(ctx, purgeMarkerOrgLate)
	require.NoError(t, err)
	require.True(t, purged, "a marker written after the discovery began survives it")
}
