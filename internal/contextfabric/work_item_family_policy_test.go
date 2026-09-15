package contextfabric

import "testing"

func TestWorkItemFamilyRegistryPolicyIsClosed(t *testing.T) {
	vocabulary := QuestionFamilyVocabulary()
	families := append([]QuestionFamily(nil), vocabulary[:]...)
	families = append(families, "", "future_family")
	for _, family := range families {
		t.Run(string(family), func(t *testing.T) {
			definition, known := LookupQuestionFamily(family)
			allowed := known && definition.allowsWorkItemTuple
			want := family == QuestionFamilyScopedCohortStatus
			if allowed != want {
				t.Fatalf("family %q tuple policy=%v want=%v", family, allowed, want)
			}
			frame := prospectiveTupleFrame(GoalAssessState, GoalCountOrAggregate)
			got := prospectiveWorkItemTupleAdmission(&frame, allowed, TimeContext{Axis: TemporalCurrent})
			if (got == workItemTupleProspective) != want {
				t.Fatalf("family %q admission=%v want allowed=%v", family, got, want)
			}
		})
	}
}

func TestWorkItemFamilyPolicyVersionFence(t *testing.T) {
	state := semanticFixture(t)
	plan := &AnswerPlan{Family: state.Family}
	if state.FamilyTableVersion != "question-family.v3" {
		t.Fatalf("current capture family version=%q want v3", state.FamilyTableVersion)
	}
	stored := StoredInvestigationResult{SemanticState: state, SemanticStateRead: SemanticStateReadAvailable}
	if got := semanticStateAdmission(stored, plan); got != ContinuationReasonNone {
		t.Fatalf("current semantic carrier refused: %s", got)
	}
	state.FamilyTableVersion = "question-family.v2"
	if got := semanticStateAdmission(stored, plan); got != ContinuationReasonContextVersionMismatch {
		t.Fatalf("legacy v2 semantic carrier admitted: %s", got)
	}
}
