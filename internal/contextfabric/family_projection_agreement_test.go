package contextfabric

import (
	"testing"
)

// The shadow comparison's own tests: that every class is REACHABLE from
// real inputs, that the three frame states stay distinguishable, and that
// the comparison cannot move what is routed.

func agreementFrame(t *testing.T, goals []InvestigationGoal, expression SubjectExpression, temporal TemporalIntent) QuestionFrame {
	t.Helper()
	result := ValidateFrame(QuestionFrame{Goals: goals, SubjectExpression: expression, Temporal: temporal}, nil, "")
	if result.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("fixture frame is illegal (%s / %s)", result.Failure.Invariant, result.Failure.Detail)
	}
	return result.Frame
}

// TestEveryAgreementClassHasAFixtureThatLandsInIt is the dead-tier gate.
//
// A classification arm nothing reaches is indistinguishable from an arm
// that never fires: it reads as green for its whole life, and the class
// distribution the flip decision reads would show a structural zero as if
// it were an empirical one. Every class below is reached from a REAL frame
// and a REAL precedence sample -- not from a hand-built outcome struct --
// so each fixture also demonstrates the situation the class names.
func TestEveryAgreementClassHasAFixtureThatLandsInIt(t *testing.T) {
	t.Parallel()

	namedTeam := labelNamed("a", SubjectTeam)
	grouped := labelGrouped(SubjectTeam, SubjectProject)
	discovered := SubjectExpression{Kind: SubjectExpressionDiscoveredKind,
		Discovered: &DiscoveredSetExpression{MemberKind: SubjectTeam}}
	explicit := SubjectExpression{Kind: SubjectExpressionExplicitSet,
		Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
			projectionNamedOperand("a", SubjectTeam), projectionNamedOperand("b", SubjectTeam)}}}
	org := SubjectExpression{Kind: SubjectExpressionOrganizationScope, Org: &OrganizationScopeExpression{}}

	cases := []struct {
		class FamilyAgreementClass
		why   string
		frame QuestionFrame
		// sample is the precedence table's input. It is a REAL sample
		// shape -- what one interpretation could actually emit.
		sample FamilySample
	}{
		{
			class:  FamilyAgreementAgreed,
			why:    "a grouped frame and a model that emitted the grouping kind: both tables read the same structure and land together",
			frame:  agreementFrame(t, []InvestigationGoal{GoalAssessState}, grouped, TemporalIntentCurrent),
			sample: FamilySample{GroupKind: SubjectTeam, Shape: ShapeExplicitCohort, SubjectTerms: []string{"a"}},
		},
		{
			class:  FamilyAgreementProjectionUnclassified,
			why:    "no frame reached validation, so the projection has no topology to read while the precedence table still routes on Shape",
			frame:  QuestionFrame{},
			sample: FamilySample{Shape: ShapeSingleSubject, SubjectTerms: []string{"a"}},
		},
		{
			class:  FamilyAgreementPrecedenceUnclassified,
			why:    "an explicit cohort whose members were never emitted as two DISTINCT terms falls to precedence row 7, which the precedence table's own comment names; the frame emitted the operands so the projection sees the set",
			frame:  agreementFrame(t, []InvestigationGoal{GoalCompare}, explicit, TemporalIntentCurrent),
			sample: FamilySample{Shape: ShapeExplicitCohort, SubjectTerms: []string{"a"}},
		},
		{
			class:  FamilyAgreementGoalRowUnreachable,
			why:    "a single-subject trend question: the projection reaches `trend`, which the precedence table declares deliberately unreachable in its slice",
			frame:  agreementFrame(t, []InvestigationGoal{GoalDescribeTrend}, namedTeam, TemporalIntentTimeSeries),
			sample: FamilySample{Shape: ShapeSingleSubject, SubjectTerms: []string{"a"}},
		},
		{
			class:  FamilyAgreementComparisonTermCount,
			why:    "BEHAVIOUR CHANGE B6, and the reason this counter exists: a grouped question whose interpretation happened to emit two distinct subject terms is TAKEN by the precedence comparison row before Shape is ever read. Both Q-A typo replicates are this case",
			frame:  agreementFrame(t, []InvestigationGoal{GoalAssessState}, grouped, TemporalIntentCurrent),
			sample: FamilySample{Shape: ShapeDiscoveredCohort, SubjectTerms: []string{"each team", "project statuses"}},
		},
		{
			class:  FamilyAgreementOrganizationRoute,
			why:    "BEHAVIOUR CHANGE B5: an org-wide question is `open` to the precedence table, which ranks a discovered cohort; the frame says the organization IS the subject",
			frame:  agreementFrame(t, []InvestigationGoal{GoalAssessState}, org, TemporalIntentCurrent),
			sample: FamilySample{Shape: ShapeOpen, SubjectTerms: nil},
		},
		{
			class:  FamilyAgreementShapeDivergence,
			why:    "the least stable field in the interpretation disagreeing with the union: a discovered-cohort frame whose sample emitted Shape=single_subject. Six replicates of two questions produced three distinct Shape values, so this is not hypothetical",
			frame:  agreementFrame(t, []InvestigationGoal{GoalRankOrSurvey}, discovered, TemporalIntentCurrent),
			sample: FamilySample{Shape: ShapeSingleSubject, SubjectTerms: []string{"a"}},
		},
		{
			class:  FamilyAgreementUnexplained,
			why:    "a model that emitted a grouping kind on a plain single-subject frame. Precedence row 1 fires on GroupKind ALONE, and no named class describes that pair -- which is exactly the residual this class exists to make visible rather than absorb",
			frame:  agreementFrame(t, []InvestigationGoal{GoalAssessState}, namedTeam, TemporalIntentCurrent),
			sample: FamilySample{GroupKind: SubjectTeam, Shape: ShapeSingleSubject, SubjectTerms: []string{"a"}},
		},
	}

	if len(cases) != FamilyAgreementClassCount {
		t.Fatalf("%d fixtures for %d classes -- every class needs one that LANDS in it", len(cases), FamilyAgreementClassCount)
	}
	covered := map[FamilyAgreementClass]bool{}
	for _, testCase := range cases {
		agreement := ClassifyFamilyAgreement(DeriveQuestionFamily(testCase.frame), ResolveFamilyForSample(testCase.sample))
		if agreement.Class != testCase.class {
			t.Errorf("fixture for %q landed in %q instead (projected %q via %s, precedence %q via %s).\n  The fixture says: %s",
				testCase.class, agreement.Class, agreement.ProjectedFamily, agreement.ProjectedRow,
				agreement.PrecedenceFamily, agreement.PrecedenceRow, testCase.why)
		}
		if covered[testCase.class] {
			t.Errorf("two fixtures declare class %q; one class is then untested", testCase.class)
		}
		covered[testCase.class] = true
	}
	for _, class := range FamilyAgreementClassVocabulary() {
		if !covered[class] {
			t.Errorf("class %q has no fixture that lands in it -- a tier nothing reaches reads exactly like a check that always answers no", class)
		}
	}
}

// TestAgreementIsTrueOnlyForTheAgreedClass keeps the derived boolean from
// drifting away from the class it is derived from.
func TestAgreementIsTrueOnlyForTheAgreedClass(t *testing.T) {
	t.Parallel()
	frames := generateFrames(t)
	checked := 0
	for _, generated := range frames {
		for _, shape := range []InvestigationShape{ShapeSingleSubject, ShapeDiscoveredCohort, ShapeOpen, ShapeExplicitCohort} {
			agreement := ClassifyFamilyAgreement(
				DeriveQuestionFamily(generated.frame),
				ResolveFamilyForSample(FamilySample{Shape: shape, SubjectTerms: []string{"a"}}),
			)
			checked++
			if agreement.Agreed != (agreement.Class == FamilyAgreementAgreed) {
				t.Fatalf("Agreed=%v with class %q", agreement.Agreed, agreement.Class)
			}
			if agreement.Agreed != (agreement.ProjectedFamily == agreement.PrecedenceFamily) {
				t.Fatalf("Agreed=%v but projected=%q precedence=%q", agreement.Agreed, agreement.ProjectedFamily, agreement.PrecedenceFamily)
			}
			if !ValidFamilyAgreementClass(agreement.Class) {
				t.Fatalf("class %q is not a vocabulary member -- the classifier is meant to be total", agreement.Class)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no comparison was made")
	}
	t.Logf("%d comparisons, class and boolean consistent on every one", checked)
}

// TestShadowDistinguishesAbsentFromRefusedFromCompared is the countability
// gate on the production shadow.
//
// Three states that a single "did they agree" boolean would collapse: the
// model emitted no frame, the frame was refused, and a comparison actually
// ran. Collapsing them would put emission failures into the disagreement
// rate, and the flip decision reads that rate.
func TestShadowDistinguishesAbsentFromRefusedFromCompared(t *testing.T) {
	t.Parallel()
	valid := agreementFrame(t, []InvestigationGoal{GoalAssessState}, labelNamed("a", SubjectTeam), TemporalIntentCurrent)
	outcome := ResolveQuestionFamily([]FamilySample{{Shape: ShapeSingleSubject, SubjectTerms: []string{"a"}}})

	t.Run("no frame emitted", func(t *testing.T) {
		t.Parallel()
		shadow := ShadowFamilyAgreement(ModelExecutionReceipt{}, outcome)
		if shadow.FrameObserved {
			t.Fatal("a receipt with no frame reported FrameObserved")
		}
		if shadow.Agreement.Class != "" {
			t.Fatalf("a receipt with no frame produced class %q -- an absent frame is not a disagreement", shadow.Agreement.Class)
		}
	})

	t.Run("frame refused", func(t *testing.T) {
		t.Parallel()
		refused := QuestionFrame{}
		shadow := ShadowFamilyAgreement(ModelExecutionReceipt{
			QuestionFrame: &refused,
			FrameOutcome:  FrameValidationOutcomeRefusedInvalid,
		}, outcome)
		if shadow.FrameObserved {
			t.Fatal("a REFUSED frame reported FrameObserved -- a frame the server declined to accept must not be projected")
		}
		if shadow.FrameOutcome != FrameValidationOutcomeRefusedInvalid {
			t.Fatalf("the refusal outcome did not reach the shadow: %q", shadow.FrameOutcome)
		}
	})

	t.Run("comparison runs", func(t *testing.T) {
		t.Parallel()
		frame := valid
		shadow := ShadowFamilyAgreement(ModelExecutionReceipt{
			QuestionFrame: &frame,
			FrameOutcome:  FrameValidationOutcomeValid,
		}, outcome)
		if !shadow.FrameObserved {
			t.Fatal("a valid frame did not report FrameObserved")
		}
		if shadow.ProjectionVersion != QuestionFrameVersion {
			t.Fatalf("projection version %q, want the server constant %q -- a persisted disagreement must be replayable against the table that produced it", shadow.ProjectionVersion, QuestionFrameVersion)
		}
		if !shadow.Agreement.Agreed {
			t.Fatalf("a named-subject frame and a single_subject sample must agree; got %q vs %q", shadow.Agreement.ProjectedFamily, shadow.Agreement.PrecedenceFamily)
		}
	})
}

// TestShadowComparesAgainstTheROUTEDFamilyNotTheWinningSample is the
// N-greater-than-one correctness the shadow needs before the ensemble is
// turned on.
//
// With no strict majority the engine routes `unclassified` while every
// individual sample resolved to something. A shadow keyed on the winning
// sample would report agreement with a family the engine did not use --
// silently, and only once N changed.
func TestShadowComparesAgainstTheROUTEDFamilyNotTheWinningSample(t *testing.T) {
	t.Parallel()
	// Two samples, two different families: 1-1 is not a strict majority,
	// so the resolver refuses and routes unclassified.
	outcome := ResolveQuestionFamily([]FamilySample{
		{Shape: ShapeSingleSubject, SubjectTerms: []string{"a"}},
		{Shape: ShapeDiscoveredCohort, SubjectTerms: []string{"a"}},
	})
	if outcome.Family != QuestionFamilyUnclassified {
		t.Fatalf("fixture no longer produces a rejected plurality: routed %q", outcome.Family)
	}
	if outcome.WinningSampleIndex >= 0 {
		t.Fatalf("a rejected plurality has no winner; index %d", outcome.WinningSampleIndex)
	}

	frame := agreementFrame(t, []InvestigationGoal{GoalAssessState}, labelNamed("a", SubjectTeam), TemporalIntentCurrent)
	shadow := ShadowFamilyAgreement(ModelExecutionReceipt{QuestionFrame: &frame, FrameOutcome: FrameValidationOutcomeValid}, outcome)

	if shadow.Agreement.PrecedenceFamily != QuestionFamilyUnclassified {
		t.Fatalf("the shadow compared against %q, but the engine routed %q", shadow.Agreement.PrecedenceFamily, outcome.Family)
	}
	if shadow.Agreement.PrecedenceRow != FamilyPrecedenceRowNone {
		t.Fatalf("a rejected plurality fired no row; the shadow attributed it to %q", shadow.Agreement.PrecedenceRow)
	}
	if shadow.Agreement.Class != FamilyAgreementPrecedenceUnclassified {
		t.Fatalf("class %q, want precedence_unclassified", shadow.Agreement.Class)
	}
}
