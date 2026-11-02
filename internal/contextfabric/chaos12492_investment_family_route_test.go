package contextfabric

import (
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12492: investment_allocation was planned in the registry and never
// served. A valid frame projected to it, and the route sent the answer back to
// subject_investigation, so no treemap could be authorized. These run the real
// projection, the real precedence table, the real classifier and the real route.

func routedFamily(frame QuestionFrame, sample FamilySample) FamilyRouteDecision {
	return RouteFrameFamily(ClassifyFamilyAgreement(DeriveQuestionFamily(frame), ResolveFamilyForSample(sample)), &frame, sample.ScopeAnchorKind)
}

func allocationSubject() SubjectExpression {
	return SubjectExpression{Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{Terms: []string{"acme/widgets"}}}
}

func goalFrame(expression SubjectExpression, goals ...InvestigationGoal) QuestionFrame {
	return QuestionFrame{Goals: goals, SubjectExpression: expression, Temporal: TemporalIntentCurrent}
}

func TestANamedSubjectWithTheGoalServesInvestmentAllocation(t *testing.T) {
	t.Parallel()
	for name, sample := range map[string]FamilySample{
		"single subject":     {Shape: ShapeSingleSubject, SubjectTerms: []string{"acme/widgets"}},
		"single, hinted":     {Shape: ShapeSingleSubject, SubjectTerms: []string{"acme/widgets"}, RequestedKind: contractsv1.ContextFabricSubjectRepository},
		"open, named a term": {Shape: ShapeOpen, SubjectTerms: []string{"acme/widgets"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := routedFamily(goalFrame(allocationSubject(), GoalAllocateInvestment), sample)
			if got.Family != QuestionFamilyInvestmentAllocation || got.Source != FamilyRouteProjected || got.Disposition != FamilyRouteIntendedChange || !got.Switched {
				t.Fatalf("decision %+v, want investment_allocation from the projection, a switch", got)
			}
			// The open sample reaches the family through the shape_divergence
			// row, which predates this route. Only the single subject samples
			// prove the named subject exception.
			if want := FamilyAgreementGoalRowUnreachable; sample.Shape == ShapeSingleSubject && got.Class != want {
				t.Fatalf("class %q, want %q", got.Class, want)
			}
		})
	}
	// The trend of an effort split keeps both goals, and it is an allocation.
	got := routedFamily(goalFrame(allocationSubject(), GoalAllocateInvestment, GoalDescribeTrend), FamilySample{Shape: ShapeSingleSubject, SubjectTerms: []string{"acme/widgets"}})
	if got.Family != QuestionFamilyInvestmentAllocation {
		t.Fatalf("with both goals: %+v", got)
	}
}

// A structure signal of the precedence table's own is not a range difference,
// and it keeps the class it has.
func TestAPrecedenceStructureSignalIsNotSwitched(t *testing.T) {
	t.Parallel()
	got := routedFamily(goalFrame(allocationSubject(), GoalAllocateInvestment), FamilySample{Shape: ShapeSingleSubject, SubjectTerms: []string{"a"}, GroupKind: contractsv1.ContextFabricSubjectTeam})
	if got.Family == QuestionFamilyInvestmentAllocation {
		t.Fatalf("decision %+v: a group kind must not be read as a named subject's allocation", got)
	}
}

// CHAOS-12585: this test pinned the decline ("an organization frame stays where
// it was"). Reach t9 asked where the organization invests and got a cohort
// ranking with no fact, and the lead's ruling on CHAOS-4610 serves the
// allocation family for an allocation question over the organization. The
// organization's own investment fact answers it, for every caller.
func TestAnOrganizationAllocationQuestionServesTheFamily(t *testing.T) {
	t.Parallel()
	frame := goalFrame(SubjectExpression{Kind: SubjectExpressionOrganizationScope, Org: &OrganizationScopeExpression{}}, GoalAllocateInvestment)
	if projected := DeriveQuestionFamily(frame).Family; projected != QuestionFamilyInvestmentAllocation {
		t.Fatalf("the frame projects to %q: the route's source would not be the projection", projected)
	}
	for _, shape := range []InvestigationShape{ShapeOpen, ShapeSingleSubject} {
		sample := FamilySample{Shape: shape}
		got := routedFamily(frame, sample)
		if got.Family != QuestionFamilyInvestmentAllocation || got.Source != FamilyRouteProjected || got.Disposition != FamilyRouteIntendedChange {
			t.Fatalf("%s: decision %+v, want investment_allocation from the projection", shape, got)
		}
		if got.Switched != (ResolveFamilyForSample(sample).Family != QuestionFamilyInvestmentAllocation) {
			t.Fatalf("%s: decision %+v: a switch is measured against the precedence family", shape, got)
		}
	}
}

// Check 10: only the exact goal set {allocate_investment} is an allocation
// question over the organization. A second goal asks more than where effort
// went, and another goal alone asks something else, so the table decides, as it
// did before.
func TestAnOrganizationFrameWithASecondGoalKeepsTheTable(t *testing.T) {
	t.Parallel()
	checked := 0
	for _, goal := range InvestigationGoalVocabulary() {
		if goal == GoalAllocateInvestment {
			continue
		}
		for _, shape := range allInvestigationShapes() {
			// The goal beside allocate_investment, and the goal alone.
			for _, goals := range [][]InvestigationGoal{{GoalAllocateInvestment, goal}, {goal}} {
				frame := goalFrame(SubjectExpression{Kind: SubjectExpressionOrganizationScope, Org: &OrganizationScopeExpression{}}, goals...)
				sample := FamilySample{Shape: shape}
				agreement := ClassifyFamilyAgreement(DeriveQuestionFamily(frame), ResolveFamilyForSample(sample))
				got := RouteFrameFamily(agreement, &frame, "")
				if want := RouteQuestionFamily(agreement); got != want {
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

// Check 10: the organization and a team's children are the only scope anchors
// this route serves (the second is CHAOS-12606). Every other topology with the
// goal set {allocate_investment}, and children_of_scope under any anchor that is
// not a team, keep what the table decides, as before this change.
func TestOnlyTheOrganizationAndATeamsChildrenTakeTheScopeAllocationRoute(t *testing.T) {
	t.Parallel()
	expressions := []SubjectExpression{
		{Kind: SubjectExpressionGroupedMembers, Grouped: &GroupedSetExpression{}},
		{Kind: SubjectExpressionExplicitSet, Explicit: &ExplicitSetExpression{}},
		{Kind: SubjectExpressionDiscoveredKind, Discovered: &DiscoveredSetExpression{MemberKind: contractsv1.ContextFabricSubjectTeam}},
		{Kind: SubjectExpressionChildrenOfScope, Scoped: &ScopedSetExpression{AnchorTerms: []string{"a"}, MemberKind: contractsv1.ContextFabricSubjectRepository}},
	}
	anchors := []SubjectKind{"", contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectProject, contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectOrganization}
	checked := 0
	for _, expression := range expressions {
		for _, anchor := range anchors {
			if expression.Kind == SubjectExpressionChildrenOfScope && anchor == contractsv1.ContextFabricSubjectTeam {
				continue
			}
			for _, shape := range allInvestigationShapes() {
				frame := goalFrame(expression, GoalAllocateInvestment)
				sample := FamilySample{Shape: shape, SubjectTerms: []string{"acme/widgets"}, ScopeAnchorKind: anchor, ScopeAnchorTerm: "a"}
				agreement := ClassifyFamilyAgreement(DeriveQuestionFamily(frame), ResolveFamilyForSample(sample))
				if got, want := RouteFrameFamily(agreement, &frame, anchor), RouteQuestionFamily(agreement); got != want {
					t.Fatalf("%s under %q, %s: decision %+v, want the table's %+v", expression.Kind, anchor, shape, got, want)
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("the sweep checked nothing")
	}
}

func TestATrendFrameStaysUnreachable(t *testing.T) {
	t.Parallel()
	got := routedFamily(goalFrame(allocationSubject(), GoalDescribeTrend), FamilySample{Shape: ShapeSingleSubject, SubjectTerms: []string{"acme/widgets"}})
	if got.Family != QuestionFamilySubjectInvestigation || got.Source != FamilyRoutePrecedence || got.Disposition != FamilyRouteDeclinedNotObserved {
		t.Fatalf("decision %+v, want the declined route: trend is not in this ruling", got)
	}
	if !slices.Contains(UnreachableQuestionFamilies(), QuestionFamilyTrend) {
		t.Fatal("trend left the unreachable families")
	}
	if slices.Contains(UnreachableQuestionFamilies(), QuestionFamilyInvestmentAllocation) {
		t.Fatal("investment_allocation is still declared unreachable")
	}
}

// A model pick is judged against the precedence table, which still has no row
// for this family. The route's exception does not change that count.
func TestAModelPickOfTheFamilyStaysOutsideThePrecedenceTable(t *testing.T) {
	t.Parallel()
	got := ResolveFamilyForSample(FamilySample{Shape: ShapeSingleSubject, SubjectTerms: []string{"acme/widgets"}, ModelFamily: QuestionFamilyInvestmentAllocation})
	if got.IncompatibilityReason != FamilyIncompatibilityUnreachable || got.AttemptedFamily != QuestionFamilyInvestmentAllocation || !got.Downgraded {
		t.Fatalf("outcome %+v, want the reason that names the table's range", got)
	}
}

// A stored answer was planned under the table it records. The bump is what keeps
// an answer planned as subject_investigation from being replayed for these
// questions, because the reuse lookup runs before interpretation.
func TestTheTableVersionFencesAnswersPlannedBeforeThisChange(t *testing.T) {
	t.Parallel()
	if QuestionFamilyTableVersion == "question-family.v4" || QuestionFamilyTableVersion == "question-family.v5" {
		t.Fatal("the family table changed and its version did not: reuse would replay the old plan")
	}
}

// routedByTheTableAlone is the route with the named subject exception taken out,
// which is the route before this change.
func routedByTheTableAlone(agreement FamilyAgreement) (QuestionFamily, FamilyRouteSource, FamilyRouteDisposition) {
	for _, rule := range familyRouteTable {
		if rule.class != agreement.Class {
			continue
		}
		if rule.source == FamilyRouteProjected {
			return agreement.ProjectedFamily, rule.source, rule.disposition
		}
		return agreement.PrecedenceFamily, rule.source, rule.disposition
	}
	return agreement.PrecedenceFamily, FamilyRoutePrecedence, FamilyRouteDeclinedUnexplained
}

// No question without the goal changes family: over every goal set of one or two
// goals without it, every expression kind and every shape, the decision is what
// the table alone decides, which is the route before this change.
func TestNoFrameWithoutTheGoalChangesFamily(t *testing.T) {
	t.Parallel()
	var goals []InvestigationGoal
	for _, goal := range InvestigationGoalVocabulary() {
		if goal != GoalAllocateInvestment {
			goals = append(goals, goal)
		}
	}
	expressions := []SubjectExpression{allocationSubject(), {Kind: SubjectExpressionOrganizationScope},
		{Kind: SubjectExpressionDiscoveredKind, Discovered: &DiscoveredSetExpression{MemberKind: contractsv1.ContextFabricSubjectTeam}},
		{Kind: SubjectExpressionChildrenOfScope, Scoped: &ScopedSetExpression{AnchorTerms: []string{"a"}, MemberKind: contractsv1.ContextFabricSubjectProject}}}
	checked := 0
	for _, first := range goals {
		for _, second := range goals {
			for _, expression := range expressions {
				for _, shape := range allInvestigationShapes() {
					sample := FamilySample{Shape: shape, SubjectTerms: []string{"acme/widgets"}}
					frame := goalFrame(expression, first, second)
					agreement := ClassifyFamilyAgreement(DeriveQuestionFamily(frame), ResolveFamilyForSample(sample))
					got := RouteFrameFamily(agreement, &frame, "")
					family, source, disposition := routedByTheTableAlone(agreement)
					checked++
					if got.Family != family || got.Source != source || got.Disposition != disposition {
						t.Fatalf("goals %v, %s, %s: decision %+v, want %s from %s, %s", []InvestigationGoal{first, second}, expression.Kind, shape, got, family, source, disposition)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("the sweep checked nothing")
	}
}

// The plan's budget holds, and the family keeps what subject_investigation gave
// these questions: a clarification of the subject, and the views it allowed.
func TestTheAllocationPlanKeepsWhatItsQuestionsHad(t *testing.T) {
	t.Parallel()
	allocation, ok := LookupQuestionFamily(QuestionFamilyInvestmentAllocation)
	if !ok {
		t.Fatal("no definition")
	}
	subject, _ := LookupQuestionFamily(QuestionFamilySubjectInvestigation)
	if allocation.Budget != subject.Budget {
		t.Fatalf("budget %+v, want the budget these questions had, %+v", allocation.Budget, subject.Budget)
	}
	if !slices.Equal(allocation.FactRoles, subject.FactRoles) {
		t.Fatalf("fact roles %v, want %v", allocation.FactRoles, subject.FactRoles)
	}
	for _, axis := range subject.ApplicableAxes {
		if !slices.Contains(allocation.ApplicableAxes, axis) {
			t.Errorf("axis %q is lost: an ambiguous subject could not be asked about", axis)
		}
	}
	if !slices.Equal(allocation.AskOrder, subject.AskOrder) {
		t.Errorf("ask order %v, want %v", allocation.AskOrder, subject.AskOrder)
	}
	// The plan a turn receives, from the real builder, under one budget.
	budget := ResponseBudget{MaxItems: 30, MaxSerializedBytes: 262144}
	planned := func(family QuestionFamily) AnswerPlan {
		return PlanAnswer(PlanAnswerInput{Family: QuestionFamilyOutcome{Family: family}, Budget: budget, MaxCohortMembers: 50})
	}
	allocated, investigated := planned(QuestionFamilyInvestmentAllocation), planned(QuestionFamilySubjectInvestigation)
	if allocated.Family != QuestionFamilyInvestmentAllocation {
		t.Fatalf("planned family %q", allocated.Family)
	}
	if allocated.Budget != investigated.Budget {
		t.Errorf("planned budget %+v, want %+v", allocated.Budget, investigated.Budget)
	}
	for _, axis := range investigated.Axes {
		if !slices.Contains(allocated.Axes, axis) {
			t.Errorf("planned axes %v lost %q", allocated.Axes, axis)
		}
	}
	for _, kind := range []contractsv1.ContextFabricRenderKind{contractsv1.ContextFabricRenderKindTable, contractsv1.ContextFabricRenderKindSeries,
		contractsv1.ContextFabricRenderKindTreemap, contractsv1.ContextFabricRenderKindSunburst} {
		if !slices.Contains(allocated.RenderKinds, kind) {
			t.Errorf("render kind %q is not planned", kind)
		}
	}
}
