package pginvestigation_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func setEpochCheckpointWatermark(t *testing.T, ctx context.Context, db *sql.DB, orgID string, epoch int64, source, watermark string) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_projection_checkpoints (org_id, epoch, source, cursor, source_version, backend_watermark, updated_at)
VALUES ($1, $2, $3, 'cursor', 'v1', $4, now())
ON CONFLICT (org_id, epoch, source) DO UPDATE SET backend_watermark = EXCLUDED.backend_watermark, updated_at = now()`,
		orgID, epoch, source, watermark)
	require.NoError(t, err)
}

func setServingLifecycle(t *testing.T, ctx context.Context, db *sql.DB, orgID string, active, building int64) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_graph_lifecycle (org_id, active_epoch, last_allocated_epoch, status, target_epoch, required_sources, updated_at)
VALUES ($1, $2, $3, 'building', $3, '["linear"]'::jsonb, now())`, orgID, active, building)
	require.NoError(t, err)
}

// A building epoch's checkpoint must not stand in for the serving epoch's
// watermark in the snapshot an answer is stored under.
func TestSourceWatermarkSnapshot_ReadsOnlyTheServingEpoch(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-serving-epoch-snapshot"}
	setServingLifecycle(t, ctx, db, principal.OrgID, 1, 2)
	setEpochCheckpointWatermark(t, ctx, db, principal.OrgID, 0, "linear", "legacy-wm")
	setEpochCheckpointWatermark(t, ctx, db, principal.OrgID, 1, "linear", "serving-wm")
	setEpochCheckpointWatermark(t, ctx, db, principal.OrgID, 2, "linear", "building-wm")
	// Sources present only outside the serving epoch: independent of row
	// order, they must be absent from the serving snapshot.
	setEpochCheckpointWatermark(t, ctx, db, principal.OrgID, 0, "legacy-only", "legacy-only-wm")
	setEpochCheckpointWatermark(t, ctx, db, principal.OrgID, 2, "building-only", "building-only-wm")

	store := mustReuseStore(t, db, time.Hour)
	snapshot, err := store.SnapshotSourceWatermarks(ctx, principal.OrgID)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"linear": "serving-wm"}, map[string]string(snapshot))
}

// Advancing a building epoch must not expire a reusable answer; advancing
// the serving epoch must.
func TestFindReusable_FollowsTheServingEpochWatermarkOnly(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-serving-epoch-reuse"}
	setServingLifecycle(t, ctx, db, principal.OrgID, 1, 2)
	setEpochCheckpointWatermark(t, ctx, db, principal.OrgID, 1, "linear", "serving-wm")
	setEpochCheckpointWatermark(t, ctx, db, principal.OrgID, 2, "linear", "building-wm")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_serving_epoch01", principal.OrgID, "Is the auth migration on track?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "premise: reusable before any watermark moves")

	setEpochCheckpointWatermark(t, ctx, db, principal.OrgID, 2, "linear", "building-wm-advanced")
	_, ok, _, err = store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "a building epoch's watermark must not expire an answer served from the serving epoch")

	setEpochCheckpointWatermark(t, ctx, db, principal.OrgID, 1, "linear", "serving-wm-advanced")
	_, ok, _, err = store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "the serving epoch's watermark moved: the answer must expire")
}

// An organization that never ran a lifecycle build has no lifecycle row and
// serves epoch 0.
func TestSourceWatermarkSnapshot_NoLifecycleRowReadsEpochZero(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-serving-epoch-legacy"}
	setEpochCheckpointWatermark(t, ctx, db, principal.OrgID, 0, "linear", "legacy-wm")
	setEpochCheckpointWatermark(t, ctx, db, principal.OrgID, 3, "linear", "orphan-wm")
	setEpochCheckpointWatermark(t, ctx, db, principal.OrgID, 3, "orphan-only", "orphan-only-wm")

	store := mustReuseStore(t, db, time.Hour)
	snapshot, err := store.SnapshotSourceWatermarks(ctx, principal.OrgID)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"linear": "legacy-wm"}, map[string]string(snapshot))
}
