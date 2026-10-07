package answerprojection

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func countRowFixture(stage contractsv1.ContextFabricOutcomeStage, requirement, obligation string, outcome contractsv1.ContextFabricPlanRequirementOutcome, served int) contractsv1.ContextFabricPlanRequirementOutcomeRow {
	return contractsv1.ContextFabricPlanRequirementOutcomeRow{Stage: stage, Requirement: requirement, Obligation: obligation, Outcome: outcome, Served: served, Declared: served}
}

func projectionWith(omitted, members int, rows ...contractsv1.ContextFabricPlanRequirementOutcomeRow) contractsv1.ContextFabricAnswerProjection {
	projection := contractsv1.ContextFabricAnswerProjection{
		Cohort: &contractsv1.ContextFabricProjectedCohort{Members: make([]contractsv1.ContextFabricProjectedCohortMember, members)},
	}
	projection.ProjectionBudget.CohortMembersOmitted = omitted
	projection.Completeness.Outcomes = rows
	return projection
}

func TestCountRowsBesideCutMembersGuards(t *testing.T) {
	t.Parallel()
	assembled := contractsv1.ContextFabricOutcomeStageAssembledResult
	satisfied := contractsv1.ContextFabricRequirementSatisfied
	narrowed := contractsv1.ContextFabricRequirementNarrowed
	cases := []struct {
		name       string
		projection contractsv1.ContextFabricAnswerProjection
		want       int
	}{
		{"satisfied count row beside a cut", projectionWith(5, 20, countRowFixture(assembled, "r", "count", satisfied, 25)), 1},
		{"narrowed count row beside a cut", projectionWith(5, 20, countRowFixture(assembled, "r", "count", narrowed, 25)), 1},
		{"nothing omitted", projectionWith(0, 20, countRowFixture(assembled, "r", "count", satisfied, 25)), 0},
		{"no cohort", func() contractsv1.ContextFabricAnswerProjection {
			p := projectionWith(5, 0, countRowFixture(assembled, "r", "count", satisfied, 25))
			p.Cohort = nil
			return p
		}(), 0},
		{"planning stage row", projectionWith(5, 20, countRowFixture(contractsv1.ContextFabricOutcomeStagePlanning, "r", "count", satisfied, 25)), 0},
		{"projection stage row", projectionWith(5, 20, countRowFixture(contractsv1.ContextFabricOutcomeStageProjection, "r", "count", satisfied, 25)), 0},
		{"other obligation", projectionWith(5, 20, countRowFixture(assembled, "r", "evidence", satisfied, 25)), 0},
		{"no requirement identity", projectionWith(5, 20, countRowFixture(assembled, "", "count", satisfied, 25)), 0},
		{"unavailable count", projectionWith(5, 20, countRowFixture(assembled, "r", "count", contractsv1.ContextFabricRequirementUnavailable, 25)), 0},
		{"count already within the served members", projectionWith(5, 20, countRowFixture(assembled, "r", "count", satisfied, 20)), 0},
		{"count below the served members", projectionWith(5, 20, countRowFixture(assembled, "r", "count", satisfied, 10)), 0},
	}
	for _, tc := range cases {
		got := countRowsBesideCutMembers(tc.projection)
		if len(got) != tc.want {
			t.Errorf("%s: appended %d rows, want %d", tc.name, len(got), tc.want)
			continue
		}
		for _, row := range got {
			if row.Requirement != "r" || row.Served != 20 || row.Declared != 25 || row.Stage != contractsv1.ContextFabricOutcomeStageProjection || row.Outcome != narrowed {
				t.Errorf("%s: row = %+v", tc.name, row)
			}
		}
	}
}
