package contextfabric

import (
	"context"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE BOUNDED I9 REPAIR, EXECUTED. Every case drives the production
// interpreter (RuntimeQuestionInterpreter.Interpret -> resolveFrame ->
// validateProposedFrame) with a configured logger, and reads what the turn
// carries out: the family outcome's frame and gate, the receipt the sink
// persisted, and the one frame-validation line. The serve, persist, by-id and
// continuation cases drive Engine.Investigate over the same interpreter.
//
// The Class A fixture is corpus row neg-mentions-teams-but-not-grouped, by
// its columns: family scoped_cohort_status, variant children_of_scope,
// member_kind team, group_kind none, anchor kind repository. Its archived
// misread proposed named_subject with a count goal and requested_member_hint
// team. No question text is carried.

const repairAnchorTerm = "anchor-repo"

// classAReceipt is the misread turn's receipt: the member hint the same call
// emitted, and the anchor kind its sample stated.
func classAReceipt() ModelExecutionReceipt {
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.RequestedSubjectKind = SubjectTeam
	receipt.ScopeAnchorKind = SubjectRepository
	return receipt
}

// countOverNamedSubject is the misread proposal: a count goal over one named
// subject.
func countOverNamedSubject() QuestionFrame {
	return QuestionFrame{
		Goals:             []InvestigationGoal{GoalCountOrAggregate},
		SubjectExpression: SubjectExpression{Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{Terms: []string{repairAnchorTerm}}},
		Temporal:          TemporalIntentCurrent,
	}
}

// repairInterpretation is the interpreted question the fake runtime returns
// beside the receipt; flat is its flat subject terms, the terms retrieval
// searches.
func repairInterpretation(flat []string) InterpretedQuestion {
	return InterpretedQuestion{
		Shape: ShapeSingleSubject, RequestedJudgment: "count",
		SubjectTerms: flat, TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}
}

type repairRun struct {
	outcome QuestionFamilyOutcome
	receipt ModelExecutionReceipt
	line    map[string]any
	// family is the same interpretation's family-resolution line, whose
	// shadow keys say whether a validated frame was observed.
	family map[string]any
}

const familyResolutionMessage = "context fabric question family resolution"

// interpretForRepair runs ONE production interpretation of frame under
// receipt and returns what it carried out.
func interpretForRepair(t *testing.T, receipt ModelExecutionReceipt, frame QuestionFrame, flat []string) repairRun {
	t.Helper()
	logs := captureEngineLogger(t)
	receipt.QuestionFrame = &frame
	sink := &fakeReceiptSink{}
	interpreter := RuntimeQuestionInterpreter{
		Runtime:         fakeModelRuntime{interpreted: repairInterpretation(flat), receipt: receipt},
		Sink:            sink,
		FrameTelemetry:  logs.telemetry,
		FamilyTelemetry: logs.telemetry,
		Requirements:    registryDeriver{},
	}
	_, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_repair"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	families := linesWithMessage(t, logs.configured.String(), familyResolutionMessage)
	if len(families) != 1 {
		t.Fatalf("family-resolution lines on the configured logger = %d, want exactly 1 per interpretation", len(families))
	}
	lines := linesWithMessage(t, logs.configured.String(), frameValidationMessage)
	if len(lines) != 1 {
		t.Fatalf("frame-validation lines on the configured logger = %d, want exactly 1 per interpretation", len(lines))
	}
	if lines[0]["level"] != "INFO" {
		t.Fatalf("level = %v, want INFO", lines[0]["level"])
	}
	if len(sink.recorded) == 0 {
		t.Fatal("the sink recorded no receipt")
	}
	return repairRun{outcome: outcome, receipt: sink.recorded[len(sink.recorded)-1], line: lines[0], family: families[0]}
}

// assertRepairLine compares against LITERALS, never the producer's constants.
func assertRepairLine(t *testing.T, line map[string]any, want map[string]any) {
	t.Helper()
	for key, value := range want {
		got, present := line[key]
		if !present {
			t.Errorf("%s is ABSENT from the frame-validation line", key)
			continue
		}
		if got != value {
			t.Errorf("%s = %v (%T), want %v (%T)", key, got, got, value, value)
		}
	}
}

// TestACountOverANamedSubjectIsRepairedIntoTheScopedCohortItsHintNames is the
// Class A fixture: the gate repairs the misread into the corpus row's own
// reading, keeps every other field of the proposal, and says so on the line.
func TestACountOverANamedSubjectIsRepairedIntoTheScopedCohortItsHintNames(t *testing.T) {
	dimension := HealthDimensionVocabulary()[0]
	proposal := countOverNamedSubject()
	proposal.Temporal = TemporalIntentBoundedWindow
	proposal.Dimensions = []HealthDimension{dimension}

	run := interpretForRepair(t, classAReceipt(), proposal, []string{repairAnchorTerm})

	frame := run.outcome.Frame
	if frame == nil {
		t.Fatalf("outcome.Frame is nil: the Class A proposal was refused (gate %s)", run.outcome.Gate.Observable())
	}
	expression := frame.SubjectExpression
	if expression.Kind != SubjectExpressionChildrenOfScope || expression.Scoped == nil || expression.Named != nil {
		t.Fatalf("repaired expression = %+v, want children_of_scope with only the scoped variant set", expression)
	}
	if !reflect.DeepEqual(expression.Scoped.AnchorTerms, []string{repairAnchorTerm}) {
		t.Fatalf("anchor terms = %#v, want the named subject's terms %#v", expression.Scoped.AnchorTerms, []string{repairAnchorTerm})
	}
	if expression.Scoped.MemberKind != SubjectTeam {
		t.Fatalf("member kind = %q, want team (the hint), not the anchor kind repository", expression.Scoped.MemberKind)
	}
	if !reflect.DeepEqual(frame.Goals, []InvestigationGoal{GoalCountOrAggregate}) || frame.Temporal != TemporalIntentBoundedWindow ||
		!reflect.DeepEqual(frame.Dimensions, []HealthDimension{dimension}) || len(frame.Emphasis) != 0 {
		t.Fatalf("repaired frame changed a field the repair may not touch: goals %v temporal %q dimensions %v emphasis %v",
			frame.Goals, frame.Temporal, frame.Dimensions, frame.Emphasis)
	}
	if !frame.HasObligation(ObligationCount) {
		t.Fatalf("repaired frame obligations %v lack count: it was not derived from the repaired frame", frame.Obligations)
	}
	if run.outcome.Gate.Outcome != FrameGatePassed {
		t.Fatalf("gate = %s, want passed", run.outcome.Gate.Observable())
	}
	if !reflect.DeepEqual(run.outcome.FrameObligations, frame.Obligations) {
		t.Fatalf("carried obligations %v disagree with the carried frame's %v", run.outcome.FrameObligations, frame.Obligations)
	}
	if run.receipt.FrameOutcome != FrameValidationOutcomeRepaired || run.receipt.FrameFailedInvariant != "" || run.receipt.FrameGateOutcome != FrameGatePassed {
		t.Fatalf("persisted receipt outcome/invariant/gate = %q/%q/%q, want repaired//passed",
			run.receipt.FrameOutcome, run.receipt.FrameFailedInvariant, run.receipt.FrameGateOutcome)
	}
	if run.receipt.QuestionFrame == nil || !framesEqual(*run.receipt.QuestionFrame, *frame) {
		t.Fatal("the persisted receipt does not carry the repaired frame the turn acts on")
	}
	if run.receipt.RequirementCellsDerived == 0 {
		t.Fatal("no requirement cells were derived for the repaired frame")
	}
	assertRepairLine(t, run.line, map[string]any{
		"outcome":                "repaired",
		"failed_invariant":       "",
		"failure_detail":         "",
		"proposed_kind":          "named_subject",
		"proposed_member_kind":   "unset",
		"requested_member_hint":  "team",
		"frame_gate":             "passed",
		"refuse_basis":           "none",
		"cohort_discoverability": "discoverable",
		"repair_decision":        "applied",
		"repair":                 "count_kind_collapse",
		"repair_invariant":       "i9",
		"repair_kind_before":     "named_subject",
		"repair_kind_after":      "children_of_scope",
		"repair_member_kind":     "team",
		"repair_terms_match":     "match",
		"repair_attempts":        float64(1),
	})
	if got, _ := run.line["requirement_cells_derived"].(float64); got == 0 {
		t.Fatalf("requirement_cells_derived = %v on the line, want the repaired frame's derived cells", run.line["requirement_cells_derived"])
	}
	assertRepairLine(t, run.family, map[string]any{
		"shadow_frame_observed": true,
		"shadow_frame_outcome":  "repaired",
	})
}

type repairCell struct {
	cell        string
	receipt     func(*ModelExecutionReceipt)
	frame       func() QuestionFrame
	wantOutcome FrameValidationOutcome
	// flat is the interpretation's flat subject terms; nil means the Class A
	// named term, and a non-nil empty slice means none.
	flat []string
	// wantKind is the carried frame's kind, "" when the turn carries none.
	wantKind SubjectExpressionKind
	// wantAnchors is the carried scoped frame's anchor terms, nil when the
	// cell carries no scoped frame.
	wantAnchors []string
	wantLine    map[string]any
}

func refusedI9Line(decision string) map[string]any {
	return map[string]any{
		"outcome": "refused_invalid", "failed_invariant": "i9", "failure_detail": "count_requires_set_valued_kind",
		"frame_gate": "rejected:i9", "repair_decision": decision, "repair": "count_kind_collapse", "repair_invariant": "i9",
		"repair_kind_after": "none", "repair_member_kind": "none", "repair_terms_match": "not_evaluated", "repair_attempts": float64(0),
	}
}

func notApplicableLine(outcome, invariant, gate string) map[string]any {
	return map[string]any{
		"outcome": outcome, "failed_invariant": invariant, "frame_gate": gate,
		"repair_decision": "not_applicable", "repair": "none", "repair_invariant": "none",
		"repair_kind_before": "none", "repair_kind_after": "none", "repair_member_kind": "none",
		"repair_terms_match": "not_evaluated", "repair_attempts": float64(0),
	}
}

func appliedLine() map[string]any {
	return map[string]any{
		"outcome": "repaired", "failed_invariant": "", "frame_gate": "passed", "repair_decision": "applied",
		"repair_kind_before": "named_subject", "repair_kind_after": "children_of_scope", "repair_member_kind": "team",
		"repair_terms_match": "match", "repair_attempts": float64(1),
	}
}

func repairCells() []repairCell {
	withNamed := func(mutate func(*NamedSubjectExpression)) func() QuestionFrame {
		return func() QuestionFrame {
			frame := countOverNamedSubject()
			mutate(frame.SubjectExpression.Named)
			return frame
		}
	}
	withFrame := func(mutate func(*QuestionFrame)) func() QuestionFrame {
		return func() QuestionFrame {
			frame := countOverNamedSubject()
			mutate(&frame)
			return frame
		}
	}
	refusedI9 := func(decision, kindBefore string) map[string]any {
		line := refusedI9Line(decision)
		line["repair_kind_before"] = kindBefore
		return line
	}
	refusedI9Terms := func(decision, termsMatch string) map[string]any {
		line := refusedI9(decision, "named_subject")
		line["repair_terms_match"] = termsMatch
		return line
	}
	multiTerm := withNamed(func(named *NamedSubjectExpression) { named.Terms = []string{"owner", repairAnchorTerm} })
	return []repairCell{
		{
			cell: "hint absent", receipt: func(r *ModelExecutionReceipt) { r.RequestedSubjectKind = "" },
			frame: countOverNamedSubject, wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: refusedI9("declined_hint_absent", "named_subject"),
		},
		{
			cell: "hint dropped as unrecognised by the sanitizer",
			receipt: func(r *ModelExecutionReceipt) {
				r.RequestedSubjectKind = ""
				r.RequestedSubjectKindUnrecognized = true
			},
			frame: countOverNamedSubject, wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: refusedI9("declined_hint_unrecognized", "named_subject"),
		},
		{
			cell: "hint outside the vocabulary", receipt: func(r *ModelExecutionReceipt) { r.RequestedSubjectKind = SubjectKind("squad") },
			frame: countOverNamedSubject, wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: refusedI9("declined_hint_unrecognized", "named_subject"),
		},
		{
			cell:    "hint equals the named subject's stated kind",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withNamed(func(named *NamedSubjectExpression) {
				kind := SubjectTeam
				named.ExpectedKind = &kind
			}),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine:    refusedI9("declined_hint_is_subject_kind", "named_subject"),
		},
		{
			cell: "hint equals the sample's anchor kind", receipt: func(r *ModelExecutionReceipt) { r.ScopeAnchorKind = SubjectTeam },
			frame: countOverNamedSubject, wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: refusedI9("declined_hint_is_subject_kind", "named_subject"),
		},
		{
			cell:    "the named subject states a different kind than the hint",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withNamed(func(named *NamedSubjectExpression) {
				kind := SubjectRepository
				named.ExpectedKind = &kind
			}),
			wantOutcome: FrameValidationOutcomeRepaired, wantKind: SubjectExpressionChildrenOfScope,
			wantAnchors: []string{repairAnchorTerm}, wantLine: appliedLine(),
		},
		{
			cell: "flat terms differ from the named subject's terms", receipt: func(*ModelExecutionReceipt) {},
			frame: countOverNamedSubject, flat: []string{"flat-term"}, wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: refusedI9Terms("declined_terms_diverge", "diverge"),
		},
		{
			cell: "flat terms empty", receipt: func(*ModelExecutionReceipt) {},
			frame: countOverNamedSubject, flat: []string{}, wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: refusedI9Terms("declined_terms_diverge", "diverge"),
		},
		{
			cell: "flat terms a superset of the named terms", receipt: func(*ModelExecutionReceipt) {},
			frame: countOverNamedSubject, flat: []string{repairAnchorTerm, "other-term"}, wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: refusedI9Terms("declined_terms_diverge", "diverge"),
		},
		{
			cell: "flat terms a subset of a multi-term named subject", receipt: func(*ModelExecutionReceipt) {},
			frame: multiTerm, flat: []string{repairAnchorTerm}, wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: refusedI9Terms("declined_terms_diverge", "diverge"),
		},
		{
			cell: "flat terms equal up to case and duplicates", receipt: func(*ModelExecutionReceipt) {},
			frame: countOverNamedSubject, flat: []string{"ANCHOR-REPO", repairAnchorTerm}, wantOutcome: FrameValidationOutcomeRepaired,
			wantKind: SubjectExpressionChildrenOfScope, wantAnchors: []string{repairAnchorTerm}, wantLine: appliedLine(),
		},
		{
			// A named subject with several terms is one subject with several
			// retrieval pointers; I5 admits several anchor terms.
			cell: "multi-term named subject with the same flat terms", receipt: func(*ModelExecutionReceipt) {},
			frame: multiTerm, flat: []string{repairAnchorTerm, "owner"}, wantOutcome: FrameValidationOutcomeRepaired,
			wantKind: SubjectExpressionChildrenOfScope, wantAnchors: []string{"owner", repairAnchorTerm}, wantLine: appliedLine(),
		},
		{
			cell: "hint a vocabulary kind no discovery arm serves", receipt: func(r *ModelExecutionReceipt) { r.RequestedSubjectKind = contractsv1.ContextFabricSubjectDeployment },
			frame: countOverNamedSubject, wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: refusedI9Terms("declined_hint_unservable", "match"),
		},
		{
			cell: "hint organization", receipt: func(r *ModelExecutionReceipt) { r.RequestedSubjectKind = SubjectOrganization },
			frame: countOverNamedSubject, wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: refusedI9Terms("declined_hint_unservable", "match"),
		},
		{
			// An explicit set enumerates named operands and is not a scoped
			// cohort, so it is never repaired.
			cell: "explicit_set count with a hint", receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.SubjectExpression = SubjectExpression{Kind: SubjectExpressionExplicitSet, Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
					{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"operand-a"}}},
					{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"operand-b"}}},
				}}}
			}),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine:    refusedI9("declined_not_named_subject", "explicit_set"),
		},
		{
			cell: "repaired frame drops the requested group axis", receipt: func(r *ModelExecutionReceipt) { r.GroupKind = SubjectProject },
			frame: countOverNamedSubject, wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: map[string]any{
				"outcome": "refused_invalid", "failed_invariant": "i6", "failure_detail": "requested_group_axis_not_expressed",
				"frame_gate": "rejected:i6", "repair_decision": "refused_after_repair", "repair_invariant": "i9",
				"repair_kind_before": "named_subject", "repair_kind_after": "children_of_scope", "repair_member_kind": "team",
				"repair_terms_match": "match", "repair_attempts": float64(1),
			},
		},
		{
			cell: "repaired frame fails a phase-A2 invariant", receipt: func(*ModelExecutionReceipt) {},
			frame:       withFrame(func(frame *QuestionFrame) { frame.Emphasis = []AnswerEmphasis{EmphasisNegativeOutliers} }),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: map[string]any{
				"outcome": "refused_invalid", "failed_invariant": "i14", "failed_phase": "a2",
				"frame_gate": "rejected:i14", "repair_decision": "refused_after_repair",
				"repair_kind_after": "children_of_scope", "repair_member_kind": "team", "repair_terms_match": "match",
				"repair_attempts": float64(1),
			},
		},
		{
			cell: "an earlier A1 invariant fails first", receipt: func(*ModelExecutionReceipt) {},
			frame:       withFrame(func(frame *QuestionFrame) { frame.Goals = []InvestigationGoal{GoalCountOrAggregate, GoalDescribeTrend} }),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine:    notApplicableLine("refused_invalid", "i8", "rejected:i8"),
		},
		{
			cell: "a blank named term fails I3 first", receipt: func(*ModelExecutionReceipt) {},
			frame:       withNamed(func(named *NamedSubjectExpression) { named.Terms = []string{" "} }),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine:    notApplicableLine("refused_invalid", "i3", "rejected:i3"),
		},
		{
			cell: "control: a named subject that counts nothing", receipt: func(*ModelExecutionReceipt) {},
			frame:       withFrame(func(frame *QuestionFrame) { frame.Goals = []InvestigationGoal{GoalAssessState} }),
			wantOutcome: FrameValidationOutcomeValid, wantKind: SubjectExpressionNamed,
			wantLine: notApplicableLine("valid", "", "passed"),
		},
		{
			cell: "control: a count already scoped", receipt: func(*ModelExecutionReceipt) {},
			frame:       withFrame(func(frame *QuestionFrame) { frame.SubjectExpression = scopedExpression(SubjectTeam) }),
			wantOutcome: FrameValidationOutcomeValid, wantKind: SubjectExpressionChildrenOfScope,
			wantLine: notApplicableLine("valid", "", "passed"),
		},
	}
}

// TestTheCountKindRepairIsBounded executes every clause of the bound through
// the production interpreter, and holds that every decision the vocabulary
// names has an executed driver -- jointly with TestTheCompareGroupedRepairIsBounded,
// held for the whole vocabulary by TestEveryFrameRepairDecisionHasAnExecutedDriver.
func TestTheCountKindRepairIsBounded(t *testing.T) {
	for _, testCase := range repairCells() {
		t.Run(testCase.cell, func(t *testing.T) {
			receipt := classAReceipt()
			testCase.receipt(&receipt)
			flat := testCase.flat
			if flat == nil {
				flat = []string{repairAnchorTerm}
			}
			run := interpretForRepair(t, receipt, testCase.frame(), flat)

			if run.receipt.FrameOutcome != testCase.wantOutcome {
				t.Errorf("receipt frame outcome = %q, want %q", run.receipt.FrameOutcome, testCase.wantOutcome)
			}
			switch {
			case testCase.wantKind == "" && run.outcome.Frame != nil:
				t.Errorf("the turn carries a %q frame, want none", run.outcome.Frame.SubjectExpression.Kind)
			case testCase.wantKind != "" && run.outcome.Frame == nil:
				t.Errorf("the turn carries no frame, want %q (gate %s)", testCase.wantKind, run.outcome.Gate.Observable())
			case testCase.wantKind != "" && run.outcome.Frame.SubjectExpression.Kind != testCase.wantKind:
				t.Errorf("carried frame kind = %q, want %q", run.outcome.Frame.SubjectExpression.Kind, testCase.wantKind)
			}
			if testCase.wantAnchors != nil {
				if run.outcome.Frame == nil || run.outcome.Frame.SubjectExpression.Scoped == nil ||
					!reflect.DeepEqual(run.outcome.Frame.SubjectExpression.Scoped.AnchorTerms, testCase.wantAnchors) {
					t.Errorf("carried frame = %+v, want anchor terms %#v (the named subject's own terms)", run.outcome.Frame, testCase.wantAnchors)
				}
			}
			if testCase.wantKind == "" && !run.outcome.Gate.Refuses() {
				t.Errorf("gate %s does not refuse a turn that carries no frame", run.outcome.Gate.Observable())
			}
			if testCase.wantKind != "" && run.outcome.Gate.Refuses() {
				t.Errorf("gate %s refuses a turn that carries a frame", run.outcome.Gate.Observable())
			}
			assertRepairLine(t, run.line, testCase.wantLine)
			assertRepairLine(t, run.family, map[string]any{
				"shadow_frame_observed": testCase.wantKind != "",
				"shadow_frame_outcome":  string(testCase.wantOutcome),
			})
		})
	}
}

// repairAtTheBound calls the production repair step on the Class A
// proposal's own refused result, carrying `attempts` prior attempts.
func repairAtTheBound(t *testing.T, attempts int) FrameValidationResult {
	t.Helper()
	receipt := classAReceipt()
	proposal := countOverNamedSubject()
	refused := validateAgainstInterpretation(receipt, proposal, ShapeSingleSubject)
	if refused.Outcome != FrameValidationOutcomeRefusedInvalid || refused.Failure.Invariant != FrameInvariantI9 {
		t.Fatalf("fixture defect: the Class A proposal validated to %q/%q without repair", refused.Outcome, refused.Failure.Invariant)
	}
	refused.Repair.Attempts = attempts
	return repairCountKindCollapse(receipt, proposal, ShapeSingleSubject, []string{repairAnchorTerm}, refused)
}

// TestTheRepairRunsAtMostOnce holds the bound on attempts: a result already
// carrying the bounded number of attempts is refused on the failure it
// carries, and the same result with none is repaired, once.
func TestTheRepairRunsAtMostOnce(t *testing.T) {
	t.Parallel()
	fresh := repairAtTheBound(t, 0)
	if fresh.Outcome != FrameValidationOutcomeRepaired || fresh.Repair.Decision != FrameRepairApplied || fresh.Repair.Attempts != 1 {
		t.Fatalf("control: fresh result outcome/decision/attempts = %q/%q/%d, want repaired/applied/1", fresh.Outcome, fresh.Repair.Decision, fresh.Repair.Attempts)
	}
	exhausted := repairAtTheBound(t, frameRepairBound)
	if exhausted.Outcome != FrameValidationOutcomeRefusedInvalid || exhausted.Failure.Invariant != FrameInvariantI9 {
		t.Fatalf("a result at the bound was repaired again: outcome %q invariant %q", exhausted.Outcome, exhausted.Failure.Invariant)
	}
	if exhausted.Repair.Decision != FrameRepairDeclinedBoundReached || exhausted.Repair.Attempts != frameRepairBound || exhausted.Repair.KindAfter != "" {
		t.Fatalf("repair at the bound = %+v, want declined_bound_reached with %d attempts and no repaired kind", exhausted.Repair, frameRepairBound)
	}
	if exhausted.Frame.SubjectExpression.Kind != "" {
		t.Fatalf("a refused result carries a frame: %+v", exhausted.Frame)
	}
}

// THE BOUNDED I7 REPAIR, EXECUTED. compareGroupedReceipt/compareOverGroupedCohort
// are the misread turn's fixtures: a compare goal over a grouped cohort
// (group team, member project), the shape corpus row
// cv-c3-grouped-explain-change's traces show -- no question text is
// carried, only its columns.

// compareGroupedReceipt is the misread turn's receipt: the group axis the
// same call requested.
func compareGroupedReceipt() ModelExecutionReceipt {
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.GroupKind = SubjectTeam
	receipt.RequestedSubjectKind = SubjectProject
	return receipt
}

// compareOverGroupedCohort is the misread proposal: a compare goal over a
// grouped cohort, paired with explain_change already stated.
func compareOverGroupedCohort() QuestionFrame {
	return QuestionFrame{
		Goals:             []InvestigationGoal{GoalCompare, GoalExplainChange},
		SubjectExpression: groupedExpression(SubjectProject, SubjectTeam),
		Temporal:          TemporalIntentPeriodComparison,
	}
}

type compareRepairCell struct {
	cell        string
	receipt     func(*ModelExecutionReceipt)
	frame       func() QuestionFrame
	wantOutcome FrameValidationOutcome
	// wantGoals is the carried frame's goal set, nil when the turn carries
	// no frame.
	wantGoals []InvestigationGoal
	// wantAcceptedJudgment is the frame-validation line's own
	// accepted_judgment value, checked only when wantGoals is non-nil
	// (there is no accepted frame otherwise). "" means the line must say
	// `none` -- no repair populated it.
	wantAcceptedJudgment string
	wantLine             map[string]any
}

func compareRefusedI7Line(decision string) map[string]any {
	return map[string]any{
		"outcome": "refused_invalid", "failed_invariant": "i7", "failure_detail": "compare_requires_explicit_set",
		"frame_gate": "rejected:i7", "repair_decision": decision, "repair": "compare_grouped_collapse", "repair_invariant": "i7",
		"repair_kind_before": "grouped_members", "repair_kind_after": "none", "repair_member_kind": "none",
		"repair_terms_match": "not_evaluated", "repair_attempts": float64(0),
	}
}

func compareAppliedLine() map[string]any {
	return map[string]any{
		"outcome": "repaired", "failed_invariant": "", "frame_gate": "passed", "repair_decision": "applied",
		"repair": "compare_grouped_collapse", "repair_invariant": "i7",
		"repair_kind_before": "grouped_members", "repair_kind_after": "grouped_members",
		"repair_member_kind": "none", "repair_terms_match": "not_evaluated", "repair_attempts": float64(1),
	}
}

// compareRepairCells is the I7 bound's own domain table: every declared
// clause, plus the two controls a shared table needs (an earlier invariant
// failing first, and a proposal I7 never touches).
func compareRepairCells() []compareRepairCell {
	withFrame := func(mutate func(*QuestionFrame)) func() QuestionFrame {
		return func() QuestionFrame {
			frame := compareOverGroupedCohort()
			mutate(&frame)
			return frame
		}
	}
	return []compareRepairCell{
		{
			// NormalizeFrame sorts Goals into vocabulary order
			// (assess_state precedes explain_change there), independent of
			// the repair's own append order.
			cell:                 "compare with explain_change already stated adds assess_state",
			receipt:              func(*ModelExecutionReceipt) {},
			frame:                compareOverGroupedCohort,
			wantOutcome:          FrameValidationOutcomeRepaired,
			wantGoals:            []InvestigationGoal{GoalAssessState, GoalExplainChange},
			wantAcceptedJudgment: "the current state and an explanation of the change",
			wantLine:             compareAppliedLine(),
		},
		{
			cell:    "compare with describe_trend already stated keeps it and adds explain_change",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.Goals = []InvestigationGoal{GoalCompare, GoalDescribeTrend}
			}),
			wantOutcome:          FrameValidationOutcomeRepaired,
			wantGoals:            []InvestigationGoal{GoalDescribeTrend, GoalExplainChange},
			wantAcceptedJudgment: "the trend over time and an explanation of the change",
			wantLine:             compareAppliedLine(),
		},
		{
			cell:    "compare with describe_trend and explain_change both already stated only drops compare",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.Goals = []InvestigationGoal{GoalCompare, GoalDescribeTrend, GoalExplainChange}
			}),
			wantOutcome:          FrameValidationOutcomeRepaired,
			wantGoals:            []InvestigationGoal{GoalDescribeTrend, GoalExplainChange},
			wantAcceptedJudgment: "the trend over time and an explanation of the change",
			wantLine:             compareAppliedLine(),
		},
		{
			// A THIRD co-occurring goal outside {assess_state,
			// describe_trend, explain_change} -- unevidenced today, but
			// replaceCompareGoal passes any such goal through unchanged
			// (it only ever removes compare), so requestedJudgmentForGoals
			// must still name it rather than silently dropping it from the
			// text synthesis and Info both read.
			cell:    "a third co-occurring goal is passed through and named in the composed judgment",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.Goals = []InvestigationGoal{GoalCompare, GoalExplainChange, GoalRankOrSurvey}
			}),
			wantOutcome:          FrameValidationOutcomeRepaired,
			wantGoals:            []InvestigationGoal{GoalAssessState, GoalRankOrSurvey, GoalExplainChange},
			wantAcceptedJudgment: "the current state and a ranking or survey and an explanation of the change",
			wantLine:             compareAppliedLine(),
		},
		{
			// A scoped cohort has no evidenced reading for this bound; the
			// traces this repair answers show grouped_members only.
			cell:    "compare over a scoped cohort has no evidenced reading and is declined",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.Goals = []InvestigationGoal{GoalCompare}
				frame.SubjectExpression = scopedExpression(SubjectTeam)
			}),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: func() map[string]any {
				line := compareRefusedI7Line("declined_not_grouped_cohort")
				line["repair_kind_before"] = "children_of_scope"
				return line
			}(),
		},
		{
			// explain_change is added unconditionally; a repaired frame
			// whose Temporal never left `current` still fails I8, and the
			// turn is refused with the invariant the repair could not
			// clear -- never relaxed to serve it anyway.
			cell:    "the repaired frame still fails I8 without a trend-compatible temporal",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.Temporal = TemporalIntentCurrent
			}),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: map[string]any{
				"outcome": "refused_invalid", "failed_invariant": "i8", "failure_detail": "trend_requires_non_current_temporal",
				"frame_gate": "rejected:i8", "repair_decision": "refused_after_repair", "repair_invariant": "i7",
				"repair_kind_before": "grouped_members", "repair_kind_after": "grouped_members",
				"repair_member_kind": "none", "repair_terms_match": "not_evaluated", "repair_attempts": float64(1),
			},
		},
		{
			cell:    "an earlier A1 invariant (I6, group equals member) fails first",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.SubjectExpression = groupedExpression(SubjectTeam, SubjectTeam)
			}),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine:    notApplicableLine("refused_invalid", "i6", "rejected:i6"),
		},
		{
			cell:    "control: describe_trend and explain_change alone never fail I7",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.Goals = []InvestigationGoal{GoalDescribeTrend, GoalExplainChange}
			}),
			wantOutcome: FrameValidationOutcomeValid,
			wantGoals:   []InvestigationGoal{GoalDescribeTrend, GoalExplainChange},
			wantLine:    notApplicableLine("valid", "", "passed"),
		},
	}
}

// TestTheCompareGroupedRepairIsBounded executes every clause of the I7
// bound through the production interpreter.
func TestTheCompareGroupedRepairIsBounded(t *testing.T) {
	for _, testCase := range compareRepairCells() {
		t.Run(testCase.cell, func(t *testing.T) {
			receipt := compareGroupedReceipt()
			testCase.receipt(&receipt)
			run := interpretForRepair(t, receipt, testCase.frame(), []string{repairAnchorTerm})

			if run.receipt.FrameOutcome != testCase.wantOutcome {
				t.Errorf("receipt frame outcome = %q, want %q", run.receipt.FrameOutcome, testCase.wantOutcome)
			}
			switch {
			case testCase.wantGoals == nil && run.outcome.Frame != nil:
				t.Errorf("the turn carries a %v frame, want none", run.outcome.Frame.Goals)
			case testCase.wantGoals != nil && run.outcome.Frame == nil:
				t.Errorf("the turn carries no frame, want goals %v (gate %s)", testCase.wantGoals, run.outcome.Gate.Observable())
			case testCase.wantGoals != nil && !reflect.DeepEqual(run.outcome.Frame.Goals, testCase.wantGoals):
				t.Errorf("carried frame goals = %v, want %v", run.outcome.Frame.Goals, testCase.wantGoals)
			}
			if testCase.wantGoals == nil && !run.outcome.Gate.Refuses() {
				t.Errorf("gate %s does not refuse a turn that carries no frame", run.outcome.Gate.Observable())
			}
			if testCase.wantGoals != nil && run.outcome.Gate.Refuses() {
				t.Errorf("gate %s refuses a turn that carries a frame", run.outcome.Gate.Observable())
			}
			assertRepairLine(t, run.line, testCase.wantLine)
			assertGoalsLogValue(t, run.line, "accepted_goals", testCase.wantGoals)
			assertRepairLine(t, run.line, map[string]any{"accepted_judgment": noneWhenEmpty(testCase.wantAcceptedJudgment)})
		})
	}
}

// assertGoalsLogValue compares a goalsLogValue-rendered log field
// (decoded JSON: []any of string) against want, by string content --
// straight `!=` panics on a slice-valued any, which is why this is not
// folded into assertRepairLine.
func assertGoalsLogValue(t *testing.T, line map[string]any, key string, want []InvestigationGoal) {
	t.Helper()
	raw, present := line[key]
	if !present {
		t.Errorf("%s is ABSENT from the frame-validation line", key)
		return
	}
	got, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s = %#v (%T), want a []any", key, raw, raw)
	}
	gotStrings := make([]string, len(got))
	for i, v := range got {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("%s[%d] = %#v (%T), want a string", key, i, v, v)
		}
		gotStrings[i] = s
	}
	wantStrings := make([]string, len(want))
	for i, goal := range want {
		wantStrings[i] = string(goal)
	}
	if !reflect.DeepEqual(gotStrings, wantStrings) {
		t.Errorf("%s = %v, want %v", key, gotStrings, wantStrings)
	}
}

// compareRepairAtTheBound calls the production repair step on the
// compare-grouped proposal's own refused result, carrying `attempts` prior
// attempts.
func compareRepairAtTheBound(t *testing.T, attempts int) FrameValidationResult {
	t.Helper()
	receipt := compareGroupedReceipt()
	proposal := compareOverGroupedCohort()
	refused := validateAgainstInterpretation(receipt, proposal, ShapeSingleSubject)
	if refused.Outcome != FrameValidationOutcomeRefusedInvalid || refused.Failure.Invariant != FrameInvariantI7 {
		t.Fatalf("fixture defect: the compare-grouped proposal validated to %q/%q without repair", refused.Outcome, refused.Failure.Invariant)
	}
	refused.Repair.Attempts = attempts
	return repairCompareGroupedCollapse(receipt, proposal, ShapeSingleSubject, nil, refused)
}

// TestTheCompareGroupedRepairRunsAtMostOnce holds the bound on attempts, the
// same shape TestTheRepairRunsAtMostOnce holds for the I9 repair.
func TestTheCompareGroupedRepairRunsAtMostOnce(t *testing.T) {
	t.Parallel()
	fresh := compareRepairAtTheBound(t, 0)
	if fresh.Outcome != FrameValidationOutcomeRepaired || fresh.Repair.Decision != FrameRepairApplied || fresh.Repair.Attempts != 1 {
		t.Fatalf("control: fresh result outcome/decision/attempts = %q/%q/%d, want repaired/applied/1", fresh.Outcome, fresh.Repair.Decision, fresh.Repair.Attempts)
	}
	exhausted := compareRepairAtTheBound(t, frameRepairBound)
	if exhausted.Outcome != FrameValidationOutcomeRefusedInvalid || exhausted.Failure.Invariant != FrameInvariantI7 {
		t.Fatalf("a result at the bound was repaired again: outcome %q invariant %q", exhausted.Outcome, exhausted.Failure.Invariant)
	}
	if exhausted.Repair.Decision != FrameRepairDeclinedBoundReached || exhausted.Repair.Attempts != frameRepairBound || exhausted.Repair.KindAfter != "" {
		t.Fatalf("repair at the bound = %+v, want declined_bound_reached with %d attempts and no repaired kind", exhausted.Repair, frameRepairBound)
	}
}

// compareGroupedPreRepairJudgment is the fixture's own pre-repair
// InterpretedQuestion.RequestedJudgment -- a placeholder describing the
// misread goal, never real corpus text, distinct from repairInterpretation's
// own "count" placeholder (that one belongs to the I9 fixtures this file's
// other tests share) so a synthesis-carry test can name its actual "before"
// value instead of guessing at a shared helper's unrelated placeholder.
const compareGroupedPreRepairJudgment = "compare"

// compareGroupedInterpretation is repairInterpretation with RequestedJudgment
// overridden to compareGroupedPreRepairJudgment, for the one test that reads
// the field's pre-repair value.
func compareGroupedInterpretation(flat []string) InterpretedQuestion {
	interpretation := repairInterpretation(flat)
	interpretation.RequestedJudgment = compareGroupedPreRepairJudgment
	return interpretation
}

// newCompareRepairEngine drives the production engine over the misread
// compare-grouped proposal, with a synthesizer SPY that captures the
// SynthesisInput it received -- the one channel synthesis reads
// InterpretedQuestion.RequestedJudgment through (chaos4636_synthesis_assembly.go),
// separate from the receipt and from the accepted frame the retrieval graph
// below receives.
func newCompareRepairEngine(t *testing.T) (*Engine, *[]SynthesisInput) {
	t.Helper()
	logs := captureEngineLogger(t)
	receipt := compareGroupedReceipt()
	proposal := compareOverGroupedCohort()
	receipt.QuestionFrame = &proposal
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	graph := &retrievalRecordingGraph{graphReaderStub: graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Cohort: countingCohort(SubjectProject, 2), Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}}
	captured := make([]SynthesisInput, 0, 1)
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{
			Runtime:        fakeModelRuntime{interpreted: compareGroupedInterpretation([]string{repairAnchorTerm}), receipt: receipt},
			Sink:           &fakeReceiptSink{},
			FrameTelemetry: logs.telemetry,
			Requirements:   registryDeriver{},
		},
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			captured = append(captured, input)
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Explained.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Explained.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:      store,
		Telemetry:    &recordingTelemetry{},
		Requirements: registryDeriver{},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(600, 0).UTC() },
		NewResultID:    func() string { return "result_compare_repair_01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine, &captured
}

// TestTheRepairedRequestedJudgmentReachesSynthesis drives Engine.Investigate
// over the production interpreter: synthesis receives
// Interpretation.RequestedJudgment stating the ACCEPTED shape (an
// explanation of the change), never the model's pre-repair "compare" --
// the P2 class this repair's carry exists to close, proven at the one call
// site that actually reads the field.
func TestTheRepairedRequestedJudgmentReachesSynthesis(t *testing.T) {
	engine, captured := newCompareRepairEngine(t)
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_compare_repair_01"

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_repair"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationComplete {
		t.Fatalf("status = %q (basis %q, limitations %#v), want complete: the repaired turn was not served", result.Status, result.RefusalBasis, result.Limitations)
	}
	if len(*captured) != 1 {
		t.Fatalf("synthesizer calls = %d, want exactly 1", len(*captured))
	}
	got := (*captured)[0].Interpretation.RequestedJudgment
	want := "the current state and an explanation of the change"
	if got != want {
		t.Fatalf("SynthesisInput.Interpretation.RequestedJudgment = %q, want %q (the fixture's own pre-repair value was %q)",
			got, want, compareGroupedPreRepairJudgment)
	}
	if got == compareGroupedPreRepairJudgment {
		t.Fatal("SynthesisInput.Interpretation.RequestedJudgment still carries the fixture's pre-repair value: the carry never overwrote it")
	}
}

// TestTheRepairedRequestedJudgmentReachesTheEnsembleWinner drives Interpret()
// over the SampledRuntime path (interpretEnsemble), not interpretOneSample
// directly -- both paths call interpretOneSample, but only a test that goes
// through Interpret() itself proves the ensemble's own winner-selection
// (interpretEnsemble's own `return winner.question, ...`) still returns the
// PER-SAMPLE question interpretOneSample already fixed, not some other
// sample's or a merged one. Every sample is given the identical
// compare-grouped fixture, so whichever index wins carries the same
// expectation.
func TestTheRepairedRequestedJudgmentReachesTheEnsembleWinner(t *testing.T) {
	receipt := compareGroupedReceipt()
	proposal := compareOverGroupedCohort()
	receipt.QuestionFrame = &proposal
	question := compareGroupedInterpretation([]string{repairAnchorTerm})
	sampled := &sampledRuntimeStub{
		receipt: receipt,
		perIdx:  map[int]InterpretedQuestion{0: question, 1: question, 2: question},
	}
	interpreter := RuntimeQuestionInterpreter{SampledRuntime: sampled, Sink: &concurrentReceiptSink{}, EnsembleSize: 3}

	got, _, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_repair"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	want := "the current state and an explanation of the change"
	if got.RequestedJudgment != want {
		t.Fatalf("ensemble winner RequestedJudgment = %q, want %q (the fixture's own pre-repair value was %q)",
			got.RequestedJudgment, want, compareGroupedPreRepairJudgment)
	}
}

// TestCompareGroupedRepairDecisionIsInTheClosedVocabulary holds that this
// repair's own decline decision is a member of the closed vocabulary array,
// not only a constant a call site names: a constant dropped from the
// enumerated array would still compile and still log a value, invisibly
// leaving it out of every consumer that walks the vocabulary rather than
// naming the constant directly.
func TestCompareGroupedRepairDecisionIsInTheClosedVocabulary(t *testing.T) {
	for _, member := range FrameRepairDecisionVocabulary() {
		if member == FrameRepairDeclinedNotGroupedCohort {
			return
		}
	}
	t.Fatalf("%q is not a member of FrameRepairDecisionVocabulary()", FrameRepairDeclinedNotGroupedCohort)
}

// TestRequestedJudgmentCoversTheWholeGoalVocabulary walks
// InvestigationGoalVocabulary() by enumeration: every declared Goal has a
// non-empty fragment in goalJudgmentPhrase, so a ninth goal added to the
// vocabulary with no fragment here fails this test instead of silently
// composing a phrase that drops it out of the text synthesis and Info both
// read.
func TestRequestedJudgmentCoversTheWholeGoalVocabulary(t *testing.T) {
	for _, goal := range InvestigationGoalVocabulary() {
		if goalJudgmentPhrase[goal] == "" {
			t.Errorf("goalJudgmentPhrase has no fragment for %q", goal)
		}
	}
}

// TestRequestedJudgmentForGoalsComposesEveryAcceptedGoal holds the
// composition itself, not only the table: a goal a repair merely PASSES
// THROUGH (never invents) still contributes its own fragment, and the
// result names every accepted goal exactly once regardless of how many
// co-occur.
func TestRequestedJudgmentForGoalsComposesEveryAcceptedGoal(t *testing.T) {
	got := requestedJudgmentForGoals([]InvestigationGoal{GoalAssessState, GoalRankOrSurvey, GoalExplainChange})
	want := "the current state and a ranking or survey and an explanation of the change"
	if got != want {
		t.Fatalf("requestedJudgmentForGoals(...) = %q, want %q", got, want)
	}
}

// THE BOUNDED I7 REPAIR FOR DISCOVERED_KIND, EXECUTED (CHAOS-6003).
// rankingReceipt/compareOverDiscoveredCohort are the misread turn's
// fixtures: a compare goal paired with rank_or_survey over a discovered
// cohort (member team), the shape corpus row
// cv-c4-discovered-rank-both-ends's traces show -- no question text is
// carried, only its columns (family discovered_cohort_ranking, variant
// discovered_kind, member_kind team).

// rankingReceipt is the misread turn's receipt. Neither this repair's own
// guard nor I7 itself reads a group/member hint, so this is the plain
// fixture with no field overridden.
func rankingReceipt() ModelExecutionReceipt {
	return validModelReceiptFixture(ModelOperationInterpret)
}

// compareOverDiscoveredCohort is the misread proposal: a compare goal
// paired with rank_or_survey over a discovered cohort.
func compareOverDiscoveredCohort() QuestionFrame {
	return QuestionFrame{
		Goals:             []InvestigationGoal{GoalCompare, GoalRankOrSurvey},
		SubjectExpression: discoveredExpression(SubjectTeam),
		Temporal:          TemporalIntentCurrent,
	}
}

type rankingRepairCell struct {
	cell        string
	receipt     func(*ModelExecutionReceipt)
	frame       func() QuestionFrame
	wantOutcome FrameValidationOutcome
	// wantGoals is the carried frame's goal set, nil when the turn carries
	// no frame.
	wantGoals []InvestigationGoal
	// wantAcceptedJudgment is the frame-validation line's own
	// accepted_judgment value, checked only when wantGoals is non-nil. ""
	// means the line must say `none` -- no repair populated it.
	wantAcceptedJudgment string
	wantLine             map[string]any
}

func rankingRefusedI7Line(decision string) map[string]any {
	return map[string]any{
		"outcome": "refused_invalid", "failed_invariant": "i7", "failure_detail": "compare_requires_explicit_set",
		"frame_gate": "rejected:i7", "repair_decision": decision, "repair": "compare_ranking_collapse", "repair_invariant": "i7",
		"repair_kind_before": "discovered_kind", "repair_kind_after": "none", "repair_member_kind": "none",
		"repair_terms_match": "not_evaluated", "repair_attempts": float64(0),
	}
}

func rankingAppliedLine() map[string]any {
	return map[string]any{
		"outcome": "repaired", "failed_invariant": "", "frame_gate": "passed", "repair_decision": "applied",
		"repair": "compare_ranking_collapse", "repair_invariant": "i7",
		"repair_kind_before": "discovered_kind", "repair_kind_after": "discovered_kind",
		"repair_member_kind": "none", "repair_terms_match": "not_evaluated", "repair_attempts": float64(1),
	}
}

// rankingRepairCells is the I7-over-discovered_kind bound's own domain
// table: the whole cell set the invariant names -- goal sets (rank_or_survey
// present/absent) x cohort kinds (discovered_kind and every sibling kind
// this repair does NOT own) x the two controls a shared table needs (a
// preceding invariant failing first, and a proposal I7 never touches).
func rankingRepairCells() []rankingRepairCell {
	withFrame := func(mutate func(*QuestionFrame)) func() QuestionFrame {
		return func() QuestionFrame {
			frame := compareOverDiscoveredCohort()
			mutate(&frame)
			return frame
		}
	}
	return []rankingRepairCell{
		{
			// accepted_judgment stays "none": unlike the grouped sibling,
			// this repair invents no goal, so the model's own free text
			// (untouched, carried on the interpretation itself, not on
			// this event) already describes the surviving Goals and a
			// generic substitute would only discard it -- see this
			// repair's own doc comment and
			// TestTheRankingRepairPreservesTheModelsOwnJudgment below.
			cell:                 "compare with rank_or_survey over a discovered cohort drops compare",
			receipt:              func(*ModelExecutionReceipt) {},
			frame:                compareOverDiscoveredCohort,
			wantOutcome:          FrameValidationOutcomeRepaired,
			wantGoals:            []InvestigationGoal{GoalRankOrSurvey},
			wantAcceptedJudgment: "",
			wantLine:             rankingAppliedLine(),
		},
		{
			// A co-occurring goal outside {compare, rank_or_survey} is kept,
			// unchanged, never dropped and never used to invent a second
			// companion goal the grouped sibling would add.
			cell:    "a third co-occurring goal is kept unchanged",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.Goals = []InvestigationGoal{GoalCompare, GoalRankOrSurvey, GoalAssessState}
			}),
			wantOutcome:          FrameValidationOutcomeRepaired,
			wantGoals:            []InvestigationGoal{GoalAssessState, GoalRankOrSurvey},
			wantAcceptedJudgment: "",
			wantLine:             rankingAppliedLine(),
		},
		{
			// THE INVARIANT'S OWN NEGATIVE: compare with NO rank_or_survey
			// has no ranking reading to fall back to. Never repaired into a
			// ranking nobody asked for -- stays a rejection.
			cell:    "compare alone, with no rank_or_survey, is declined and stays refused",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.Goals = []InvestigationGoal{GoalCompare}
			}),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine:    rankingRefusedI7Line("declined_not_ranking_goal"),
		},
		{
			// compare with rank_or_survey over children_of_scope: not this
			// repair's cohort shape. repairCompareGroupedCollapse (which
			// runs first in frameRepairTable) already declined it as
			// declined_not_grouped_cohort; this repair passes that decision
			// through rather than recording a second, competing decline.
			cell:    "compare with rank_or_survey over a scoped cohort has no evidenced reading and is declined",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.SubjectExpression = scopedExpression(SubjectTeam)
			}),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: func() map[string]any {
				line := compareRefusedI7Line("declined_not_grouped_cohort")
				line["repair_kind_before"] = "children_of_scope"
				return line
			}(),
		},
		{
			// compare with rank_or_survey over a named subject: same class
			// of pass-through.
			cell:    "compare with rank_or_survey over a named subject has no evidenced reading and is declined",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.SubjectExpression = SubjectExpression{Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{Terms: []string{repairAnchorTerm}}}
			}),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: func() map[string]any {
				line := compareRefusedI7Line("declined_not_grouped_cohort")
				line["repair_kind_before"] = "named_subject"
				return line
			}(),
		},
		{
			// compare with rank_or_survey over organization_scope: same
			// class of pass-through.
			cell:    "compare with rank_or_survey over organization scope has no evidenced reading and is declined",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.SubjectExpression = SubjectExpression{Kind: SubjectExpressionOrganizationScope, Org: &OrganizationScopeExpression{}}
			}),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: func() map[string]any {
				line := compareRefusedI7Line("declined_not_grouped_cohort")
				line["repair_kind_before"] = "organization_scope"
				return line
			}(),
		},
		{
			// grouped_members is the SIBLING repair's own shape
			// (repairCompareGroupedCollapse): it applies and
			// adds explain_change (its own unconditional companion goal),
			// resolved before this repair runs, so this repair's guard
			// (which only proceeds while Failure.Invariant holds i7)
			// never touches the outcome the sibling already settled.
			cell:    "grouped_members is the sibling repair's shape, resolved before this one runs",
			receipt: func(r *ModelExecutionReceipt) { r.GroupKind = SubjectTeam; r.RequestedSubjectKind = SubjectProject },
			frame: withFrame(func(frame *QuestionFrame) {
				frame.SubjectExpression = groupedExpression(SubjectProject, SubjectTeam)
				frame.Temporal = TemporalIntentPeriodComparison
			}),
			wantOutcome:          FrameValidationOutcomeRepaired,
			wantGoals:            []InvestigationGoal{GoalAssessState, GoalRankOrSurvey, GoalExplainChange},
			wantAcceptedJudgment: "the current state and a ranking or survey and an explanation of the change",
			wantLine:             compareAppliedLine(),
		},
		{
			// An explicit comparison set is NEVER repaired away: I7 itself
			// never fails for explicit_set, so neither I7 repair is ever
			// invoked.
			cell:    "control: an explicit comparison set never fails I7, whatever else the proposal states",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.SubjectExpression = SubjectExpression{Kind: SubjectExpressionExplicitSet, Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
					{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"operand-a"}}},
					{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"operand-b"}}},
				}}}
			}),
			wantOutcome: FrameValidationOutcomeValid,
			wantGoals:   []InvestigationGoal{GoalCompare, GoalRankOrSurvey},
			wantLine:    notApplicableLine("valid", "", "passed"),
		},
		{
			// The repaired frame (goals: rank_or_survey, describe_trend)
			// still fails I8 without a trend-compatible temporal -- refused
			// with the invariant the repair could not clear, never relaxed
			// to serve it anyway.
			cell:    "the repaired frame still fails I8 without a trend-compatible temporal",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.Goals = []InvestigationGoal{GoalCompare, GoalRankOrSurvey, GoalDescribeTrend}
			}),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: map[string]any{
				"outcome": "refused_invalid", "failed_invariant": "i8", "failure_detail": "trend_requires_non_current_temporal",
				"frame_gate": "rejected:i8", "repair_decision": "refused_after_repair", "repair_invariant": "i7",
				"repair_kind_before": "discovered_kind", "repair_kind_after": "discovered_kind",
				"repair_member_kind": "none", "repair_terms_match": "not_evaluated", "repair_attempts": float64(1),
			},
		},
		{
			cell:    "a preceding A1 invariant (I15, empty goals) fails first",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.Goals = []InvestigationGoal{}
			}),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine:    notApplicableLine("refused_invalid", "i15", "rejected:i15"),
		},
		{
			cell:    "control: rank_or_survey alone never fails I7",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.Goals = []InvestigationGoal{GoalRankOrSurvey}
			}),
			wantOutcome: FrameValidationOutcomeValid,
			wantGoals:   []InvestigationGoal{GoalRankOrSurvey},
			wantLine:    notApplicableLine("valid", "", "passed"),
		},
	}
}

// TestTheCompareRankingRepairIsBounded executes every clause of the I7
// bound (discovered_kind, CHAOS-6003) through the production interpreter.
func TestTheCompareRankingRepairIsBounded(t *testing.T) {
	for _, testCase := range rankingRepairCells() {
		t.Run(testCase.cell, func(t *testing.T) {
			receipt := rankingReceipt()
			testCase.receipt(&receipt)
			run := interpretForRepair(t, receipt, testCase.frame(), []string{repairAnchorTerm})

			if run.receipt.FrameOutcome != testCase.wantOutcome {
				t.Errorf("receipt frame outcome = %q, want %q", run.receipt.FrameOutcome, testCase.wantOutcome)
			}
			switch {
			case testCase.wantGoals == nil && run.outcome.Frame != nil:
				t.Errorf("the turn carries a %v frame, want none", run.outcome.Frame.Goals)
			case testCase.wantGoals != nil && run.outcome.Frame == nil:
				t.Errorf("the turn carries no frame, want goals %v (gate %s)", testCase.wantGoals, run.outcome.Gate.Observable())
			case testCase.wantGoals != nil && !reflect.DeepEqual(run.outcome.Frame.Goals, testCase.wantGoals):
				t.Errorf("carried frame goals = %v, want %v", run.outcome.Frame.Goals, testCase.wantGoals)
			}
			if testCase.wantGoals == nil && !run.outcome.Gate.Refuses() {
				t.Errorf("gate %s does not refuse a turn that carries no frame", run.outcome.Gate.Observable())
			}
			if testCase.wantGoals != nil && run.outcome.Gate.Refuses() {
				t.Errorf("gate %s refuses a turn that carries a frame", run.outcome.Gate.Observable())
			}
			assertRepairLine(t, run.line, testCase.wantLine)
			assertGoalsLogValue(t, run.line, "accepted_goals", testCase.wantGoals)
			assertRepairLine(t, run.line, map[string]any{"accepted_judgment": noneWhenEmpty(testCase.wantAcceptedJudgment)})
		})
	}
}

// rankingRepairAtTheBound calls the production repair step on the
// compare-ranking proposal's own refused result, carrying `attempts` prior
// attempts.
func rankingRepairAtTheBound(t *testing.T, attempts int) FrameValidationResult {
	t.Helper()
	receipt := rankingReceipt()
	proposal := compareOverDiscoveredCohort()
	refused := validateAgainstInterpretation(receipt, proposal, ShapeSingleSubject)
	if refused.Outcome != FrameValidationOutcomeRefusedInvalid || refused.Failure.Invariant != FrameInvariantI7 {
		t.Fatalf("fixture defect: the compare-ranking proposal validated to %q/%q without repair", refused.Outcome, refused.Failure.Invariant)
	}
	refused.Repair.Attempts = attempts
	return repairCompareRankingCollapse(receipt, proposal, ShapeSingleSubject, nil, refused)
}

// TestTheCompareRankingRepairRunsAtMostOnce holds the bound on attempts, the
// same shape held for the I9 and grouped-I7 repairs.
func TestTheCompareRankingRepairRunsAtMostOnce(t *testing.T) {
	t.Parallel()
	fresh := rankingRepairAtTheBound(t, 0)
	if fresh.Outcome != FrameValidationOutcomeRepaired || fresh.Repair.Decision != FrameRepairApplied || fresh.Repair.Attempts != 1 {
		t.Fatalf("control: fresh result outcome/decision/attempts = %q/%q/%d, want repaired/applied/1", fresh.Outcome, fresh.Repair.Decision, fresh.Repair.Attempts)
	}
	exhausted := rankingRepairAtTheBound(t, frameRepairBound)
	if exhausted.Outcome != FrameValidationOutcomeRefusedInvalid || exhausted.Failure.Invariant != FrameInvariantI7 {
		t.Fatalf("a result at the bound was repaired again: outcome %q invariant %q", exhausted.Outcome, exhausted.Failure.Invariant)
	}
	if exhausted.Repair.Decision != FrameRepairDeclinedBoundReached || exhausted.Repair.Attempts != frameRepairBound || exhausted.Repair.KindAfter != "" {
		t.Fatalf("repair at the bound = %+v, want declined_bound_reached with %d attempts and no repaired kind", exhausted.Repair, frameRepairBound)
	}
}

// TestCompareRankingRepairDecisionsAreInTheClosedVocabulary holds that this
// repair's own decline decisions are members of the closed vocabulary
// array, not only constants a call site names.
func TestCompareRankingRepairDecisionsAreInTheClosedVocabulary(t *testing.T) {
	for _, want := range []FrameRepairDecision{FrameRepairDeclinedNotRankingGoal} {
		found := false
		for _, member := range FrameRepairDecisionVocabulary() {
			if member == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q is not a member of FrameRepairDecisionVocabulary()", want)
		}
	}
}

// rankingSyntheticJudgment is a distinctive, synthetic (non-corpus)
// RequestedJudgment string: specific enough that a generic closed-phrase
// substitute could never produce it by composition, so a test asserting it
// reached synthesis unchanged cannot pass by accident.
const rankingSyntheticJudgment = "rank the fixture subjects by a stated criterion"

// newRankingRepairEngine drives the production engine over the misread
// compare-ranking proposal, with a synthesizer SPY -- the same shape
// newCompareRepairEngine uses for the grouped sibling, proving this
// repair's own carry behavior (deliberately NOT populating
// RequestedJudgment -- see repairCompareRankingCollapse's own doc comment)
// leaves the model's own free text to reach synthesis untouched.
func newRankingRepairEngine(t *testing.T) (*Engine, *[]SynthesisInput) {
	t.Helper()
	logs := captureEngineLogger(t)
	receipt := rankingReceipt()
	proposal := compareOverDiscoveredCohort()
	receipt.QuestionFrame = &proposal
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	graph := &retrievalRecordingGraph{graphReaderStub: graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Cohort: countingCohort(SubjectTeam, 2), Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}}
	captured := make([]SynthesisInput, 0, 1)
	interpretation := repairInterpretation([]string{repairAnchorTerm})
	interpretation.RequestedJudgment = rankingSyntheticJudgment
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{
			Runtime:        fakeModelRuntime{interpreted: interpretation, receipt: receipt},
			Sink:           &fakeReceiptSink{},
			FrameTelemetry: logs.telemetry,
			Requirements:   registryDeriver{},
		},
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			captured = append(captured, input)
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Ranked.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Ranked.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:      store,
		Telemetry:    &recordingTelemetry{},
		Requirements: registryDeriver{},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(600, 0).UTC() },
		NewResultID:    func() string { return "result_ranking_repair_01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine, &captured
}

// TestTheRankingRepairPreservesTheModelsOwnJudgment drives Engine.Investigate
// over the production interpreter: synthesis receives the model's OWN
// RequestedJudgment text unchanged, never a generic closed-phrase
// substitute -- proven with a distinctive synthetic value a substitute could
// never construct by composition (goalJudgmentPhrase has no fragment able to
// spell it). An unconditional overwrite with requestedJudgmentForGoals's
// generic phrase would discard a real, specific ranking criterion (e.g. a
// stated "rank teams by deployment stability") for a bland "a ranking or
// survey" even when the model's own text already correctly described the
// surviving Goals -- because, unlike the grouped-cohort
// sibling, this repair invents no goal the model did not already state.
func TestTheRankingRepairPreservesTheModelsOwnJudgment(t *testing.T) {
	engine, captured := newRankingRepairEngine(t)
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_ranking_repair_01"

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_repair"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationComplete {
		t.Fatalf("status = %q (basis %q, limitations %#v), want complete: the repaired turn was not served", result.Status, result.RefusalBasis, result.Limitations)
	}
	if len(*captured) != 1 {
		t.Fatalf("synthesizer calls = %d, want exactly 1", len(*captured))
	}
	got := (*captured)[0].Interpretation.RequestedJudgment
	if got != rankingSyntheticJudgment {
		t.Fatalf("SynthesisInput.Interpretation.RequestedJudgment = %q, want the model's own unmodified %q", got, rankingSyntheticJudgment)
	}
}

// THE FOURTH BOUNDED REPAIR, EXECUTED (CHAOS-5992). Unlike the other three,
// this repair does not answer a FrameValidationFailure -- it fires on a
// frame phase A1/A2 already accepted, heading off the LATER
// member_kind_unservable refusal DecideFrameGate would otherwise reach for
// it. groupedMetricByRepoReceipt/groupedMetricByRepoFrame are the misread
// turn's fixtures: a grouped cohort (group repository, member metric) the
// corpus row basis-grouped-metric-by-repo's traces show -- no question text
// is carried, only its columns (family grouped_cohort_status, group
// repository, member unservable).

// groupedMetricByRepoReceipt is the misread turn's receipt: the group axis
// the same call requested.
func groupedMetricByRepoReceipt() ModelExecutionReceipt {
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.GroupKind = SubjectRepository
	return receipt
}

// groupedMetricByRepoFrame is the misread proposal: a grouped cohort whose
// member kind names a fact concept ("metric"), not a servable entity --
// distinct from the ILLEGAL self-group shape (group == member), which I6
// already refuses and this repair never reaches.
func groupedMetricByRepoFrame() QuestionFrame {
	return QuestionFrame{
		Goals:             []InvestigationGoal{GoalCountOrAggregate},
		SubjectExpression: groupedExpression(SubjectMetric, SubjectRepository),
		Temporal:          TemporalIntentCurrent,
	}
}

type memberKindAliasRepairCell struct {
	cell        string
	receipt     func(*ModelExecutionReceipt)
	frame       func() QuestionFrame
	wantOutcome FrameValidationOutcome
	// wantKind is the carried frame's kind. Unlike the other repair cell
	// tables, a DECLINED cell here still carries a frame (the proposal,
	// untouched, still Valid) -- the turn is refused, downstream, by
	// DecideFrameGate, which this table's wantLine frame_gate value
	// exercises directly rather than treating "no repair" as "no frame".
	wantKind SubjectExpressionKind
	// wantMemberKind is the carried discovered_kind frame's own member
	// kind on the applied path, empty otherwise.
	wantMemberKind SubjectKind
	wantLine       map[string]any
}

func memberKindAliasAppliedLine() map[string]any {
	return map[string]any{
		"outcome": "repaired", "failed_invariant": "", "frame_gate": "passed", "repair_decision": "applied",
		"repair": "member_kind_fact_alias_collapse", "repair_invariant": "none",
		"repair_kind_before": "grouped_members", "repair_kind_after": "discovered_kind",
		"repair_member_kind": "repository", "repair_terms_match": "not_evaluated", "repair_attempts": float64(1),
	}
}

func withGroupHintSource(line map[string]any, source string) map[string]any {
	line["group_hint_source"] = source
	return line
}

// memberKindAliasDeclinedLine is every DECLINE this repair produces: the
// proposed frame validated (Outcome stays valid, Kind stays grouped_members,
// no repair invariant applies because none failed), and DecideFrameGate
// -- not this function -- is what refuses the turn, on the proposal's own
// still-unservable member kind. gate is the frame_gate token that refusal
// renders as; "passed" for the one control (a genuinely discoverable
// member) that is not refused at all.
func memberKindAliasDeclinedLine(decision, gate string) map[string]any {
	return map[string]any{
		"outcome": "valid", "failed_invariant": "", "frame_gate": gate, "repair_decision": decision,
		"repair": "member_kind_fact_alias_collapse", "repair_invariant": "none",
		"repair_kind_before": "grouped_members", "repair_kind_after": "none",
		"repair_member_kind": "none", "repair_terms_match": "not_evaluated", "repair_attempts": float64(0),
	}
}

// memberKindAliasRepairCells is this repair's own domain table: goal/kind
// combinations across (member kind alias-set membership) x (group kind
// servability) x (declared-vs-receipt group kind match) x the two controls
// every repair table needs (an invariant that fails before this repair
// ever sees the frame, and a DIRECT model proposal of the identical
// repaired shape).
func memberKindAliasRepairCells() []memberKindAliasRepairCell {
	withFrame := func(mutate func(*QuestionFrame)) func() QuestionFrame {
		return func() QuestionFrame {
			frame := groupedMetricByRepoFrame()
			mutate(&frame)
			return frame
		}
	}
	return []memberKindAliasRepairCell{
		{
			cell:           "metric member over a servable group is collapsed to a flat discovered cohort",
			receipt:        func(*ModelExecutionReceipt) {},
			frame:          groupedMetricByRepoFrame,
			wantOutcome:    FrameValidationOutcomeRepaired,
			wantKind:       SubjectExpressionDiscoveredKind,
			wantMemberKind: SubjectRepository,
			wantLine:       memberKindAliasAppliedLine(),
		},
		{
			// A genuine two-level ask (incidents grouped by repository,
			// both servable) is NEVER collapsed -- that would lose the
			// member dimension the question named. Untouched (not even
			// CONSIDERED -- not_applicable, the same as every other
			// repair's own shape it does not own), it still serves fine on
			// its own: frame_gate passes.
			cell:    "a genuinely discoverable member kind is never collapsed",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.SubjectExpression = groupedExpression(SubjectIncident, SubjectRepository)
			}),
			wantOutcome: FrameValidationOutcomeValid,
			wantKind:    SubjectExpressionGroupedMembers,
			wantLine:    notApplicableLine("valid", "", "passed"),
		},
		{
			// An unservable member kind outside the ticket-scoped alias
			// set has no evidenced reading and is never guessed at --
			// stays refused downstream, exactly as before this repair.
			cell:    "an unservable member kind outside the alias set has no evidenced reading",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.SubjectExpression = groupedExpression(SubjectDeployment, SubjectRepository)
			}),
			wantOutcome: FrameValidationOutcomeValid,
			wantKind:    SubjectExpressionGroupedMembers,
			wantLine:    memberKindAliasDeclinedLine("declined_not_fact_alias_kind", "refused:member_kind_unservable"),
		},
		{
			// The model named the group axis only inside its frame; the
			// receipt takes it from that frame, so the repair completes and
			// the line says where the group kind came from.
			cell:           "a group kind named only by the frame is adopted and the collapse applies",
			receipt:        func(r *ModelExecutionReceipt) { r.GroupKind = "" },
			frame:          groupedMetricByRepoFrame,
			wantOutcome:    FrameValidationOutcomeRepaired,
			wantKind:       SubjectExpressionDiscoveredKind,
			wantMemberKind: SubjectRepository,
			wantLine:       withGroupHintSource(memberKindAliasAppliedLine(), "frame"),
		},
		{
			// A flat hint the model stated is never overwritten by the frame.
			cell:           "a stated flat group kind keeps its own source",
			receipt:        func(*ModelExecutionReceipt) {},
			frame:          groupedMetricByRepoFrame,
			wantOutcome:    FrameValidationOutcomeRepaired,
			wantKind:       SubjectExpressionDiscoveredKind,
			wantMemberKind: SubjectRepository,
			wantLine:       withGroupHintSource(memberKindAliasAppliedLine(), "model"),
		},
		{
			// A flat hint dropped as unrecognised is a stated disagreement
			// the frame never papers over.
			cell:        "an unrecognised flat group kind is never replaced by the frame's",
			receipt:     func(r *ModelExecutionReceipt) { r.GroupKind = ""; r.GroupKindUnrecognized = true },
			frame:       groupedMetricByRepoFrame,
			wantOutcome: FrameValidationOutcomeValid,
			wantKind:    SubjectExpressionGroupedMembers,
			wantLine:    withGroupHintSource(memberKindAliasDeclinedLine("declined_group_kind_mismatch", "refused:member_kind_unservable"), "none"),
		},
		{
			// The collapse TARGET (the group kind) must itself be
			// servable, or the repair would trade one unservable refusal
			// for another and hide the first.
			cell:    "a group kind with no discovery arm is never collapsed into",
			receipt: func(r *ModelExecutionReceipt) { r.GroupKind = SubjectDeployment },
			frame: withFrame(func(frame *QuestionFrame) {
				frame.SubjectExpression = groupedExpression(SubjectMetric, SubjectDeployment)
			}),
			wantOutcome: FrameValidationOutcomeValid,
			wantKind:    SubjectExpressionGroupedMembers,
			wantLine:    memberKindAliasDeclinedLine("declined_group_kind_unservable", "refused:member_kind_unservable"),
		},
		{
			// The receipt's own GroupKind (the signal that routed this
			// turn) must equal the frame's own declared GroupKind -- two
			// disagreeing signals are never silently reconciled by
			// trusting one of them.
			cell:        "a receipt group-kind hint that disagrees with the frame's own is never trusted",
			receipt:     func(r *ModelExecutionReceipt) { r.GroupKind = SubjectTeam },
			frame:       groupedMetricByRepoFrame,
			wantOutcome: FrameValidationOutcomeValid,
			wantKind:    SubjectExpressionGroupedMembers,
			wantLine:    memberKindAliasDeclinedLine("declined_group_kind_mismatch", "refused:member_kind_unservable"),
		},
		{
			// THE ILLEGAL SELF-GROUP CASE STAYS REFUSED, UNTOUCHED: I6
			// fails before this repair ever sees a Valid outcome, so it
			// is not_applicable, exactly like the other two repairs on an
			// invariant they do not answer.
			cell:    "an illegal self-group frame fails I6 before this repair ever runs",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.SubjectExpression = groupedExpression(SubjectRepository, SubjectRepository)
			}),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine:    notApplicableLine("refused_invalid", "i6", "rejected:i6"),
		},
		{
			// THE PROVENANCE BOUND'S OWN CONTROL: a DIRECT model proposal
			// of the IDENTICAL shape this repair produces (discovered_kind
			// over the requested group kind, no group axis expressed) is
			// refused exactly as before this repair existed -- the
			// admission is never inferred from shape, only from the
			// provenance this repair itself stamps.
			cell:    "a direct proposal of the repaired shape is refused exactly as before",
			receipt: func(*ModelExecutionReceipt) {},
			frame: func() QuestionFrame {
				return QuestionFrame{
					Goals:             []InvestigationGoal{GoalAssessState},
					SubjectExpression: discoveredExpression(SubjectRepository),
					Temporal:          TemporalIntentCurrent,
				}
			},
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: map[string]any{
				"outcome": "refused_invalid", "failed_invariant": "i6", "failure_detail": "requested_group_axis_not_expressed",
				"frame_gate": "rejected:i6", "repair_decision": "not_applicable", "repair": "none", "repair_invariant": "none",
				"repair_kind_before": "none", "repair_kind_after": "none", "repair_member_kind": "none",
				"repair_terms_match": "not_evaluated", "repair_attempts": float64(0),
			},
		},
		{
			cell:    "a preceding A1 invariant (I15, empty goals) fails first",
			receipt: func(*ModelExecutionReceipt) {},
			frame: withFrame(func(frame *QuestionFrame) {
				frame.Goals = []InvestigationGoal{}
			}),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine:    notApplicableLine("refused_invalid", "i15", "rejected:i15"),
		},
	}
}

// TestTheMemberKindFactAliasRepairIsBounded executes every clause of this
// repair's own bound through the production interpreter.
func TestTheMemberKindFactAliasRepairIsBounded(t *testing.T) {
	for _, testCase := range memberKindAliasRepairCells() {
		t.Run(testCase.cell, func(t *testing.T) {
			receipt := groupedMetricByRepoReceipt()
			testCase.receipt(&receipt)
			run := interpretForRepair(t, receipt, testCase.frame(), []string{repairAnchorTerm})

			if run.receipt.FrameOutcome != testCase.wantOutcome {
				t.Errorf("receipt frame outcome = %q, want %q", run.receipt.FrameOutcome, testCase.wantOutcome)
			}
			switch {
			case testCase.wantKind == "" && run.outcome.Frame != nil:
				t.Errorf("the turn carries a %v frame, want none", run.outcome.Frame.SubjectExpression.Kind)
			case testCase.wantKind != "" && run.outcome.Frame == nil:
				t.Fatalf("the turn carries no frame, want %q (gate %s)", testCase.wantKind, run.outcome.Gate.Observable())
			case testCase.wantKind != "" && run.outcome.Frame.SubjectExpression.Kind != testCase.wantKind:
				t.Errorf("carried frame kind = %q, want %q", run.outcome.Frame.SubjectExpression.Kind, testCase.wantKind)
			}
			if testCase.wantMemberKind != "" {
				if run.outcome.Frame == nil || run.outcome.Frame.SubjectExpression.Discovered == nil || run.outcome.Frame.SubjectExpression.Discovered.MemberKind != testCase.wantMemberKind {
					t.Errorf("carried frame = %+v, want discovered_kind member %q", run.outcome.Frame, testCase.wantMemberKind)
				}
			}
			if testCase.wantKind == "" && !run.outcome.Gate.Refuses() {
				t.Errorf("gate %s does not refuse a turn that carries no frame", run.outcome.Gate.Observable())
			}
			// NOTE, unlike the other three repairs' cell tables: wantKind !=
			// "" does not imply the gate serves here. member_kind_unservable
			// is a DIFFERENT refusal shape from rejected_invalid -- the
			// frame itself is valid and still carried (for disclosure);
			// only its declared population cannot be discovered. A declined
			// cell legitimately carries both a frame and a refusing gate.
			assertRepairLine(t, run.line, testCase.wantLine)
		})
	}
}

// memberKindAliasRepairAtTheBound calls the production repair step on the
// grouped-metric proposal's own already-valid result, carrying `attempts`
// prior attempts.
func memberKindAliasRepairAtTheBound(t *testing.T, attempts int) FrameValidationResult {
	t.Helper()
	receipt := groupedMetricByRepoReceipt()
	proposal := groupedMetricByRepoFrame()
	valid := validateAgainstInterpretation(receipt, proposal, ShapeSingleSubject)
	if valid.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("fixture defect: the grouped-metric proposal validated to %q without reaching this repair", valid.Outcome)
	}
	valid.Repair.Attempts = attempts
	return repairMemberKindFactAliasCollapse(receipt, proposal, ShapeSingleSubject, nil, valid)
}

// TestTheMemberKindFactAliasRepairRunsAtMostOnce holds the bound on
// attempts, the same shape held for the other three repairs.
func TestTheMemberKindFactAliasRepairRunsAtMostOnce(t *testing.T) {
	t.Parallel()
	fresh := memberKindAliasRepairAtTheBound(t, 0)
	if fresh.Outcome != FrameValidationOutcomeRepaired || fresh.Repair.Decision != FrameRepairApplied || fresh.Repair.Attempts != 1 {
		t.Fatalf("control: fresh result outcome/decision/attempts = %q/%q/%d, want repaired/applied/1", fresh.Outcome, fresh.Repair.Decision, fresh.Repair.Attempts)
	}
	exhausted := memberKindAliasRepairAtTheBound(t, frameRepairBound)
	if exhausted.Outcome != FrameValidationOutcomeValid || exhausted.Repair.Decision != FrameRepairDeclinedBoundReached || exhausted.Repair.Attempts != frameRepairBound {
		t.Fatalf("repair at the bound = %+v, want valid/declined_bound_reached/%d attempts", exhausted.Repair, frameRepairBound)
	}
	if exhausted.Frame.SubjectExpression.Kind != SubjectExpressionGroupedMembers {
		t.Fatalf("a result at the bound carries a repaired frame: %+v", exhausted.Frame)
	}
}

// TestMemberKindFactAliasRepairDecisionsAreInTheClosedVocabulary holds
// that this repair's own decline decisions are members of the closed
// vocabulary array.
func TestMemberKindFactAliasRepairDecisionsAreInTheClosedVocabulary(t *testing.T) {
	for _, want := range []FrameRepairDecision{
		FrameRepairDeclinedNotFactAliasKind,
		FrameRepairDeclinedGroupKindUnservable,
		FrameRepairDeclinedGroupKindMismatch,
	} {
		found := false
		for _, member := range FrameRepairDecisionVocabulary() {
			if member == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q is not a member of FrameRepairDecisionVocabulary()", want)
		}
	}
}

// TestMemberKindFactAliasRepairNameIsInTheClosedVocabulary holds that this
// repair's own name is a member of the closed vocabulary array -- the name
// counterpart to TestMemberKindFactAliasRepairDecisionsAreInTheClosedVocabulary
// above. frameRepairTable (the actual dispatch slice) and frameRepairNames
// (the exported vocabulary array) are two independent declarations; a repair
// can be wired into the former while silently missing from the latter, which
// no other test in this file catches.
func TestMemberKindFactAliasRepairNameIsInTheClosedVocabulary(t *testing.T) {
	for _, member := range FrameRepairNameVocabulary() {
		if member == FrameRepairMemberKindFactAliasCollapse {
			return
		}
	}
	t.Errorf("%q is not a member of FrameRepairNameVocabulary()", FrameRepairMemberKindFactAliasCollapse)
}

// newMemberKindAliasRepairEngine drives the production engine over the
// misread grouped-metric proposal, with a recording graph, the same shape
// newRepairEngine uses for the count-kind repair.
func newMemberKindAliasRepairEngine(t *testing.T) (*Engine, *retrievalRecordingGraph) {
	t.Helper()
	logs := captureEngineLogger(t)
	receipt := groupedMetricByRepoReceipt()
	proposal := groupedMetricByRepoFrame()
	receipt.QuestionFrame = &proposal
	graph := &retrievalRecordingGraph{graphReaderStub: graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Cohort: countingCohort(SubjectRepository, 3), Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}}
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{
			Runtime:        fakeModelRuntime{interpreted: repairInterpretation([]string{repairAnchorTerm}), receipt: receipt},
			Sink:           &fakeReceiptSink{},
			FrameTelemetry: logs.telemetry,
			Requirements:   registryDeriver{},
		},
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Assessed.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Assessed.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:      store,
		Telemetry:    &recordingTelemetry{},
		Requirements: registryDeriver{},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(600, 0).UTC() },
		NewResultID:    func() string { return "result_member_kind_alias_repair_01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine, graph
}

// TestTheGroupedMetricTurnIsServedAsAFlatRepositoryCohort drives
// Engine.Investigate end to end: the repaired turn SERVES, retrieval
// receives the discovered_kind{MemberKind: repository} frame this repair
// produced (never the grouped_members proposal the model emitted), and the
// turn is not refused at either of the two seams that independently
// compare a group kind against a member kind for equality -- the frame
// seam (requestedGroupAxisDropped, model_runtime.go) and the plan seam
// (planGroupAxisCollapsed's call site, this file), found and fixed
// together: a frame this repair produces collapses group and member onto
// the SAME kind by construction, and would trip both invariant-I6-shaped
// checks as a data-shape "surprise" without the provenance
// (QuestionFrame.CollapsedGroupAxisMemberKind) each one reads before
// concluding one.
func TestTheGroupedMetricTurnIsServedAsAFlatRepositoryCohort(t *testing.T) {
	engine, graph := newMemberKindAliasRepairEngine(t)
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_member_kind_alias_repair_01"

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_repair"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationComplete {
		t.Fatalf("status = %q (basis %q, limitations %#v), want complete: the collapsed cohort was not served", result.Status, result.RefusalBasis, result.Limitations)
	}
	if len(graph.frames) == 0 {
		t.Fatal("retrieval was never called for the repaired turn")
	}
	for call, frame := range graph.frames {
		if frame == nil || frame.SubjectExpression.Kind != SubjectExpressionDiscoveredKind ||
			frame.SubjectExpression.Discovered == nil || frame.SubjectExpression.Discovered.MemberKind != SubjectRepository {
			t.Fatalf("retrieval call %d received %+v, want a discovered_kind repository cohort", call, frame)
		}
	}
}

// TestTheRepairProvenanceSurvivesTheSemanticStatePersistenceCycle holds that the
// provenance the repair stamps on its frame is still on the frame after the
// semantic-state write and read that carries a frame into a later turn, and
// that a direct proposal of the identical shape carries none after the same
// persistence cycle. A carried frame that lost the stamp is refused at the frame
// seam and at the plan seam even though the turn that produced it was served.
func TestTheRepairProvenanceSurvivesTheSemanticStatePersistenceCycle(t *testing.T) {
	persistCycle := func(t *testing.T, frame QuestionFrame) QuestionFrame {
		t.Helper()
		gate := DecideFrameGate(FrameValidationResult{Frame: frame, Outcome: FrameValidationOutcomeValid}, true)
		state := BuildSemanticState(SemanticStateInput{
			Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyGroupedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, Gate: gate},
			EmittedShape:    ShapeOpen,
			GroupKind:       SubjectRepository,
			FamilyVersion:   QuestionFamilyTableVersion,
			RequestIdentity: carrierRequestIdentity("provenance persistence cycle"),
		})
		raw, err := EncodeSemanticState(state)
		if err != nil {
			t.Fatalf("EncodeSemanticState() error = %v", err)
		}
		decoded, status := DecodeSemanticState(raw)
		if status != SemanticStateReadAvailable || decoded == nil || decoded.Frame == nil {
			t.Fatalf("DecodeSemanticState() = (%v, %v), want an available state carrying the frame", decoded, status)
		}
		return *decoded.Frame
	}

	receipt := groupedMetricByRepoReceipt()
	proposed := groupedMetricByRepoFrame()
	repaired := validateProposedFrame(receipt, proposed, ShapeOpen, nil)
	if repaired.Outcome != FrameValidationOutcomeRepaired || repaired.Frame.CollapsedGroupAxisMemberKind != SubjectRepository {
		t.Fatalf("fixture defect: the repair did not stamp its provenance (outcome %q, stamp %q)", repaired.Outcome, repaired.Frame.CollapsedGroupAxisMemberKind)
	}

	t.Run("the repaired frame keeps its provenance", func(t *testing.T) {
		carried := persistCycle(t, repaired.Frame)
		if carried.CollapsedGroupAxisMemberKind != SubjectRepository {
			t.Fatalf("carried provenance = %q, want %q", carried.CollapsedGroupAxisMemberKind, SubjectRepository)
		}
		if requestedGroupAxisDropped(receipt, carried) {
			t.Fatal("the carried repaired frame is refused as a dropped group axis")
		}
	})

	t.Run("a direct proposal of the same shape still carries none", func(t *testing.T) {
		direct := repaired.Frame
		direct.CollapsedGroupAxisMemberKind = ""
		carried := persistCycle(t, direct)
		if carried.CollapsedGroupAxisMemberKind != "" {
			t.Fatalf("carried provenance = %q, want none", carried.CollapsedGroupAxisMemberKind)
		}
		if !requestedGroupAxisDropped(receipt, carried) {
			t.Fatal("a carried direct proposal is admitted as an expressed group axis")
		}
	})
}

// TestADirectGroupEqualsMemberDiscoveryIsRefusedAtThePlanSeam is the plan
// seam's OWN provenance control (CHAOS-5992), the sibling of
// TestTheCompareRankingRepairIsBounded's "direct proposal" cell at the
// frame seam: a DIRECT model proposal already shaped discovered_kind over
// the requested group kind (no grouped_members proposal, no repair
// involved at all) still collapses group onto member once discovery
// returns that same kind -- and is refused exactly as
// planGroupAxisCollapsed's own call site always refused it, because this
// frame carries no CollapsedGroupAxisMemberKind provenance for the engine's
// own repair-awareness check to find.
func TestADirectGroupEqualsMemberDiscoveryIsRefusedAtThePlanSeam(t *testing.T) {
	logs := captureEngineLogger(t)
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.GroupKind = SubjectRepository
	proposal := QuestionFrame{
		Goals:             []InvestigationGoal{GoalCountOrAggregate},
		SubjectExpression: groupedExpression(SubjectIncident, SubjectRepository),
		Temporal:          TemporalIntentCurrent,
	}
	receipt.QuestionFrame = &proposal
	// The graph returns REPOSITORY as the cohort kind despite the frame
	// declaring incident as its member -- the "data came back surprising"
	// scenario this seam's own doc comment describes, never touched by
	// this repair (member=incident is servable, so
	// repairMemberKindFactAliasCollapse treats it as not_applicable --
	// never even considered -- and never stamps provenance on a frame it
	// does not touch).
	graph := &retrievalRecordingGraph{graphReaderStub: graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Cohort: countingCohort(SubjectRepository, 3), Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}}
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{
			Runtime:        fakeModelRuntime{interpreted: repairInterpretation([]string{repairAnchorTerm}), receipt: receipt},
			Sink:           &fakeReceiptSink{},
			FrameTelemetry: logs.telemetry,
			Requirements:   registryDeriver{},
		},
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("synthesis must not run for a turn the plan seam refuses")
			return InvestigationResult{}, nil
		}),
		Results:      store,
		Telemetry:    &recordingTelemetry{},
		Requirements: registryDeriver{},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(600, 0).UTC() },
		NewResultID:    func() string { return "result_plan_seam_control_01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_plan_seam_control_01"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_repair"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.RefusalBasis != contractsv1.ContextFabricRefusalBasisFrameInvariantViolated {
		t.Fatalf("basis = %q, want %q: a frame with no repair provenance must still refuse when discovery collapses group onto member",
			result.RefusalBasis, contractsv1.ContextFabricRefusalBasisFrameInvariantViolated)
	}
}

// TestEveryFrameRepairDecisionHasAnExecutedDriver holds, over the WHOLE
// closed vocabulary and both repairs together, what
// TestTheCountKindRepairIsBounded and TestTheCompareGroupedRepairIsBounded
// each hold only for their own repair's decisions: every decision the
// vocabulary names has an executed driver somewhere in this file.
func TestEveryFrameRepairDecisionHasAnExecutedDriver(t *testing.T) {
	produced := map[FrameRepairDecision]bool{}
	for _, testCase := range repairCells() {
		receipt := classAReceipt()
		testCase.receipt(&receipt)
		flat := testCase.flat
		if flat == nil {
			flat = []string{repairAnchorTerm}
		}
		run := interpretForRepair(t, receipt, testCase.frame(), flat)
		produced[FrameRepairDecision(run.line["repair_decision"].(string))] = true
	}
	for _, attempts := range []int{0, frameRepairBound} {
		produced[repairAtTheBound(t, attempts).Repair.Decision] = true
	}
	for _, testCase := range compareRepairCells() {
		receipt := compareGroupedReceipt()
		testCase.receipt(&receipt)
		run := interpretForRepair(t, receipt, testCase.frame(), []string{repairAnchorTerm})
		produced[FrameRepairDecision(run.line["repair_decision"].(string))] = true
	}
	for _, attempts := range []int{0, frameRepairBound} {
		produced[compareRepairAtTheBound(t, attempts).Repair.Decision] = true
	}
	for _, testCase := range rankingRepairCells() {
		receipt := rankingReceipt()
		testCase.receipt(&receipt)
		run := interpretForRepair(t, receipt, testCase.frame(), []string{repairAnchorTerm})
		produced[FrameRepairDecision(run.line["repair_decision"].(string))] = true
	}
	for _, attempts := range []int{0, frameRepairBound} {
		produced[rankingRepairAtTheBound(t, attempts).Repair.Decision] = true
	}
	for _, testCase := range memberKindAliasRepairCells() {
		receipt := groupedMetricByRepoReceipt()
		testCase.receipt(&receipt)
		run := interpretForRepair(t, receipt, testCase.frame(), []string{repairAnchorTerm})
		produced[FrameRepairDecision(run.line["repair_decision"].(string))] = true
	}
	for _, attempts := range []int{0, frameRepairBound} {
		produced[memberKindAliasRepairAtTheBound(t, attempts).Repair.Decision] = true
	}
	for _, member := range FrameRepairDecisionVocabulary() {
		if !produced[member] {
			t.Errorf("no executed driver produces repair decision %q", member)
		}
	}
}

// classAScoringTriggers is the D41 Class A predicate, over the ACCEPTED frame
// (the frame the persisted receipt carries: the frame acted on, or the
// refused proposal) against a corpus row's intended columns.
func classAScoringTriggers(accepted QuestionFrame, intendedVariant SubjectExpressionKind, intendedMember, intendedGroup SubjectKind) bool {
	if !accepted.HasGoal(GoalCountOrAggregate) {
		return false
	}
	switch accepted.SubjectExpression.Kind {
	case SubjectExpressionNamed, SubjectExpressionExplicitSet:
	default:
		return false
	}
	switch intendedVariant {
	case SubjectExpressionChildrenOfScope, SubjectExpressionDiscoveredKind, SubjectExpressionGroupedMembers, SubjectExpressionOrganizationScope:
	default:
		return false
	}
	member, _ := accepted.SubjectExpression.MemberKind()
	group, _ := accepted.SubjectExpression.GroupKind()
	return accepted.SubjectExpression.Kind != intendedVariant || member != intendedMember || group != intendedGroup
}

// TestClassAScoresZeroOverTheRepairedFixture scores the three archived
// decisive-turn shapes of the row (named_subject, count, hint team) through
// the production interpreter: 0 of 3 trigger, and each accepted frame equals
// the row's intended columns.
func TestClassAScoresZeroOverTheRepairedFixture(t *testing.T) {
	const (
		intendedVariant = SubjectExpressionChildrenOfScope
		intendedMember  = SubjectTeam
		intendedGroup   = SubjectKind("")
	)
	reps := []struct {
		name    string
		receipt func(*ModelExecutionReceipt)
		terms   []string
	}{
		{"rep1: anchor kind stated", func(*ModelExecutionReceipt) {}, []string{repairAnchorTerm}},
		{"rep2: anchor kind absent", func(r *ModelExecutionReceipt) { r.ScopeAnchorKind = "" }, []string{repairAnchorTerm}},
		{"rep3: two anchor terms", func(*ModelExecutionReceipt) {}, []string{"owner", repairAnchorTerm}},
	}
	triggered := 0
	for _, rep := range reps {
		receipt := classAReceipt()
		rep.receipt(&receipt)
		frame := countOverNamedSubject()
		frame.SubjectExpression.Named.Terms = rep.terms
		run := interpretForRepair(t, receipt, frame, rep.terms)
		if run.receipt.QuestionFrame == nil {
			t.Fatalf("%s: the receipt carries no frame at all", rep.name)
		}
		accepted := *run.receipt.QuestionFrame
		if classAScoringTriggers(accepted, intendedVariant, intendedMember, intendedGroup) {
			triggered++
		}
		member, _ := accepted.SubjectExpression.MemberKind()
		group, _ := accepted.SubjectExpression.GroupKind()
		if accepted.SubjectExpression.Kind != intendedVariant || member != intendedMember || group != intendedGroup {
			t.Errorf("%s: accepted frame variant/member/group = %q/%q/%q, want the row's intended %q/%q/%q",
				rep.name, accepted.SubjectExpression.Kind, member, group, intendedVariant, intendedMember, intendedGroup)
		}
		if scoped := accepted.SubjectExpression.Scoped; scoped == nil || !reflect.DeepEqual(scoped.AnchorTerms, rep.terms) {
			t.Errorf("%s: scoped variant = %+v, want anchor terms %#v", rep.name, scoped, rep.terms)
		}
	}
	if triggered != 0 {
		t.Fatalf("Class A triggered on %d of %d reps, want 0", triggered, len(reps))
	}
	// The predicate is live: the refused proposal itself triggers it.
	if !classAScoringTriggers(countOverNamedSubject(), intendedVariant, intendedMember, intendedGroup) {
		t.Fatal("control: the Class A predicate does not trigger on the misread proposal, so a 0 proves nothing")
	}
}

// retrievalRecordingGraph records, per ResolveSubjects call, the frame, the
// anchor-kind hint and the flat subject terms the engine handed retrieval.
type retrievalRecordingGraph struct {
	graphReaderStub
	frames      []*QuestionFrame
	anchorKinds []SubjectKind
	flatTerms   [][]string
}

func (g *retrievalRecordingGraph) ResolveSubjects(ctx context.Context, principal storage.Principal, request InvestigationRequest, interpreted InterpretedQuestion, binding ResolvedGraphBinding, kind *ConfirmedExpectedKind, anchor *ConfirmedAnchorSelection, frame *QuestionFrame, anchorKind SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.frames = append(g.frames, frame)
	g.anchorKinds = append(g.anchorKinds, anchorKind)
	g.flatTerms = append(g.flatTerms, append([]string(nil), interpreted.SubjectTerms...))
	return g.graphReaderStub.ResolveSubjects(ctx, principal, request, interpreted, binding, kind, anchor, frame, anchorKind)
}

const repairedCountMembers = 3

// newRepairEngine builds an engine over the production interpreter for the
// Class A proposal with the given flat subject terms, a recording graph that
// commits the repository anchor and discovers its team members, and a store
// that keeps what the turn saved.
func newRepairEngine(t *testing.T, flat []string) (*Engine, *retrievalRecordingGraph, *staticResultStore) {
	t.Helper()
	return newRepairEngineWithReceipt(t, flat, func(*ModelExecutionReceipt, *QuestionFrame) {})
}

// newRepairEngineWithReceipt is newRepairEngine with a hook to mutate the
// receipt and the proposed frame BEFORE the interpreter runs -- so a test can
// drive a receipt shape the fixed Class A default does not cover (e.g. no
// stated ScopeAnchorKind at all) through the SAME production engine.
func newRepairEngineWithReceipt(t *testing.T, flat []string, mutate func(*ModelExecutionReceipt, *QuestionFrame)) (*Engine, *retrievalRecordingGraph, *staticResultStore) {
	t.Helper()
	logs := captureEngineLogger(t)
	anchor := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:" + repairAnchorTerm, Label: repairAnchorTerm}
	receipt := classAReceipt()
	proposal := countOverNamedSubject()
	mutate(&receipt, &proposal)
	receipt.QuestionFrame = &proposal
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	graph := &retrievalRecordingGraph{graphReaderStub: graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{anchor}},
		context: GraphContext{
			Cohort: countingCohort(SubjectTeam, repairedCountMembers), Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
		bases: provenCommitBases(anchor),
	}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{
			Runtime:        fakeModelRuntime{interpreted: repairInterpretation(flat), receipt: receipt},
			Sink:           &fakeReceiptSink{},
			FrameTelemetry: logs.telemetry,
			Requirements:   registryDeriver{},
		},
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Counted.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Counted.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:      store,
		Telemetry:    &recordingTelemetry{},
		Requirements: registryDeriver{},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(600, 0).UTC() },
		NewResultID:    func() string { return "result_i9_repair_01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine, graph, store
}

// TestTheRepairedCountIsServedPersistedAndCarried drives Engine.Investigate
// over the production interpreter: retrieval receives the repaired frame, its
// anchor-kind hint and flat subject terms equal to the anchor terms; the turn
// serves a scoped count over the anchor's team members, persists the repaired
// frame, reads it back by id through the codec, and a continuation composes
// from it.
func TestTheRepairedCountIsServedPersistedAndCarried(t *testing.T) {
	engine, graph, store := newRepairEngine(t, []string{repairAnchorTerm})
	principal := storage.Principal{OrgID: "org_repair"}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_i9_repair_01"

	result, err := engine.Investigate(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(graph.frames) == 0 {
		t.Fatal("retrieval was never called for the repaired turn")
	}
	for call, frame := range graph.frames {
		assertRepairedCarriedFrame(t, "handed to retrieval", frame)
		if graph.anchorKinds[call] != SubjectRepository {
			t.Fatalf("retrieval call %d anchor-kind hint = %q, want repository", call, graph.anchorKinds[call])
		}
		if !reflect.DeepEqual(graph.flatTerms[call], frame.SubjectExpression.Scoped.AnchorTerms) {
			t.Fatalf("retrieval call %d searches flat terms %v, want the repaired anchor terms %v", call, graph.flatTerms[call], frame.SubjectExpression.Scoped.AnchorTerms)
		}
	}
	if result.Status != InvestigationComplete {
		t.Fatalf("status = %q (basis %q, limitations %#v), want complete: the repaired count was not served", result.Status, result.RefusalBasis, result.Limitations)
	}
	rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 1 {
		t.Fatalf("assembled-result count rows = %d, want exactly 1", len(rows))
	}
	if want := string(ObligationCount) + "/" + string(SubjectRoleMember) + "/" + string(SubjectTeam); rows[0].Requirement != want {
		t.Fatalf("count row requirement = %q, want %q", rows[0].Requirement, want)
	}
	if rows[0].Served != repairedCountMembers || rows[0].Outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count row served/outcome = %d/%q, want %d/satisfied", rows[0].Served, rows[0].Outcome, repairedCountMembers)
	}

	if store.savedSemantic == nil || store.savedSemantic.State == nil || store.saved == nil {
		t.Fatal("the turn persisted no semantic state")
	}
	saved := store.savedSemantic.State
	assertRepairedCarriedFrame(t, "persisted", saved.Frame)
	if saved.Validation.GateOutcome != FrameGatePassed || saved.Validation.FailedInvariant != "" {
		t.Fatalf("persisted validation gate/invariant = %q/%q, want passed/none", saved.Validation.GateOutcome, saved.Validation.FailedInvariant)
	}

	store.results[result.ResultID] = *store.saved
	store.states = map[string]*PersistedSemanticState{result.ResultID: saved}
	stored, err := store.Get(context.Background(), principal, result.ResultID)
	if err != nil {
		t.Fatalf("Get(%q) error = %v", result.ResultID, err)
	}
	if stored.SemanticStateRead != SemanticStateReadAvailable || stored.SemanticState == nil {
		t.Fatalf("by-id read status = %q, want available", stored.SemanticStateRead)
	}
	assertRepairedCarriedFrame(t, "read by id", stored.SemanticState.Frame)

	accepted := composeAcceptedContext(compositionInput{Carried: stored.SemanticState, TransitionEstablished: true})
	if accepted.Outcome != CompositionAccepted || accepted.Gate.Outcome != FrameGatePassed {
		t.Fatalf("continuation composition outcome/gate = %q/%s (failed %q), want accepted/passed", accepted.Outcome, accepted.Gate.Observable(), accepted.FailedInvariant)
	}
	assertRepairedCarriedFrame(t, "carried by the continuation", accepted.Frame)
}

// TestARepairedProposalWithNoStatedAnchorKindCarriesTheNamedSubjectsOwnKind
// drives the production engine over a receipt that states NO top-level
// ScopeAnchorKind at all (unlike classAReceipt's default) but DOES state the
// named subject's own ExpectedKind -- exactly the shape a repaired proposal
// carries when the model's own emission of the two is inconsistent.
// CHAOS-5825: before the repair carried ScopeAnchorKind, retrieval received
// no anchor-kind hint at all on this path; the assertion below is what the
// anchor pool's "receipt" source (graphrank/chaos5393_anchor_pool.go) reads
// downstream of it.
func TestARepairedProposalWithNoStatedAnchorKindCarriesTheNamedSubjectsOwnKind(t *testing.T) {
	engine, graph, _ := newRepairEngineWithReceipt(t, []string{repairAnchorTerm}, func(r *ModelExecutionReceipt, frame *QuestionFrame) {
		r.ScopeAnchorKind = ""
		kind := SubjectRepository
		frame.SubjectExpression.Named.ExpectedKind = &kind
	})
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_i9_repair_03"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_repair"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(graph.frames) == 0 {
		t.Fatal("retrieval was never called for the repaired turn")
	}
	for call := range graph.frames {
		if graph.anchorKinds[call] != SubjectRepository {
			t.Fatalf("retrieval call %d anchor-kind hint = %q, want repository (carried from the named subject's own stated kind, not the absent receipt field)", call, graph.anchorKinds[call])
		}
	}
	if result.Status != InvestigationComplete {
		t.Fatalf("status = %q (basis %q, limitations %#v), want complete", result.Status, result.RefusalBasis, result.Limitations)
	}
}

// TestARepairedProposalWithNoAnchorKindSignalAtAllCarriesNone drives the
// same engine as above but with NEITHER the receipt's ScopeAnchorKind NOR the
// named subject's own ExpectedKind stated: there is nothing for the repair to
// carry, and it must carry nothing rather than invent a kind -- the anchor
// hint stays empty.
func TestARepairedProposalWithNoAnchorKindSignalAtAllCarriesNone(t *testing.T) {
	engine, graph, _ := newRepairEngineWithReceipt(t, []string{repairAnchorTerm}, func(r *ModelExecutionReceipt, frame *QuestionFrame) {
		r.ScopeAnchorKind = ""
	})
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_i9_repair_04"
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_repair"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	for call := range graph.frames {
		if graph.anchorKinds[call] != "" {
			t.Fatalf("retrieval call %d anchor-kind hint = %q, want empty: no field named a kind for the repair to carry", call, graph.anchorKinds[call])
		}
	}
}

// TestADivergentTermsTurnIsRefusedBeforeRetrieval drives the same engine with
// flat subject terms that differ from the named subject's term: the repair
// declines, the turn is refused on I9, and retrieval never receives a
// repaired frame.
func TestADivergentTermsTurnIsRefusedBeforeRetrieval(t *testing.T) {
	engine, graph, _ := newRepairEngine(t, []string{"flat-term"})
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_i9_repair_02"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_repair"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	for call, frame := range graph.frames {
		if frame != nil && frame.SubjectExpression.Kind == SubjectExpressionChildrenOfScope {
			t.Fatalf("retrieval call %d received a repaired frame while searching %v", call, graph.flatTerms[call])
		}
	}
	switch result.Status {
	case InvestigationComplete, InvestigationPartial, InvestigationDegraded:
		t.Fatalf("status = %q, want a refusal: the repair must not serve a count retrieval would search for by other terms", result.Status)
	}
	if result.RefusalBasis != contractsv1.ContextFabricRefusalBasisFrameInvariantViolated {
		t.Fatalf("refusal basis = %q, want %q", result.RefusalBasis, contractsv1.ContextFabricRefusalBasisFrameInvariantViolated)
	}
}

// repairTableFixture drives ONE frameRepairTable entry to its own
// FrameRepairApplied path, so the parity test below can reflect over what it
// carried.
type repairTableFixture struct {
	name    string
	receipt ModelExecutionReceipt
	frame   QuestionFrame
	shape   InvestigationShape
	terms   []string
	// zeroCarryFields names FrameRepairCarry fields this repair's shape
	// never states -- e.g. ScopeAnchorKind for a repair that never touches
	// SubjectExpression, since grouped_members has no scope anchor to state
	// (scope_anchor_kind.go). Every field of FrameRepairCarry not named
	// here is checked non-zero on the applied path instead; a field this
	// struct declares here is checked EXACTLY zero, so a repair cannot
	// silently skip a field it should populate by omitting it from both
	// checks.
	zeroCarryFields map[string]bool
}

// repairTableFixtures is ONE fixture per frameRepairTable entry, in the same
// order. TestEveryRepairPopulatesEveryCarriedField fails closed if this list
// and frameRepairTable ever have different lengths: a repair joining the
// table without a fixture here is exactly the unproven-completeness gap
// the parity test closes.
func repairTableFixtures() []repairTableFixture {
	frame := countOverNamedSubject()
	kind := SubjectRepository
	frame.SubjectExpression.Named.ExpectedKind = &kind
	return []repairTableFixture{
		{
			name:            "count_kind_collapse",
			receipt:         classAReceipt(),
			frame:           frame,
			shape:           ShapeSingleSubject,
			terms:           []string{repairAnchorTerm},
			zeroCarryFields: map[string]bool{"RequestedJudgment": true},
		},
		{
			name:            "compare_grouped_collapse",
			receipt:         compareGroupedReceipt(),
			frame:           compareOverGroupedCohort(),
			shape:           ShapeSingleSubject,
			terms:           []string{repairAnchorTerm},
			zeroCarryFields: map[string]bool{"ScopeAnchorKind": true},
		},
		{
			name:            "compare_ranking_collapse",
			receipt:         rankingReceipt(),
			frame:           compareOverDiscoveredCohort(),
			shape:           ShapeSingleSubject,
			terms:           []string{repairAnchorTerm},
			zeroCarryFields: map[string]bool{"ScopeAnchorKind": true, "RequestedJudgment": true},
		},
		{
			name:            "member_kind_fact_alias_collapse",
			receipt:         groupedMetricByRepoReceipt(),
			frame:           groupedMetricByRepoFrame(),
			shape:           ShapeSingleSubject,
			terms:           []string{repairAnchorTerm},
			zeroCarryFields: map[string]bool{"ScopeAnchorKind": true, "RequestedJudgment": true},
		},
	}
}

// TestEveryRepairPopulatesEveryCarriedField walks frameRepairTable by
// reflection against FrameRepairCarry: every repair, driven to
// FrameRepairApplied by its own fixture, must account for every field on
// FrameRepairCarry -- populating it, or its fixture explicitly declaring
// (zeroCarryFields) that this repair's shape never states it. A repair
// added to the table without updating this test's fixture list is caught by
// the length check below; a carried field added to FrameRepairCarry with no
// fixture accounting for it defaults every existing repair to "must
// populate", the same fail-closed direction the original single-repair
// version of this test held -- shape-equivalence a repaired proposal must
// hold with the direct proposal it repairs into.
func TestEveryRepairPopulatesEveryCarriedField(t *testing.T) {
	fixtures := repairTableFixtures()
	if len(fixtures) != len(frameRepairTable) {
		t.Fatalf("frameRepairTable has %d repair(s) but this test has %d fixture(s): "+
			"a repair joined the table without a parity fixture", len(frameRepairTable), len(fixtures))
	}
	for i, repair := range frameRepairTable {
		fixture := fixtures[i]
		t.Run(fixture.name, func(t *testing.T) {
			base := validateAgainstInterpretation(fixture.receipt, fixture.frame, fixture.shape)
			result := repair(fixture.receipt, fixture.frame, fixture.shape, fixture.terms, base)
			if result.Repair.Decision != FrameRepairApplied {
				t.Fatalf("fixture %q must drive the repair to %q to exercise its carry, got %q",
					fixture.name, FrameRepairApplied, result.Repair.Decision)
			}
			carry := reflect.ValueOf(result.Repair.Carry)
			carryType := carry.Type()
			for f := 0; f < carry.NumField(); f++ {
				name := carryType.Field(f).Name
				isZero := carry.Field(f).IsZero()
				wantZero := fixture.zeroCarryFields[name]
				if isZero && !wantZero {
					t.Errorf("repair %q left FrameRepairCarry field %q at its zero value on the applied path",
						fixture.name, name)
				}
				if !isZero && wantZero {
					t.Errorf("repair %q populated FrameRepairCarry field %q, but its fixture declares that field never applies to this shape",
						fixture.name, name)
				}
			}
		})
	}
}

func assertRepairedCarriedFrame(t *testing.T, where string, frame *QuestionFrame) {
	t.Helper()
	if frame == nil {
		t.Fatalf("%s: no frame", where)
	}
	expression := frame.SubjectExpression
	if expression.Kind != SubjectExpressionChildrenOfScope || expression.Scoped == nil ||
		expression.Scoped.MemberKind != SubjectTeam || !reflect.DeepEqual(expression.Scoped.AnchorTerms, []string{repairAnchorTerm}) {
		t.Fatalf("%s: frame expression = %+v, want children_of_scope anchored on %q with member kind team", where, expression, repairAnchorTerm)
	}
}
