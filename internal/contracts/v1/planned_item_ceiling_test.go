package v1

import "testing"

func TestPlannedItemCeilingIsTheHigherOfConfiguredAndPlanned(t *testing.T) {
	var result ContextFabricInvestigationResult
	if got := ContextFabricPlannedItemCeiling(result, 30); got != 30 {
		t.Fatalf("no plan = %d, want the configured 30", got)
	}
	result.AnswerPlan = &ContextFabricAnswerPlan{Budget: ContextFabricAnswerPlanBudget{MaxItems: 116}}
	if got := ContextFabricPlannedItemCeiling(result, 30); got != 116 {
		t.Fatalf("raised plan = %d, want 116", got)
	}
	result.AnswerPlan.Budget.MaxItems = 12
	if got := ContextFabricPlannedItemCeiling(result, 30); got != 30 {
		t.Fatalf("lower plan = %d, want the configured 30 (a plan never lowers the ceiling)", got)
	}
}
