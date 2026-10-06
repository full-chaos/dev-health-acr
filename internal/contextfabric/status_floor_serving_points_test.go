package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func truncatedPopulationNoMemberResult() InvestigationResult {
	r := affirmationResult()
	r.Status = InvestigationNoMatch
	r.Completeness.Outcomes = []RequirementOutcomeRow{{
		Requirement: "evidence/count", Obligation: string(ObligationCount),
		Stage:         contractsv1.ContextFabricOutcomeStageAssembledResult,
		Outcome:       contractsv1.ContextFabricRequirementNarrowed,
		CauseCoverage: contractsv1.ContextFabricCoverageDetailPopulationTruncated,
		Served:        0, Declared: 0,
	}}
	return r
}

// A floor is a floor: the completeness authority, with either flag on, never
// serves a status above the population floor.
func TestServedLateWritersKeepPopulationFloorUnderEveryAuthorityFlag(t *testing.T) {
	for _, flags := range []struct{ enabled, symmetric bool }{{false, false}, {true, false}, {true, true}, {false, true}} {
		engine := completenessAuthorityTestEngine(t, EngineDependencies{}, flags.enabled, flags.symmetric)
		served, _ := engine.servedLateWriters(context.Background(), truncatedPopulationNoMemberResult())
		if served.Status != InvestigationDegraded {
			t.Fatalf("flags %+v: status = %q, want degraded", flags, served.Status)
		}
		if served.Completeness.TerminalStatus != served.Status {
			t.Fatalf("flags %+v: terminal status %q != status %q", flags, served.Completeness.TerminalStatus, served.Status)
		}
	}
}

// The byte fit measures the document the route serves: the budget-trim shape
// carries the same floor.
func TestBudgetTrimServedShapeCarriesPopulationFloor(t *testing.T) {
	engine := completenessAuthorityTestEngine(t, EngineDependencies{}, true, true)
	shaped := engine.budgetTrimServedShape(context.Background(), truncatedPopulationNoMemberResult())
	if shaped.Status != InvestigationDegraded || !hasLimitation(shaped.Limitations, truncatedPopulationNoMemberLimitation) {
		t.Fatalf("trim shape status %q, limitation present %v", shaped.Status, hasLimitation(shaped.Limitations, truncatedPopulationNoMemberLimitation))
	}
}

// The limitation is service-authored, so a bounded projection keeps it.
func TestTruncatedPopulationLimitationIsServiceAuthored(t *testing.T) {
	for _, known := range contractsv1.ContextFabricServiceAuthoredLimitations() {
		if known == truncatedPopulationNoMemberLimitation {
			return
		}
	}
	t.Fatal("truncated-population limitation is not in ContextFabricServiceAuthoredLimitations")
}
