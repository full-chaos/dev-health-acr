package v1

import (
	"strings"
	"testing"
)

func validInvestmentMixClaim() ContextFabricClaimedFact {
	value := 0.55
	return ContextFabricClaimedFact{
		ClaimID: "claim_investment_quality", Kind: ContextFabricFactInvestment,
		Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project.v2:linear:p1", Label: "P1"},
		Field:   "theme_quality", Value: ContextFabricScalarValue{Number: &value},
		InvestmentMix: &ContextFabricClaimedInvestmentMix{
			InvestmentMixSource: ContextFabricInvestmentMixSourceOwningTeamRollup,
			RollupBasis:         "team_project_ownership_via_owned_repos_work_unit_investments",
			WorkUnitCount:       7240,
		},
	}
}

func TestAClaimedInvestmentMixValidates(t *testing.T) {
	t.Parallel()
	if err := validInvestmentMixClaim().Validate(); err != nil {
		t.Fatalf("valid investment_mix claim: %v", err)
	}
	claim := validInvestmentMixClaim()
	claim.InvestmentMix = nil
	if err := claim.Validate(); err != nil {
		t.Fatalf("an investment claim without investment_mix (additive, optional): %v", err)
	}
}

func TestAClaimedInvestmentMixIsRefusedOutsideItsBounds(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*ContextFabricClaimedFact){
		"on a status claim": func(c *ContextFabricClaimedFact) { c.Kind = ContextFabricFactStatus },
		"unknown source":    func(c *ContextFabricClaimedFact) { c.InvestmentMix.InvestmentMixSource = "team_vote" },
		"empty basis":       func(c *ContextFabricClaimedFact) { c.InvestmentMix.RollupBasis = "" },
		"padded basis":      func(c *ContextFabricClaimedFact) { c.InvestmentMix.RollupBasis = " basis" },
		"basis over the bound": func(c *ContextFabricClaimedFact) {
			c.InvestmentMix.RollupBasis = strings.Repeat("b", ContextFabricClaimedFieldMaxLength+1)
		},
		"negative units":     func(c *ContextFabricClaimedFact) { c.InvestmentMix.WorkUnitCount = -1 },
		"negative own units": func(c *ContextFabricClaimedFact) { c.InvestmentMix.ProjectOwnWorkUnitCount = -1 },
	}
	for name, mutate := range cases {
		claim := validInvestmentMixClaim()
		mutate(&claim)
		if err := claim.Validate(); err == nil || !strings.Contains(err.Error(), "investment_mix") {
			t.Errorf("%s: Validate() = %v, want an investment_mix refusal", name, err)
		}
	}
}
