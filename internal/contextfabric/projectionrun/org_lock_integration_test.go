package projectionrun_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func newOrgLockTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	// CHAOS-4855: pinned by digest (was a bare tag) so
	// TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX resolves this to the ghcr.io
	// mirror by digest, same as every other postgres:18-alpine pull in
	// this module.
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44",
		tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

// This is the amendment's cross-process proof: two independent
// PostgresOrgLocker instances against the SAME database (standing in for
// two acr-projector replicas) must never both hold the same organization's
// lock at once, and the lock must be reusable after release.
func TestPostgresOrgLocker_excludesConcurrentReplicasThenReleases(t *testing.T) {
	ctx := context.Background()
	db := newOrgLockTestDatabase(t, ctx)
	replicaA, err := projectionrun.NewPostgresOrgLocker(db)
	require.NoError(t, err)
	replicaB, err := projectionrun.NewPostgresOrgLocker(db)
	require.NoError(t, err)

	unlockA, err := replicaA.Lock(ctx, "org-1")
	require.NoError(t, err)

	_, err = replicaB.Lock(ctx, "org-1")
	require.True(t, errors.Is(err, projectionrun.ErrOrgLocked), "a second replica must not acquire an already-held organization lock, got: %v", err)

	// A different organization is unaffected.
	unlockOther, err := replicaB.Lock(ctx, "org-2")
	require.NoError(t, err)
	require.NoError(t, unlockOther())

	require.NoError(t, unlockA())

	// Now that org-1 is released, replica B can acquire it.
	unlockB, err := replicaB.Lock(ctx, "org-1")
	require.NoError(t, err)
	require.NoError(t, unlockB())
}

func TestPostgresOrgLocker_rejectsNilDatabase(t *testing.T) {
	_, err := projectionrun.NewPostgresOrgLocker(nil)
	require.Error(t, err)
}
