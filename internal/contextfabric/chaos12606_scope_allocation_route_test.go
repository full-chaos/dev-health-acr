package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12606: "how is team X investing across its repositories" arrives as a
// children_of_scope frame under a team anchor. Topology decides the projection,
// so it projects to scoped_cohort_status, and the precedence table agrees
// (ops-askdev-auth's no-model repro at 89c9133c). The lead's ruling on
// CHAOS-4610 serves investment_allocation for it, from the team's own fact.

func teamChildrenFrame(goals ...InvestigationGoal) QuestionFrame {
	return goalFrame(SubjectExpression{Kind: SubjectExpressionChildrenOfScope, Scoped: &ScopedSetExpression{AnchorTerms: []string{"Platform"}, MemberKind: contractsv1.ContextFabricSubjectRepository}}, goals...)
}

func teamChildrenSample(shape InvestigationShape) FamilySample {
	return FamilySample{Shape: shape, SubjectTerms: []string{"Platform"}, ScopeAnchorKind: contractsv1.ContextFabricSubjectTeam, ScopeAnchorTerm: "Platform", RequestedKind: contractsv1.ContextFabricSubjectRepository}
}

func TestATeamsChildrenAllocationQuestionServesTheFamily(t *testing.T) {
	t.Parallel()
	frame := teamChildrenFrame(GoalAllocateInvestment)
	if projected := DeriveQuestionFamily(frame).Family; projected != QuestionFamilyScopedCohortStatus {
		t.Fatalf("the frame projects to %q: topology no longer decides the projection", projected)
	}
	for _, shape := range []InvestigationShape{ShapeDiscoveredCohort, ShapeOpen, ShapeSingleSubject} {
		sample := teamChildrenSample(shape)
		if before := ResolveFamilyForSample(sample).Family; before != QuestionFamilyScopedCohortStatus {
			t.Fatalf("%s: the precedence table gives %q; the repro gave scoped_cohort_status", shape, before)
		}
		got := routedFamily(frame, sample)
		if got.Family != QuestionFamilyInvestmentAllocation || got.Source != FamilyRouteScopeAllocation || got.Disposition != FamilyRouteIntendedChange || !got.Switched {
			t.Fatalf("%s: decision %+v, want investment_allocation from the scope allocation rule, a switch", shape, got)
		}
	}
}

// Check 10: under a team anchor, any other goal set keeps the table.
func TestATeamsChildrenFrameWithAnotherGoalSetKeepsTheTable(t *testing.T) {
	t.Parallel()
	checked := 0
	for _, goal := range InvestigationGoalVocabulary() {
		if goal == GoalAllocateInvestment {
			continue
		}
		for _, goals := range [][]InvestigationGoal{{GoalAllocateInvestment, goal}, {goal}} {
			for _, shape := range allInvestigationShapes() {
				frame := teamChildrenFrame(goals...)
				sample := teamChildrenSample(shape)
				agreement := ClassifyFamilyAgreement(DeriveQuestionFamily(frame), ResolveFamilyForSample(sample))
				if got, want := RouteFrameFamily(agreement, &frame, contractsv1.ContextFabricSubjectTeam), RouteQuestionFamily(agreement); got != want {
					t.Fatalf("goals %v, %s: decision %+v, want the table's %+v", goals, shape, got, want)
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("the sweep checked nothing")
	}
}

// The organization's route keeps its projected source: its frame projects to
// investment_allocation, so naming the rule would hide that the projection
// agreed.
func TestTheOrganizationRouteKeepsItsProjectedSource(t *testing.T) {
	t.Parallel()
	frame := goalFrame(SubjectExpression{Kind: SubjectExpressionOrganizationScope, Org: &OrganizationScopeExpression{}}, GoalAllocateInvestment)
	if got := routedFamily(frame, FamilySample{Shape: ShapeOpen}); got.Source != FamilyRouteProjected {
		t.Fatalf("decision %+v, want the projected source", got)
	}
}

// The treemap of a team's children allocation answer: its frame is a cohort
// variant, but the route answers from the team's own fact, so the rule draws it.
func TestATeamsChildrenAllocationAnswerGetsItsTreemap(t *testing.T) {
	t.Parallel()
	frame := teamChildrenFrame(GoalAllocateInvestment)
	for _, shape := range []InvestigationShape{ShapeDiscoveredCohort, ShapeOpen} {
		result := themeShareResultFor(contractsv1.ContextFabricSubjectTeam, 0.5, 0.5)
		result.Interpretation.Shape = shape
		shapes, event := selectRenderShapes(result, &frame, RenderShapeOptions{InvestmentThemeShare: true})
		if len(treemapsOf(shapes)) != 1 {
			t.Fatalf("%s: shapes %+v, skip %q: want one treemap", shape, shapes, skipOf(t, event))
		}
	}
}

// Check 10: the exemption is this route's answer alone. A cohort served beside
// it, a second goal, or another family is still a cohort answer.
func TestTheTreemapExemptionIsTheRoutesAnswerAlone(t *testing.T) {
	t.Parallel()
	for name, plant := range map[string]struct {
		frame QuestionFrame
		edit  func(*InvestigationResult)
	}{
		"a cohort served":  {teamChildrenFrame(GoalAllocateInvestment), func(r *InvestigationResult) { r.Cohort = &Cohort{} }},
		"a second goal":    {teamChildrenFrame(GoalAllocateInvestment, GoalAssessState), func(*InvestigationResult) {}},
		"another goal":     {teamChildrenFrame(GoalAssessState), func(*InvestigationResult) {}},
		"another family":   {teamChildrenFrame(GoalAllocateInvestment), func(r *InvestigationResult) { r.AnswerPlan.Family = QuestionFamilyScopedCohortStatus }},
		"another topology": {goalFrame(SubjectExpression{Kind: SubjectExpressionDiscoveredKind, Discovered: &DiscoveredSetExpression{MemberKind: contractsv1.ContextFabricSubjectTeam}}, GoalAllocateInvestment), func(*InvestigationResult) {}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := themeShareResultFor(contractsv1.ContextFabricSubjectTeam, 0.5, 0.5)
			result.Interpretation.Shape = ShapeDiscoveredCohort
			plant.edit(&result)
			frame := plant.frame
			shapes, _ := selectRenderShapes(result, &frame, RenderShapeOptions{InvestmentThemeShare: true})
			if len(treemapsOf(shapes)) != 0 {
				t.Fatalf("shapes %+v", shapes)
			}
		})
	}
}
