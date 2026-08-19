package pginvestigation_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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
			ProjectionVersion: "v1", QueryVersion: "v1", InterpretationVersion: "v1", SynthesisVersion: "v1", CanonicalServiceVersion: "v1", ModelIdentity: "test/model-v1",
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

	saveErr := store.Save(cancelled, storage.Principal{OrgID: "org-1"}, validResult("result-cancelled-save"), nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0)
	require.Error(t, saveErr)
	require.True(t, errors.Is(saveErr, context.Canceled), "save error should be context.Canceled, got %v", saveErr)
	require.False(t, errors.Is(saveErr, contextfabric.ErrUnavailable), "a canceled context is not a bounded dependency failure")

	// Seed a row (with a live context) so Get has something to reach for.
	require.NoError(t, store.Save(ctx, storage.Principal{OrgID: "org-1"}, validResult("result-cancelled-get"), nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0))

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
	require.NoError(t, store.Save(ctx, storage.Principal{OrgID: "org-1"}, validResult("result-deadline-seed"), nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0))

	expired, cancel := context.WithTimeout(ctx, time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	saveErr := store.Save(expired, storage.Principal{OrgID: "org-1"}, validResult("result-deadline-save"), nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0)
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
	require.NoError(t, store.Save(ctx, storage.Principal{OrgID: "org-1"}, validResult("result-non-enumerating"), nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0))

	_, wrongOrgErr := store.Get(ctx, storage.Principal{OrgID: "org-2"}, "result-non-enumerating")
	_, unknownIDErr := store.Get(ctx, storage.Principal{OrgID: "org-2"}, "result-does-not-exist")

	require.ErrorIs(t, wrongOrgErr, pginvestigation.ErrNotFound)
	require.ErrorIs(t, unknownIDErr, pginvestigation.ErrNotFound)
	require.Equal(t, unknownIDErr.Error(), wrongOrgErr.Error(), "wrong-org and truly-missing must produce the identical error")
}

// TestStore_getRejectsStoredResultThatFailsValidation is the M2 Get-guard
// probe (Codex adversarial + delta review, CHAOS-3755) for the production
// store specifically -- memoryinvestigation has its own white-box version
// (store_internal_test.go), but nothing exercised this store's copy of the
// same guard. Save validates on write, so the only way to reach Get's own
// validation is to write a row directly, bypassing Save -- exactly the
// state the guard exists for (a row that reached storage some other way).
func TestStore_getRejectsStoredResultThatFailsValidation(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	store, err := pginvestigation.NewStore(db)
	require.NoError(t, err)

	// Syntactically valid JSON, semantically invalid InvestigationResult:
	// missing schema_version, status, interpretation, coverage, and every
	// required collection. json.Unmarshal cannot catch this -- only
	// InvestigationResult.Validate() can.
	_, err = db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_investigation_results (result_id, org_id, payload, generated_at)
VALUES ($1, $2, $3, $4)`, "result-corrupt-pg", "org-1", []byte(`{"result_id":"result-corrupt-pg","question":"what happened?"}`), time.Now().UTC())
	require.NoError(t, err)

	_, getErr := store.Get(ctx, storage.Principal{OrgID: "org-1"}, "result-corrupt-pg")
	require.Error(t, getErr)
	require.Contains(t, getErr.Error(), "stored investigation result is invalid")
}

// TestStore_getRejectsStoredResultWithExplicitNullDegradedReasons is the P2
// fix (Codex delta review, CHAOS-3755): coverage.degraded_reasons is
// omitempty in Go and optional (not `null`) in the JSON Schema -- an
// explicit `null` collapses to the same Go nil slice an omitted field
// would decode to, so it must be caught on the raw stored bytes, not after
// json.Unmarshal has already erased the distinction. The row is planted
// with a direct INSERT (bypassing Save, which never produces explicit
// null through its own marshal path) to reach the state this guards
// against: a row written by some other path.
func TestStore_getRejectsStoredResultWithExplicitNullDegradedReasons(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	store, err := pginvestigation.NewStore(db)
	require.NoError(t, err)

	valid := validResult("result-explicit-null")
	encoded, err := json.Marshal(valid)
	require.NoError(t, err)
	tainted := bytes.Replace(encoded, []byte(`"sources":[]`), []byte(`"sources":[],"degraded_reasons":null`), 1)
	require.NotEqual(t, string(encoded), string(tainted), "test setup: expected substring not found in fixture JSON")

	_, err = db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_investigation_results (result_id, org_id, payload, generated_at)
VALUES ($1, $2, $3, $4)`, valid.ResultID, "org-1", tainted, valid.GeneratedAt)
	require.NoError(t, err)

	_, getErr := store.Get(ctx, storage.Principal{OrgID: "org-1"}, valid.ResultID)
	require.Error(t, getErr)
	require.Contains(t, getErr.Error(), "degraded_reasons")
	require.Contains(t, getErr.Error(), "null")
}

// TestStore_explicitNullDegradedReasonsParity runs the SHARED explicit-null
// table against Postgres. pginvestigation and memoryinvestigation each
// carry their own copy of the raw-bytes check; this table is what stops
// the two from drifting apart.
func TestStore_explicitNullDegradedReasonsParity(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)

	paritytest.RunExplicitNullDegradedReasonsSuite(t, func(t *testing.T) (contextfabric.InvestigationResultStore, paritytest.RawSeed) {
		store, err := pginvestigation.NewStore(db)
		require.NoError(t, err)
		return store, func(t *testing.T, orgID, resultID string, payload []byte) {
			t.Helper()
			_, execErr := db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_investigation_results (result_id, org_id, payload, generated_at)
VALUES ($1, $2, $3, $4)`, resultID, orgID, payload, time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC))
			require.NoError(t, execErr)
		}
	})
}
