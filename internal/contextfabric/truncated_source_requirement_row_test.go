package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A truncated fact source whose cause is a source-level shortfall publishes a
// row the outcome validator admits, for every such cause the coverage layer can
// carry onto it. A scope-expansion gap on the team blockers rollup reached
// production as a refused row (invalid result) before this held.
func TestTruncatedSourceRowIsAdmittedForEverySourceTruncationCause(t *testing.T) {
	requirement := contractsv1.ContextFabricPlanRequirement{
		Requirement: "remaining_work/member/team", Obligation: string(ObligationRemainingWork),
		Role: string(SubjectRoleMember), Subject: SubjectTeam, Kind: string(ObligationKindRead),
		FactKinds: []FactKind{FactBlockers}, Scope: string(CompletionScopeEachMember),
		Quantifier: string(CompletionQuantifierAtLeastOne),
	}
	threshold, ok := readQuantifierThreshold(requirement.Quantifier)
	if !ok {
		t.Fatal("at_least_one is not a recognised read quantifier")
	}
	for _, code := range []contractsv1.ContextFabricCoverageDetailCode{
		contractsv1.ContextFabricCoverageDetailFactProviderReported,
		contractsv1.ContextFabricCoverageDetailFactScopeUnexpanded,
	} {
		code := code
		t.Run(string(code), func(t *testing.T) {
			coverage := factCoverage(FactBlockers, SourceTruncated)
			coverage.Details = []CoverageDetail{{Source: canonicalFactSourcePrefix + string(FactBlockers), FactKind: FactBlockers, Code: code, Degrading: true}}
			evidence := evaluateReadRequirement(requirement, coverage, map[FactKind]bool{FactBlockers: true})
			row, published, _ := readRequirementOutcomeRow(requirement, threshold, evidence, readPopulationEvidence{})
			if !published {
				t.Fatal("no row published")
			}
			if row.Outcome != contractsv1.ContextFabricRequirementNarrowed || row.CauseCoverage != code || !row.CauseObserved {
				t.Fatalf("row = %+v, want narrowed carrying the observed cause %q", row, code)
			}
			if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
				t.Fatalf("the published row is refused by the outcome validator: %v", err)
			}
		})
	}
}
