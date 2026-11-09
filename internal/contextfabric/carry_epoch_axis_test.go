package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	windowCarryMsg = "context fabric window carry"
	kindCarryMsg   = "context fabric kind carry"
	ledgerMsg      = "context fabric confirmed need ledger"
)

// epochGraphReader is graphReaderStub bound to a chosen graph epoch, so one
// fixture can be served before and after a lifecycle rebuild. It records the
// confirmed kind and the subject hints every resolution call received.
type epochGraphReader struct {
	graphReaderStub
	epoch int64
	kinds []*ConfirmedExpectedKind
	hints []int
}

func (g *epochGraphReader) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "stub-key", Epoch: g.epoch}, nil
}

func (g *epochGraphReader) ResolveSubjects(ctx context.Context, principal storage.Principal, request InvestigationRequest, interpreted InterpretedQuestion, binding ResolvedGraphBinding, confirmedKind *ConfirmedExpectedKind, confirmedAnchor *ConfirmedAnchorSelection, frame *QuestionFrame, scopeAnchorKind SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.kinds = append(g.kinds, confirmedKind)
	g.hints = append(g.hints, len(request.RequestedScope.SubjectHints))
	return g.graphReaderStub.ResolveSubjects(ctx, principal, request, interpreted, binding, confirmedKind, confirmedAnchor, frame, scopeAnchorKind)
}

// epochStore serves a chosen GraphEpoch per result id. A nil value is an
// unstamped row (pre-3898-S2, or a composition that never bound an epoch).
type epochStore struct {
	staticResultStore
	epochs map[string]*int64
}

func (s *epochStore) Get(ctx context.Context, principal storage.Principal, resultID string) (StoredInvestigationResult, error) {
	stored, err := s.staticResultStore.Get(ctx, principal, resultID)
	if err != nil {
		return stored, err
	}
	if epoch, ok := s.epochs[resultID]; ok {
		stored.GraphEpoch = epoch
	}
	return stored, nil
}

func epochPtr(v int64) *int64 { return &v }

var epochProject = SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}

func newEpochGraph(bound int64) *epochGraphReader {
	return &epochGraphReader{epoch: bound, graphReaderStub: graphReaderStub{
		resolution: SubjectResolution{
			Candidates: []SubjectCandidate{{
				ReceiptID: "receipt_turn_candidate1", Subject: epochProject, State: ResolutionCommitted,
				MatchReasons: []string{"Exact canonical subject hint matched the organization graph."}, Confidence: 0.97,
			}},
			Committed: []SubjectRef{epochProject},
		},
		context: emptyGraphContext(),
		bases:   provenCommitBases(epochProject),
	}}
}

// confirmedAxesPrior is a prior turn that confirmed expected_kind=team
// (kindr_ receipt) and a trailing_90d window (clarification_confirmed).
// Neither value names a graph id: both are closed-vocabulary members.
func confirmedAxesPrior(resultID string) InvestigationResult {
	prior := validInvestigationResult()
	prior.ResultID = resultID
	prior.Status = InvestigationClarificationRequired
	prior.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{
		RelativeID: RelativeWindowTrailing90D, Provenance: WindowClarificationConfirmed,
	}
	prior.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, resultID+"_older", "kindr_"+resultID),
	}
	return prior
}

// carryEpochTurn runs one real Investigate through the real slog sink and
// returns the result plus the LAST line per message, decoded.
func carryEpochTurn(t *testing.T, store InvestigationResultStore, graph *epochGraphReader, request InvestigationRequest) (InvestigationResult, map[string]map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	sink := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	engine, err := NewEngine(EngineDependencies{
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
		Results: store, Telemetry: sink,
	}, EngineOptions{
		ServiceVersion: "carry-epoch-axis-test",
		Now:            func() time.Time { return time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_turn_after" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	lines := map[string]map[string]any{}
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		var rec map[string]any
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		msg, _ := rec["msg"].(string)
		switch msg {
		case windowCarryMsg, kindCarryMsg, ledgerMsg:
			t.Logf("LINE %s", line)
		}
		lines[msg] = rec
	}
	return result, lines
}

func servedAxes(result InvestigationResult) (window string, carried []string, reasked []contractsv1.ContextFabricStructureNeedKind) {
	if w := result.EffectiveEvidenceWindow; w != nil {
		window = string(w.RelativeID) + "/" + string(w.Provenance)
	}
	for _, entry := range result.ConfirmedStructure {
		if entry.Source == contractsv1.ContextFabricStructureSourceCarried {
			carried = append(carried, string(entry.Member)+"="+entry.AppliedValue)
		}
	}
	if result.StructureNeeds != nil {
		reasked = result.StructureNeeds.Missing
	}
	return window, carried, reasked
}

// lineField asserts one decoded field. JSON numbers decode as float64, so an
// int expectation is compared as float64; a missing key fails.
func lineField(t *testing.T, lines map[string]map[string]any, msg, key string, want any) {
	t.Helper()
	rec, ok := lines[msg]
	if !ok {
		t.Errorf("no %q line on the trace", msg)
		return
	}
	got, ok := rec[key]
	if !ok {
		t.Errorf("%q line has no %q field (want %v)", msg, key, want)
		return
	}
	if n, isInt := want.(int); isInt {
		want = float64(n)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%q %s = %v, want %v", msg, key, got, want)
	}
}

func assertConfirmedAxesServed(t *testing.T, result InvestigationResult) {
	t.Helper()
	window, carried, reasked := servedAxes(result)
	if want := string(RelativeWindowTrailing90D) + "/" + string(WindowClarificationConfirmed); window != want {
		t.Errorf("served window = %s, want %s: the confirmed window must be served, not the inferred default", window, want)
	}
	if want := []string{"window=trailing_90d", "expected_kind=team"}; !reflect.DeepEqual(carried, want) {
		t.Errorf("carried entries = %v, want %v: both carries must be disclosed", carried, want)
	}
	for _, member := range reasked {
		if member == contractsv1.ContextFabricStructureNeedWindow {
			t.Errorf("structure_needs.missing names window: a window the user confirmed is asked again")
		}
	}
}

// TestCarry_KindAndWindowSurviveAGraphEpochRebuild drives one turn whose
// parent confirmed expected_kind=team and a trailing_90d window at graph
// epoch 7. The arms differ ONLY in the bound epoch and the parent's stamp.
func TestCarry_KindAndWindowSurviveAGraphEpochRebuild(t *testing.T) {
	t.Parallel()
	if !contractsv1.ValidContextFabricSubjectKind(contractsv1.ContextFabricSubjectTeam) || !ValidRelativeWindowID(RelativeWindowTrailing90D) {
		t.Fatal("fixture: carried kind and window must be closed-vocabulary members, not graph ids")
	}
	for _, arm := range []struct {
		name       string
		priorEpoch *int64
		bound      int64
		otherEpoch int
	}{
		{name: "control_same_epoch", priorEpoch: epochPtr(7), bound: 7, otherEpoch: 0},
		{name: "rebuild_between_turns", priorEpoch: epochPtr(7), bound: 8, otherEpoch: 1},
		{name: "unstamped_parent", priorEpoch: nil, bound: 8, otherEpoch: 1},
	} {
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()
			prior := confirmedAxesPrior("result_prior_epoch_7")
			store := &epochStore{
				staticResultStore: staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
				epochs:            map[string]*int64{prior.ResultID: arm.priorEpoch},
			}
			graph := newEpochGraph(arm.bound)
			request := validInvestigationRequest()
			request.ParentResultID = prior.ResultID

			result, lines := carryEpochTurn(t, store, graph, request)
			window, carried, reasked := servedAxes(result)
			t.Logf("OUTCOME arm=%s bound_epoch=%d status=%s window_carry=%v kind_carry=%v served_window=%s carried=%v reasked=%v",
				arm.name, arm.bound, result.Status, lines[windowCarryMsg]["outcome"], lines[kindCarryMsg]["outcome"], window, carried, reasked)

			lineField(t, lines, kindCarryMsg, "outcome", string(KindCarryHit))
			lineField(t, lines, windowCarryMsg, "outcome", string(WindowCarryHit))
			lineField(t, lines, kindCarryMsg, "carried_kind", string(contractsv1.ContextFabricSubjectTeam))
			lineField(t, lines, kindCarryMsg, "other_epoch_visited", arm.otherEpoch)
			lineField(t, lines, windowCarryMsg, "other_epoch_visited", arm.otherEpoch)
			assertConfirmedAxesServed(t, result)
			// The carried kind reaches the FRESH read on the bound epoch.
			sawTeam := false
			for _, kind := range graph.kinds {
				if kind != nil && kind.Kind == contractsv1.ContextFabricSubjectTeam {
					sawTeam = true
				}
			}
			if !sawTeam {
				t.Errorf("graph ResolveSubjects never received ConfirmedExpectedKind=team on epoch %d", arm.bound)
			}
		})
	}
}

// TestCarry_CrossEpochChainWalksPastAnOtherEpochHop: the parent (epoch 7)
// confirmed nothing itself; its stored parent (unstamped) holds the
// confirmations. Both hops are from another epoch than the bound 8.
func TestCarry_CrossEpochChainWalksPastAnOtherEpochHop(t *testing.T) {
	t.Parallel()
	origin := confirmedAxesPrior("result_origin_unstamped")
	hop := validInvestigationResult()
	hop.ResultID = "result_hop_epoch_7"
	hop.EffectiveEvidenceWindow = nil
	hop.ConfirmedStructure = nil
	store := &epochStore{
		staticResultStore: staticResultStore{
			results: map[string]InvestigationResult{origin.ResultID: origin, hop.ResultID: hop},
			parents: map[string]string{hop.ResultID: origin.ResultID},
		},
		epochs: map[string]*int64{origin.ResultID: nil, hop.ResultID: epochPtr(7)},
	}
	request := validInvestigationRequest()
	request.ParentResultID = hop.ResultID

	result, lines := carryEpochTurn(t, store, newEpochGraph(8), request)
	for _, msg := range []string{kindCarryMsg, windowCarryMsg} {
		lineField(t, lines, msg, "outcome", "hit")
		lineField(t, lines, msg, "chain_depth", 1)
		lineField(t, lines, msg, "via_stored_ancestry", true)
		lineField(t, lines, msg, "other_epoch_visited", 2)
	}
	assertConfirmedAxesServed(t, result)
}

// TestResolveCarriedKind_CrossEpochSiblingIsScannedForConflict: a sibling
// from another epoch is now READ, so its disagreeing kind is seen.
func TestResolveCarriedKind_CrossEpochSiblingIsScannedForConflict(t *testing.T) {
	t.Parallel()
	same := validInvestigationResult()
	same.ResultID = "result_same_epoch"
	same.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_turn_zero", "kindr_turn_zero_01"),
	}
	other := validInvestigationResult()
	other.ResultID = "result_other_epoch"
	other.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectProject, "result_turn_zero", "kindr_turn_zero_02"),
	}
	store := &perResultEpochStore{
		staticResultStore: staticResultStore{results: map[string]InvestigationResult{same.ResultID: same, other.ResultID: other}},
		epochs:            map[string]int64{same.ResultID: 0, other.ResultID: 7},
	}
	engine := buildCarryTestEngine(t, store)
	request := validInvestigationRequest()
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: same.ResultID, ReceiptID: "candr_same_01"}}
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: other.ResultID, ReceiptID: "kindr_other_01"}}

	got := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0}, nil, "")
	if got.Outcome != KindCarryMissConflictingKinds || got.Kind != "" {
		t.Fatalf("resolveCarriedKind() = %#v, want miss_conflicting_kinds and no kind: the other-epoch sibling disagrees", got)
	}
}

// TestResolveCarriedKind_NearestCrossEpochConfirmationWins: a depth-0
// carrier from another epoch is read, so its kind wins over a depth-1 one.
func TestResolveCarriedKind_NearestCrossEpochConfirmationWins(t *testing.T) {
	t.Parallel()
	breadcrumb := validInvestigationResult()
	breadcrumb.ResultID = "result_depth0_breadcrumb"
	breadcrumb.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{{
		Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: "trailing_30d",
		Source: contractsv1.ContextFabricStructureSourceReceipt, PriorResultID: "result_depth1",
		ReceiptID: "winr_depth1_01", Provenance: contractsv1.ContextFabricStructureClarificationConfirmed,
		Disposition: contractsv1.ContextFabricStructureDispositionApplied,
	}}
	nearer := validInvestigationResult()
	nearer.ResultID = "result_depth0_other_epoch"
	nearer.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectProject, "result_older", "kindr_older_01"),
	}
	deeper := validInvestigationResult()
	deeper.ResultID = "result_depth1"
	deeper.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_older", "kindr_older_02"),
	}
	store := &perResultEpochStore{
		staticResultStore: staticResultStore{results: map[string]InvestigationResult{
			breadcrumb.ResultID: breadcrumb, nearer.ResultID: nearer, deeper.ResultID: deeper,
		}},
		epochs: map[string]int64{breadcrumb.ResultID: 0, nearer.ResultID: 7, deeper.ResultID: 0},
	}
	engine := buildCarryTestEngine(t, store)
	request := validInvestigationRequest()
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: breadcrumb.ResultID, ReceiptID: "candr_depth0_01"}}
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: nearer.ResultID, ReceiptID: "kindr_depth0_01"}}

	got := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0}, nil, "")
	if got.Outcome != KindCarryHit || got.Kind != contractsv1.ContextFabricSubjectProject || got.ChainDepth != 0 {
		t.Fatalf("resolveCarriedKind() = %#v, want a depth-0 hit carrying project (nearest confirmation wins)", got)
	}
}

// TestResolveCarriedWindow_CrossEpochSiblingIsScannedForConflict: the window
// walk now reads an other-epoch sibling too, so two disagreeing confirmed
// windows at one depth fail closed instead of answering under the readable one.
func TestResolveCarriedWindow_CrossEpochSiblingIsScannedForConflict(t *testing.T) {
	t.Parallel()
	same := validInvestigationResult()
	same.ResultID = "result_same_epoch"
	same.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing30D, Provenance: WindowClarificationConfirmed}
	other := validInvestigationResult()
	other.ResultID = "result_other_epoch"
	other.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing90D, Provenance: WindowClarificationConfirmed}
	store := &perResultEpochStore{
		staticResultStore: staticResultStore{results: map[string]InvestigationResult{same.ResultID: same, other.ResultID: other}},
		epochs:            map[string]int64{same.ResultID: 0, other.ResultID: 7},
	}
	engine := buildCarryTestEngine(t, store)
	request := validInvestigationRequest()
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: same.ResultID, ReceiptID: "candr_same_01"}}
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: other.ResultID, ReceiptID: "kindr_other_01"}}

	got := engine.resolveCarriedWindow(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != WindowCarryMissConflictingWindows || got.Window != nil {
		t.Fatalf("resolveCarriedWindow() = %#v, want miss_conflicting_windows and no window", got)
	}
}

// TestCarry_StaleLedgerFallsThroughToTheLegacyWalk: the confirmed-need ledger
// stays epoch-gated as one unit (it can hold graph ids). On a rebuild it is
// dropped, and the kind is then carried by the legacy walk from the parent's
// own ConfirmedStructure. The ledger kind (project) and the legacy kind
// (team) differ on purpose, so the line shows which path served it.
func TestCarry_StaleLedgerFallsThroughToTheLegacyWalk(t *testing.T) {
	t.Parallel()
	for _, arm := range []struct {
		name          string
		bound         int64
		ledgerOutcome ConfirmedNeedLedgerOutcome
		carriedKind   contractsv1.ContextFabricSubjectKind
	}{
		{name: "control_same_epoch", bound: 7, ledgerOutcome: ConfirmedNeedLedgerHit, carriedKind: contractsv1.ContextFabricSubjectProject},
		{name: "rebuild_between_turns", bound: 8, ledgerOutcome: ConfirmedNeedLedgerDroppedStaleGraphEpoch, carriedKind: contractsv1.ContextFabricSubjectTeam},
	} {
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()
			parent, state, child := parentAndChildForLedgerTest(t)
			for i := range child.Conversation {
				child.Conversation[i].TurnID = "turn_ledger_" + string(rune('a'+i))
				child.Conversation[i].CreatedAt = time.Unix(int64(480+i), 0).UTC()
			}
			state.ConfirmedNeeds = []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectProject)}}
			parent.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
				confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_turn_one", "kindr_turn_one_01"),
			}
			store := &epochStore{
				staticResultStore: *ledgerTestStore(parent, state),
				epochs:            map[string]*int64{parent.ResultID: epochPtr(7)},
			}
			_, lines := carryEpochTurn(t, store, newEpochGraph(arm.bound), child)
			lineField(t, lines, ledgerMsg, "outcome", string(arm.ledgerOutcome))
			lineField(t, lines, kindCarryMsg, "outcome", string(KindCarryHit))
			lineField(t, lines, kindCarryMsg, "carried_kind", string(arm.carriedKind))
		})
	}
}

// TestCarry_RebuildStillStripsTheStaleSubjectReceipt: candidate ids keep the
// gate. The subject receipt on the other-epoch parent is skipped and adds no
// hint, while kind and window still carry through parent_result_id.
func TestCarry_RebuildStillStripsTheStaleSubjectReceipt(t *testing.T) {
	t.Parallel()
	prior := confirmedAxesPrior("result_prior_epoch_7")
	prior.SubjectResolution = SubjectResolution{
		Candidates: []SubjectCandidate{{
			ReceiptID: "receipt_abc12345", Subject: epochProject, State: ResolutionCommitted,
			MatchReasons: []string{"Exact canonical subject hint matched the organization graph."}, Confidence: 1,
		}},
		Committed: []SubjectRef{},
	}
	store := &epochStore{
		staticResultStore: staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		epochs:            map[string]*int64{prior.ResultID: epochPtr(7)},
	}
	graph := newEpochGraph(8)
	request := validInvestigationRequest()
	request.ParentResultID = prior.ResultID
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: "receipt_abc12345"}}

	result, lines := carryEpochTurn(t, store, graph, request)
	want := []contractsv1.ContextFabricPriorSubjectReceiptEntry{
		{PriorResultID: prior.ResultID, ReceiptID: "receipt_abc12345", Disposition: contractsv1.ContextFabricPriorSubjectReceiptSkippedStaleGraphEpoch},
	}
	if !reflect.DeepEqual(result.SubjectResolution.PriorSubjectReceiptDispositions, want) {
		t.Errorf("PriorSubjectReceiptDispositions = %#v, want %#v", result.SubjectResolution.PriorSubjectReceiptDispositions, want)
	}
	for i, n := range graph.hints {
		if n != 0 {
			t.Errorf("ResolveSubjects call %d got %d subject hints, want 0: a stale receipt must add no hint", i, n)
		}
	}
	for _, msg := range []string{kindCarryMsg, windowCarryMsg} {
		lineField(t, lines, msg, "outcome", "hit")
		lineField(t, lines, msg, "seed_source", string(CarrySeedParentField))
	}
	assertConfirmedAxesServed(t, result)
}

// TestCarry_RebuildDoesNotBypassTheSameQuestionCheck: a parent from another
// epoch that answered a different question is refused for drift.
func TestCarry_RebuildDoesNotBypassTheSameQuestionCheck(t *testing.T) {
	t.Parallel()
	prior := confirmedAxesPrior("result_prior_epoch_7")
	prior.Question = "Which repositories does the Ops Team own?"
	store := &epochStore{
		staticResultStore: staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		epochs:            map[string]*int64{prior.ResultID: epochPtr(7)},
	}
	request := validInvestigationRequest()
	request.ParentResultID = prior.ResultID

	result, lines := carryEpochTurn(t, store, newEpochGraph(8), request)
	lineField(t, lines, kindCarryMsg, "outcome", string(KindCarryMissQuestionDrift))
	lineField(t, lines, windowCarryMsg, "outcome", string(WindowCarryMissQuestionDrift))
	if _, carried, _ := servedAxes(result); len(carried) != 0 {
		t.Errorf("carried entries = %v, want none for a drifted parent", carried)
	}
}

// TestResolveCarriedPlan_KeepsTheEpochGate pins the plan axis decision: the
// plan carry stays epoch-gated (its file states the gate protects cohort
// comparability across turns).
func TestResolveCarriedPlan_KeepsTheEpochGate(t *testing.T) {
	t.Parallel()
	prior := validInvestigationResult()
	prior.ResultID = "result_plan_carrier"
	prior.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{
		Family: QuestionFamilyGroupedCohortStatus, FamilyVersion: QuestionFamilyTableVersion, GroupKind: contractsv1.ContextFabricSubjectTeam,
	}
	store := &perResultEpochStore{
		staticResultStore: staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		epochs:            map[string]int64{prior.ResultID: 7},
	}
	engine := buildCarryTestEngine(t, store)
	request := validInvestigationRequest()
	request.ParentResultID = prior.ResultID

	if got := engine.resolveCarriedPlan(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 7}, nil); got.Outcome != PlanCarryHit || got.Family != QuestionFamilyGroupedCohortStatus {
		t.Fatalf("same epoch: resolveCarriedPlan() = %#v, want a hit carrying %q", got, QuestionFamilyGroupedCohortStatus)
	}
	if got := engine.resolveCarriedPlan(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 8}, nil); got.Outcome != PlanCarryMissStaleGraphEpoch || got.Family != "" {
		t.Fatalf("other epoch: resolveCarriedPlan() = %#v, want miss_stale_graph_epoch and no family", got)
	}
}
