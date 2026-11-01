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
	return RouteQuestionFamily(ClassifyFamilyAgreement(DeriveQuestionFamily(frame), ResolveFamilyForSample(sample)))
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

// The ruling names a named subject. An organization frame stays where it was:
// nothing commits an organization subject, and a repository-bound caller gets no
// organization fact.
func TestAnOrganizationInvestmentQuestionKeepsItsFamily(t *testing.T) {
	t.Parallel()
	frame := goalFrame(SubjectExpression{Kind: SubjectExpressionOrganizationScope}, GoalAllocateInvestment)
	sample := FamilySample{Shape: ShapeOpen}
	got := routedFamily(frame, sample)
	if want := ResolveFamilyForSample(sample).Family; got.Family != want || got.Family == QuestionFamilyInvestmentAllocation {
		t.Fatalf("decision %+v, want the precedence family %q", got, want)
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
	if QuestionFamilyTableVersion == "question-family.v4" {
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
					got := RouteQuestionFamily(agreement)
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
