package contextfabric

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4234: the class-default window gate (regime A) used to return a
// window-ONLY disclosure because ResolveSubjects never ran under it
// (CHAOS-4118 option (a)). The two kiac replicates of record showed
// 45/50 and 43/48 positive-arm expected_kind offer misses were regime-A
// rows with no candidate pool at all. The ruling (team-lead, 2026-08-24):
// run ResolveSubjects in an OFFERS-ONLY mode under the gate, keep its
// StructureOfferMaterial, discard every commit-bearing output, and
// compose kind/handle/candidate offers BESIDE the window offer. Offers
// minted under an inferred window are non-decisive disclosure, the same
// class as the window offer itself -- the CHAOS-4040 bar (no committed
// material under an inferred window) is untouched.

func chaos4234GatedMaterial() StructureOfferMaterial {
	return StructureOfferMaterial{
		Missing: []StructureNeedKind{
			contractsv1.ContextFabricStructureNeedExpectedKind,
			contractsv1.ContextFabricStructureNeedSubjectHandle,
		},
		KindOptions: []KindOption{
			{Kind: SubjectPullRequest, Label: "a pull request", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
			{Kind: SubjectWorkItem, Label: "a work item", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
		},
		HandleOptions: []HandleOption{
			{Kind: SubjectPullRequest, PatternID: "pull_request_number", Value: "532", SourceColumn: "number", Label: "pull request #532", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
		},
	}
}

// chaos4234GatedGraph returns a fake whose ResolveSubjects would COMMIT a
// subject if its output were honoured -- so a gated result that carries
// no committed material proves the engine discarded the resolution,
// rather than proving the fake had nothing to commit.
func chaos4234GatedGraph() *acceptanceGraphReader {
	return &acceptanceGraphReader{
		resolution: SubjectResolution{
			Candidates: []SubjectCandidate{{Subject: acceptanceProject(), Confidence: 0.99}},
			Committed:  []SubjectRef{acceptanceProject()},
		},
		context:  emptyGraphContext(),
		material: chaos4234GatedMaterial(),
	}
}

func TestCHAOS4234_ClassDefaultGate_ComposesKindAndHandleOffersBesideTheWindowOffer(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	graph := chaos4234GatedGraph()
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	telemetry := &recordingTelemetry{}
	engine := buildWindowGateEngineWithTelemetry(t, interpreter, graph, store, telemetry)

	request := validInvestigationRequest() // no EvidenceWindow -- class-default gate applies

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a confirmation-required result", err)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("Status = %q, want clarification_required: the window gate still holds", result.Status)
	}
	if graph.resolveCalls != 1 {
		t.Fatalf("graph.resolveCalls = %d, want exactly 1: the offers-only resolve under the gate", graph.resolveCalls)
	}
	if graph.discoverCalls != 0 {
		t.Fatalf("graph.discoverCalls = %d, want 0: the gate still stops everything past subject resolution", graph.discoverCalls)
	}
	if len(result.SubjectResolution.Committed) != 0 || len(result.SubjectResolution.Candidates) != 0 {
		t.Fatalf("SubjectResolution = %#v, want NO committed material and NO candidates under the gate, even though the graph would have committed %q", result.SubjectResolution, acceptanceProject().CanonicalID)
	}
	if result.WindowClarification == nil || len(result.WindowClarification.Options) == 0 {
		t.Fatal("WindowClarification is nil or empty, want the window offer to survive unchanged")
	}
	if result.StructureNeeds == nil {
		t.Fatal("StructureNeeds is nil, want window + kind + handle disclosure")
	}
	wantMissing := []StructureNeedKind{
		contractsv1.ContextFabricStructureNeedWindow,
		contractsv1.ContextFabricStructureNeedExpectedKind,
		contractsv1.ContextFabricStructureNeedSubjectHandle,
	}
	if !reflect.DeepEqual(result.StructureNeeds.Missing, wantMissing) {
		t.Fatalf("StructureNeeds.Missing = %#v, want %#v (window first: it is still the gate's own ask)", result.StructureNeeds.Missing, wantMissing)
	}
	if !reflect.DeepEqual(result.StructureNeeds.WindowOptions, result.WindowClarification.Options) {
		t.Fatalf("StructureNeeds.WindowOptions = %#v, want the identical option set as WindowClarification.Options", result.StructureNeeds.WindowOptions)
	}
	if len(result.StructureNeeds.KindOptions) != 2 {
		t.Fatalf("StructureNeeds.KindOptions = %#v, want the 2 kind offers the offers-only resolve produced", result.StructureNeeds.KindOptions)
	}
	for _, opt := range result.StructureNeeds.KindOptions {
		if !strings.HasPrefix(opt.ReceiptID, contractsv1.ContextFabricKindOptionReceiptPrefix) || opt.OptionID == "" {
			t.Fatalf("kind option %#v, want a minted kindr_ receipt and option id so turn 2 can redeem it", opt)
		}
	}
	if len(result.StructureNeeds.HandleOptions) != 1 || !strings.HasPrefix(result.StructureNeeds.HandleOptions[0].ReceiptID, contractsv1.ContextFabricHandleOptionReceiptPrefix) {
		t.Fatalf("StructureNeeds.HandleOptions = %#v, want the 1 handle offer with a minted handr_ receipt", result.StructureNeeds.HandleOptions)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result fails Validate(): %v", err)
	}
	if store.saved == nil || !reflect.DeepEqual(store.saved.StructureNeeds, result.StructureNeeds) {
		t.Fatalf("store.saved.StructureNeeds = %#v, want the SAME offers persisted so a turn-2 receipt resolves against them", store.saved)
	}
	if !reflect.DeepEqual(telemetry.structureNeedsDisclosed, wantMissing) {
		t.Fatalf("structureNeedsDisclosed = %#v, want %#v", telemetry.structureNeedsDisclosed, wantMissing)
	}
}

func TestCHAOS4234_ClassDefaultGate_OffersOnlyResolveErrorFailsOpenToWindowOnly(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	graph := chaos4234GatedGraph()
	graph.err = errors.New("falkor unavailable")
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	engine := buildWindowGateEngine(t, interpreter, graph, store)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want the window-only disclosure: an offers-only read must never block the gated terminal", err)
	}
	if graph.resolveCalls != 1 {
		t.Fatalf("graph.resolveCalls = %d, want 1", graph.resolveCalls)
	}
	if result.Status != InvestigationClarificationRequired || result.StructureNeeds == nil {
		t.Fatalf("Status/StructureNeeds = %q/%#v, want clarification_required with a window-only disclosure", result.Status, result.StructureNeeds)
	}
	if len(result.StructureNeeds.Missing) != 1 || result.StructureNeeds.Missing[0] != contractsv1.ContextFabricStructureNeedWindow {
		t.Fatalf("StructureNeeds.Missing = %#v, want exactly [window]", result.StructureNeeds.Missing)
	}
	if len(result.StructureNeeds.KindOptions) != 0 || len(result.StructureNeeds.HandleOptions) != 0 {
		t.Fatalf("kind/handle options = %#v/%#v, want none when the offers-only read failed", result.StructureNeeds.KindOptions, result.StructureNeeds.HandleOptions)
	}
}

func TestCHAOS4234_ClassDefaultGate_RefusedNoClarificationNeverRunsTheOffersOnlyResolve(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	graph := chaos4234GatedGraph()
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	engine := buildWindowGateEngine(t, interpreter, graph, store)

	request := validInvestigationRequest()
	request.Options.AllowClarification = false

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("Status = %q, want no_match (refused, no clarification allowed)", result.Status)
	}
	if graph.resolveCalls != 0 {
		t.Fatalf("graph.resolveCalls = %d, want 0: no clarification can be offered, so nothing is read", graph.resolveCalls)
	}
	if result.StructureNeeds != nil {
		t.Fatalf("StructureNeeds = %#v, want nil on a refused terminal", result.StructureNeeds)
	}
}

func TestCHAOS4234_OneTurnRedemption_WindowAndKindReceiptsFromOneGatedResult(t *testing.T) {
	t.Parallel()
	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	gated := validInvestigationResult()
	gated.ResultID = "result_prior_gated_4234"
	gated.Status = InvestigationClarificationRequired
	gated.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_gated4234aaaa", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd},
	}}
	gated.StructureNeeds = &StructureNeeds{
		Missing:       []StructureNeedKind{contractsv1.ContextFabricStructureNeedWindow, contractsv1.ContextFabricStructureNeedExpectedKind},
		WindowOptions: gated.WindowClarification.Options,
		KindOptions: []KindOption{
			{ReceiptID: "kindr_gated4234aaaa", OptionID: "opt_pr", Label: "a pull request", Kind: SubjectPullRequest, OfferSource: contractsv1.ContextFabricStructureOfferEngine},
		},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{gated.ResultID: gated}}

	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
	engine := buildWindowGateEngine(t, interpreter, graph, store)

	request := validInvestigationRequest()
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: gated.ResultID, ReceiptID: "winr_gated4234aaaa"}}
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: gated.ResultID, ReceiptID: "kindr_gated4234aaaa"}}

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.WindowClarification != nil {
		t.Fatalf("WindowClarification = %#v, want nil: the window receipt cleared the gate in the SAME turn as the kind receipt", result.WindowClarification)
	}
	if graph.resolveCalls != 1 {
		t.Fatalf("graph.resolveCalls = %d, want exactly 1 DECISIVE resolve (no gated offers-only pass once the window is confirmed)", graph.resolveCalls)
	}
	if graph.lastConfirmedKind == nil || graph.lastConfirmedKind.Kind != SubjectPullRequest {
		t.Fatalf("confirmed kind reaching ResolveSubjects = %#v, want pull_request from the redeemed kindr_ receipt", graph.lastConfirmedKind)
	}
	if len(result.ConfirmedStructure) != 2 {
		t.Fatalf("ConfirmedStructure = %#v, want both the kind and the window confirmations applied", result.ConfirmedStructure)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result fails Validate(): %v", err)
	}
}

func buildWindowGateEngineWithTelemetry(t *testing.T, interpreter QuestionInterpreter, graph *acceptanceGraphReader, results InvestigationResultStore, telemetry EngineTelemetry) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreter,
		Graph:       graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatalf("ReadFacts called with %#v -- a gated request must never reach the canonical fact read", request)
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("Synthesize called -- a gated request must never reach synthesis")
			return InvestigationResult{}, nil
		}),
		Results:   results,
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "chaos-4234-test",
		Now:            func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_chaos4234_gate01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}
