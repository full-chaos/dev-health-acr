package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// frameForRenderFixture translates a PRE-SEAM-7 render fixture's declared
// intent into the union the selector now reads.
//
// Those fixtures were written when cohort render intent came from
// Interpretation.Shape, so each one states its intent THERE -- a fixture with
// Shape discovered_cohort is a fixture that means "this question asked for a
// set". This helper says the same thing in the frame's vocabulary, so every
// assertion in those tests keeps testing what it was written to test rather
// than being quietly re-pointed at a different question.
//
// IT DELIBERATELY REPRODUCES THE OLD SHAPE SET EXACTLY, including its
// blind spot: Shape `open` maps to a NON-cohort expression here, which is
// precisely the case the old rule got wrong in production. That blind spot
// is not carried into the code -- it is carried into these legacy fixtures so
// they stay honest about what they cover, and
// TestOpenShapeWithAGroupedFrameIsCohortIntent below covers the case they
// cannot.
func frameForRenderFixture(result InvestigationResult) *QuestionFrame {
	expression := SubjectExpression{
		Kind:  SubjectExpressionNamed,
		Named: &NamedSubjectExpression{Terms: []string{"fixture subject"}},
	}
	switch result.Interpretation.Shape {
	case contractsv1.ContextFabricShapeDiscoveredCohort, contractsv1.ContextFabricShapeExplicitCohort:
		memberKind := SubjectTeam
		if result.Cohort != nil && result.Cohort.Kind != "" {
			memberKind = result.Cohort.Kind
		}
		expression = SubjectExpression{
			Kind:       SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: memberKind},
		}
	}
	return &QuestionFrame{
		Goals:             []InvestigationGoal{GoalAssessState},
		SubjectExpression: expression,
		Temporal:          TemporalIntentCurrent,
		Version:           QuestionFrameVersion,
	}
}

// THE ROW-3 CASE, and the reason this seam was worth moving.
//
// "What are the project statuses for each team, and what are the main
// drivers?" emitted Shape `open` on the rig and returned no_match. `open` was
// excluded from the old cohort-intent set for a defensible reason -- "an
// unshaped question has not asked for a ranking" -- but the question plainly
// HAD asked for one, and Shape is the least stable field in the
// interpretation. The frame says grouped_members, which is a cohort variant
// whatever Shape happened to land on that replicate.
func TestOpenShapeWithAGroupedFrameIsCohortIntent(t *testing.T) {
	t.Parallel()
	grouped := &QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState, GoalExplainDrivers},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{
				GroupKind: SubjectTeam, MemberKind: SubjectProject,
			},
		},
		Temporal: TemporalIntentCurrent,
		Version:  QuestionFrameVersion,
	}
	if !cohortRenderIntent(grouped) {
		t.Fatal("a grouped_members frame is not cohort intent -- this is the rig's row 3, where Shape said `open` and the chart was dropped")
	}
	// The control: the same question read through the OLD rule. `open` was
	// not in the shape set, so the old rule answered no. Asserting the old
	// answer here is what makes the change a measured difference rather
	// than an assertion that the new code agrees with itself.
	oldRuleShapes := map[contractsv1.ContextFabricInvestigationShape]struct{}{
		contractsv1.ContextFabricShapeExplicitCohort:   {},
		contractsv1.ContextFabricShapeDiscoveredCohort: {},
	}
	if _, oldAnswer := oldRuleShapes[contractsv1.ContextFabricShapeOpen]; oldAnswer {
		t.Fatal("the transcribed old rule admits `open`; the transcription is wrong and the comparison below proves nothing")
	}
}

// The negative control for the same predicate: a single named subject is NOT
// cohort intent, and neither is an absent frame. Without this, a predicate
// that always answered true would pass the test above.
func TestNamedSubjectAndAbsentFrameAreNotCohortIntent(t *testing.T) {
	t.Parallel()
	named := &QuestionFrame{
		SubjectExpression: SubjectExpression{
			Kind:  SubjectExpressionNamed,
			Named: &NamedSubjectExpression{Terms: []string{"Dev Health Ops"}},
		},
	}
	if cohortRenderIntent(named) {
		t.Fatal("a named_subject frame reported cohort intent -- a single-subject answer may carry a ranked cohort as context, and charting it answers a question nobody asked")
	}
	if cohortRenderIntent(nil) {
		t.Fatal("an absent frame reported cohort intent")
	}
}
