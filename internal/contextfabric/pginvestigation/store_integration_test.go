package pginvestigation_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation/paritytest"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

func newInvestigationTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runner, err := migrations.Embedded()
	require.NoError(t, err)
	_, err = runner.Apply(ctx, db)
	require.NoError(t, err)
	return db
}

// TestStore_parity runs the shared contextfabric.InvestigationResultStore
// behavior table (save/get roundtrip, org scoping, immutability) against
// Postgres. memoryinvestigation's store_test.go runs the exact same table
// against the in-memory store, so the two implementations cannot silently
// drift apart. All cases share one container/database: every case uses a
// distinct result_id, so a fresh *pginvestigation.Store wrapping the same
// *sql.DB is an independent scope per case without needing per-case
// containers or truncation.
func TestStore_parity(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)

	paritytest.RunSuite(t,
		func(t *testing.T) contextfabric.InvestigationResultStore {
			store, err := pginvestigation.NewStore(db)
			require.NoError(t, err)
			return store
		},
		func(err error) bool { return errors.Is(err, pginvestigation.ErrNotFound) },
	)
}

func TestStore_saveAndGetReturnContextCanceledWithoutWrappingAsUnavailable(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	store, err := pginvestigation.NewStore(db)
	require.NoError(t, err)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	saveErr := store.Save(cancelled, storage.Principal{OrgID: "org-1"}, contextfabric.InvestigationResult{
		ResultID: "result-cancelled-save", GeneratedAt: time.Now().UTC(),
	})
	require.Error(t, saveErr)
	require.True(t, errors.Is(saveErr, context.Canceled), "save error should be context.Canceled, got %v", saveErr)
	require.False(t, errors.Is(saveErr, contextfabric.ErrUnavailable), "a canceled context is not a bounded dependency failure")

	// Seed a row (with a live context) so Get has something to reach for.
	require.NoError(t, store.Save(ctx, storage.Principal{OrgID: "org-1"}, contextfabric.InvestigationResult{
		ResultID: "result-cancelled-get", GeneratedAt: time.Now().UTC(),
	}))

	_, getErr := store.Get(cancelled, storage.Principal{OrgID: "org-1"}, "result-cancelled-get")
	require.Error(t, getErr)
	require.True(t, errors.Is(getErr, context.Canceled), "get error should be context.Canceled, got %v", getErr)
	require.False(t, errors.Is(getErr, contextfabric.ErrUnavailable))
}

func TestStore_saveAndGetReturnUnavailableOnDeadlineExceeded(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	store, err := pginvestigation.NewStore(db)
	require.NoError(t, err)
	require.NoError(t, store.Save(ctx, storage.Principal{OrgID: "org-1"}, contextfabric.InvestigationResult{
		ResultID: "result-deadline-seed", GeneratedAt: time.Now().UTC(),
	}))

	expired, cancel := context.WithTimeout(ctx, time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	saveErr := store.Save(expired, storage.Principal{OrgID: "org-1"}, contextfabric.InvestigationResult{
		ResultID: "result-deadline-save", GeneratedAt: time.Now().UTC(),
	})
	require.Error(t, saveErr)
	require.True(t, errors.Is(saveErr, context.DeadlineExceeded), "save error should be context.DeadlineExceeded, got %v", saveErr)

	_, getErr := store.Get(expired, storage.Principal{OrgID: "org-1"}, "result-deadline-seed")
	require.Error(t, getErr)
	require.True(t, errors.Is(getErr, context.DeadlineExceeded), "get error should be context.DeadlineExceeded, got %v", getErr)
}

func TestStore_getUnknownResultIDIsIndistinguishableFromWrongOrg(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	store, err := pginvestigation.NewStore(db)
	require.NoError(t, err)
	require.NoError(t, store.Save(ctx, storage.Principal{OrgID: "org-1"}, contextfabric.InvestigationResult{
		ResultID: "result-non-enumerating", GeneratedAt: time.Now().UTC(),
	}))

	_, wrongOrgErr := store.Get(ctx, storage.Principal{OrgID: "org-2"}, "result-non-enumerating")
	_, unknownIDErr := store.Get(ctx, storage.Principal{OrgID: "org-2"}, "result-does-not-exist")

	require.ErrorIs(t, wrongOrgErr, pginvestigation.ErrNotFound)
	require.ErrorIs(t, unknownIDErr, pginvestigation.ErrNotFound)
	require.Equal(t, unknownIDErr.Error(), wrongOrgErr.Error(), "wrong-org and truly-missing must produce the identical error")
}
