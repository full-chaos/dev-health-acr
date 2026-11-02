package devhealthfacts_test

// CHAOS-12601: reach t9 served an organization allocation question with no fact
// and completeness "complete". The investment producer declared
// allocation_breakdown for team, project and repository but not for the
// organization, though it serves the organization and declares its breakdown
// table, so the planner wrote "not_applicable, fact_no_declaring_producer" and
// the answer read complete whatever was served. This runs the question through
// the real Engine.Investigate over a real ClickHouse with the production
// registry (the CHAOS-12585 harness).

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

const organizationAllocationRequirement = "allocation_breakdown/subject/organization"

// organizationAllocationRow is the row that decides the requirement: the
// planning row when the plan already says it does not apply (the defect), and
// otherwise the row the assembled answer evaluated, which follows it.
func organizationAllocationRow(t *testing.T, result contextfabric.InvestigationResult) contractsv1.ContextFabricPlanRequirementOutcomeRow {
	t.Helper()
	var deciding *contractsv1.ContextFabricPlanRequirementOutcomeRow
	for i, row := range result.Completeness.Outcomes {
		if row.Requirement != organizationAllocationRequirement {
			continue
		}
		if deciding == nil || row.Stage != contractsv1.ContextFabricOutcomeStagePlanning {
			deciding = &result.Completeness.Outcomes[i]
		}
	}
	if deciding == nil {
		t.Fatalf("no %s row in %+v", organizationAllocationRequirement, result.Completeness.Outcomes)
	}
	return *deciding
}

func TestAServedOrganizationBreakdownSatisfiesItsRequirement(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-allocation-completeness"
	query := organizationQuestionStore(t, ctx, orgID)
	result, model := serveOrganizationQuestion(t, ctx, query, orgID, storage.Principal{OrgID: orgID}, contextfabric.GoalAllocateInvestment)
	if model.offered != 1 {
		t.Fatalf("the model was offered %d organization facts, want 1", model.offered)
	}
	row := organizationAllocationRow(t, result)
	if row.Stage != contractsv1.ContextFabricOutcomeStageAssembledResult || row.Outcome != contractsv1.ContextFabricRequirementSatisfied || row.Served < 1 {
		t.Fatalf("row %+v: the breakdown was served, so the assembled answer satisfies the requirement", row)
	}
}

// Missing is not complete (check 12): an organization window that holds no
// stored work serves no fact, and the answer says it is not a full one.
func TestAnEmptyOrganizationWindowIsNotACompleteAllocationAnswer(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-allocation-completeness-empty"
	rawQuery, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	result, model := serveOrganizationQuestion(t, ctx, statementthreads.API(rawQuery), orgID, storage.Principal{OrgID: orgID}, contextfabric.GoalAllocateInvestment)
	if model.offered != 0 {
		t.Fatalf("the model was offered %d organization facts, want none", model.offered)
	}
	if result.Completeness.State == contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("completeness %+v: no breakdown was served, so the answer is not complete", result.Completeness)
	}
	row := organizationAllocationRow(t, result)
	if row.Stage != contractsv1.ContextFabricOutcomeStageAssembledResult || row.Outcome != contractsv1.ContextFabricRequirementUnavailable || row.Served != 0 {
		t.Fatalf("row %+v: the producer declares the breakdown, and none was served", row)
	}
}

// Codex r1 on #1947 (executed): answer reuse served a stored organization
// allocation answer whose only row said "not applicable", so it read complete.
// The query version is a reuse-key dimension; it moves with this declaration, so
// such an answer is never replayed.
func TestTheQueryVersionFencesAnswersSavedBeforeTheOrganizationObligation(t *testing.T) {
	t.Parallel()
	if devhealthfacts.QueryVersion == "devhealthfacts.clickhouse.v269" {
		t.Fatal("the organization obligation changed what an answer reads and the query version did not move: reuse would replay a complete answer with no breakdown")
	}
}
