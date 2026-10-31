package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// discloseInvestmentMix (CHAOS-12160) stamps every investment claim of an
// assembled answer with the attribution its grounding fact carries: the
// project's own work, or its owning teams' roll-up, and the work units of
// each. CHAOS-5930 decision A makes a project's mix a DISCLOSED roll-up when
// the project has no measurable work of its own in the window; before this,
// the label stayed on the fact and the served claim read as the project's own
// investment.
//
// It runs once, after every claim of the answer is minted (the model's or a
// supplied draft's, the cardinality claim, the cohort, status and blockers
// drivers), on the facts the claims are grounded against. It overwrites what
// a claim carried: a draft never authors the disclosure, and a claim whose
// fact carries no investment_mix_source (a team, repository or organization
// mix, or a derived value) carries none.
func discloseInvestmentMix(claims []ClaimedFact, facts []CanonicalFact) []ClaimedFact {
	for i := range claims {
		claims[i].InvestmentMix = nil
		if claims[i].Kind != FactInvestment {
			continue
		}
		fact, ok := claimSourceFact(facts, claims[i])
		if !ok {
			continue
		}
		claims[i].InvestmentMix = investmentMixOf(fact)
	}
	return claims
}

// investmentMixOf is the disclosure a project investment fact carries, or nil
// when the fact names no mix source.
func investmentMixOf(fact CanonicalFact) *contractsv1.ContextFabricClaimedInvestmentMix {
	source, ok := stringField(fact, FactFieldInvestmentMixSource)
	if !ok || source == "" {
		return nil
	}
	basis, _ := stringField(fact, "rollup_basis")
	units, _ := integerField(fact, "work_unit_count")
	own := units
	if source == InvestmentMixSourceOwningTeamRollup {
		own, _ = integerField(fact, "project_own_work_unit_count")
	}
	return &contractsv1.ContextFabricClaimedInvestmentMix{
		InvestmentMixSource:     contractsv1.ContextFabricInvestmentMixSource(source),
		RollupBasis:             basis,
		WorkUnitCount:           units,
		ProjectOwnWorkUnitCount: own,
	}
}
