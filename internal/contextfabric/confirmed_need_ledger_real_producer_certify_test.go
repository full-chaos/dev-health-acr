package contextfabric

// The confirmed-need-ledger line's declaration is certified elsewhere only
// against events built by struct literal (confirmed_need_ledger_certify_test.go,
// package contextfabric_test). That proves the declaration and the
// certifier agree with EACH OTHER; it never drives the real producer
// (resolveConfirmedNeedLedger, reached only through Engine.Investigate) for
// the ledger's OWN outcome vocabulary, so a real producer emitting a value
// outside its own declared vocabulary would ship undetected. This file
// drives every ConfirmedNeedLedgerOutcome through a real Investigate call,
// reusing the SAME fixtures chaos5639_confirmed_need_test.go already
// established for each outcome class (parentAndChildForLedgerTest,
// ledgerTestStore) -- only the engine differs, because those tests call
// resolveConfirmedNeedLedger directly and Fatal on any Interpret/Facts/
// Synthesize call, while this file must complete a full Investigate() to
// reach the production JSON sink. The capture-axis fields (capture_decision,
// anchor_agreement, applied_anchor_basis, anchor_disposition,
// capture_skip_reason) are certified from their own already-established
// real-producer rigs in confirmed_need_ledger_capture_axis_real_producer_certify_test.go,
// the same split frame_validation_real_producer_certify_test.go already
// established for the frame-validation line.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ledgerOutcomeGraph is a minimal, fully-wired GraphReader for driving
// Engine.Investigate through the confirmed-need ledger's own resolution
// point for each ConfirmedNeedLedgerOutcome. epoch is settable so the
// stale-graph-epoch outcome -- which needs THIS turn's own binding to name
// a DIFFERENT epoch than the parent's stored one -- is reachable the same
// way TestResolveConfirmedNeedLedger_DropsOnStaleGraphEpoch already drives
// it at the resolveConfirmedNeedLedger level directly.
type ledgerOutcomeGraph struct{ epoch int64 }

func (g ledgerOutcomeGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "ledger-outcome-key", Epoch: g.epoch}, nil
}

func (g ledgerOutcomeGraph) ResolveSubjects(_ context.Context, _ storage.Principal, _ InvestigationRequest, _ InterpretedQuestion, _ ResolvedGraphBinding, _ *ConfirmedExpectedKind, _ *ConfirmedAnchorSelection, _ *QuestionFrame, _ SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	return SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, StructureOfferMaterial{}, nil, nil, nil
}

func (g ledgerOutcomeGraph) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	return emptyGraphContext(), nil
}

// ledgerOutcomeTestEngine is the fully-wired engine every outcome scenario
// below shares: an Interpreter/Facts/Synthesizer that always succeed
// trivially, so the deferred confirmed-need-ledger emit (which fires above
// every one of them) is reached regardless of what they return -- the
// outcome under test is decided entirely by resolveConfirmedNeedLedger,
// above all of them, from store and epoch alone.
func ledgerOutcomeTestEngine(t *testing.T, store InvestigationResultStore, telemetry EngineTelemetry, epoch int64) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Graph: ledgerOutcomeGraph{epoch: epoch},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return validInvestigationResult(), nil
		}),
		Results:   store,
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "chaos-5802-ledger-outcome-certify",
		Now:            func() time.Time { return time.Unix(600, 0).UTC() },
		NewResultID:    func() string { return "result_5802_ledger_outcome" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// stampConversation backfills TurnID/CreatedAt on every conversation turn
// -- Validate() (called from Investigate, unlike a direct
// resolveConfirmedNeedLedger call) requires both, and
// parentAndChildForLedgerTest's fixture omits them because none of its
// other callers reach Validate (see
// TestInvestigate_ConfirmedNeedLedgerAppliesThroughThePublicEntryPoint's
// own comment on the same requirement).
func stampConversation(request *InvestigationRequest) {
	for i := range request.Conversation {
		request.Conversation[i].TurnID = fmt.Sprintf("turn_%d", i)
		request.Conversation[i].CreatedAt = time.Unix(int64(590+i), 0).UTC()
	}
}

// ConfirmedNeedLedgerOutcomeRealProducerScenarios names every
// ConfirmedNeedLedgerOutcome this file can run, in ConfirmedNeedLedgerOutcomeVocabulary's
// own declared order, so a member added there reaches this list without a
// second, independently maintained one.
func ConfirmedNeedLedgerOutcomeRealProducerScenarios() []string {
	names := make([]string, 0, len(ConfirmedNeedLedgerOutcomeVocabulary()))
	for _, outcome := range ConfirmedNeedLedgerOutcomeVocabulary() {
		names = append(names, string(outcome))
	}
	return names
}

// RunConfirmedNeedLedgerOutcomeRealProducerScenarioForTest drives ONE named
// outcome scenario through a real Engine.Investigate call and returns the
// production slog JSON bytes it wrote, so the external certification pin
// can certify the ACTUAL emitted line rather than a struct built by hand.
func RunConfirmedNeedLedgerOutcomeRealProducerScenarioForTest(t *testing.T, scenario string) (log []byte, orgID string) {
	t.Helper()
	orgID = "org_acceptance"
	telemetry, buf := jsonLedgerTelemetry()

	switch ConfirmedNeedLedgerOutcome(scenario) {
	case ConfirmedNeedLedgerMissNoReference:
		_, _, childRequest := parentAndChildForLedgerTest(t)
		childRequest.ParentResultID = ""
		stampConversation(&childRequest)
		runLedgerOutcomeTurn(t, telemetry, &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}}, 0, childRequest)
	case ConfirmedNeedLedgerMissUnloadable:
		_, _, childRequest := parentAndChildForLedgerTest(t)
		childRequest.ParentResultID = "result_does_not_exist"
		stampConversation(&childRequest)
		runLedgerOutcomeTurn(t, telemetry, &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}}, 0, childRequest)
	case ConfirmedNeedLedgerMissEmpty:
		parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
		parentState.ConfirmedNeeds = []ConfirmedNeedEntry{}
		stampConversation(&childRequest)
		runLedgerOutcomeTurn(t, telemetry, ledgerTestStore(parentResult, parentState), 0, childRequest)
	case ConfirmedNeedLedgerDroppedQuestionIndeterminate:
		parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
		parentResult.Question, childRequest.Question, childRequest.Conversation = "?", "?", nil
		stampConversation(&childRequest)
		runLedgerOutcomeTurn(t, telemetry, ledgerTestStore(parentResult, parentState), 0, childRequest)
	case ConfirmedNeedLedgerDroppedQuestionChanged:
		parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
		childRequest.Question, childRequest.Conversation = "Which repositories does the Platform team own?", nil
		stampConversation(&childRequest)
		runLedgerOutcomeTurn(t, telemetry, ledgerTestStore(parentResult, parentState), 0, childRequest)
	case ConfirmedNeedLedgerDroppedIdentityIncomparable:
		parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
		parentState.RequestIdentity = SemanticRequestIdentity{}
		stampConversation(&childRequest)
		runLedgerOutcomeTurn(t, telemetry, ledgerTestStore(parentResult, parentState), 0, childRequest)
	case ConfirmedNeedLedgerDroppedIdentityChanged:
		parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
		childRequest.RequestedScope.RepositorySlugs = []string{"full-chaos/dev-health-acr"}
		stampConversation(&childRequest)
		runLedgerOutcomeTurn(t, telemetry, ledgerTestStore(parentResult, parentState), 0, childRequest)
	case ConfirmedNeedLedgerDroppedStaleGraphEpoch:
		parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
		store := ledgerTestStore(parentResult, parentState)
		staleEpoch := int64(7)
		store.graphEpoch = &staleEpoch
		stampConversation(&childRequest)
		runLedgerOutcomeTurn(t, telemetry, store, 8, childRequest)
	case ConfirmedNeedLedgerHit:
		parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
		stampConversation(&childRequest)
		runLedgerOutcomeTurn(t, telemetry, ledgerTestStore(parentResult, parentState), 0, childRequest)
	default:
		t.Fatalf("unknown ConfirmedNeedLedgerOutcomeRealProducer scenario %q", scenario)
	}
	return buf.Bytes(), orgID
}

// runLedgerOutcomeTurn drives Investigate and tolerates an error return --
// several outcome classes above (an unloadable/incomparable/stale-epoch
// parent) are legitimate business states, never wire-validation failures,
// but this file certifies the DEFERRED LEDGER LINE only, which fires
// before Investigate's own return either way.
func runLedgerOutcomeTurn(t *testing.T, telemetry EngineTelemetry, store InvestigationResultStore, epoch int64, request InvestigationRequest) {
	t.Helper()
	engine := ledgerOutcomeTestEngine(t, store, telemetry, epoch)
	_, _ = engine.Investigate(context.Background(), storage.Principal{OrgID: "org_acceptance"}, request)
}
