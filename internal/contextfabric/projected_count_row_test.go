package contextfabric

import (
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func projectedCountRows(rows []contractsv1.ContextFabricPlanRequirementOutcomeRow) []contractsv1.ContextFabricPlanRequirementOutcomeRow {
	var found []contractsv1.ContextFabricPlanRequirementOutcomeRow
	for _, row := range rows {
		if row.Obligation == string(ObligationCount) {
			found = append(found, row)
		}
	}
	return found
}

func TestProjectedCountRowDoesNotOverstateTheServedMemberSet(t *testing.T) {
	t.Parallel()
	const canonical = 30
	const cut = 25
	result, _ := runCensusInvestigation(t, canonical, 40, true, false)
	projection := answerprojection.Project(result, answerprojection.Budget{MaxCohortMembers: cut})

	if projection.Cohort == nil || len(projection.Cohort.Members) != cut {
		t.Fatalf("projected members = %v, want %d (fixture must cut)", projection.Cohort, cut)
	}
	rows := projectedCountRows(projection.Completeness.Outcomes)
	if len(rows) == 0 {
		t.Fatal("projection carries no count row; fixture does not reach the count obligation")
	}
	effective := rows[len(rows)-1]
	if effective.Stage != contractsv1.ContextFabricOutcomeStageProjection ||
		effective.Outcome != contractsv1.ContextFabricRequirementNarrowed ||
		effective.Served != cut || effective.Declared != canonical ||
		effective.Requirement == "" || effective.Requirement != rows[0].Requirement {
		t.Errorf("effective count row = %+v, want projection-stage narrowed %d/%d for the count requirement", effective, cut, canonical)
	}
	if projection.Completeness.State != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Errorf("state = %q, want partial", projection.Completeness.State)
	}
	if effective.Impact != contractsv1.ContextFabricAnswerImpactScope || effective.CauseOverrun != contractsv1.ContextFabricBudgetOverrunBytes || !effective.CauseObserved || len(effective.Refinements) != 1 {
		t.Errorf("effective count row cause/refinement = %+v", effective)
	}
	if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(effective); err != nil {
		t.Errorf("effective row invalid: %v", err)
	}
}

func TestProjectedCountRowIsUnchangedWhenNothingWasCut(t *testing.T) {
	t.Parallel()
	result, _ := runCensusInvestigation(t, 4, 40, true, false)
	projection := answerprojection.Project(result, answerprojection.Budget{})
	canonicalRows := projectedCountRows(result.Completeness.Outcomes)
	rows := projectedCountRows(projection.Completeness.Outcomes)
	if !reflect.DeepEqual(rows, canonicalRows) {
		t.Fatalf("count rows = %+v, want %+v", rows, canonicalRows)
	}
}
