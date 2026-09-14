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
// beside the receipt.
func repairInterpretation() InterpretedQuestion {
	return InterpretedQuestion{
		Shape: ShapeSingleSubject, RequestedJudgment: "count",
		SubjectTerms: []string{repairAnchorTerm}, TimeContext: TimeContext{Axis: TemporalCurrent},
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
func interpretForRepair(t *testing.T, receipt ModelExecutionReceipt, frame QuestionFrame) repairRun {
	t.Helper()
	logs := captureEngineLogger(t)
	receipt.QuestionFrame = &frame
	sink := &fakeReceiptSink{}
	interpreter := RuntimeQuestionInterpreter{
		Runtime:         fakeModelRuntime{interpreted: repairInterpretation(), receipt: receipt},
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

	run := interpretForRepair(t, classAReceipt(), proposal)

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
	// wantKind is the carried frame's kind, "" when the turn carries none.
	wantKind SubjectExpressionKind
	wantLine map[string]any
}

func refusedI9Line(decision string) map[string]any {
	return map[string]any{
		"outcome": "refused_invalid", "failed_invariant": "i9", "failure_detail": "count_requires_set_valued_kind",
		"frame_gate": "rejected:i9", "repair_decision": decision, "repair": "count_kind_collapse", "repair_invariant": "i9",
		"repair_kind_after": "none", "repair_member_kind": "none", "repair_attempts": float64(0),
	}
}

func notApplicableLine(outcome, invariant, gate string) map[string]any {
	return map[string]any{
		"outcome": outcome, "failed_invariant": invariant, "frame_gate": gate,
		"repair_decision": "not_applicable", "repair": "none", "repair_invariant": "none",
		"repair_kind_before": "none", "repair_kind_after": "none", "repair_member_kind": "none", "repair_attempts": float64(0),
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
			wantLine: map[string]any{
				"outcome": "repaired", "frame_gate": "passed", "repair_decision": "applied",
				"repair_kind_after": "children_of_scope", "repair_member_kind": "team", "repair_attempts": float64(1),
			},
		},
		{
			cell: "hint a vocabulary kind no discovery arm serves", receipt: func(r *ModelExecutionReceipt) { r.RequestedSubjectKind = contractsv1.ContextFabricSubjectDeployment },
			frame: countOverNamedSubject, wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: refusedI9("declined_hint_unservable", "named_subject"),
		},
		{
			cell: "hint organization", receipt: func(r *ModelExecutionReceipt) { r.RequestedSubjectKind = SubjectOrganization },
			frame: countOverNamedSubject, wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: refusedI9("declined_hint_unservable", "named_subject"),
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
				"repair_attempts": float64(1),
			},
		},
		{
			cell: "repaired frame fails a phase-A2 invariant", receipt: func(*ModelExecutionReceipt) {},
			frame:       withFrame(func(frame *QuestionFrame) { frame.Emphasis = []AnswerEmphasis{EmphasisNegativeOutliers} }),
			wantOutcome: FrameValidationOutcomeRefusedInvalid,
			wantLine: map[string]any{
				"outcome": "refused_invalid", "failed_invariant": "i14", "failed_phase": "a2",
				"frame_gate": "rejected:i14", "repair_decision": "refused_after_repair",
				"repair_kind_after": "children_of_scope", "repair_member_kind": "team", "repair_attempts": float64(1),
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
// names has an executed driver.
func TestTheCountKindRepairIsBounded(t *testing.T) {
	produced := map[string]bool{}
	for _, testCase := range repairCells() {
		t.Run(testCase.cell, func(t *testing.T) {
			receipt := classAReceipt()
			testCase.receipt(&receipt)
			run := interpretForRepair(t, receipt, testCase.frame())
			produced[run.line["repair_decision"].(string)] = true

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
	for _, attempts := range []int{0, frameRepairBound} {
		result := repairAtTheBound(t, attempts)
		produced[string(result.Repair.Decision)] = true
	}
	for _, member := range FrameRepairDecisionVocabulary() {
		if !produced[string(member)] {
			t.Errorf("no executed driver produces repair decision %q", member)
		}
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
	return repairCountKindCollapse(receipt, proposal, ShapeSingleSubject, refused)
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
		run := interpretForRepair(t, receipt, frame)
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

// TestTheRepairedCountIsServedPersistedAndCarried drives Engine.Investigate
// over the production interpreter: the repaired turn serves a scoped count
// over the anchor's team members, persists the repaired frame, reads it back
// by id through the codec, and a continuation composes from it.
func TestTheRepairedCountIsServedPersistedAndCarried(t *testing.T) {
	const members = 3
	logs := captureEngineLogger(t)
	anchor := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:" + repairAnchorTerm, Label: repairAnchorTerm}
	receipt := classAReceipt()
	proposal := countOverNamedSubject()
	receipt.QuestionFrame = &proposal
	telemetry := &recordingTelemetry{}
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{
			Runtime:        fakeModelRuntime{interpreted: repairInterpretation(), receipt: receipt},
			Sink:           &fakeReceiptSink{},
			FrameTelemetry: logs.telemetry,
			Requirements:   registryDeriver{},
		},
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{anchor}},
			context: GraphContext{
				Cohort: countingCohort(SubjectTeam, members), Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
			bases: provenCommitBases(anchor),
		},
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
		Telemetry:    telemetry,
		Requirements: registryDeriver{},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(600, 0).UTC() },
		NewResultID:    func() string { return "result_i9_repair_01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	principal := storage.Principal{OrgID: "org_repair"}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_i9_repair_01"

	result, err := engine.Investigate(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
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
	if rows[0].Served != members || rows[0].Outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count row served/outcome = %d/%q, want %d/satisfied", rows[0].Served, rows[0].Outcome, members)
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
