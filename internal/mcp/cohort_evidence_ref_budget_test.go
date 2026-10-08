package mcp

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestCohortEvidenceRefBudgetGivesEveryServedMemberItsReference(t *testing.T) {
	cohort := &contractsv1.ContextFabricCohort{Members: make([]contractsv1.ContextFabricCohortMember, 19)}
	if got := cohortEvidenceRefBudget(defaultMaxAnswerEvidenceRefs, nil, cohort); got != 19+defaultMaxAnswerEvidenceRefs {
		t.Fatalf("default budget with 19 members = %d, want %d", got, 19+defaultMaxAnswerEvidenceRefs)
	}
	if got := cohortEvidenceRefBudget(7, &contractsv1.MCPInvestigationBudget{MaxEvidenceRefs: 7}, cohort); got != 7 {
		t.Fatalf("a caller's own budget = %d, want 7", got)
	}
	if got := cohortEvidenceRefBudget(defaultMaxAnswerEvidenceRefs, nil, nil); got != defaultMaxAnswerEvidenceRefs {
		t.Fatalf("no cohort = %d, want the default", got)
	}
	workItems := &contractsv1.ContextFabricCohort{Kind: contractsv1.ContextFabricSubjectWorkItem, Members: make([]contractsv1.ContextFabricCohortMember, 19)}
	if got := cohortEvidenceRefBudget(defaultMaxAnswerEvidenceRefs, nil, workItems); got != defaultMaxAnswerEvidenceRefs {
		t.Fatalf("work-item list = %d, want the default", got)
	}
	grouped := &contractsv1.ContextFabricCohort{Members: make([]contractsv1.ContextFabricCohortMember, 19), Groups: []contractsv1.ContextFabricCohortGroup{{}}}
	if got := cohortEvidenceRefBudget(defaultMaxAnswerEvidenceRefs, nil, grouped); got != defaultMaxAnswerEvidenceRefs {
		t.Fatalf("grouped cohort = %d, want the default", got)
	}
	huge := &contractsv1.ContextFabricCohort{Members: make([]contractsv1.ContextFabricCohortMember, 5000)}
	if got := cohortEvidenceRefBudget(defaultMaxAnswerEvidenceRefs, nil, huge); got != contractsv1.ContextFabricProjectedEvidenceMaxCount {
		t.Fatalf("huge cohort = %d, want the projection ceiling", got)
	}
}
