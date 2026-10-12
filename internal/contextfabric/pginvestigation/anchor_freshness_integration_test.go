package pginvestigation_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	"github.com/stretchr/testify/require"
)

// The ownership source's watermark at the serving epoch, read by a store
// with answer reuse off: another epoch's row and another source's row never
// stand in for it.
func TestReadAnchorFreshness_ReadsTheOwnershipSourceAtTheServingEpochWithReuseOff(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	orgID := "org-anchor-freshness-serving"
	setServingLifecycle(t, ctx, db, orgID, 1, 2)
	setEpochCheckpointWatermark(t, ctx, db, orgID, 0, contextfabric.AnchorOwnershipSource, "ownership-legacy")
	setEpochCheckpointWatermark(t, ctx, db, orgID, 2, contextfabric.AnchorOwnershipSource, "ownership-building")
	setEpochCheckpointWatermark(t, ctx, db, orgID, 1, "dev_health_clickhouse", "entities-serving")

	store, err := pginvestigation.NewStore(db)
	require.NoError(t, err)

	read, err := store.ReadAnchorFreshness(ctx, orgID)
	require.NoError(t, err)
	require.Equal(t, contextfabric.AnchorFreshnessRead{ServingEpoch: 1}, read, "no ownership checkpoint at the serving epoch: unrecorded, never another epoch's or another source's")

	setEpochCheckpointWatermark(t, ctx, db, orgID, 1, contextfabric.AnchorOwnershipSource, "ownership-serving")
	read, err = store.ReadAnchorFreshness(ctx, orgID)
	require.NoError(t, err)
	require.Equal(t, contextfabric.AnchorFreshnessRead{ServingEpoch: 1, OwnershipWatermark: "ownership-serving", Recorded: true}, read)

	setEpochCheckpointWatermark(t, ctx, db, orgID, 1, "dev_health_clickhouse", "entities-serving-advanced")
	read, err = store.ReadAnchorFreshness(ctx, orgID)
	require.NoError(t, err)
	require.Equal(t, "ownership-serving", read.OwnershipWatermark, "a non-ownership source moving does not move the ownership freshness")
}

// An organization with no lifecycle row serves epoch 0, and an empty
// watermark on a present checkpoint is recorded.
func TestReadAnchorFreshness_NoLifecycleRowReadsEpochZero(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	orgID := "org-anchor-freshness-legacy"
	setEpochCheckpointWatermark(t, ctx, db, orgID, 0, contextfabric.AnchorOwnershipSource, "")
	setEpochCheckpointWatermark(t, ctx, db, orgID, 3, contextfabric.AnchorOwnershipSource, "ownership-orphan")

	store, err := pginvestigation.NewStore(db)
	require.NoError(t, err)
	read, err := store.ReadAnchorFreshness(ctx, orgID)
	require.NoError(t, err)
	require.Equal(t, contextfabric.AnchorFreshnessRead{ServingEpoch: 0, OwnershipWatermark: "", Recorded: true}, read)

	_, err = store.ReadAnchorFreshness(ctx, " ")
	require.Error(t, err)
}
