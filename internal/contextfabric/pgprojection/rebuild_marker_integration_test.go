package pgprojection_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/stretchr/testify/require"
)

func TestRebuildMarkerStore_beginIsIdempotentAndVisibleUntilCompleted(t *testing.T) {
	ctx := context.Background()
	store, err := pgprojection.NewRebuildMarkerStore(newCheckpointTestDatabase(t, ctx))
	require.NoError(t, err)

	inProgress, err := store.IsRebuildInProgress(ctx, "org-1")
	require.NoError(t, err)
	require.False(t, inProgress, "no marker before BeginRebuild")

	require.NoError(t, store.BeginRebuild(ctx, "org-1"))
	inProgress, err = store.IsRebuildInProgress(ctx, "org-1")
	require.NoError(t, err)
	require.True(t, inProgress)

	// Idempotent: a second BeginRebuild (a resume, or a crash-retry) must
	// not error.
	require.NoError(t, store.BeginRebuild(ctx, "org-1"))
	inProgress, err = store.IsRebuildInProgress(ctx, "org-1")
	require.NoError(t, err)
	require.True(t, inProgress)

	require.NoError(t, store.CompleteRebuild(ctx, "org-1"))
	inProgress, err = store.IsRebuildInProgress(ctx, "org-1")
	require.NoError(t, err)
	require.False(t, inProgress)

	// Idempotent: completing an already-absent marker must not error.
	require.NoError(t, store.CompleteRebuild(ctx, "org-1"))
}

func TestRebuildMarkerStore_isolatesByOrganization(t *testing.T) {
	ctx := context.Background()
	store, err := pgprojection.NewRebuildMarkerStore(newCheckpointTestDatabase(t, ctx))
	require.NoError(t, err)
	require.NoError(t, store.BeginRebuild(ctx, "org-1"))

	orgTwoInProgress, err := store.IsRebuildInProgress(ctx, "org-2")
	require.NoError(t, err)
	require.False(t, orgTwoInProgress, "a different organization's marker must not be affected")
}

func TestRebuildMarkerStore_rejectsEmptyOrganization(t *testing.T) {
	ctx := context.Background()
	store, err := pgprojection.NewRebuildMarkerStore(newCheckpointTestDatabase(t, ctx))
	require.NoError(t, err)
	require.Error(t, store.BeginRebuild(ctx, ""))
	_, err = store.IsRebuildInProgress(ctx, "")
	require.Error(t, err)
	require.Error(t, store.CompleteRebuild(ctx, ""))
}
