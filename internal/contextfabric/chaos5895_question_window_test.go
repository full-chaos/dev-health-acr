package contextfabric

// CHAOS-5895: the window a caller confirmed governs the time of the identical
// question it was offered for.
//
// INVARIANT. A turn that redeems a window offer and asks the offering result
// the identical question executes under that confirmed window -- whatever else
// the turn redeems or states, and whether or not it names the offering result
// again as its parent -- so a sampled interpreted axis never vetoes that
// confirmation to nothing. A turn the confirmation does not govern (a changed
// question, a parent naming another result, a carrier from another graph epoch
// or one recording a non-current axis) keeps its typed outcome: the fresh
// reading's axis-conflict veto, or the continuation refusal. The window the
// confirmed-need ledger remembers from the answered parent is the same
// confirmation carried one turn on, and the same rule decides its axis.
//
// The domain is ENUMERATED from production vocabularies, every cell is executed
// through Engine.Investigate, and the expected outcome of every cell is stated
// by questionWindowOracle -- written from the invariant above, never by calling
// the code under test.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// questionWindowSelection is what the turn does BESIDE redeeming the window.
type questionWindowSelection string

const (
	selectionNone        questionWindowSelection = "none"
	selectionKindReceipt questionWindowSelection = "kind_receipt"
	selectionStatedScope questionWindowSelection = "stated_scope"
	// selectionEmptyHints is the threaded caller's own shape: a subject-hint
	// list that is present and empty.
	selectionEmptyHints questionWindowSelection = "empty_subject_hints"
)

func questionWindowSelections() []questionWindowSelection {
	return []questionWindowSelection{selectionNone, selectionEmptyHints, selectionKindReceipt, selectionStatedScope}
}

type questionWindowCarrier string

const (
	carrierCurrent              questionWindowCarrier = "current"
	carrierNonCurrent           questionWindowCarrier = "range_recorded"
	carrierEpochStale           questionWindowCarrier = "epoch_stale"
	questionIdentical                                 = "identical"
	questionChanged                                   = "changed"
	outcomeConfirmed                                  = "served_confirmed_window"
	outcomeFreshAgreed                                = "served_fresh_current"
	outcomeAxisVeto                                   = "refused_window_axis_conflict"
	outcomeContinuation                               = "refused_continuation_context_unverifiable"
	questionWindowOtherID                             = "result_5895_other_turn_01"
	questionWindowKindReceiptID                       = "kindr_5895resolvable0001"
)

func questionWindowCarriers() []questionWindowCarrier {
	return []questionWindowCarrier{carrierCurrent, carrierNonCurrent, carrierEpochStale}
}

type questionWindowCell struct {
	Parent    ContinuationParentReference
	Selection questionWindowSelection
	Question  string
	Carrier   questionWindowCarrier
	Fresh     contractsv1.ContextFabricTemporalAxis
}

func (c questionWindowCell) key() string {
	return fmt.Sprintf("%s/%s/%s/%s/%s", c.Parent, c.Selection, c.Question, c.Carrier, c.Fresh)
}

// questionWindowDomain is the whole domain, every axis read from its producer.
func questionWindowDomain() []questionWindowCell {
	axes := contractsv1.ContextFabricTemporalAxisVocabulary()
	var cells []questionWindowCell
	for _, parent := range continuationParentReferences() {
		for _, selection := range questionWindowSelections() {
			for _, question := range []string{questionIdentical, questionChanged} {
				for _, carrier := range questionWindowCarriers() {
					for _, fresh := range axes {
						cells = append(cells, questionWindowCell{parent, selection, question, carrier, fresh})
					}
				}
			}
		}
	}
	return cells
}

// questionWindowOracle states, per cell, what the invariant requires. It
// returns the served outcome and a one-line reason.
func questionWindowOracle(c questionWindowCell) (string, string) {
	governs := c.Parent != ContinuationParentOtherResult && c.Question == questionIdentical && c.Carrier != carrierEpochStale
	windowOnly := c.Parent != ContinuationParentOtherResult && (c.Selection == selectionNone || c.Selection == selectionEmptyHints)
	switch {
	case windowOnly && c.Carrier == carrierEpochStale:
		return outcomeContinuation, "window-only turn whose carrier fails the taint gate: continuation refused on its carrier"
	case windowOnly && governs && c.Carrier == carrierNonCurrent:
		return outcomeContinuation, "window-only turn on the identical question whose carrier recorded another axis: continuation refused on its carrier"
	case c.Fresh == contractsv1.ContextFabricTemporalCurrent:
		return outcomeFreshAgreed, "fresh axis is current: no disagreement with the confirmed window"
	case governs && c.Carrier == carrierCurrent:
		return outcomeConfirmed, "identical question to the offering current-axis carrier: the confirmed window governs, the sampled axis is a diagnostic"
	default:
		return outcomeAxisVeto, "confirmation does not govern this turn and the fresh axis moved off current: the fresh reading's typed veto"
	}
}

func questionWindowFreshTime(axis contractsv1.ContextFabricTemporalAxis) TimeContext {
	switch axis {
	case contractsv1.ContextFabricTemporalCurrent:
		return TimeContext{Axis: TemporalCurrent}
	case contractsv1.ContextFabricTemporalRange:
		return TimeContext{Axis: TemporalRange, Start: &axis5582RangeStart, End: &axis5582RangeEnd}
	default:
		return TimeContext{Axis: axis, AsOf: &axis5582AsOf}
	}
}

// questionWindowRun executes one cell through the engine and classifies what
// it served from the served document and the emitted decision line.
func questionWindowRun(t *testing.T, c questionWindowCell) (string, axis5582Run) {
	t.Helper()
	question := validInvestigationRequest().Question
	prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	prior.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{{
			ReceiptID: questionWindowKindReceiptID, OptionID: "opt_team",
			Label: "a team", Kind: SubjectTeam, OfferSource: "engine",
		}},
	}
	if c.Carrier == carrierNonCurrent {
		prior.Interpretation.TimeContext = TimeContext{Axis: TemporalRange, Start: &axis5582RangeStart, End: &axis5582RangeEnd}
	}
	other := continuationPrior(t, questionWindowOtherID, question, QuestionFamilyDiscoveredCohortRanking, "")
	store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior, other.ResultID: other}}
	if c.Carrier == carrierEpochStale {
		stale := int64(97)
		store.graphEpoch = &stale
	}
	request := continuationRequest(question)
	switch c.Parent {
	case ContinuationParentWindowReceiptResult:
		request.ParentResultID = prior.ResultID
	case ContinuationParentOtherResult:
		request.ParentResultID = other.ResultID
	}
	switch c.Selection {
	case selectionKindReceipt:
		request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: questionWindowKindReceiptID}}
	case selectionStatedScope:
		request.RequestedScope.RepositorySlugs = []string{"widget-service"}
	case selectionEmptyHints:
		request.RequestedScope.SubjectHints = []contractsv1.ContextFabricSubjectHint{}
	}
	if c.Question == questionChanged {
		request.Question = question + " Include the drivers."
	}
	run := axis5582Investigate(t, store,
		freshAxisInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam, timeContext: questionWindowFreshTime(c.Fresh)}, request)
	if run.err != nil {
		return "error:" + run.err.Error(), run
	}
	line := run.soleDecisionLine(t)
	switch {
	case axisConflictLimitationServed(run.result):
		return outcomeAxisVeto, run
	case run.result.RefusalBasis == contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable:
		return outcomeContinuation, run
	case run.result.RefusalBasis != "":
		return "refused_" + string(run.result.RefusalBasis), run
	case line["interpreted_axis_outcome"] == string(ContinuationAxisOverriddenByReceipt) && line["executed_axis"] == "current":
		return outcomeConfirmed, run
	case line["interpreted_axis_outcome"] == string(ContinuationAxisAgreed) && line["executed_axis"] == "current":
		return outcomeFreshAgreed, run
	default:
		return fmt.Sprintf("unclassified(outcome=%v executed=%v status=%s)", line["interpreted_axis_outcome"], line["executed_axis"], run.result.Status), run
	}
}

// TestQuestionWindow_DomainIsTheProductOfTheProductionVocabularies pins the
// domain's size to the product of the vocabularies it is generated from, so a
// new parent relation or temporal axis joins the enumeration by existing.
func TestQuestionWindow_DomainIsTheProductOfTheProductionVocabularies(t *testing.T) {
	t.Parallel()
	want := len(continuationParentReferences()) * len(questionWindowSelections()) * 2 * len(questionWindowCarriers()) * len(contractsv1.ContextFabricTemporalAxisVocabulary())
	cells := questionWindowDomain()
	if len(cells) != want || want == 0 {
		t.Fatalf("domain = %d cells, want %d", len(cells), want)
	}
	seen := map[string]bool{}
	for _, c := range cells {
		if seen[c.key()] {
			t.Fatalf("duplicate cell %s", c.key())
		}
		seen[c.key()] = true
	}
	if len(continuationParentReferences()) != 3 {
		t.Errorf("parent reference vocabulary = %v, want the three relations the oracle decides", continuationParentReferences())
	}
}

// TestQuestionWindow_EveryCellServesWhatTheInvariantRequires executes the whole
// domain through Engine.Investigate.
func TestQuestionWindow_EveryCellServesWhatTheInvariantRequires(t *testing.T) {
	t.Parallel()
	tally := map[string]int{}
	var failures []string
	for _, c := range questionWindowDomain() {
		want, reason := questionWindowOracle(c)
		got, run := questionWindowRun(t, c)
		tally[want]++
		if got != want {
			failures = append(failures, fmt.Sprintf("%s: got %s, want %s (%s)", c.key(), got, want, reason))
			continue
		}
		line := run.soleDecisionLine(t)
		// The confirmation is a fact about the question and its carrier, read
		// before and apart from the carrier's recorded axis.
		wantConfirmed := c.Parent != ContinuationParentOtherResult && c.Question == questionIdentical && c.Carrier != carrierEpochStale
		if line["question_window_confirmed"] != wantConfirmed {
			failures = append(failures, fmt.Sprintf("%s: question_window_confirmed = %v, want %v", c.key(), line["question_window_confirmed"], wantConfirmed))
		}
		if line["parent_reference"] != string(c.Parent) {
			failures = append(failures, fmt.Sprintf("%s: parent_reference = %v, want %s", c.key(), line["parent_reference"], c.Parent))
		}
		// A served turn carries the confirmed window on the served document,
		// with the receipt's provenance -- never a fresh or default one.
		if served := want == outcomeConfirmed || want == outcomeFreshAgreed; served {
			window := run.result.EffectiveEvidenceWindow
			if window == nil || window.Provenance != WindowClarificationConfirmed || window.RelativeID != RelativeWindowTrailing90D {
				failures = append(failures, fmt.Sprintf("%s: served window = %+v, want the confirmed trailing-90-day window", c.key(), window))
			}
		}
		if line["interpreted_axis"] != string(c.Fresh) {
			failures = append(failures, fmt.Sprintf("%s: interpreted_axis = %v, want %s", c.key(), line["interpreted_axis"], c.Fresh))
		}
	}
	keys := make([]string, 0, len(tally))
	for k := range tally {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var summary []string
	for _, k := range keys {
		summary = append(summary, fmt.Sprintf("%s=%d", k, tally[k]))
	}
	t.Logf("enumerated %d cells: %s", len(questionWindowDomain()), strings.Join(summary, " "))
	for _, f := range failures {
		t.Error(f)
	}
	// Every outcome class the oracle names is reached by at least one cell.
	for _, class := range []string{outcomeConfirmed, outcomeFreshAgreed, outcomeAxisVeto, outcomeContinuation} {
		if tally[class] == 0 {
			t.Errorf("outcome class %s reached by no cell", class)
		}
	}
}

// failingGetStore fails every Get for one id and serves the rest.
type failingGetStore struct {
	*staticResultStore
	failID string
}

func (s failingGetStore) Get(ctx context.Context, principal storage.Principal, id string) (StoredInvestigationResult, error) {
	if id == s.failID {
		return StoredInvestigationResult{}, fmt.Errorf("store unavailable (fixture)")
	}
	return s.staticResultStore.Get(ctx, principal, id)
}

// nilEpochStore serves every result with no stored graph epoch, the shape a
// store that keeps none returns: the epoch cannot be proven.
type nilEpochStore struct{ *staticResultStore }

func (s nilEpochStore) Get(ctx context.Context, principal storage.Principal, id string) (StoredInvestigationResult, error) {
	stored, err := s.staticResultStore.Get(ctx, principal, id)
	stored.GraphEpoch = nil
	return stored, err
}

// TestQuestionWindow_ConfirmationReadsTheCarrierAndReportsEveryExit drives
// confirmQuestionWindow directly for the exits the engine cannot reach (the
// window redemption reads the same carrier first and vetoes on its failure),
// each with the trace it leaves.
func TestQuestionWindow_ConfirmationReadsTheCarrierAndReportsEveryExit(t *testing.T) {
	t.Parallel()
	question := validInvestigationRequest().Question
	prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	base := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
	withCarrierStates(t, base)
	binding := ResolvedGraphBinding{Epoch: 0}
	for _, tc := range []struct {
		name          string
		store         InvestigationResultStore
		mutate        func(*InvestigationRequest)
		wantRead      ContinuationCarrierRead
		wantAxis      contractsv1.ContextFabricTemporalAxis
		wantConfirmed bool
	}{
		{"read_ok_identical", base, nil, ContinuationCarrierReadOK, TemporalCurrent, true},
		{"read_failed", failingGetStore{base, prior.ResultID}, nil, ContinuationCarrierReadFailed, "", false},
		{"no_store", nil, nil, "", "", false},
		{"changed_question", base, func(r *InvestigationRequest) { r.Question += " Include the drivers." }, ContinuationCarrierReadOK, TemporalCurrent, false},
		{"parent_other", base, func(r *InvestigationRequest) { r.ParentResultID = continuationOlderID }, "", "", false},
		{"parent_same", base, func(r *InvestigationRequest) { r.ParentResultID = " " + continuationPriorID + " " }, ContinuationCarrierReadOK, TemporalCurrent, true},
		{"parent_case_differs", base, func(r *InvestigationRequest) { r.ParentResultID = strings.ToUpper(continuationPriorID) }, "", "", false},
		{"two_receipts", base, func(r *InvestigationRequest) {
			r.PriorWindowReceipts = append(r.PriorWindowReceipts, BoundSubjectReceipt{ResultID: continuationOlderID, ReceiptID: continuationReceiptID})
		}, "", "", false},
	} {
		tc := tc
		// Sequential: the cells share one recording store.
		t.Run(tc.name, func(t *testing.T) {
			request := continuationRequest(question)
			if tc.mutate != nil {
				tc.mutate(&request)
			}
			engine := &Engine{}
			if tc.store != nil {
				engine.results = tc.store
			}
			got := engine.confirmQuestionWindow(context.Background(), acceptancePrincipal(), request, binding, newWindowContinuationDecision(request))
			if got.CarrierRead != tc.wantRead || got.CarriedAxis != tc.wantAxis || got.QuestionWindowConfirmed != tc.wantConfirmed {
				t.Fatalf("read=%q axis=%q confirmed=%v, want read=%q axis=%q confirmed=%v",
					got.CarrierRead, got.CarriedAxis, got.QuestionWindowConfirmed, tc.wantRead, tc.wantAxis, tc.wantConfirmed)
			}
			if got.Disposition != ContinuationNotApplicable || got.TransitionEstablished {
				t.Fatalf("confirmation touched the reading's decision: disposition=%q transition=%v", got.Disposition, got.TransitionEstablished)
			}
		})
	}
	unproven := (&Engine{results: nilEpochStore{base}}).confirmQuestionWindow(context.Background(), acceptancePrincipal(), continuationRequest(question), binding, newWindowContinuationDecision(continuationRequest(question)))
	if unproven.CarrierRead != ContinuationCarrierReadOK || unproven.CarriedAxis != "" || unproven.QuestionWindowConfirmed {
		t.Fatalf("no stored epoch: read=%q axis=%q confirmed=%v, want read, empty axis, unconfirmed", unproven.CarrierRead, unproven.CarriedAxis, unproven.QuestionWindowConfirmed)
	}
	stale := int64(97)
	staleStore := &staticResultStore{results: base.results, states: base.states, graphEpoch: &stale}
	got := (&Engine{results: staleStore}).confirmQuestionWindow(context.Background(), acceptancePrincipal(), continuationRequest(question), binding, newWindowContinuationDecision(continuationRequest(question)))
	if got.CarrierRead != ContinuationCarrierReadOK || got.CarriedAxis != "" || got.QuestionWindowConfirmed {
		t.Fatalf("stale epoch: read=%q axis=%q confirmed=%v, want read, empty axis, unconfirmed", got.CarrierRead, got.CarriedAxis, got.QuestionWindowConfirmed)
	}
}

// TestQuestionWindow_ParentReferenceDomain is the classifier's whole input
// domain, each cell with a literal expectation.
func TestQuestionWindow_ParentReferenceDomain(t *testing.T) {
	t.Parallel()
	one := []BoundSubjectReceipt{{ResultID: "result_a", ReceiptID: continuationReceiptID}}
	for _, tc := range []struct {
		name       string
		parent     string
		receipts   []BoundSubjectReceipt
		want       ContinuationParentReference
		wantWindow bool
	}{
		{"absent", "", one, ContinuationParentAbsent, true},
		{"whitespace_only", "   ", one, ContinuationParentAbsent, true},
		{"same", "result_a", one, ContinuationParentWindowReceiptResult, true},
		{"same_padded", "\tresult_a ", one, ContinuationParentWindowReceiptResult, true},
		{"same_receipt_id_padded", "result_a", []BoundSubjectReceipt{{ResultID: " result_a", ReceiptID: continuationReceiptID}}, ContinuationParentWindowReceiptResult, true},
		{"case_differs", "RESULT_A", one, ContinuationParentOtherResult, false},
		{"prefix", "result_", one, ContinuationParentOtherResult, false},
		{"other", "result_b", one, ContinuationParentOtherResult, false},
		{"no_receipt", "result_a", nil, ContinuationParentOtherResult, false},
		{"empty_receipt_list", "result_a", []BoundSubjectReceipt{}, ContinuationParentOtherResult, false},
		{"blank_receipt_result", "result_a", []BoundSubjectReceipt{{ResultID: " ", ReceiptID: continuationReceiptID}}, ContinuationParentOtherResult, false},
		{"two_receipts_same_result", "result_a", []BoundSubjectReceipt{one[0], one[0]}, ContinuationParentOtherResult, false},
		{"same_beside_blank_receipt", "result_a", []BoundSubjectReceipt{one[0], {ResultID: " ", ReceiptID: continuationReceiptID}}, ContinuationParentWindowReceiptResult, true},
		{"two_receipts_absent_parent", "", []BoundSubjectReceipt{one[0], {ResultID: "result_b", ReceiptID: continuationReceiptID}}, ContinuationParentAbsent, false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request := InvestigationRequest{ParentResultID: tc.parent, PriorWindowReceipts: tc.receipts}
			if got := continuationParentReferenceOf(request); got != tc.want {
				t.Fatalf("parent reference = %q, want %q", got, tc.want)
			}
			if !ValidContinuationParentReference(continuationParentReferenceOf(request)) {
				t.Fatalf("classifier produced a non-member")
			}
			_, gotWindow := windowReceiptCarrierID(request)
			if gotWindow != tc.wantWindow {
				t.Fatalf("windowReceiptCarrierID ok = %v, want %v", gotWindow, tc.wantWindow)
			}
			_, gotOnly := windowOnlyReferencedResultID(request)
			if gotOnly != tc.wantWindow {
				t.Fatalf("windowOnlyReferencedResultID ok = %v, want %v", gotOnly, tc.wantWindow)
			}
		})
	}
	if ValidContinuationParentReference("invented") || ValidContinuationParentReference("") {
		t.Fatal("membership admits a non-member")
	}
}

// TestQuestionWindow_AByteDifferentWindowOnlyFollowUpTakesTheFreshPath pins
// the path the confirmed-window rule does NOT widen: a follow-up whose text
// differs from the answered parent's, redeeming no window offer, continues no
// confirmed window. The remembered window is not admitted (the ledger refuses a
// changed question), no axis decision runs, and the fresh reading governs --
// the path it takes on main.
func TestQuestionWindow_AByteDifferentWindowOnlyFollowUpTakesTheFreshPath(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	_, two, _ := windowLedgerChain(t, h, "byte_different")
	h.historical = true
	axesMark, decisionsMark := len(h.telemetry.rememberedWindowAxes), len(h.telemetry.windowContinuationDecisions)
	request := continuingNeedTurn(needTurnRequest("request_need_byte_different_three", false), two.result.ResultID)
	request.Question += " Over another window."
	request.RequestedScope.SubjectHints = []contractsv1.ContextFabricSubjectHint{}
	three := h.turn(request, committingNeedResponse())
	if len(three.ledgers) != 1 || three.ledgers[0].Outcome == ConfirmedNeedLedgerHit {
		t.Fatalf("ledger = %#v, want one line refusing the changed question", three.ledgers)
	}
	if got := len(h.telemetry.rememberedWindowAxes) - axesMark; got != 0 {
		t.Fatalf("remembered-window axis decisions = %d, want 0", got)
	}
	if got := len(h.telemetry.windowContinuationDecisions) - decisionsMark; got != 0 {
		t.Fatalf("window continuation decisions = %d, want 0", got)
	}
	if window := three.result.EffectiveEvidenceWindow; window != nil && window.Provenance == WindowClarificationConfirmed {
		t.Fatalf("served a confirmed window %#v on a turn that confirmed none", window)
	}
}

// TestQuestionWindow_RememberedWindowAxisDomain is the remembered-window axis
// decision's input domain: ledger application × parent read × parent axis ×
// fresh axis, each cell with a literal outcome.
func TestQuestionWindow_RememberedWindowAxisDomain(t *testing.T) {
	t.Parallel()
	question := validInvestigationRequest().Question
	prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	ranged := prior
	ranged.ResultID = continuationOlderID
	ranged.Interpretation.TimeContext = TimeContext{Axis: TemporalRange, Start: &axis5582RangeStart, End: &axis5582RangeEnd}
	store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior, ranged.ResultID: ranged}}
	window := &contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing90D, Provenance: WindowClarificationConfirmed}
	applied := func(source string) ledgerWindowApplication {
		return ledgerWindowApplication{Present: true, Decision: ConfirmedNeedLedgerWindowApplied, SourceResultID: source, Window: window}
	}
	current := TimeContext{Axis: TemporalCurrent}
	drift := TimeContext{Axis: TemporalRange, Start: &axis5582RangeStart, End: &axis5582RangeEnd}
	for _, tc := range []struct {
		name      string
		store     InvestigationResultStore
		app       ledgerWindowApplication
		fresh     TimeContext
		request   TimeContext
		committed bool
		want      rememberedWindowAxisDecision
	}{
		{"applied/current_parent/drift", store, applied(prior.ResultID), drift, current, true,
			rememberedWindowAxisDecision{prior.ResultID, ContinuationCarrierReadOK, TemporalRange, TemporalCurrent, TemporalCurrent, ContinuationAxisOverriddenByReceipt}},
		{"applied/current_parent/fresh_current", store, applied(prior.ResultID), current, current, true,
			rememberedWindowAxisDecision{prior.ResultID, ContinuationCarrierReadOK, TemporalCurrent, TemporalCurrent, TemporalCurrent, ContinuationAxisAgreed}},
		{"applied/range_parent/drift", store, applied(ranged.ResultID), drift, current, true,
			rememberedWindowAxisDecision{ranged.ResultID, ContinuationCarrierReadOK, TemporalRange, TemporalRange, TemporalRange, ContinuationAxisVetoed}},
		{"applied/read_failed/drift", failingGetStore{store, prior.ResultID}, applied(prior.ResultID), drift, current, true,
			rememberedWindowAxisDecision{prior.ResultID, ContinuationCarrierReadFailed, TemporalRange, "", TemporalRange, ContinuationAxisVetoed}},
		{"applied/no_store/drift", nil, applied(prior.ResultID), drift, current, true,
			rememberedWindowAxisDecision{prior.ResultID, ContinuationCarrierNotRead, TemporalRange, "", TemporalRange, ContinuationAxisVetoed}},
		{"not_applied/drift", store, ledgerWindowApplication{Present: true, Decision: ConfirmedNeedLedgerWindowNotApplicable, SourceResultID: prior.ResultID}, drift, current, true,
			rememberedWindowAxisDecision{prior.ResultID, ContinuationCarrierNotRead, TemporalRange, "", TemporalRange, ContinuationAxisVetoed}},
		{"applied_without_window/drift", store, ledgerWindowApplication{Present: true, Decision: ConfirmedNeedLedgerWindowApplied, SourceResultID: prior.ResultID}, drift, current, true,
			rememberedWindowAxisDecision{prior.ResultID, ContinuationCarrierNotRead, TemporalRange, "", TemporalRange, ContinuationAxisVetoed}},
		{"applied/request_not_current", store, applied(prior.ResultID), drift, TimeContext{Axis: TemporalValidTime, AsOf: &axis5582AsOf}, true,
			rememberedWindowAxisDecision{prior.ResultID, ContinuationCarrierReadOK, TemporalRange, TemporalCurrent, TemporalRange, ContinuationAxisVetoed}},
		{"applied/uncommitted", store, applied(prior.ResultID), drift, current, false,
			rememberedWindowAxisDecision{prior.ResultID, ContinuationCarrierReadOK, TemporalRange, TemporalCurrent, TemporalRange, ContinuationAxisNotEvaluated}},
	} {
		tc := tc
		// Sequential: the cells share one recording store.
		t.Run(tc.name, func(t *testing.T) {
			engine := &Engine{}
			if tc.store != nil {
				engine.results = tc.store
			}
			_, got := engine.decideRememberedWindowAxis(context.Background(), acceptancePrincipal(), tc.app, tc.fresh, true, tc.request, tc.committed)
			if got != tc.want {
				t.Fatalf("decision = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestQuestionWindow_RememberedWindowAxisLineGuardsEveryClosedField drives the
// production emitter with a value outside each closed field's vocabulary and
// requires the unrecognised token in that field alone.
func TestQuestionWindow_RememberedWindowAxisLineGuardsEveryClosedField(t *testing.T) {
	t.Parallel()
	valid := rememberedWindowAxisDecision{SourceResultID: "result_a", CarrierRead: ContinuationCarrierReadOK, InterpretedAxis: TemporalRange, CarriedAxis: TemporalCurrent, DecidedAxis: TemporalCurrent, Outcome: ContinuationAxisOverriddenByReceipt}
	for _, tc := range []struct {
		key    string
		invent func(*rememberedWindowAxisDecision)
	}{
		{"carrier_read", func(d *rememberedWindowAxisDecision) { d.CarrierRead = "invented" }},
		{"interpreted_axis", func(d *rememberedWindowAxisDecision) { d.InterpretedAxis = "invented" }},
		{"carried_axis", func(d *rememberedWindowAxisDecision) { d.CarriedAxis = "invented" }},
		{"decided_axis", func(d *rememberedWindowAxisDecision) { d.DecidedAxis = "invented" }},
		{"outcome", func(d *rememberedWindowAxisDecision) { d.Outcome = "invented" }},
	} {
		tc := tc
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			decision := valid
			tc.invent(&decision)
			var buf bytes.Buffer
			NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))).RecordRememberedWindowAxis(context.Background(), acceptancePrincipal(), decision)
			var line map[string]any
			if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
				t.Fatalf("line is not JSON: %v", err)
			}
			for _, key := range []string{"carrier_read", "interpreted_axis", "carried_axis", "decided_axis", "outcome"} {
				unrecognised := line[key] == continuationTelemetryUnrecognised
				if unrecognised != (key == tc.key) {
					t.Errorf("%s = %v with %s invented", key, line[key], tc.key)
				}
			}
		})
	}
}
