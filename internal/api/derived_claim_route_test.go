package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// derivationModelRuntime is a model runtime with no model: it interprets as
// one fixed question and answers every synthesis with a draft built from the
// fact ids of the input it was handed, counting the calls it receives.
type derivationModelRuntime struct {
	syntheses *int
	value     int64
}

func successReceipt(operation contextfabric.ModelOperation) contextfabric.ModelExecutionReceipt {
	receipt := validRouteTestModelReceipt(operation)
	receipt.Outcome = "success"
	receipt.OutputDigest = contextfabric.DigestModelValue([]byte("route-test-output"))
	return receipt
}

func (r derivationModelRuntime) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "release readiness", SubjectTerms: []string{"Ask Dev"},
		TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactReadiness}},
	}, successReceipt(contextfabric.ModelOperationInterpret), nil
}

func (r derivationModelRuntime) SynthesizeAnswer(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	*r.syntheses++
	var open, closed string
	for _, fact := range input.Facts.Facts {
		if _, ok := fact.Fields["open_items"]; ok {
			open = fact.ID
		}
		if _, ok := fact.Fields["closed_items"]; ok {
			closed = fact.ID
		}
	}
	draft := validRouteTestSynthesisDraft()
	draft.EvidenceRefIDs = []string{"evidence_readiness_0001", "evidence_readiness_0002"}
	draft.Drivers[0].EvidenceRefIDs = []string{"evidence_readiness_0001"}
	value := r.value
	draft.ClaimedFacts = []contextfabric.ClaimedFact{{
		ClaimID: "claim_total_items", Kind: contextfabric.FactReadiness, Subject: suppliedRouteProject(), Field: "total_items",
		Value:       contextfabric.ScalarValue{Integer: &value},
		DerivedFrom: []contractsv1.ContextFabricDerivationInput{{FactID: open, Field: "open_items"}, {FactID: closed, Field: "closed_items"}},
		Operation:   contractsv1.ContextFabricDerivationSum,
	}}
	return draft, successReceipt(contextfabric.ModelOperationSynthesize), nil
}

// derivationGraph is liveGraphReader with a context that carries no
// relationship path: the answer under test rests on facts alone.
type derivationGraph struct{ authorizingGraph }

func (g derivationGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	return contextfabric.GraphContext{
		Resolution:       contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{g.project}},
		Paths:            []contextfabric.RelationshipPath{},
		DriverCandidates: []contextfabric.DriverJudgment{},
		EvidenceRefIDs:   []string{},
		FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{g.project}}},
		Coverage:         contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
	}, nil
}

func derivationRoute(t *testing.T, value int64) (*httptest.ResponseRecorder, int) {
	t.Helper()
	project := suppliedRouteProject()
	bundle := liveCanonicalFacts(project)
	bundle.Facts = []contextfabric.CanonicalFact{
		{Kind: contextfabric.FactReadiness, Subject: project, Fields: map[string]contextfabric.FactValue{"open_items": contextfabric.IntegerFactValue(7)}, EvidenceRefIDs: []string{"evidence_readiness_0001"}, SourceState: contextfabric.SourceAvailable, Source: "ops", SourceVersion: "v1"},
		{Kind: contextfabric.FactReadiness, Subject: project, Fields: map[string]contextfabric.FactValue{"closed_items": contextfabric.IntegerFactValue(3)}, EvidenceRefIDs: []string{"evidence_readiness_0002"}, SourceState: contextfabric.SourceAvailable, Source: "ops", SourceVersion: "v1"},
	}
	contextfabric.AssignFactIDs(bundle.Facts)
	syntheses := 0
	runtime := derivationModelRuntime{syntheses: &syntheses, value: value}
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: runtime},
		Graph:       derivationGraph{authorizingGraph{liveGraphReader: liveGraphReader{project: project}, outcome: contextfabric.StoredSubjectAdmitted}},
		Facts:       liveFactReader{bundle: bundle},
		Synthesizer: contextfabric.RuntimeAnswerSynthesizer{Runtime: runtime, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "derived-claim-route", Backend: "graph"}},
		Results:     memoryinvestigation.NewStore(),
	}, contextfabric.EngineOptions{
		ServiceVersion: "derived-claim-route",
		Now:            func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_derived_claim01" },
	})
	if err != nil {
		t.Fatal(err)
	}
	app, token := newLiveContextFabricTestApp(t, engine)
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, windowedInvestigationHTTPRequest(t, token, nil))
	return recorder, syntheses
}

// A draft with one grounded derivation over two served facts is served
// through the real route and engine with one synthesis call, and the served
// claim carries its inputs and its operation.
func TestARouteServesAGroundedDerivedClaimWithItsInputsAndOperation(t *testing.T) {
	recorder, syntheses := derivationRoute(t, 10)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if syntheses != 1 {
		t.Fatalf("synthesis calls = %d, want 1", syntheses)
	}
	var result contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("served result fails its own contract: %v", err)
	}
	var derived *contractsv1.ContextFabricClaimedFact
	for index := range result.ClaimedFacts {
		if result.ClaimedFacts[index].ClaimID == "claim_total_items" {
			derived = &result.ClaimedFacts[index]
		}
	}
	if derived == nil {
		t.Fatalf("claims = %#v, want the derived claim served", result.ClaimedFacts)
	}
	if derived.Operation != contractsv1.ContextFabricDerivationSum || len(derived.DerivedFrom) != 2 ||
		derived.DerivedFrom[0].Field != "open_items" || derived.DerivedFrom[1].Field != "closed_items" ||
		derived.Value.Integer == nil || *derived.Value.Integer != 10 || derived.FactID != "" || len(derived.Rows) != 0 {
		t.Fatalf("served claim = %#v, want sum of open_items and closed_items = 10 with no fact_id and no rows", *derived)
	}
	if !strings.Contains(recorder.Body.String(), `"derived_from"`) || !strings.Contains(recorder.Body.String(), `"operation":"sum"`) {
		t.Fatalf("wire body does not carry derived_from and operation: %s", recorder.Body.String())
	}
}

// The same draft with a value the inputs do not reproduce is rejected: the
// route serves the degraded answer, with no derived claim in it.
func TestARouteDoesNotServeADerivedClaimWhoseValueTheInputsDoNotReproduce(t *testing.T) {
	recorder, syntheses := derivationRoute(t, 11)
	if syntheses != 1 {
		t.Fatalf("synthesis calls = %d, want 1", syntheses)
	}
	var result contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("status %d: %v", recorder.Code, err)
	}
	if result.Status != contractsv1.ContextFabricInvestigationDegraded {
		t.Fatalf("status = %q, want degraded (the rejected draft is not served)", result.Status)
	}
	// The degraded answer still restates served facts under server-minted
	// claim ids; none of them is the rejected derivation.
	for _, claim := range result.ClaimedFacts {
		if claim.ClaimID == "claim_total_items" || len(claim.DerivedFrom) != 0 || claim.Operation != "" || (claim.Value.Integer != nil && *claim.Value.Integer == 11) {
			t.Fatalf("a derivation the inputs do not reproduce was served: %#v", claim)
		}
	}
}
