package contextfabric

import (
	"context"
	"errors"
	"fmt"
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
	// CHAOS-4314: window_expand is carried on WindowExpandOptions directly,
	// deliberately NEVER added to Missing (see that field's own doc
	// comment -- a StructureNeedKind enum addition would require a new v1
	// major, an additive field does not) -- the offers-only pool this
	// fixture's own KindOptions/HandleOptions carries is non-empty, so the
	// gate's disclosure now recommends expanding.
	if len(result.StructureNeeds.WindowExpandOptions) != 1 {
		t.Fatalf("StructureNeeds.WindowExpandOptions = %#v, want exactly 1 recommendation", result.StructureNeeds.WindowExpandOptions)
	}
	expand := result.StructureNeeds.WindowExpandOptions[0]
	if !windowExpandTargetsExisting(result.StructureNeeds.WindowOptions, expand) {
		t.Fatalf("window_expand option %#v does not name any window_options entry %#v", expand, result.StructureNeeds.WindowOptions)
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
	// codex round-2 finding #3: a regression removing
	// RecordGatedOfferResolution's call must fail a test, not just the
	// structure-disclosure assertions above.
	if want := []GatedOfferResolutionOutcome{GatedOfferResolutionComposed}; !reflect.DeepEqual(telemetry.gatedOfferResolutions, want) {
		t.Fatalf("gatedOfferResolutions = %#v, want %#v", telemetry.gatedOfferResolutions, want)
	}
	// CHAOS-4314: window_gated_offered/window_gated_silent split's producer
	// signal -- a composed offers-only pool means offered=true.
	if want := []bool{true}; !reflect.DeepEqual(telemetry.windowGateOfferDisclosures, want) {
		t.Fatalf("windowGateOfferDisclosures = %#v, want %#v", telemetry.windowGateOfferDisclosures, want)
	}
}

// windowExpandTargetsExisting reports whether expand's ReceiptID/OptionID/
// Label/RelativeID all match a real entry in options -- the CHAOS-4314
// referential integrity relationship ContextFabricStructureNeeds.Validate
// itself enforces (windowOptionMatches, internal/contracts/v1),
// reimplemented locally here since that helper is unexported outside
// package v1.
func windowExpandTargetsExisting(options []WindowOption, expand contractsv1.ContextFabricWindowExpandOption) bool {
	for _, opt := range options {
		if opt.ReceiptID == expand.ReceiptID && opt.OptionID == expand.OptionID &&
			opt.Label == expand.Label && opt.RelativeID == expand.RelativeID {
			return true
		}
	}
	return false
}

func TestCHAOS4234_ClassDefaultGate_OffersOnlyResolveErrorFailsOpenToWindowOnly(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	graph := chaos4234GatedGraph()
	graph.err = errors.New("falkor unavailable")
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	telemetry := &recordingTelemetry{}
	engine := buildWindowGateEngineWithTelemetry(t, interpreter, graph, store, telemetry)

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
	if want := []GatedOfferResolutionOutcome{GatedOfferResolutionFailed}; !reflect.DeepEqual(telemetry.gatedOfferResolutions, want) {
		t.Fatalf("gatedOfferResolutions = %#v, want %#v", telemetry.gatedOfferResolutions, want)
	}
	// CHAOS-4314: a failed offers-only read has nothing to recommend --
	// window_gated_silent, not offered.
	if want := []bool{false}; !reflect.DeepEqual(telemetry.windowGateOfferDisclosures, want) {
		t.Fatalf("windowGateOfferDisclosures = %#v, want %#v", telemetry.windowGateOfferDisclosures, want)
	}
}

// TestCHAOS4234_ClassDefaultGate_NeverProjectedOrgIsDistinctFromEmpty is
// codex round-2 finding #2, confirmed and fixed: ErrGraphNotProjected used
// to fall through to StructureNeedsWouldDisclose's false branch and record
// GatedOfferResolutionEmpty, conflating "no graph exists yet" with "the
// graph exists and genuinely has nothing to offer" -- the same distinction
// subjectlessTerminalReasons' "graph_not_projected" value already makes
// for the decisive path (chaos4077_never_projected_org_test.go). Both
// degrade to the SAME window-only terminal; only the telemetry outcome
// must differ.
func TestCHAOS4234_ClassDefaultGate_NeverProjectedOrgIsDistinctFromEmpty(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	graph := chaos4234GatedGraph()
	graph.err = fmt.Errorf("query context graph: %w", ErrGraphNotProjected)
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	telemetry := &recordingTelemetry{}
	engine := buildWindowGateEngineWithTelemetry(t, interpreter, graph, store, telemetry)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want the window-only disclosure: a never-projected org must never block the gated terminal", err)
	}
	if result.Status != InvestigationClarificationRequired || result.StructureNeeds == nil {
		t.Fatalf("Status/StructureNeeds = %q/%#v, want clarification_required with a window-only disclosure", result.Status, result.StructureNeeds)
	}
	if len(result.StructureNeeds.Missing) != 1 || result.StructureNeeds.Missing[0] != contractsv1.ContextFabricStructureNeedWindow {
		t.Fatalf("StructureNeeds.Missing = %#v, want exactly [window]", result.StructureNeeds.Missing)
	}
	if want := []GatedOfferResolutionOutcome{GatedOfferResolutionNotProjected}; !reflect.DeepEqual(telemetry.gatedOfferResolutions, want) {
		t.Fatalf("gatedOfferResolutions = %#v, want %#v -- a never-projected org must be distinguishable from an ordinary empty pool", telemetry.gatedOfferResolutions, want)
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

// TestCHAOS4234_ClassDefaultGate_V2AnchorOffersGetV2SchemaAndValidator is
// codex round-1 finding 1, confirmed and fixed: gatedOfferMaterial's
// ResolveSubjects call is the SAME unconditional anchorOfferMaterial path
// the decisive turn uses (resolve.go's combineStructureOfferMaterial),
// which can mint membership-verify (V2) anchor offers -- the gate must
// not silently persist them under the V1 schema/validator.
func TestCHAOS4234_ClassDefaultGate_V2AnchorOffersGetV2SchemaAndValidator(t *testing.T) {
	t.Parallel()
	v2Material := StructureOfferMaterial{
		Missing: []StructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor},
		AnchorOptions: []contractsv1.ContextFabricAnchorOption{
			contractsv1.ContextFabricAnchorOptionV2{
				Kind: SubjectRepository, CanonicalID: "repository_ask_dev", Label: "ask-dev",
				MatchedTermHash: "aaaaaaaaaaaaaaaaaaaaaaaa", OfferSource: contractsv1.ContextFabricStructureOfferEngine,
			}.ToV1Wire(),
		},
		AnchorOptionsRequireV2: true,
	}
	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	graph := chaos4234GatedGraph()
	graph.material = v2Material
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	engine := buildWindowGateEngine(t, interpreter, graph, store)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a valid V2 gated result, not a validation error", err)
	}
	if result.SchemaVersion != InvestigationResultSchemaV2 {
		t.Fatalf("result.SchemaVersion = %q, want %q: V2 anchor offers under the gate must stamp the V2 schema, same as the decisive path (unresolved.go)", result.SchemaVersion, InvestigationResultSchemaV2)
	}
	if result.Versions.ContractVersion != InvestigationResultSchemaV2 {
		t.Fatalf("result.Versions.ContractVersion = %q, want %q", result.Versions.ContractVersion, InvestigationResultSchemaV2)
	}
	if len(result.StructureNeeds.AnchorOptions) != 1 {
		t.Fatalf("result.StructureNeeds.AnchorOptions = %#v, want the 1 V2-minted anchor offer to survive", result.StructureNeeds.AnchorOptions)
	}
	if store.saved == nil || store.saved.SchemaVersion != InvestigationResultSchemaV2 {
		t.Fatalf("store.saved.SchemaVersion = %#v, want %q persisted", store.saved, InvestigationResultSchemaV2)
	}
}

// TestCHAOS4234_ClassDefaultGate_V1MaterialStillGetsV1Schema is the
// control for the test above: ordinary V1 kind/handle material (the
// shape every other CHAOS-4234 test in this file uses) must be
// completely unaffected by the V2 dispatch this fix added.
func TestCHAOS4234_ClassDefaultGate_V1MaterialStillGetsV1Schema(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	graph := chaos4234GatedGraph()
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	engine := buildWindowGateEngine(t, interpreter, graph, store)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.SchemaVersion != InvestigationResultSchemaV1 || result.Versions.ContractVersion != InvestigationResultSchemaV1 {
		t.Fatalf("SchemaVersion/ContractVersion = %q/%q, want V1/V1 unchanged", result.SchemaVersion, result.Versions.ContractVersion)
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
