package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The diagnostic names the same requirement the completeness derivation counts
// as an unevaluated read, so a not_applicable planning seed is never named.
func TestANotApplicableReadSeedIsNeverTheUnevaluatedDecidingRequirement(t *testing.T) {
	t.Parallel()
	row := func(outcome contractsv1.ContextFabricPlanRequirementOutcome) contractsv1.ContextFabricPlanRequirementOutcomeRow {
		return contractsv1.ContextFabricPlanRequirementOutcomeRow{Stage: contractsv1.ContextFabricOutcomeStagePlanning, Requirement: "health/member/incident", Obligation: "health", Outcome: outcome, Impact: contractsv1.ContextFabricAnswerImpactNone}
	}
	if name, found := decidingUnevaluatedReadRequirement([]contractsv1.ContextFabricPlanRequirementOutcomeRow{row(contractsv1.ContextFabricRequirementSatisfied)}); !found || name != "health/member/incident" {
		t.Fatalf("control: a satisfied planning-only read must be named, got %q %v", name, found)
	}
	if name, found := decidingUnevaluatedReadRequirement([]contractsv1.ContextFabricPlanRequirementOutcomeRow{row(contractsv1.ContextFabricRequirementNotApplicable)}); found {
		t.Fatalf("a not_applicable seed was named as the unevaluated requirement: %q", name)
	}
}
