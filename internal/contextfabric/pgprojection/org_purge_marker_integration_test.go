package pgprojection_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/stretchr/testify/require"
)

const (
	purgeMarkerOrg      = "0f7c2d4e-8a31-4b6d-9c15-2e7a6b9d1f03"
	purgeMarkerOrgLate  = "9b3e1f20-5c44-4d7a-8e61-0a2d7c5f3b18"
	purgeMarkerOrgAfter = "c4d8a1b6-2e97-4f30-b5a2-7d1e9f604c35"
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

	for _, spelling := range []string{strings.ToUpper(purgeMarkerOrg), "{" + purgeMarkerOrg + "}", "urn:uuid:" + purgeMarkerOrg, strings.ReplaceAll(purgeMarkerOrg, "-", ""), " " + purgeMarkerOrg + " "} {
		purged, err = store.IsPurged(ctx, spelling)
		require.NoError(t, err)
		require.True(t, purged, "the check reads the spelling %q", spelling)
	}
	purged, err = store.IsPurged(ctx, "org-not-a-uuid")
	require.NoError(t, err)
	require.False(t, purged)

	// A purge whose transaction wrote its marker before the discovery began
	// but committed after it began.
	slow, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = slow.ExecContext(ctx, `INSERT INTO acr.context_fabric_org_purge_markers (org_id, purged_at) VALUES ($1, clock_timestamp())`, purgeMarkerOrgLate)
	require.NoError(t, err)

	clear, release, err := store.BeginDiscovery(ctx)
	require.NoError(t, err)
	defer release()

	require.NoError(t, slow.Commit())
	_, err = db.ExecContext(ctx, `INSERT INTO acr.context_fabric_org_purge_markers (org_id, purged_at) VALUES ($1, clock_timestamp())`, purgeMarkerOrgAfter)
	require.NoError(t, err)

	cleared, err := clear(ctx, []string{"org-not-a-uuid", "6a1b2c3d-0000-4000-8000-000000000001", strings.ToUpper(purgeMarkerOrg), purgeMarkerOrgLate, purgeMarkerOrgAfter})
	require.NoError(t, err)
	require.Equal(t, []string{purgeMarkerOrg}, cleared, "only markers committed before the discovery began are cleared")
	purged, err = store.IsPurged(ctx, purgeMarkerOrg)
	require.NoError(t, err)
	require.False(t, purged)
	for _, org := range []string{purgeMarkerOrgLate, purgeMarkerOrgAfter} {
		purged, err = store.IsPurged(ctx, org)
		require.NoError(t, err)
		require.True(t, purged, "a marker committed after the discovery began survives it: %s", org)
	}
}
