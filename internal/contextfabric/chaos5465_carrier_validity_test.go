package contextfabric

import "testing"

func r4CheckedPrior(t *testing.T, id, question string, family QuestionFamily, group SubjectKind) InvestigationResult {
	t.Helper()
	prior := continuationPrior(t, id, question, family, group)
	plan := PlanAnswer(PlanAnswerInput{Family: QuestionFamilyOutcome{Family: family, Source: QuestionFamilySourceModel, WinningSample: FamilySample{GroupKind: group}}, Budget: ResponseBudget{MaxItems: 50, MaxSerializedBytes: continuationCarrierBudgetBytes()}, MaxCohortMembers: 20})
	prior.AnswerPlan = &plan
	if err := prior.Validate(); err != nil {
		t.Fatalf("invalid carrier fixture: %v", err)
	}
	return prior
}

func TestReviewR4_CarrierValidity(t *testing.T) {
	for _, family := range []QuestionFamily{QuestionFamilyGroupedCohortStatus, QuestionFamilyDiscoveredCohortRanking} {
		group := SubjectKind("")
		if family == QuestionFamilyGroupedCohortStatus {
			group = SubjectTeam
		}
		prior := continuationPrior(t, continuationPriorID, validInvestigationRequest().Question, family, group)
		plan := PlanAnswer(PlanAnswerInput{Family: QuestionFamilyOutcome{Family: family, Source: QuestionFamilySourceModel, WinningSample: FamilySample{GroupKind: group}}, Budget: ResponseBudget{MaxItems: 50, MaxSerializedBytes: continuationCarrierBudgetBytes()}, MaxCohortMembers: 20})
		prior.AnswerPlan = &plan
		if err := prior.Validate(); err != nil {
			t.Errorf("family=%s invalid: %v", family, err)
		} else {
			t.Logf("family=%s prior.Validate()=nil", family)
		}
	}
}
