package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestWindowCarry_TurnThreeCommitsWithCarriedWindow is CHAOS-4360's own
// red-first proof: the exact three-turn shape live-proven twice on the kiac
// pilot (cf-rulings.md 2026-08-27 06:30/09:10/13:40).
//
// Turn 2 (not replayed here -- its OUTCOME is the fixture: priorResult)
// confirmed a window via winr_ receipt (Provenance=clarification_confirmed)
// and asked for a fresh subject clarification, offering a candidate the
// caller could pick. Turn 3 (this test) carries ONLY that candidate pick
// back via PriorSubjectReceipts -- never the window receipt, which would be
// vetoed_stale on redemption (receipts are single-use; unchanged by this
// ticket) -- and states no window of its own.
//
// BEFORE CHAOS-4360: turn 3's own composeEffectiveWindow has nothing to
// work with, produces Provenance=inferred_default, the CHAOS-4234
// gated-class-default branch fires, and composePriorSubjectReceiptDispositions
// can only ever classify the candidate receipt as skipped_failed_reauth (the
// gate's own resolution is offers-only and discarded by ruling) -- this test
// is RED on any revision before this ticket's engine.go change.
//
// AFTER: resolveCarriedWindow finds turn 2's own confirmed window on the
// referenced prior result, turn 3's effective window becomes that carried
// (non-inferred) window, the CHAOS-4234 gate never fires, ResolveSubjects
// runs its real decisive resolution, and the candidate receipt reaches
// "applied" against that real resolution -- the SAME, unmodified
// composePriorSubjectReceiptDispositions mechanism CHAOS-3478 already
// shipped, now finally reachable.
func TestWindowCarry_TurnThreeCommitsWithCarriedWindow(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}

	// The fixture standing in for turn 2's own persisted result: a
	// clarification_required round that nonetheless confirmed a window
	// (windowConfirmationRequiredResult composes EffectiveEvidenceWindow on
	// every terminal it produces, decisive or not -- see that function's
	// own doc comment) and offered a candidate the caller went on to pick.
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_1"
	priorResult.Status = InvestigationClarificationRequired
	priorResult.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{
		RelativeID: RelativeWindowTrailing90D, Provenance: WindowClarificationConfirmed,
	}
	priorResult.SubjectResolution = SubjectResolution{
		Candidates: []SubjectCandidate{{
			ReceiptID: "receipt_abc12345", Subject: project, State: ResolutionCommitted,
			MatchReasons: []string{"Exact canonical subject hint matched the organization graph."}, Confidence: 1,
		}},
		Committed: []SubjectRef{},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{"result_prior_1": priorResult}}
	telemetry := &recordingTelemetry{}

	// graphReaderStub (engine_test.go), not acceptanceGraphReader: once the
	// carry fixes turn 3's window, this stub answers the ordinary DECISIVE
	// ResolveSubjects/DiscoverContext pair (it does not itself distinguish
	// offers-only from decisive calls -- the engine's own gate is what used
	// to intercept it), and provenCommitBases exempts the committed subject
	// from the unrelated CHAOS-4085 commit-affirmation gate -- this test's
	// subject matter is the window carry, not that gate.
	graph := graphReaderStub{
		resolution: SubjectResolution{
			Candidates: []SubjectCandidate{{
				ReceiptID: "receipt_turn3_candidate1", Subject: project, State: ResolutionCommitted,
				MatchReasons: []string{"Exact canonical subject hint matched the organization graph."}, Confidence: 0.97,
			}},
			Committed: []SubjectRef{project},
		},
		context: emptyGraphContext(),
		bases:   provenCommitBases(project),
	}

	engine, err := NewEngine(EngineDependencies{
		// bootstrapInterpretation carries no WindowClass/Confidence of its
		// own -- the SAME "nothing stated, class table decides" shape that
		// makes the pre-existing CHAOS-4234 gated test's window
		// inferred_default in the first place.
		Interpreter: &countingInterpreter{interpretation: bootstrapInterpretation()},
		Graph:       graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Ask Dev is on track.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts:        []ClaimedFact{},
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Ask Dev is on track based on available context.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: store, Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "chaos-4360-test",
		Now:            func() time.Time { return time.Date(2026, 8, 27, 20, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_turn_3" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := validInvestigationRequest() // no EvidenceWindow -- class-default would gate, absent the carry
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_1", ReceiptID: "receipt_abc12345"}}

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a decisive result once the window carries", err)
	}
	if result.Status != InvestigationComplete {
		t.Fatalf("Status = %q, want complete: the carried window must defeat the CHAOS-4234 gate", result.Status)
	}
	if len(result.SubjectResolution.Committed) != 1 || result.SubjectResolution.Committed[0] != project {
		t.Fatalf("SubjectResolution.Committed = %#v, want [%#v]: turn 3 must actually commit", result.SubjectResolution.Committed, project)
	}
	wantDispositions := []contractsv1.ContextFabricPriorSubjectReceiptEntry{
		{PriorResultID: "result_prior_1", ReceiptID: "receipt_abc12345", Disposition: contractsv1.ContextFabricPriorSubjectReceiptApplied},
	}
	if !reflect.DeepEqual(result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions) {
		t.Fatalf("PriorSubjectReceiptDispositions = %#v, want %#v (applied, not skipped_failed_reauth)", result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions)
	}
	if result.EffectiveEvidenceWindow == nil || result.EffectiveEvidenceWindow.Provenance != WindowClarificationConfirmed || result.EffectiveEvidenceWindow.RelativeID != RelativeWindowTrailing90D {
		t.Fatalf("EffectiveEvidenceWindow = %#v, want the carried trailing-90d window, provenance clarification_confirmed (never inferred_default)", result.EffectiveEvidenceWindow)
	}
	wantEntry := contractsv1.ContextFabricConfirmedStructureEntry{
		Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: string(RelativeWindowTrailing90D),
		Source: contractsv1.ContextFabricStructureSourceCarried, PriorResultID: "result_prior_1",
		Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
	}
	found := false
	for _, entry := range result.ConfirmedStructure {
		if reflect.DeepEqual(entry, wantEntry) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ConfirmedStructure = %#v, want an entry %#v disclosing the carry", result.ConfirmedStructure, wantEntry)
	}
	if len(telemetry.windowCarries) != 1 || telemetry.windowCarries[0] != (windowCarryRecord{WindowCarryHit, 0}) {
		t.Fatalf("telemetry.windowCarries = %#v, want exactly one hit at depth 0", telemetry.windowCarries)
	}
	if len(telemetry.windowCanonicalizationOutcomes) == 0 || telemetry.windowCanonicalizationOutcomes[len(telemetry.windowCanonicalizationOutcomes)-1] != WindowCanonicalizationCarried {
		t.Fatalf("telemetry.windowCanonicalizationOutcomes = %#v, want the final entry to be %q (never inferred_default)", telemetry.windowCanonicalizationOutcomes, WindowCanonicalizationCarried)
	}
}

// buildCarryTestEngine wires a minimal Engine whose only exercised
// dependency is Results -- Interpreter/Graph/Facts/Synthesizer all fatal
// the test if called, because every test in this file calls
// resolveCarriedWindow directly rather than through Investigate.
func buildCarryTestEngine(t *testing.T, store InvestigationResultStore) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			t.Fatal("Interpret must not be called by a direct resolveCarriedWindow test")
			return InterpretedQuestion{}, nil
		}),
		Graph: neverProjectedGraphReader{t: t},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatal("ReadFacts must not be called by a direct resolveCarriedWindow test")
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("Synthesize must not be called by a direct resolveCarriedWindow test")
			return InvestigationResult{}, nil
		}),
		Results: store,
	}, EngineOptions{ServiceVersion: "chaos-4360-carry-unit-test", Now: func() time.Time { return time.Unix(400, 0).UTC() }, NewResultID: func() string { return "result_carry_unit_test" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// TestResolveCarriedWindow_MissesWhenPriorWindowIsItselfInferred proves the
// CHAOS-4040 bar applied to the SOURCE side of a carry: an inferred window
// can never BE a source, only ever a destination -- carrying an
// already-inferred window forward would just relabel a guess as confirmed.
func TestResolveCarriedWindow_MissesWhenPriorWindowIsItselfInferred(t *testing.T) {
	t.Parallel()
	prior := validInvestigationResult()
	prior.ResultID = "result_prior_inferred"
	prior.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{
		RelativeID: RelativeWindowTrailing90D, Provenance: WindowInferredDefault,
	}
	store := &staticResultStore{results: map[string]InvestigationResult{"result_prior_inferred": prior}}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_inferred", ReceiptID: "receipt_abc12345"}}

	got := engine.resolveCarriedWindow(context.Background(), acceptancePrincipal(), request, request.PriorSubjectReceipts, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != WindowCarryMissNoConfirmedWindow || got.Window != nil {
		t.Fatalf("resolveCarriedWindow() = %#v, want miss_no_confirmed_window and no window", got)
	}
}

// TestResolveCarriedWindow_WalksChainToNearestConfirmation proves the
// bounded multi-hop walk: the request names only the MOST RECENT prior
// result (B), which itself carries no window of its own but DOES name an
// earlier result (A) via its own ConfirmedStructure (a different member
// confirmed on that hop, still pointing back through the SAME
// PriorResultID convention) -- A is where the window was actually
// confirmed. "Nearest confirmation wins": the walk must find it at A, one
// hop past the directly-referenced result.
func TestResolveCarriedWindow_WalksChainToNearestConfirmation(t *testing.T) {
	t.Parallel()
	resultA := validInvestigationResult()
	resultA.ResultID = "result_turn_a"
	resultA.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{
		RelativeID: RelativeWindowTrailing30D, Provenance: WindowQuestionStated,
	}
	resultB := validInvestigationResult()
	resultB.ResultID = "result_turn_b"
	resultB.EffectiveEvidenceWindow = nil // this hop confirmed no window of its own
	resultB.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{{
		Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: "project",
		Source: contractsv1.ContextFabricStructureSourceReceipt, PriorResultID: "result_turn_a",
		ReceiptID: "kindr_turn_a_01", Provenance: contractsv1.ContextFabricStructureClarificationConfirmed,
		Disposition: contractsv1.ContextFabricStructureDispositionApplied,
	}}
	store := &staticResultStore{results: map[string]InvestigationResult{
		"result_turn_a": resultA, "result_turn_b": resultB,
	}}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_turn_b", ReceiptID: "receipt_xyz"}}

	got := engine.resolveCarriedWindow(context.Background(), acceptancePrincipal(), request, request.PriorSubjectReceipts, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != WindowCarryHit {
		t.Fatalf("resolveCarriedWindow().Outcome = %q, want hit", got.Outcome)
	}
	if got.ChainDepth != 1 {
		t.Fatalf("resolveCarriedWindow().ChainDepth = %d, want 1 (one hop past the directly-referenced result)", got.ChainDepth)
	}
	if got.SourceResultID != "result_turn_a" {
		t.Fatalf("resolveCarriedWindow().SourceResultID = %q, want %q", got.SourceResultID, "result_turn_a")
	}
	if got.Window == nil || got.Window.RelativeID != RelativeWindowTrailing30D {
		t.Fatalf("resolveCarriedWindow().Window = %#v, want result_turn_a's own trailing-30d window", got.Window)
	}
}

// TestResolveCarriedWindow_FailsClosedOnStaleGraphEpoch proves the
// CHAOS-3898 §2.2 ingress taint gate applies to a carry lookup identically
// to resolvePriorSubjectHints' own: a candidate whose stored GraphEpoch
// disagrees with this investigation's own binding is never trusted, even
// though its own window would otherwise be a clean hit.
func TestResolveCarriedWindow_FailsClosedOnStaleGraphEpoch(t *testing.T) {
	t.Parallel()
	prior := validInvestigationResult()
	prior.ResultID = "result_prior_stale"
	prior.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{
		RelativeID: RelativeWindowTrailing90D, Provenance: WindowClarificationConfirmed,
	}
	staleEpoch := int64(3)
	store := &staticResultStore{
		results:    map[string]InvestigationResult{"result_prior_stale": prior},
		graphEpoch: &staleEpoch,
	}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_stale", ReceiptID: "receipt_abc"}}

	got := engine.resolveCarriedWindow(context.Background(), acceptancePrincipal(), request, request.PriorSubjectReceipts, ResolvedGraphBinding{Epoch: 7})
	if got.Outcome != WindowCarryMissStaleGraphEpoch || got.Window != nil {
		t.Fatalf("resolveCarriedWindow() = %#v, want miss_stale_graph_epoch and no window", got)
	}
}

// TestResolveCarriedWindow_MissesOnUnloadableResult proves a Get error (not
// found, unauthorized, transient failure) degrades to a disclosed miss,
// never an error propagated out of Investigate.
func TestResolveCarriedWindow_MissesOnUnloadableResult(t *testing.T) {
	t.Parallel()
	store := &staticResultStore{results: map[string]InvestigationResult{}, getErr: errors.New("boom")}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_missing", ReceiptID: "receipt_abc"}}

	got := engine.resolveCarriedWindow(context.Background(), acceptancePrincipal(), request, request.PriorSubjectReceipts, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != WindowCarryMissUnloadable || got.Window != nil {
		t.Fatalf("resolveCarriedWindow() = %#v, want miss_unloadable and no window", got)
	}
}

// TestResolveCarriedWindow_MissesWhenNoPriorReferenced proves the cheap,
// I/O-free short-circuit: a request naming no prior result at all (every
// one of the six receipt fields empty) never calls the store.
func TestResolveCarriedWindow_MissesWhenNoPriorReferenced(t *testing.T) {
	t.Parallel()
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	engine := buildCarryTestEngine(t, store)

	got := engine.resolveCarriedWindow(context.Background(), acceptancePrincipal(), validInvestigationRequest(), nil, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != WindowCarryMissNoReference || got.Window != nil {
		t.Fatalf("resolveCarriedWindow() = %#v, want miss_no_reference and no window", got)
	}
	if len(store.gotIDs) != 0 {
		t.Fatalf("store.gotIDs = %#v, want no Get calls when nothing was referenced", store.gotIDs)
	}
}

// TestComposeCarriedWindowEntry_NilOnEveryMissOutcome proves the wire
// disclosure is composed ONLY for an actual hit -- every miss reason
// composes nothing (the ordinary "nothing to disclose" convention this
// package uses throughout, matching composeConfirmedStructure's own
// empty-echo rule).
func TestComposeCarriedWindowEntry_NilOnEveryMissOutcome(t *testing.T) {
	t.Parallel()
	for _, outcome := range []WindowCarryOutcome{
		WindowCarryNotAttempted, WindowCarryMissNoReference, WindowCarryMissUnloadable,
		WindowCarryMissStaleGraphEpoch, WindowCarryMissNoConfirmedWindow, WindowCarryMissDepthExceeded,
	} {
		if got := composeCarriedWindowEntry(windowCarryResult{Outcome: outcome}); got != nil {
			t.Fatalf("composeCarriedWindowEntry(%q) = %#v, want nil", outcome, got)
		}
	}
}

// TestResolveCarriedWindow_IgnoresUnvalidatedSubjectReceipts is codex R1 P1
// (fixed): a PriorSubjectReceipts entry whose ReceiptID matches NO candidate
// in its referenced prior result is CHAOS-3478's own best-effort case --
// resolvePriorSubjectHints classifies it skipped_no_match and the
// investigation proceeds regardless, never vetoing. Before this fix, carry
// seeded its walk from the RAW request field, so that same unmatched
// receipt -- one that resolved NOTHING -- could still reach into the prior
// result it merely NAMED purely to steal its window, turning an otherwise
// inert bad receipt into a live CHAOS-4234-gate bypass. Passing an EMPTY
// validatedSubjectReceipts (simulating "resolvePriorSubjectHints found no
// match") while the raw request still names the same prior result must
// produce a miss, even though that prior result's own window is a clean,
// genuinely confirmed hit.
func TestResolveCarriedWindow_IgnoresUnvalidatedSubjectReceipts(t *testing.T) {
	t.Parallel()
	prior := validInvestigationResult()
	prior.ResultID = "result_prior_unmatched"
	prior.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{
		RelativeID: RelativeWindowTrailing90D, Provenance: WindowClarificationConfirmed,
	}
	store := &staticResultStore{results: map[string]InvestigationResult{"result_prior_unmatched": prior}}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	// The raw wire field names a real prior result, but the receipt matched
	// nothing this call -- resolvePriorSubjectHints' own validated return
	// would be empty for it, so the test passes nil directly rather than
	// re-deriving that classification.
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_unmatched", ReceiptID: "receipt_matches_nothing"}}

	got := engine.resolveCarriedWindow(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != WindowCarryMissNoReference || got.Window != nil {
		t.Fatalf("resolveCarriedWindow() = %#v, want miss_no_reference and no window: an unvalidated PriorSubjectReceipts entry must never seed the carry walk", got)
	}
	if len(store.gotIDs) != 0 {
		t.Fatalf("store.gotIDs = %#v, want no Get calls: the unvalidated receipt must not even be looked up", store.gotIDs)
	}
}

// TestResolveCarriedWindow_PreservesOriginProvenance is codex R1 P2 (fixed):
// a question_stated origin window must carry that SAME provenance onto the
// carried EffectiveEvidenceWindow (verbatim copy, unaffected by this fix --
// see carriableWindow) -- proving here that composeCarriedWindowEntry's own
// disclosure entry now agrees, instead of hard-coding
// clarification_confirmed regardless of the origin's real authority tier.
func TestResolveCarriedWindow_PreservesOriginProvenance(t *testing.T) {
	t.Parallel()
	prior := validInvestigationResult()
	prior.ResultID = "result_prior_stated"
	prior.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{
		RelativeID: RelativeWindowTrailing30D, Provenance: WindowQuestionStated,
	}
	store := &staticResultStore{results: map[string]InvestigationResult{"result_prior_stated": prior}}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_stated", ReceiptID: "receipt_abc"}}

	got := engine.resolveCarriedWindow(context.Background(), acceptancePrincipal(), request, request.PriorSubjectReceipts, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != WindowCarryHit || got.Window == nil || got.Window.Provenance != WindowQuestionStated {
		t.Fatalf("resolveCarriedWindow() = %#v, want a hit preserving question_stated provenance on the window itself", got)
	}
	entry := composeCarriedWindowEntry(got)
	if entry == nil || entry.Provenance != contractsv1.ContextFabricStructureQuestionStated {
		t.Fatalf("composeCarriedWindowEntry() = %#v, want provenance=question_stated to match the origin, not a hard-coded clarification_confirmed", entry)
	}
}

// TestResolveCarriedWindow_ReportsDepthExceededNotNoConfirmedWindow is codex
// R1 P2 (fixed): when carryChainMaxVisited candidates are consumed WITHOUT
// exhausting the frontier the walk was given, the unvisited remainder must
// be reported as miss_depth_exceeded, never silently reclassified as
// miss_no_confirmed_window (which claims an exhaustive search that never
// actually happened -- AGENTS.md's own "no measurement that did not happen
// may read as coverage" bar). carryChainMaxVisited+5 directly-referenced,
// loadable, taint-gate-passing results with no window and no further links
// exercises exactly the cap-mid-frontier case the fix targets.
func TestResolveCarriedWindow_ReportsDepthExceededNotNoConfirmedWindow(t *testing.T) {
	t.Parallel()
	results := make(map[string]InvestigationResult, carryChainMaxVisited+5)
	var receipts []BoundSubjectReceipt
	for i := 0; i < carryChainMaxVisited+5; i++ {
		id := fmt.Sprintf("result_prior_cap_%02d", i)
		r := validInvestigationResult()
		r.ResultID = id
		r.EffectiveEvidenceWindow = nil
		r.ConfirmedStructure = nil
		results[id] = r
		receipts = append(receipts, BoundSubjectReceipt{ResultID: id, ReceiptID: fmt.Sprintf("receipt_%02d", i)})
	}
	store := &staticResultStore{results: results}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	request.PriorSubjectReceipts = receipts

	got := engine.resolveCarriedWindow(context.Background(), acceptancePrincipal(), request, receipts, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != WindowCarryMissDepthExceeded || got.Window != nil {
		t.Fatalf("resolveCarriedWindow() = %#v, want miss_depth_exceeded (the visit cap left candidates unexplored), not miss_no_confirmed_window", got)
	}
	if len(store.gotIDs) != carryChainMaxVisited {
		t.Fatalf("store.gotIDs = %d Get calls, want exactly %d (the visit cap)", len(store.gotIDs), carryChainMaxVisited)
	}
}

// TestResolveCarriedWindow_ConflictingWindowsAtSameDepthMiss is codex R3 P1
// (fixed): the six prior-receipt fields validate independently of one
// another (canonicalizeStructure checks a kind/anchor/handle/candidate
// receipt against ITS OWN named prior result; canonicalizeEvidenceWindow
// checks a window receipt against its own; resolvePriorSubjectHints checks
// PriorSubjectReceipts against its own) -- nothing requires them to share an
// origin result. A request redeeming a candidate receipt from one prior
// result and a kind receipt from a DIFFERENT one, where the two prior
// results carry genuinely DIFFERENT confirmed windows, must not silently
// pick whichever one happened to load first: that could answer under an
// arbitrary one of two real but disagreeing time windows. Both are
// referenced directly (depth 0), so this must miss outright, never guess.
func TestResolveCarriedWindow_ConflictingWindowsAtSameDepthMiss(t *testing.T) {
	t.Parallel()
	resultA := validInvestigationResult()
	resultA.ResultID = "result_conflict_a"
	resultA.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{
		RelativeID: RelativeWindowTrailing30D, Provenance: WindowClarificationConfirmed,
	}
	resultB := validInvestigationResult()
	resultB.ResultID = "result_conflict_b"
	resultB.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{
		RelativeID: RelativeWindowTrailing90D, Provenance: WindowClarificationConfirmed,
	}
	store := &staticResultStore{results: map[string]InvestigationResult{
		"result_conflict_a": resultA, "result_conflict_b": resultB,
	}}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	// Two INDEPENDENTLY valid receipts naming two DIFFERENT prior results --
	// candidate receipt from A, kind receipt from B. Neither field vetoes
	// the other; both are legitimately reachable at depth 0.
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: "result_conflict_a", ReceiptID: "candr_a"}}
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: "result_conflict_b", ReceiptID: "kindr_b"}}

	got := engine.resolveCarriedWindow(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != WindowCarryMissConflictingWindows || got.Window != nil {
		t.Fatalf("resolveCarriedWindow() = %#v, want miss_conflicting_windows and no window: two disagreeing confirmed windows at the same depth must never resolve to a silent guess", got)
	}
	if entry := composeCarriedWindowEntry(got); entry != nil {
		t.Fatalf("composeCarriedWindowEntry(%#v) = %#v, want nil on a conflict", got, entry)
	}
}

// TestResolveCarriedWindow_EquivalentWindowsAtSameDepthHit is the negative
// case for the fix above: two same-depth candidates carrying the IDENTICAL
// window (same RelativeID) must still resolve cleanly -- equivalence, not
// mere plurality, is what triggers a conflict.
func TestResolveCarriedWindow_EquivalentWindowsAtSameDepthHit(t *testing.T) {
	t.Parallel()
	resultA := validInvestigationResult()
	resultA.ResultID = "result_agree_a"
	resultA.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{
		RelativeID: RelativeWindowTrailing90D, Provenance: WindowClarificationConfirmed,
	}
	resultB := validInvestigationResult()
	resultB.ResultID = "result_agree_b"
	resultB.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{
		RelativeID: RelativeWindowTrailing90D, Provenance: WindowQuestionStated,
	}
	store := &staticResultStore{results: map[string]InvestigationResult{
		"result_agree_a": resultA, "result_agree_b": resultB,
	}}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: "result_agree_a", ReceiptID: "candr_a"}}
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: "result_agree_b", ReceiptID: "kindr_b"}}

	got := engine.resolveCarriedWindow(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != WindowCarryHit || got.Window == nil || got.Window.RelativeID != RelativeWindowTrailing90D {
		t.Fatalf("resolveCarriedWindow() = %#v, want a clean hit: two candidates agreeing on the SAME window must not be reported as a conflict", got)
	}
}
