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

// validResult builds a minimal but FULLY VALID InvestigationResult (see
// paritytest.result -- this file needs its own copy since these tests
// exercise context/error-propagation behavior, not save/get parity, and
// live outside the shared table). Save now validates on write (CHAOS-3755
// finding M2), so every fixture in this file must satisfy
// InvestigationResult.Validate() or these tests would fail at the
// validation step before ever reaching the context/timeout behavior they
// actually test.
func validResult(resultID string) contextfabric.InvestigationResult {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project-" + resultID, Label: "Project " + resultID}
	return contextfabric.InvestigationResult{
		SchemaVersion: contextfabric.InvestigationResultSchemaV1,
		ResultID:      resultID,
		RequestID:     "request-" + resultID,
		GeneratedAt:   time.Now().UTC(),
		Status:        contextfabric.InvestigationComplete,
		Question:      "question for " + resultID,
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status",
			TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
		},
		SubjectResolution:   contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{project}},
		DirectJudgment:      "judgment for " + resultID,
		DeterministicAnswer: "deterministic answer for " + resultID,
		StrongestPressures:  []string{},
		Drivers:             []contextfabric.DriverJudgment{},
		RemainingWork:       []contextfabric.Finding{},
		ReadinessGaps:       []contextfabric.Finding{},
		Paths:               []contextfabric.RelationshipPath{},
		Conflicts:           []contextfabric.Finding{},
		Limitations:         []string{},
		EvidenceRefIDs:      []string{},
		ClaimedFacts:        []contextfabric.ClaimedFact{},
		Coverage:            contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}},
		Versions: contextfabric.VersionSet{
			ServiceVersion: "test", ContractVersion: contextfabric.InvestigationResultSchemaV1, Backend: "test",
			ProjectionVersion: "v1", QueryVersion: "v1", InterpretationVersion: "v1", SynthesisVersion: "v1", CanonicalServiceVersion: "v1",
		},
		Warnings: []string{},
	}
}

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

	saveErr := store.Save(cancelled, storage.Principal{OrgID: "org-1"}, validResult("result-cancelled-save"))
	require.Error(t, saveErr)
	require.True(t, errors.Is(saveErr, context.Canceled), "save error should be context.Canceled, got %v", saveErr)
	require.False(t, errors.Is(saveErr, contextfabric.ErrUnavailable), "a canceled context is not a bounded dependency failure")

	// Seed a row (with a live context) so Get has something to reach for.
	require.NoError(t, store.Save(ctx, storage.Principal{OrgID: "org-1"}, validResult("result-cancelled-get")))

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
	require.NoError(t, store.Save(ctx, storage.Principal{OrgID: "org-1"}, validResult("result-deadline-seed")))

	expired, cancel := context.WithTimeout(ctx, time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	saveErr := store.Save(expired, storage.Principal{OrgID: "org-1"}, validResult("result-deadline-save"))
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
	require.NoError(t, store.Save(ctx, storage.Principal{OrgID: "org-1"}, validResult("result-non-enumerating")))

	_, wrongOrgErr := store.Get(ctx, storage.Principal{OrgID: "org-2"}, "result-non-enumerating")
	_, unknownIDErr := store.Get(ctx, storage.Principal{OrgID: "org-2"}, "result-does-not-exist")

	require.ErrorIs(t, wrongOrgErr, pginvestigation.ErrNotFound)
	require.ErrorIs(t, unknownIDErr, pginvestigation.ErrNotFound)
	require.Equal(t, unknownIDErr.Error(), wrongOrgErr.Error(), "wrong-org and truly-missing must produce the identical error")
}
