package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Boundary payloads are valid stored fixtures, not claims that an Engine
// produced 100 independent degradations. Save/Get, semantic encoding and
// decoding, HTTP serving and accepted Engine reuse execute production code.
func TestStoredWorkItemPGBoundaries(t *testing.T) {
	db := storedPGBoundaryDatabase(t)
	principal := storage.Principal{OrgID: callerOrgID, RepositoryScopes: []string{hostedTestRepository}}
	bound := contractsv1.ContextFabricCoverageEntriesMaxCount
	for _, tc := range []struct {
		name        string
		independent int
		existing    bool
		want        int
	}{
		{"99_independent_add_D47", bound - 1, false, http.StatusOK},
		{"100_independent_overflow", bound, false, http.StatusInternalServerError},
		{"100_including_D47_replace", bound - 1, true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := storedWorkItemTupleRouteFixture(t, principal)
			stored.Result.ResultID = "result_pg_boundary_" + tc.name
			storedPGBoundaryCoverage(&stored.Result, tc.independent, tc.existing)
			stored.SemanticState = storedPGBoundaryCanonicalState(stored.SemanticState.WorkItemCensus)
			store := storedPGBoundarySave(t, db, principal, stored)
			stored, err := store.Get(context.Background(), principal, stored.Result.ResultID)
			if err != nil {
				t.Fatal(err)
			}
			native := storedPGBoundaryNative(t, db, stored.Result.ResultID)
			carrier := portableStoredCarrierBytes(t, stored)
			decision := contextfabric.ServeStoredWorkItemTuple(stored.Result, stored.SemanticState, stored.SemanticStateRead, principal)
			if tc.want == http.StatusInternalServerError {
				if !errors.Is(decision.Err, contextfabric.ErrInvalidResult) {
					t.Errorf("serving error=%v, want ErrInvalidResult", decision.Err)
				}
			} else {
				if decision.Err != nil {
					t.Errorf("serving error=%v", decision.Err)
				}
				again := contextfabric.ServeStoredWorkItemTuple(decision.Result, stored.SemanticState, stored.SemanticStateRead, principal)
				if again.Err != nil || !reflect.DeepEqual(again.Result, decision.Result) {
					t.Error("serving transformation is not idempotent")
				}
			}
			app, token, logs := newContextFabricTestAppWithResultsAndLogs(t, nil, store)
			app.logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
			var first []byte
			for attempt := 0; attempt < 2; attempt++ {
				rec := httptest.NewRecorder()
				app.InstrumentedHandler(app.Handler()).ServeHTTP(rec, investigationResultRequest(t, token, stored.Result.ResultID))
				if rec.Code != tc.want {
					t.Errorf("HTTP=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
				}
				if tc.want == http.StatusInternalServerError {
					storedPGBoundarySafeError(t, rec.Code, rec.Body.Bytes(), stored.Result)
					continue
				}
				var result contextfabric.InvestigationResult
				if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if err := result.Validate(); err != nil {
					t.Errorf("served result invalid: %v", err)
				}
				if len(result.Coverage.Details) != bound || len(result.Coverage.DegradedReasons) != bound {
					t.Errorf("coverage pairs=%d/%d want=%d", len(result.Coverage.Details), len(result.Coverage.DegradedReasons), bound)
				}
				for _, detail := range stored.Result.Coverage.Details {
					if detail.Kind == contractsv1.ContextFabricSubjectWorkItem {
						continue
					}
					found := false
					for _, served := range result.Coverage.Details {
						if reflect.DeepEqual(detail, served) {
							found = true
						}
					}
					if !found {
						t.Errorf("independent degradation lost: %s", detail.DetailID)
					}
					if !slices.Contains(result.Coverage.DegradedReasons, detail.Raw) {
						t.Errorf("independent reason lost: %s", detail.Raw)
					}
				}
				d47 := 0
				for _, detail := range result.Coverage.Details {
					if detail.Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated && detail.Kind == contractsv1.ContextFabricSubjectWorkItem {
						d47++
						if detail.Declared == nil || *detail.Declared != contextfabric.WorkItemMembershipCensusLimit || detail.Served == nil || *detail.Served != 1 {
							t.Errorf("D47=%+v", detail)
						}
					}
				}
				if d47 != 1 {
					t.Errorf("D47 count=%d want=1", d47)
				}
				encoded, _ := json.Marshal(result)
				if attempt == 0 {
					first = encoded
				} else if !bytes.Equal(first, encoded) {
					t.Error("repeated HTTP serving changed result bytes")
				}
			}
			if tc.want == http.StatusInternalServerError {
				storedPGBoundaryInfo(t, logs.Bytes(), "result_by_id")
			}
			assertPortableStoredCarrierUnchanged(t, "PG decoded carrier", carrier, stored)
			if !bytes.Equal(native, storedPGBoundaryNative(t, db, stored.Result.ResultID)) {
				t.Error("HTTP serving changed native PG payload or semantic bytes")
			}
			t.Logf("actual PG Save/Get: independent=%d existing_D47=%t HTTP=%d; native bytes immutable", tc.independent, tc.existing, tc.want)
		})
	}
	t.Run("accepted_reuse_overflow_releases_owner", func(t *testing.T) {
		result, state := lifetimeTupleFixture(t, principal)
		result.ResultID = "result_pg_boundary_reuse_overflow"
		state.WorkItemCensus = &contextfabric.WorkItemTupleCensus{Version: contextfabric.WorkItemTupleCensusVersion, State: contextfabric.WorkItemMembershipCensusFloor, Value: contextfabric.WorkItemMembershipCensusLimit, Retained: 1, RequestedRepositoryScope: []string{}, AuthorizationDigest: state.WorkItemCensus.AuthorizationDigest}
		storedPGBoundaryCoverage(&result, bound, false)
		state = storedPGBoundaryCanonicalState(state.WorkItemCensus)
		pg := storedPGBoundarySave(t, db, principal, contextfabric.StoredInvestigationResult{Result: result, SemanticState: state})
		persisted, err := pg.Get(context.Background(), principal, result.ResultID)
		if err != nil {
			t.Fatal(err)
		}
		native := storedPGBoundaryNative(t, db, result.ResultID)
		carrier := portableStoredCarrierBytes(t, persisted)
		lookup := &storedPGBoundaryLookup{stored: persisted}
		results := &storedPGBoundaryStore{InvestigationResultStore: pg}
		segments, ok := identity.Segments(identity.KindWorkItem, result.Cohort.Members[0].Subject.CanonicalID)
		if !ok || len(segments) != 2 {
			t.Fatal("invalid member fixture")
		}
		client := &lifetimeTupleClient{id: result.Cohort.Members[0].Subject.CanonicalID, repo: segments[0], work: segments[1], count: contextfabric.WorkItemMembershipCensusLimit + 1}
		gate := newResponseOwnerAPITestGate(t)
		reader, err := devhealthfacts.NewWorkItemMembershipReader(client, devhealthfacts.WorkItemMembershipReaderOptions{Gate: gate, Telemetry: contextfabric.NoopWorkItemMembershipTelemetry{}})
		if err != nil {
			t.Fatal(err)
		}
		graph := &lifetimeTupleGraph{}
		fresh := &storedPGBoundaryFresh{}
		anchors, interpretations := 0, 0
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
		engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
			Interpreter: lifetimeTupleInterpreter(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) error {
				interpretations++
				return errors.New("unexpected fresh interpretation")
			}),
			Graph: graph, Facts: fresh, Synthesizer: fresh, Results: results, ReuseGate: lookup, Telemetry: contextfabric.NewSlogEngineTelemetry(logger),
			CandidateVerifier: func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (bool, contextfabric.CandidateVerificationReason) {
				anchors++
				return true, contextfabric.CandidateVerificationValid
			},
			WorkItemMembership: reader,
		}, contextfabric.EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_pg_boundary_unexpected_fresh" }})
		if err != nil {
			t.Fatal(err)
		}
		var engineErr error
		observer := investigatorFunc(func(ctx context.Context, p storage.Principal, r contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
			v, err := engine.Investigate(ctx, p, r)
			if held := gate.Stats().InFlight; held != 1 {
				t.Errorf("accepted reuse error released owner before HTTP response: in_flight=%d", held)
			}
			engineErr = err
			return v, err
		})
		app, token := newContextFabricTestAppWithResults(t, observer, results)
		app.config.RequestTimeout = 5 * time.Second
		rec := httptest.NewRecorder()
		app.InstrumentedHandler(app.Handler()).ServeHTTP(rec, investigationRequest(t, token))
		storedPGBoundarySafeError(t, rec.Code, rec.Body.Bytes(), result)
		if !errors.Is(engineErr, contextfabric.ErrInvalidResult) {
			t.Errorf("Engine error=%v want ErrInvalidResult", engineErr)
		}
		if lookup.calls != 1 || anchors != 1 || client.calls != 1 {
			t.Errorf("accepted reuse lookup/anchor/S1=%d/%d/%d want 1/1/1", lookup.calls, anchors, client.calls)
		}
		if interpretations != 0 || graph.calls != 0 || fresh.facts != 0 || fresh.synthesis != 0 || results.saves != 0 || results.reads != 0 {
			t.Errorf("unexpected fresh interpretation/graph/facts/synthesis/save/read=%d/%d/%d/%d/%d/%d", interpretations, graph.calls, fresh.facts, fresh.synthesis, results.saves, results.reads)
		}
		assertResponseOwnerGateFree(t, gate)
		storedPGBoundaryInfo(t, logs.Bytes(), "reuse")
		// Acceptance followed by a serving error is neither a served hit nor
		// a miss that may start fresh work. Only the failure decision applies.
		for _, line := range bytes.Split(logs.Bytes(), []byte("\n")) {
			var event map[string]any
			if json.Unmarshal(line, &event) == nil && event["msg"] == "context fabric answer reuse outcome" {
				t.Errorf("failed accepted reuse emitted a generic reuse outcome: %s", line)
			}
		}
		assertPortableStoredCarrierUnchanged(t, "accepted PG reuse carrier", carrier, lookup.stored)
		if !bytes.Equal(native, storedPGBoundaryNative(t, db, result.ResultID)) {
			t.Error("failed accepted reuse changed native PG bytes")
		}
		t.Log("actual PG Save/Get and Engine acceptance; lookup, anchor and S1 query rows controlled; HTTP 500, no fresh work, owner released")
	})
}

func storedPGBoundaryCoverage(result *contextfabric.InvestigationResult, count int, existing bool) {
	result.Coverage.Details = nil
	result.Coverage.DegradedReasons = nil
	for i := 0; i < count; i++ {
		d := storedWorkItemTupleRouteCensusDetail(2000+i, 1)
		d.Kind = contractsv1.ContextFabricSubjectRepository
		d.DetailID = fmt.Sprintf("cov-%03d", i)
		d.Raw = fmt.Sprintf("kind_census_truncated:repository:%d:1", 2000+i)
		d.Label = contractsv1.ComposeCoverageDetailLabel(d)
		result.Coverage.Details = append(result.Coverage.Details, d)
		result.Coverage.DegradedReasons = append(result.Coverage.DegradedReasons, d.Raw)
	}
	if existing {
		d := storedWorkItemTupleRouteCensusDetail(2001, 1)
		d.DetailID = "cov-existing-work-item"
		result.Coverage.Details = append(result.Coverage.Details, d)
		result.Coverage.DegradedReasons = append(result.Coverage.DegradedReasons, d.Raw)
	}
}

func storedPGBoundaryNative(t *testing.T, db *sql.DB, id string) []byte {
	t.Helper()
	var payload, semantic []byte
	if err := db.QueryRowContext(context.Background(), "SELECT payload, semantic_state FROM acr.context_fabric_investigation_results WHERE result_id = $1", id).Scan(&payload, &semantic); err != nil {
		t.Fatal(err)
	}
	return append(append(payload, '\n'), semantic...)
}

func storedPGBoundarySafeError(t *testing.T, status int, body []byte, result contextfabric.InvestigationResult) {
	t.Helper()
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if status != http.StatusInternalServerError || envelope.Error.Code != "internal_error" || envelope.Error.Retryable || envelope.RequestID == "" {
		t.Errorf("unsafe failure: HTTP=%d body=%s", status, body)
	}
	assertStoredServingContentAbsent(t, body, result)
	for _, forbidden := range []string{"stored work item coverage invalid", "max_count", "kind_census_truncated"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Errorf("error leaked %q", forbidden)
		}
	}
}

func storedPGBoundaryInfo(t *testing.T, logs []byte, surface string) {
	t.Helper()
	for _, line := range bytes.Split(logs, []byte("\n")) {
		var event map[string]any
		if json.Unmarshal(line, &event) != nil || event["msg"] != contextfabric.WorkItemStoredServingLogMessage || event["basis"] != "coverage_invalid" {
			continue
		}
		if event["level"] != "INFO" || event["surface"] != surface || event["coverage_details_before"] != float64(contractsv1.ContextFabricCoverageEntriesMaxCount) || event["coverage_reasons_before"] != float64(contractsv1.ContextFabricCoverageEntriesMaxCount) || event["coverage_details_after"] != float64(contractsv1.ContextFabricCoverageEntriesMaxCount+1) || event["coverage_reasons_after"] != float64(contractsv1.ContextFabricCoverageEntriesMaxCount+1) || event["coverage_bound"] != float64(contractsv1.ContextFabricCoverageEntriesMaxCount) {
			t.Errorf("coverage decision=%s", line)
		}
		t.Logf("configured Info sink: %s", line)
		return
	}
	t.Errorf("missing Info coverage_invalid event for %s", surface)
}

type storedPGBoundaryLookup struct {
	stored contextfabric.StoredInvestigationResult
	calls  int
}

func (g *storedPGBoundaryLookup) FindReusable(context.Context, storage.Principal, contextfabric.ReuseKey) (contextfabric.StoredInvestigationResult, bool, contextfabric.ReuseMissReason, error) {
	g.calls++
	return g.stored, true, "", nil
}

type storedPGBoundaryFresh struct{ facts, synthesis int }

func (f *storedPGBoundaryFresh) ReadFacts(context.Context, storage.Principal, contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	f.facts++
	return contextfabric.CanonicalFactBundle{}, errors.New("unexpected fresh facts")
}
func (f *storedPGBoundaryFresh) Synthesize(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
	f.synthesis++
	return contextfabric.InvestigationResult{}, errors.New("unexpected fresh synthesis")
}

type storedPGBoundaryStore struct {
	contextfabric.InvestigationResultStore
	saves, reads int
}

func (s *storedPGBoundaryStore) Get(ctx context.Context, p storage.Principal, id string) (contextfabric.StoredInvestigationResult, error) {
	s.reads++
	return s.InvestigationResultStore.Get(ctx, p, id)
}
func (s *storedPGBoundaryStore) Save(context.Context, storage.Principal, contextfabric.InvestigationResult, contextfabric.SourceWatermarkSnapshot, contextfabric.RebuildEpoch, string, contextfabric.ReuseRetrievalIdentity, contextfabric.ReusePromptVersions, contextfabric.ReuseVersionAuthorities, int64, string, contextfabric.SemanticStateWrite) error {
	s.saves++
	return errors.New("unexpected save")
}

func storedPGBoundaryCanonicalState(census *contextfabric.WorkItemTupleCensus) *contextfabric.PersistedSemanticState {
	validation := contextfabric.ValidateFrame(contextfabric.QuestionFrame{Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState}, SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Project Alpha"}, MemberKind: contextfabric.SubjectWorkItem}}, Temporal: contextfabric.TemporalIntentCurrent}, []contextfabric.AnswerObligation{contextfabric.ObligationState}, contextfabric.ShapeDiscoveredCohort)
	return contextfabric.BuildSemanticState(contextfabric.SemanticStateInput{Outcome: contextfabric.QuestionFamilyOutcome{Family: contextfabric.QuestionFamilyScopedCohortStatus, Source: contextfabric.QuestionFamilySourceModel, Frame: &validation.Frame, Gate: contextfabric.DecideFrameGate(validation, true), WinningSampleIndex: 0, WinningSample: contextfabric.FamilySample{ModelFamily: contextfabric.QuestionFamilyScopedCohortStatus, ScopeAnchorKind: contextfabric.SubjectProject, ScopeAnchorTerm: "Project Alpha"}}, EmittedShape: contextfabric.ShapeDiscoveredCohort, FamilyVersion: contextfabric.QuestionFamilyTableVersion, WorkItemCensus: census})
}
func storedPGBoundarySave(t *testing.T, db *sql.DB, p storage.Principal, s contextfabric.StoredInvestigationResult) *pginvestigation.Store {
	t.Helper()
	store, err := pginvestigation.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Result.Validate(); err != nil {
		t.Fatalf("before Save: %v", err)
	}
	if err = store.Save(context.Background(), p, s.Result, nil, nil, contextfabric.TimeAxisKeyFor(s.Result.Interpretation.TimeContext), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "", contextfabric.SemanticStateOf(s.SemanticState)); err != nil {
		t.Fatal(err)
	}
	read, err := store.Get(context.Background(), p, s.Result.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	if read.SemanticStateRead != contextfabric.SemanticStateReadAvailable {
		t.Fatalf("semantic status=%s", read.SemanticStateRead)
	}
	if err = read.Result.Validate(); err != nil {
		t.Fatalf("PG read before serving invalid: %v", err)
	}
	return store
}
func storedPGBoundaryDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44", tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Error(err)
		}
	})
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	runner, err := migrations.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Apply(ctx, db); err != nil {
		t.Fatal(fmt.Errorf("apply production migrations: %w", err))
	}
	return db
}
