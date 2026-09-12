package contextfabric

// CHAOS-5582 input domains: every input the axis decision, the carrier-axis
// admission rule and the decision line read, each cell executed with a LITERAL
// expectation stated in the row -- never computed from the code under test.

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestCHAOS5582_AxisDecisionInputDomain(t *testing.T) {
	t.Parallel()
	asOf := time.Unix(90, 0).UTC()
	carried := &continuationCarriedContext{Family: QuestionFamilyGroupedCohortStatus, GroupKind: contractsv1.ContextFabricSubjectTeam}
	applied := func(carriedAxis contractsv1.ContextFabricTemporalAxis) windowContinuationDecision {
		return windowContinuationDecision{Observed: true, Disposition: ContinuationApplied, Accepted: carried, Carried: carried, CarriedAxis: carriedAxis, TransitionEstablished: true}
	}
	current := TimeContext{Axis: TemporalCurrent}
	rangeFresh := TimeContext{Axis: TemporalRange, Start: &axis5582RangeStart, End: &axis5582RangeEnd}
	requestWithWindow := TimeContext{Axis: TemporalCurrent, AsOf: &asOf, EvidenceWindow: &RequestedEvidenceWindow{RelativeID: RelativeWindowTrailing90D}}

	for _, tc := range []struct {
		name            string
		decision        windowContinuationDecision
		fresh           TimeContext
		freshAnswerable bool
		request         TimeContext
		committed       bool
		wantAxis        contractsv1.ContextFabricTemporalAxis
		wantOutcome     ContinuationAxisOutcome
		wantAsOf        *time.Time
		wantNoBounds    bool
	}{
		// fresh axis x {canonical current, every drifted member, absent, out of vocabulary}
		{"fresh/current", applied(TemporalCurrent), current, true, current, true, TemporalCurrent, ContinuationAxisAgreed, nil, true},
		{"fresh/range", applied(TemporalCurrent), rangeFresh, true, current, true, TemporalCurrent, ContinuationAxisOverriddenByReceipt, nil, true},
		{"fresh/valid_time", applied(TemporalCurrent), TimeContext{Axis: TemporalValidTime, AsOf: &axis5582AsOf}, true, current, true, TemporalCurrent, ContinuationAxisOverriddenByReceipt, nil, true},
		{"fresh/observed_time", applied(TemporalCurrent), TimeContext{Axis: TemporalObservedTime, AsOf: &axis5582AsOf}, true, current, true, TemporalCurrent, ContinuationAxisOverriddenByReceipt, nil, true},
		{"fresh/absent", applied(TemporalCurrent), TimeContext{}, true, current, true, TemporalCurrent, ContinuationAxisOverriddenByReceipt, nil, true},
		{"fresh/out_of_vocabulary", applied(TemporalCurrent), TimeContext{Axis: "invented"}, true, current, true, TemporalCurrent, ContinuationAxisOverriddenByReceipt, nil, true},
		// carried axis x domain (fresh drifted)
		{"carried/valid_time", applied(TemporalValidTime), rangeFresh, true, current, true, TemporalRange, ContinuationAxisVetoed, nil, false},
		{"carried/observed_time", applied(TemporalObservedTime), rangeFresh, true, current, true, TemporalRange, ContinuationAxisVetoed, nil, false},
		{"carried/range", applied(TemporalRange), rangeFresh, true, current, true, TemporalRange, ContinuationAxisVetoed, nil, false},
		{"carried/absent", applied(""), rangeFresh, true, current, true, TemporalRange, ContinuationAxisVetoed, nil, false},
		{"carried/out_of_vocabulary", applied("invented"), rangeFresh, true, current, true, TemporalRange, ContinuationAxisVetoed, nil, false},
		// request axis x domain (fresh drifted, carrier current)
		{"request/valid_time", applied(TemporalCurrent), rangeFresh, true, TimeContext{Axis: TemporalValidTime, AsOf: &asOf}, true, TemporalRange, ContinuationAxisVetoed, nil, false},
		{"request/range", applied(TemporalCurrent), rangeFresh, true, TimeContext{Axis: TemporalRange, Start: &axis5582RangeStart, End: &axis5582RangeEnd}, true, TemporalRange, ContinuationAxisVetoed, nil, false},
		{"request/absent", applied(TemporalCurrent), rangeFresh, true, TimeContext{}, true, TemporalRange, ContinuationAxisVetoed, nil, false},
		{"request/out_of_vocabulary", applied(TemporalCurrent), rangeFresh, true, TimeContext{Axis: "invented"}, true, TemporalRange, ContinuationAxisVetoed, nil, false},
		// transition x disposition (fresh drifted): the axis follows the
		// established transition, never the applied reading
		{"transition/not_established_but_applied", windowContinuationDecision{Disposition: ContinuationApplied, Accepted: carried, CarriedAxis: TemporalCurrent}, rangeFresh, true, current, true, TemporalRange, ContinuationAxisVetoed, nil, false},
		{"transition/established_withheld", windowContinuationDecision{Disposition: ContinuationWithheld, CarriedAxis: TemporalCurrent, TransitionEstablished: true}, rangeFresh, true, current, true, TemporalCurrent, ContinuationAxisOverriddenByReceipt, nil, true},
		{"transition/established_not_applicable", windowContinuationDecision{Disposition: ContinuationNotApplicable, CarriedAxis: TemporalCurrent, TransitionEstablished: true}, rangeFresh, true, current, true, TemporalCurrent, ContinuationAxisOverriddenByReceipt, nil, true},
		{"transition/zero_decision", windowContinuationDecision{}, rangeFresh, true, current, true, TemporalRange, ContinuationAxisVetoed, nil, false},
		{"transition/established_fresh_current", windowContinuationDecision{Disposition: ContinuationWithheld, CarriedAxis: TemporalCurrent, TransitionEstablished: true}, current, true, current, true, TemporalCurrent, ContinuationAxisAgreed, nil, true},
		// window commitment x {false}
		{"committed/false_fresh_range", applied(TemporalCurrent), rangeFresh, true, current, false, TemporalRange, ContinuationAxisNotEvaluated, nil, false},
		{"committed/false_fresh_current", applied(TemporalCurrent), current, true, current, false, TemporalCurrent, ContinuationAxisNotEvaluated, nil, true},
		{"committed/false_not_established", windowContinuationDecision{}, rangeFresh, true, current, false, TemporalRange, ContinuationAxisNotEvaluated, nil, false},
		// fresh CURRENT axis × bound answerability (r1): an unanswerable
		// current bound is overridden on an established transition like a
		// drifted axis, and agrees (for the bound exit) without one
		{"answerability/current_unanswerable_established", applied(TemporalCurrent), TimeContext{Axis: TemporalCurrent, AsOf: &zeroInstant5582}, false, current, true, TemporalCurrent, ContinuationAxisOverriddenByReceipt, nil, true},
		{"answerability/current_unanswerable_not_established", windowContinuationDecision{Disposition: ContinuationNotApplicable, CarriedAxis: TemporalCurrent}, TimeContext{Axis: TemporalCurrent, AsOf: &zeroInstant5582}, false, current, true, TemporalCurrent, ContinuationAxisAgreed, nil, false},
		{"answerability/range_unanswerable_established", applied(TemporalCurrent), TimeContext{Axis: TemporalRange, Start: &axis5582RangeEnd, End: &axis5582RangeStart}, false, current, true, TemporalCurrent, ContinuationAxisOverriddenByReceipt, nil, true},
		{"answerability/range_unanswerable_not_established", windowContinuationDecision{}, TimeContext{Axis: TemporalRange, Start: &axis5582RangeEnd, End: &axis5582RangeStart}, false, current, true, TemporalRange, ContinuationAxisVetoed, nil, false},
		{"answerability/current_unanswerable_uncommitted", applied(TemporalCurrent), TimeContext{Axis: TemporalCurrent, AsOf: &zeroInstant5582}, false, current, false, TemporalCurrent, ContinuationAxisNotEvaluated, nil, false},
		// executed instants: the caller's as-of is kept, a fresh range's bounds
		// and the caller's requested window are not copied onto the interpretation
		{"executed/keeps_request_as_of_drops_window_and_bounds", applied(TemporalCurrent), rangeFresh, true, requestWithWindow, true, TemporalCurrent, ContinuationAxisOverriddenByReceipt, &asOf, true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, outcome := decideContinuationAxis(tc.decision, tc.fresh, tc.freshAnswerable, tc.request, tc.committed)
			if got.Axis != tc.wantAxis || outcome != tc.wantOutcome {
				t.Fatalf("decideContinuationAxis = (%q, %q), want (%q, %q)", got.Axis, outcome, tc.wantAxis, tc.wantOutcome)
			}
			if tc.wantAsOf != nil && (got.AsOf == nil || !got.AsOf.Equal(*tc.wantAsOf)) {
				t.Errorf("executed as_of = %v, want %v", got.AsOf, *tc.wantAsOf)
			}
			if tc.wantNoBounds && (got.Start != nil || got.End != nil || got.EvidenceWindow != nil) {
				t.Errorf("executed time carries bounds/window %+v, want none", got)
			}
			if !ValidContinuationAxisOutcome(outcome) {
				t.Errorf("outcome %q outside the closed vocabulary", outcome)
			}
		})
	}
}

// The carrier's recorded axis, every member of its domain, through the engine.
func TestCHAOS5582_CarrierRecordedAxisDomain(t *testing.T) {
	t.Parallel()
	question := validInvestigationRequest().Question
	for _, carrierAxis := range []struct {
		name     string
		time     TimeContext
		lineAxis string
		admitted bool
	}{
		{"current", TimeContext{Axis: TemporalCurrent}, "current", true},
		{"valid_time", TimeContext{Axis: TemporalValidTime, AsOf: &axis5582AsOf}, "valid_time", false},
		{"observed_time", TimeContext{Axis: TemporalObservedTime, AsOf: &axis5582AsOf}, "observed_time", false},
		{"range", TimeContext{Axis: TemporalRange, Start: &axis5582RangeStart, End: &axis5582RangeEnd}, "range", false},
		{"absent", TimeContext{}, "", false},
		{"out_of_vocabulary", TimeContext{Axis: "invented"}, continuationTelemetryUnrecognised, false},
	} {
		for _, fresh := range []struct {
			name string
			time TimeContext
		}{
			{"fresh_current", TimeContext{Axis: TemporalCurrent}},
			{"fresh_range", TimeContext{Axis: TemporalRange, Start: &axis5582RangeStart, End: &axis5582RangeEnd}},
		} {
			carrierAxis, fresh := carrierAxis, fresh
			t.Run(carrierAxis.name+"/"+fresh.name, func(t *testing.T) {
				t.Parallel()
				prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
				prior.Interpretation.TimeContext = carrierAxis.time
				run := axis5582Investigate(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
					freshAxisInterpreter{family: QuestionFamilyDiscoveredCohortRanking, timeContext: fresh.time}, continuationRequest(question))
				if run.err != nil {
					t.Fatalf("Investigate() error = %v", run.err)
				}
				drifted := fresh.time.Axis != TemporalCurrent
				want := map[string]any{"request_id": run.requestID, "carried_axis": carrierAxis.lineAxis, "window_receipt_count": 1, "refusal_basis": "none"}
				if !carrierAxis.admitted {
					want["refusal_basis"] = "continuation_context_unverifiable"
				}
				switch {
				case carrierAxis.admitted && !drifted:
					want["continuation_disposition"], want["decision_reason"], want["interpreted_axis_outcome"], want["executed_axis"] = "applied", "none", "agreed", "current"
				case carrierAxis.admitted && drifted:
					want["continuation_disposition"], want["decision_reason"], want["interpreted_axis_outcome"], want["executed_axis"] = "applied", "none", "overridden_by_receipt", "current"
				// A non-current carrier is withheld and the turn ENDS on the
				// continuation refusal: nothing is planned and nothing is
				// retrieved, so `executed_axis` is empty. It is the field's
				// own contract -- empty when the turn ended before the axis
				// governed anything -- and the pin reads it from the served
				// document below rather than assuming.
				case !drifted:
					want["continuation_disposition"], want["decision_reason"], want["interpreted_axis_outcome"], want["executed_axis"] = "withheld", "invalid_context", "agreed", ""
				default:
					want["continuation_disposition"], want["decision_reason"], want["interpreted_axis_outcome"], want["executed_axis"] = "withheld", "invalid_context", "vetoed", ""
				}
				assertLine(t, run.soleDecisionLine(t), want)
				servedCarried := run.result.AnswerPlan != nil && run.result.AnswerPlan.FamilySource == QuestionFamilySourceCarried
				if servedCarried != carrierAxis.admitted {
					t.Errorf("served family_source carried = %v, want %v", servedCarried, carrierAxis.admitted)
				}
				// A carrier recording another axis is WITHHELD, and a withheld
				// window-only continuation ends on its own refusal -- never on
				// the sampled axis, whichever way the fresh axis went.
				if axisConflictLimitationServed(run.result) {
					t.Errorf("axis-conflict veto served (status=%q)", run.result.Status)
				}
				refused := run.result.RefusalBasis == contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable
				if refused != !carrierAxis.admitted {
					t.Errorf("continuation refusal served = %v (basis %q), want %v", refused, run.result.RefusalBasis, !carrierAxis.admitted)
				}
			})
		}
	}
}

// PriorWindowReceipts, every shape of the field, under a drifting fresh axis.
func TestCHAOS5582_WindowReceiptInputDomain(t *testing.T) {
	t.Parallel()
	question := validInvestigationRequest().Question
	drift := axis5582DriftedAxes()[0].time
	valid := BoundSubjectReceipt{ResultID: continuationPriorID, ReceiptID: continuationReceiptID}
	twenty := make([]BoundSubjectReceipt, 0, 20)
	for i := 0; i < 20; i++ {
		twenty = append(twenty, BoundSubjectReceipt{ResultID: continuationPriorID, ReceiptID: continuationReceiptID + string(rune('a'+i))})
	}
	twentyOne := append(append([]BoundSubjectReceipt{}, twenty...), BoundSubjectReceipt{ResultID: continuationOlderID, ReceiptID: continuationReceiptID})
	for _, tc := range []struct {
		name        string
		receipts    []BoundSubjectReceipt
		wantErr     bool
		wantLine    map[string]any // nil: no continuation line
		wantVetoes  map[WindowCanonicalizationOutcome]int
		wantCarried bool
	}{
		{"null", nil, false, nil, map[WindowCanonicalizationOutcome]int{WindowCanonicalizationVetoConflict: 0}, false},
		{"empty_container", []BoundSubjectReceipt{}, false, nil, map[WindowCanonicalizationOutcome]int{WindowCanonicalizationVetoConflict: 0}, false},
		{"canonical_one_valid", []BoundSubjectReceipt{valid}, false,
			map[string]any{"decision_reason": "none", "window_receipt_count": 1, "interpreted_axis_outcome": "overridden_by_receipt"},
			map[WindowCanonicalizationOutcome]int{WindowCanonicalizationVetoConflict: 0}, true},
		{"duplicate_identical", []BoundSubjectReceipt{valid, valid}, true,
			map[string]any{"decision_reason": "request_invalid", "window_receipt_count": 2, "interpreted_axis_outcome": "not_evaluated", "interpreted_axis": ""},
			map[WindowCanonicalizationOutcome]int{WindowCanonicalizationVetoConflict: 0}, false},
		{"two_distinct", []BoundSubjectReceipt{valid, {ResultID: continuationOlderID, ReceiptID: continuationReceiptID}}, false,
			map[string]any{"decision_reason": "window_veto", "window_receipt_count": 2, "interpreted_axis_outcome": "not_evaluated"},
			map[WindowCanonicalizationOutcome]int{WindowCanonicalizationVetoConflict: 1}, false},
		{"boundary_twenty_distinct", twenty, false,
			map[string]any{"decision_reason": "window_veto", "window_receipt_count": 20, "interpreted_axis_outcome": "not_evaluated"},
			map[WindowCanonicalizationOutcome]int{WindowCanonicalizationVetoConflict: 1}, false},
		{"boundary_plus_one_twenty_one", twentyOne, true,
			map[string]any{"decision_reason": "request_invalid", "window_receipt_count": 21, "interpreted_axis_outcome": "not_evaluated"},
			map[WindowCanonicalizationOutcome]int{WindowCanonicalizationVetoConflict: 0}, false},
		{"zero_result_id", []BoundSubjectReceipt{{ResultID: "", ReceiptID: continuationReceiptID}}, true, nil,
			map[WindowCanonicalizationOutcome]int{WindowCanonicalizationVetoConflict: 0}, false},
		{"unknown_receipt_id", []BoundSubjectReceipt{{ResultID: continuationPriorID, ReceiptID: "winr_5582unknownaaaaaaaa"}}, false,
			map[string]any{"decision_reason": "window_veto", "window_receipt_count": 1, "interpreted_axis_outcome": "not_evaluated"},
			map[WindowCanonicalizationOutcome]int{WindowCanonicalizationVetoUnresolved: 1}, false},
		{"unknown_result_id", []BoundSubjectReceipt{{ResultID: "result_5582_not_stored_01", ReceiptID: continuationReceiptID}}, false,
			map[string]any{"decision_reason": "window_veto", "window_receipt_count": 1, "interpreted_axis_outcome": "not_evaluated"},
			map[WindowCanonicalizationOutcome]int{WindowCanonicalizationVetoUnresolved: 1}, false},
		{"out_of_vocabulary_receipt_prefix", []BoundSubjectReceipt{{ResultID: continuationPriorID, ReceiptID: "kindr_5582wrongprefixaaaa"}}, true,
			map[string]any{"decision_reason": "request_invalid", "window_receipt_count": 1, "interpreted_axis_outcome": "not_evaluated"},
			map[WindowCanonicalizationOutcome]int{WindowCanonicalizationVetoConflict: 0}, false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
			older := continuationPrior(t, continuationOlderID, question, QuestionFamilyDiscoveredCohortRanking, "")
			request := validInvestigationRequest()
			request.PriorWindowReceipts = tc.receipts
			run := axis5582Investigate(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior, older.ResultID: older}},
				freshAxisInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam, timeContext: drift}, request)
			if (run.err != nil) != tc.wantErr {
				t.Fatalf("Investigate() error = %v, want error %v", run.err, tc.wantErr)
			}
			lines := run.linesWithMsg(t, "context fabric window continuation decision")
			if tc.wantLine == nil {
				if len(lines) != 0 {
					t.Errorf("got %d continuation lines, want 0", len(lines))
				}
			} else {
				want := map[string]any{"request_id": run.requestID}
				for k, v := range tc.wantLine {
					want[k] = v
				}
				assertLine(t, run.soleDecisionLine(t), want)
			}
			for outcome, n := range tc.wantVetoes {
				if got := canonicalizationOutcomes(run.telemetry, outcome); got != n {
					t.Errorf("%s outcomes = %d, want %d (all: %v)", outcome, got, n, run.telemetry.windowCanonicalizationOutcomes)
				}
			}
			if got := run.result.AnswerPlan != nil && run.result.AnswerPlan.FamilySource == QuestionFamilySourceCarried; got != tc.wantCarried {
				t.Errorf("served carried = %v, want %v", got, tc.wantCarried)
			}
		})
	}
}

// An explicit evidence_window beside one valid receipt: the shapes request
// validation refuses, on both surfaces, under a drifting fresh axis.
func TestCHAOS5582_ExplicitWindowInputDomain(t *testing.T) {
	t.Parallel()
	question := validInvestigationRequest().Question
	drift := axis5582DriftedAxes()[0].time
	inside := axis5582FrozenStart.Add(futureSkewTolerance - time.Second)
	for _, tc := range []struct {
		name       string
		window     *RequestedEvidenceWindow
		wantErr    bool
		wantReason string
		wantOutcom string
		wantVeto   int
	}{
		{"null", nil, false, "none", "overridden_by_receipt", 0},
		{"empty_container", &RequestedEvidenceWindow{}, true, "request_invalid", "not_evaluated", 0},
		{"out_of_vocabulary_relative_id", &RequestedEvidenceWindow{RelativeID: "invented_window"}, true, "request_invalid", "not_evaluated", 0},
		{"relative_id_all_time_disagrees", &RequestedEvidenceWindow{RelativeID: RelativeWindowAllTime}, false, "window_veto", "not_evaluated", 1},
		{"start_only", &RequestedEvidenceWindow{Start: &axis5582FrozenStart}, true, "request_invalid", "not_evaluated", 0},
		{"end_only", &RequestedEvidenceWindow{End: &axis5582FrozenEnd}, true, "request_invalid", "not_evaluated", 0},
		{"inverted_bounds", &RequestedEvidenceWindow{Start: &axis5582FrozenEnd, End: &axis5582FrozenStart}, true, "request_invalid", "not_evaluated", 0},
		{"start_inside_skew_minus_one", &RequestedEvidenceWindow{Start: &inside, End: &axis5582FrozenEnd}, false, "none", "overridden_by_receipt", 0},
	} {
		for _, surface := range axis5582Surfaces() {
			tc, surface := tc, surface
			t.Run(surface+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
				request := axis5582Request(question, surface)
				request.TimeContext.EvidenceWindow = tc.window
				run := axis5582Investigate(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
					freshAxisInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam, timeContext: drift}, request)
				if (run.err != nil) != tc.wantErr {
					t.Fatalf("Investigate() error = %v, want error %v", run.err, tc.wantErr)
				}
				assertLine(t, run.soleDecisionLine(t), map[string]any{
					"request_id": run.requestID, "decision_reason": tc.wantReason, "interpreted_axis_outcome": tc.wantOutcom,
					"explicit_window_present": tc.window != nil, "window_receipt_count": 1,
				})
				if got := canonicalizationOutcomes(run.telemetry, WindowCanonicalizationVetoConflict); got != tc.wantVeto {
					t.Errorf("veto_conflict outcomes = %d, want %d", got, tc.wantVeto)
				}
			})
		}
	}
}

type errInterpreter5582 struct{}

func (errInterpreter5582) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	return InterpretedQuestion{}, QuestionFamilyOutcome{}, errors.New("interpreter unavailable (CHAOS-5582 fixture)")
}

// ungroupedDriftInterpreter is r1UngroupedInterpreter's validated org-scope
// frame with the fresh axis moved off current.
type ungroupedDriftInterpreter struct{ family QuestionFamily }

func (i ungroupedDriftInterpreter) Interpret(ctx context.Context, p storage.Principal, r InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	interpretation, outcome, err := r1UngroupedInterpreter{family: i.family}.Interpret(ctx, p, r)
	interpretation.TimeContext = TimeContext{Axis: TemporalRange, Start: &axis5582RangeStart, End: &axis5582RangeEnd}
	return interpretation, outcome, err
}

// The exits that end a turn before or around the axis decision each publish the
// fresh axis they reached and the outcome that describes them.
func TestCHAOS5582_EveryExitPublishesTheAxisStateItReached(t *testing.T) {
	t.Parallel()
	question := validInvestigationRequest().Question
	for _, tc := range []struct {
		name        string
		prior       func(InvestigationResult) InvestigationResult
		mutate      func(*InvestigationRequest)
		interpreter QuestionInterpreter
		wantErr     bool
		want        map[string]any
		wantVetoed  bool
	}{
		// Base-branch siblings, each executed under a drifting fresh axis: a
		// changed answer budget is decided after the transition is established
		// (confirmed axis, fresh reading served); a stated scope disqualifies
		// before any carrier is read (no transition, fresh axis governs).
		{name: "answer_budget_changed_under_drift",
			mutate:      func(r *InvestigationRequest) { r.Options.MaxSerializedBytes = r.Options.MaxSerializedBytes / 2 },
			interpreter: freshAxisInterpreter{family: QuestionFamilyDiscoveredCohortRanking, timeContext: axis5582DriftedAxes()[0].time},
			want:        map[string]any{"continuation_disposition": "not_applicable", "decision_reason": "answer_budget_changed", "interpreted_axis": "range", "carried_axis": "current", "executed_axis": "current", "interpreted_axis_outcome": "overridden_by_receipt", "refusal_basis": "none"}},
		{name: "requested_scope_under_drift",
			mutate:      func(r *InvestigationRequest) { r.RequestedScope.RepositorySlugs = []string{"widget-service"} },
			interpreter: freshAxisInterpreter{family: QuestionFamilyDiscoveredCohortRanking, timeContext: axis5582DriftedAxes()[0].time},
			want:        map[string]any{"continuation_disposition": "not_applicable", "decision_reason": "explicit_structure_hint", "interpreted_axis": "range", "carried_axis": "", "executed_axis": "range", "interpreted_axis_outcome": "vetoed", "refusal_basis": "none"},
			wantVetoed:  true},
		// An unanswerable fresh BOUND on an established transition is a
		// diagnostic like a drifted axis: overridden, answered, never refused.
		{"unanswerable_fresh_bound_on_established_transition", nil, nil, freshAxisInterpreter{family: QuestionFamilyGroupedCohortStatus, timeContext: TimeContext{Axis: TemporalValidTime}}, false,
			map[string]any{"continuation_disposition": "applied", "decision_reason": "none", "interpreted_axis": "valid_time", "carried_axis": "current", "executed_axis": "current", "interpreted_axis_outcome": "overridden_by_receipt"}, false},
		{"unanswerable_fresh_range_on_established_transition", nil, nil, freshAxisInterpreter{family: QuestionFamilyGroupedCohortStatus, timeContext: TimeContext{Axis: TemporalRange, Start: &axis5582RangeEnd, End: &axis5582RangeStart}}, false,
			map[string]any{"continuation_disposition": "applied", "decision_reason": "none", "interpreted_axis": "range", "carried_axis": "current", "executed_axis": "current", "interpreted_axis_outcome": "overridden_by_receipt"}, false},
		// A fresh CURRENT axis whose bound is unanswerable is the same sampled
		// failure on an established transition: overridden and answered.
		{"unanswerable_fresh_current_bound_on_established_transition", nil, nil, freshAxisInterpreter{family: QuestionFamilyGroupedCohortStatus, timeContext: TimeContext{Axis: TemporalCurrent, AsOf: &zeroInstant5582}}, false,
			map[string]any{"continuation_disposition": "applied", "decision_reason": "none", "interpreted_axis": "current", "carried_axis": "current", "executed_axis": "current", "interpreted_axis_outcome": "overridden_by_receipt", "refusal_basis": "none"}, false},
		// Without an established transition it still ends at the bound exit.
		{"unanswerable_fresh_current_bound_changed_question", func(p InvestigationResult) InvestigationResult {
			p.Question = "What was the status of Ask Dev last spring and what drove it?"
			return p
		}, nil, freshAxisInterpreter{family: QuestionFamilyGroupedCohortStatus, timeContext: TimeContext{Axis: TemporalCurrent, AsOf: &zeroInstant5582}}, false,
			map[string]any{"continuation_disposition": "not_applicable", "decision_reason": "as_of_unresolvable", "interpreted_axis": "current", "carried_axis": "current", "executed_axis": "", "interpreted_axis_outcome": "agreed"}, false},
		// Without an established transition the fresh bound governs, and an
		// unanswerable one ends the turn exactly as before.
		{"unanswerable_fresh_bound_changed_question", func(p InvestigationResult) InvestigationResult {
			p.Question = "What was the status of Ask Dev last spring and what drove it?"
			return p
		}, nil, freshAxisInterpreter{family: QuestionFamilyGroupedCohortStatus, timeContext: TimeContext{Axis: TemporalValidTime}}, false,
			map[string]any{"continuation_disposition": "not_applicable", "decision_reason": "as_of_unresolvable", "interpreted_axis": "valid_time", "carried_axis": "current", "executed_axis": "", "interpreted_axis_outcome": "vetoed"}, false},
		{"interpreter_error", nil, nil, errInterpreter5582{}, true,
			map[string]any{"decision_reason": "fresh_context_unavailable", "interpreted_axis": "", "executed_axis": "", "interpreted_axis_outcome": "not_evaluated"}, false},
		// The reading is withheld, the transition is not: the axis stays the
		// confirmed one and the turn is served, never refused on the sample.
		{"composition_refused_under_drift", nil, nil, ungroupedDriftInterpreter{family: QuestionFamilyDiscoveredCohortRanking}, false,
			map[string]any{"continuation_disposition": "withheld", "decision_reason": "composition_invalid", "interpreted_axis": "range", "carried_axis": "current", "executed_axis": "", "interpreted_axis_outcome": "overridden_by_receipt", "refusal_basis": "continuation_context_unverifiable"}, false},
		{"version_mismatched_carrier_under_drift", func(p InvestigationResult) InvestigationResult {
			p.AnswerPlan.FamilyVersion = "question-family.v0-not-in-force"
			return p
		}, nil, freshAxisInterpreter{family: QuestionFamilyDiscoveredCohortRanking, timeContext: axis5582DriftedAxes()[0].time}, false,
			map[string]any{"continuation_disposition": "withheld", "decision_reason": "context_version_mismatch", "interpreted_axis": "range", "carried_axis": "current", "executed_axis": "", "interpreted_axis_outcome": "overridden_by_receipt", "refusal_basis": "continuation_context_unverifiable"}, false},
		{"carrier_without_plan_under_drift", func(p InvestigationResult) InvestigationResult {
			p.AnswerPlan = nil
			return p
		}, nil, freshAxisInterpreter{family: QuestionFamilyDiscoveredCohortRanking, timeContext: axis5582DriftedAxes()[0].time}, false,
			map[string]any{"continuation_disposition": "not_applicable", "decision_reason": "missing_context", "interpreted_axis": "range", "carried_axis": "current", "executed_axis": "current", "interpreted_axis_outcome": "overridden_by_receipt", "refusal_basis": "none"}, false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
			if tc.prior != nil {
				prior = tc.prior(prior)
			}
			request := continuationRequest(question)
			if tc.mutate != nil {
				tc.mutate(&request)
			}
			run := axis5582Investigate(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}, tc.interpreter, request)
			if (run.err != nil) != tc.wantErr {
				t.Fatalf("Investigate() error = %v, want error %v", run.err, tc.wantErr)
			}
			want := map[string]any{"request_id": run.requestID, "window_receipt_count": 1, "explicit_window_present": false}
			for k, v := range tc.want {
				want[k] = v
			}
			assertLine(t, run.soleDecisionLine(t), want)
			if got := axisConflictLimitationServed(run.result); got != tc.wantVetoed {
				t.Errorf("axis-conflict veto served = %v, want %v", got, tc.wantVetoed)
			}
		})
	}
}

// The vocabulary the event specification declares IS the emitter guards'
// vocabulary: every member seats through its own field and reaches the line
// unchanged, and the census matches the registry in both directions.
func TestCHAOS5582_TheLineVocabularyIsTheGuardsVocabulary(t *testing.T) {
	t.Parallel()
	seat := map[string]func(*windowContinuationDecision, string){
		"seed_source": func(d *windowContinuationDecision, m string) { d.SeedSource = CarrySeedSource(m) },
		"family_carried": func(d *windowContinuationDecision, m string) {
			if m != "" {
				d.Carried = &continuationCarriedContext{Family: QuestionFamily(m)}
			}
		},
		"family_fresh": func(d *windowContinuationDecision, m string) {
			d.Fresh = continuationFreshProposal{Available: m != "", Family: QuestionFamily(m)}
		},
		"family_accepted": func(d *windowContinuationDecision, m string) {
			if m != "" {
				d.Accepted = &continuationCarriedContext{Family: QuestionFamily(m)}
			}
		},
		"family_source": func(d *windowContinuationDecision, m string) {
			if m != "" {
				d.Accepted = &continuationCarriedContext{Family: QuestionFamilyGroupedCohortStatus}
			}
		},
		"continuation_disposition":     func(d *windowContinuationDecision, m string) { d.Disposition = ContinuationDisposition(m) },
		"decision_reason":              func(d *windowContinuationDecision, m string) { d.Reason = ContinuationDecisionReason(m) },
		"conflict_reason":              func(d *windowContinuationDecision, m string) { d.ConflictReason = ContinuationConflictReason(m) },
		"composition_outcome":          func(d *windowContinuationDecision, m string) { d.CompositionOutcome = CompositionOutcome(m) },
		"composition_failed_invariant": func(d *windowContinuationDecision, m string) { d.CompositionFailedInvariant = m },
		"interpreted_axis": func(d *windowContinuationDecision, m string) {
			d.InterpretedAxis = contractsv1.ContextFabricTemporalAxis(m)
		},
		"carried_axis": func(d *windowContinuationDecision, m string) {
			d.CarriedAxis = contractsv1.ContextFabricTemporalAxis(m)
		},
		"executed_axis": func(d *windowContinuationDecision, m string) {
			d.ExecutedAxis = contractsv1.ContextFabricTemporalAxis(m)
		},
		"interpreted_axis_outcome": func(d *windowContinuationDecision, m string) { d.AxisOutcome = ContinuationAxisOutcome(m) },
		"carrier_read":             func(d *windowContinuationDecision, m string) { d.CarrierRead = ContinuationCarrierRead(m) },
		"refusal_basis": func(d *windowContinuationDecision, m string) {
			if m != "none" {
				d.RefusalBasis = contractsv1.ContextFabricRefusalBasis(m)
			}
		},
	}
	registry := map[string]bool{}
	for _, field := range closedDecisionFields() {
		registry[field.Key] = true
		vocabulary := ContinuationDecisionLineVocabulary(field.Key)
		if field.Key == "conflict_fields" {
			if vocabulary != nil {
				t.Errorf("conflict_fields is a joined list with no finite vocabulary, got %v", vocabulary)
			}
			continue
		}
		if len(vocabulary) == 0 {
			t.Errorf("closed field %q declares no vocabulary", field.Key)
			continue
		}
		setter, ok := seat[field.Key]
		if !ok {
			t.Errorf("closed field %q has no seat in this pin", field.Key)
			continue
		}
		seen := map[string]bool{}
		for _, member := range vocabulary {
			if seen[member] {
				t.Errorf("%s: duplicate member %q", field.Key, member)
			}
			seen[member] = true
			if member == continuationTelemetryUnrecognised {
				t.Errorf("%s: the unrecognised sentinel is declared a member", field.Key)
			}
			d := newWindowContinuationDecision(continuationRequest(validInvestigationRequest().Question))
			setter(&d, member)
			if got := closedDecisionToken(field.Key, d); got != member {
				t.Errorf("%s: member %q reaches the line as %q -- the declaration admits a value the guard refuses", field.Key, member, got)
			}
		}
	}
	for key := range seat {
		if !registry[key] {
			t.Errorf("seat %q names no registry field", key)
		}
	}
	if ContinuationDecisionLineVocabulary("not_a_field") != nil {
		t.Errorf("an unknown key returned a vocabulary")
	}
}

// EVERY FIELD THAT ASSERTS SOMETHING HAPPENED is decided from the FINAL
// continuation state at the single exit fold, never from an intermediate
// stage.
//
// `applied_window` is copied in at ADMISSION, before the composition verdict
// exists. A turn admitted and then withheld applied no window at all, and the
// line went on publishing the admitted one -- a field asserting a decision that
// did not hold. The supersession branch cleared it, but only on the `Applies()`
// path, so the far more common withholds kept it. This table walks the
// disposition x composition-outcome space THROUGH THE ENGINE and reads the
// EMITTED line: `applied_window` and `accepted_context_id` are non-empty iff
// the continuation applied, and every other cell publishes neither.
func TestCHAOS5582_TheLineAssertsOnlyDecisionsThatHeldAtEmission(t *testing.T) {
	t.Parallel()
	question := validInvestigationRequest().Question
	for _, tc := range []struct {
		name            string
		prior           func(InvestigationResult) InvestigationResult
		mutate          func(*InvestigationRequest)
		interpreter     QuestionInterpreter
		wantDisposition string
		wantComposition string
		wantApplied     bool
	}{
		{name: "applied/no_fresh_frame -- the one cell that DID apply a window",
			interpreter:     freshAxisInterpreter{family: QuestionFamilyGroupedCohortStatus, timeContext: TimeContext{Axis: TemporalCurrent}},
			wantDisposition: "applied", wantComposition: "no_fresh_frame", wantApplied: true},
		{name: "withheld/invalid -- composition could not express the carried reading",
			interpreter:     ungroupedDriftInterpreter{family: QuestionFamilyDiscoveredCohortRanking},
			wantDisposition: "withheld", wantComposition: "invalid", wantApplied: false},
		{name: "withheld/not_evaluated -- version-mismatched carrier, never composed",
			prior: func(p InvestigationResult) InvestigationResult {
				p.AnswerPlan.FamilyVersion = "question-family.v0-not-in-force"
				return p
			},
			interpreter:     freshAxisInterpreter{family: QuestionFamilyDiscoveredCohortRanking, timeContext: axis5582DriftedAxes()[0].time},
			wantDisposition: "withheld", wantComposition: "not_evaluated", wantApplied: false},
		{name: "not_applicable/not_evaluated -- carrier without a plan",
			prior: func(p InvestigationResult) InvestigationResult {
				p.AnswerPlan = nil
				return p
			},
			interpreter:     freshAxisInterpreter{family: QuestionFamilyDiscoveredCohortRanking, timeContext: axis5582DriftedAxes()[0].time},
			wantDisposition: "not_applicable", wantComposition: "not_evaluated", wantApplied: false},
		{name: "not_applicable/not_evaluated -- the request states its own scope",
			mutate:          func(r *InvestigationRequest) { r.RequestedScope.RepositorySlugs = []string{"widget-service"} },
			interpreter:     freshAxisInterpreter{family: QuestionFamilyDiscoveredCohortRanking, timeContext: axis5582DriftedAxes()[0].time},
			wantDisposition: "not_applicable", wantComposition: "not_evaluated", wantApplied: false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
			if tc.prior != nil {
				prior = tc.prior(prior)
			}
			request := continuationRequest(question)
			if tc.mutate != nil {
				tc.mutate(&request)
			}
			run := axis5582Investigate(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}, tc.interpreter, request)
			if run.err != nil {
				t.Fatalf("Investigate() error = %v", run.err)
			}
			line := run.soleDecisionLine(t)
			applied, _ := line["applied_window"].(string)
			acceptedID, _ := line["accepted_context_id"].(string)
			t.Logf("disposition=%v composition=%v applied_window=%q accepted_context_id=%q served_window=%v",
				line["continuation_disposition"], line["composition_outcome"], applied, acceptedID,
				run.result.EffectiveEvidenceWindow != nil)

			if got := line["continuation_disposition"]; got != tc.wantDisposition {
				t.Fatalf("continuation_disposition = %v, want %q -- the cell does not exercise what it claims", got, tc.wantDisposition)
			}
			if got := line["composition_outcome"]; got != tc.wantComposition {
				t.Errorf("composition_outcome = %v, want %q", got, tc.wantComposition)
			}
			// `executed_axis` follows the SAME rule keyed on a different
			// question: not "did the continuation apply" but "did this turn
			// serve anything at all". A withheld continuation that goes on to
			// answer under the fresh reading executed an axis and says so; one
			// that ends the turn executed nothing and must not. The cell's
			// class is read from the SERVED DOCUMENT rather than declared in
			// the table, so a cell cannot be filed under the wrong half.
			executedAxis, _ := line["executed_axis"].(string)
			servedSomething := run.result.AnswerPlan != nil
			t.Logf("  executed_axis=%q served_plan=%v", executedAxis, servedSomething)
			if servedSomething {
				if executedAxis == "" {
					t.Errorf("executed_axis is empty on a turn that SERVED a plan -- the line cannot say which axis the answer ran under")
				}
			} else if executedAxis != "" {
				t.Errorf("executed_axis = %q on a turn that served no plan at all -- the line claims an execution that did not happen", executedAxis)
			}

			// THE INVARIANT, asserted in BOTH directions so neither a always-
			// empty nor an always-populated field can satisfy it.
			if tc.wantApplied {
				if applied == "" {
					t.Errorf("applied_window is empty on an APPLIED continuation -- the line cannot say which window executed")
				}
				if acceptedID == "" {
					t.Errorf("accepted_context_id is empty on an APPLIED continuation")
				}
				if run.result.EffectiveEvidenceWindow == nil {
					t.Errorf("the line claims an applied window on a result that carries none")
				}
			} else {
				if applied != "" {
					t.Errorf("applied_window = %q on a %s continuation -- no window was applied and the line says one was",
						applied, tc.wantDisposition)
				}
				if acceptedID != "" {
					t.Errorf("accepted_context_id = %q on a %s continuation -- nothing executed under that context",
						acceptedID, tc.wantDisposition)
				}
			}
		})
	}
}

// refusingGate5582 is a fresh interpretation whose gate REFUSES, parameterised
// over the whole refusing half of the gate's closed vocabulary and over whether
// a frame was proposed at all. It exists so the sweep below is over the gate's
// own members rather than over a single hand-picked shape.
type refusingGate5582 struct {
	family    QuestionFamily
	gate      FrameGate
	withFrame bool
}

func (i refusingGate5582) Interpret(ctx context.Context, p storage.Principal, r InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	outcome := QuestionFamilyOutcome{
		Family: i.family, Source: QuestionFamilySourceModel, Gate: i.gate,
		WinningSampleIndex: 0, WinningSample: FamilySample{ModelFamily: i.family},
		Version: QuestionFamilyTableVersion,
	}
	if i.withFrame {
		frame := QuestionFrame{
			Goals:             []InvestigationGoal{GoalAssessState},
			SubjectExpression: SubjectExpression{Kind: SubjectExpressionOrganizationScope, Org: &OrganizationScopeExpression{}},
			Temporal:          TemporalIntentCurrent,
		}
		v := ValidateFrame(frame, nil, ShapeOpen)
		if v.Outcome != FrameValidationOutcomeValid {
			panic("fixture defect: frame invalid " + string(v.Failure.Invariant))
		}
		outcome.Frame = &v.Frame
	}
	return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, outcome, nil
}

// ON AN ESTABLISHED TRANSITION NO GATE BELONGING TO THE FRESH INTERPRETATION
// DECIDES THE TURN.
//
// Not the axis, not the bound, and not the frame gate: a frame proposed for a
// question the receipt has already settled can refuse `member_kind_unservable`
// on a reading nobody asked to execute. The rule is not "and also the frame
// gate" -- it is that the fresh proposal is not a party to such a turn at all.
// This sweep is over the REFUSING half of the gate vocabulary crossed with
// frame-proposed and frame-absent, each executed through the engine, and each
// paired with the same fixture on an UNESTABLISHED turn, where the refusal must
// still stand. Without that control the table would pass on a build that simply
// stopped refusing.
func TestCHAOS5582_NoFreshGateDecidesAnEstablishedTransition(t *testing.T) {
	t.Parallel()
	question := validInvestigationRequest().Question
	changed := "What was the status of Ask Dev last spring and what drove it?"
	gates := map[string]FrameGate{
		"rejected_invalid": {Outcome: FrameGateRejectedInvalid, FailedInvariant: FrameInvariant("i6")},
		"refused_basis":    {Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable, DeclaredMemberKind: contractsv1.ContextFabricSubjectRepository},
	}
	for gateName, gate := range gates {
		for _, withFrame := range []bool{false, true} {
			shape := "frame_absent"
			if withFrame {
				shape = "frame_proposed"
			}
			gateName, gate, withFrame, shape := gateName, gate, withFrame, shape
			interpreter := refusingGate5582{family: QuestionFamilyDiscoveredCohortRanking, gate: gate, withFrame: withFrame}

			t.Run(gateName+"/"+shape+"/established: served", func(t *testing.T) {
				t.Parallel()
				prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
				run := axis5582Investigate(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}, interpreter, continuationRequest(question))
				if run.err != nil {
					t.Fatalf("Investigate() error = %v", run.err)
				}
				line := run.soleDecisionLine(t)
				t.Logf("disposition=%v composition=%v status=%q basis=%q", line["continuation_disposition"], line["composition_outcome"], run.result.Status, run.result.RefusalBasis)
				if run.result.Status == InvestigationNoMatch {
					t.Errorf("REFUSED on an established transition: status=%q basis=%q -- the fresh %s gate decided a turn the receipt had already settled",
						run.result.Status, run.result.RefusalBasis, gateName)
				}
				if got := line["composition_outcome"]; got == string(CompositionFreshRefused) {
					t.Errorf("composition_outcome = %v -- the fresh gate was consulted", got)
				}
				if got := line["continuation_disposition"]; got != "applied" {
					t.Errorf("continuation_disposition = %v, want applied -- the carried reading is what the caller confirmed", got)
				}
			})

			t.Run(gateName+"/"+shape+"/not_established: refusal stands", func(t *testing.T) {
				t.Parallel()
				prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
				prior.Question = changed
				run := axis5582Investigate(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}, interpreter, continuationRequest(question))
				if run.err != nil {
					t.Fatalf("Investigate() error = %v", run.err)
				}
				line := run.soleDecisionLine(t)
				t.Logf("CONTROL disposition=%v composition=%v status=%q basis=%q", line["continuation_disposition"], line["composition_outcome"], run.result.Status, run.result.RefusalBasis)
				if run.result.Status != InvestigationNoMatch {
					t.Errorf("the control was SERVED: status=%q -- nothing about this turn was confirmed, so the fresh %s gate must still end it",
						run.result.Status, gateName)
				}
				if got := line["continuation_disposition"]; got == "applied" {
					t.Errorf("continuation_disposition = applied on an unestablished turn whose fresh gate refused")
				}
			})
		}
	}
}
