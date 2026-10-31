package answerprojection

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12160: the answer projection is where Ask Dev reads a fact from, so
// the roll-up disclosure an investment claim carries must reach it too.
func TestProjectCopiesTheInvestmentMixDisclosure(t *testing.T) {
	result := richResult()
	mix := &contractsv1.ContextFabricClaimedInvestmentMix{
		InvestmentMixSource: contractsv1.ContextFabricInvestmentMixSourceOwningTeamRollup,
		RollupBasis:         "team_project_ownership_via_owned_repos_work_unit_investments",
		WorkUnitCount:       7240,
	}
	found := false
	for i := range result.ClaimedFacts {
		if result.ClaimedFacts[i].ClaimID == "claim_status_001" {
			result.ClaimedFacts[i].Kind = contractsv1.ContextFabricFactInvestment
			result.ClaimedFacts[i].InvestmentMix = mix
			found = true
		}
	}
	if !found {
		t.Fatal("fixture defect: claim_status_001 is not in richResult")
	}
	projection := Project(result, Budget{})
	var got *contractsv1.ContextFabricProjectedFact
	for i := range projection.KeyFacts {
		if projection.KeyFacts[i].ClaimID == "claim_status_001" {
			got = &projection.KeyFacts[i]
		}
	}
	if got == nil {
		t.Fatal("claim_status_001 did not reach the projection at all")
	}
	if got.InvestmentMix == nil || *got.InvestmentMix != *mix {
		t.Fatalf("projected investment_mix = %+v, want %+v", got.InvestmentMix, mix)
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection with the disclosure does not validate: %v", err)
	}
}
