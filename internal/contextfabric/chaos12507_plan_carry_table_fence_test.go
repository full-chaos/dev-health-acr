package contextfabric

import (
	"context"
	"testing"
)

// CHAOS-12507: QuestionFamilyTableVersion is a fence, and plan carry walked past
// it. A stored plan of another table gave a current plan that claimed the current
// version for a family the current table did not choose.
func TestAPlanOfAnotherFamilyTableIsNotCarried(t *testing.T) {
	t.Parallel()
	for name, version := range map[string]string{
		"an older table":      "question-family.v4",
		"no version recorded": "",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			request := validInvestigationRequest()
			prior := carriablePlanResult("result_plan_of_older_table", request.Question)
			prior.AnswerPlan.FamilyVersion = version
			request.ParentResultID = prior.ResultID
			store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
			telemetry := &recordingTelemetry{}
			if _, err := planCarryOutcomeEngine(t, store, telemetry).Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if len(telemetry.planCarryOutcomes) != 1 {
				t.Fatalf("got %d plan carry outcomes", len(telemetry.planCarryOutcomes))
			}
			got := telemetry.planCarryOutcomes[0]
			if got.outcome != PlanCarryMissStaleFamilyTable {
				t.Fatalf("outcome = %q, want %q", got.outcome, PlanCarryMissStaleFamilyTable)
			}
			if got.sourceResultID != "" {
				t.Fatalf("sourceResultID = %q on a refusal", got.sourceResultID)
			}
		})
	}
}

// The fence refuses another table and nothing else: a plan of the table in force
// still carries. The other outcomes of the walk keep their precedence.
func TestAPlanOfTheTableInForceStillCarries(t *testing.T) {
	t.Parallel()
	request := validInvestigationRequest()
	prior := carriablePlanResult("result_plan_of_this_table", request.Question)
	request.ParentResultID = prior.ResultID
	store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
	telemetry := &recordingTelemetry{}
	if _, err := planCarryOutcomeEngine(t, store, telemetry).Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(telemetry.planCarryOutcomes) != 1 || telemetry.planCarryOutcomes[0].outcome != PlanCarryHit {
		t.Fatalf("outcomes = %+v, want one hit", telemetry.planCarryOutcomes)
	}
}
