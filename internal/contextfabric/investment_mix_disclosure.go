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

// discloseCompletionForecast (CHAOS-12608) stamps every team workload claim
// with the forecast its grounding fact serves: the summary the curve and the
// percentiles table cite. Like discloseInvestmentMix it runs once, after every
// claim is minted, and overwrites what a claim carried: a draft never authors
// it. A fact that does not serve the forecast's distribution (the deployment
// switch is off, so it carries no completion_distribution_state) gives none.
func discloseCompletionForecast(claims []ClaimedFact, facts []CanonicalFact) []ClaimedFact {
	for i := range claims {
		claims[i].CompletionForecast = nil
		if claims[i].Kind != FactWorkload || claims[i].Subject.Kind != contractsv1.ContextFabricSubjectTeam || len(claims[i].DerivedFrom) > 0 {
			continue
		}
		fact, ok := claimSourceFact(facts, claims[i])
		if !ok {
			continue
		}
		claims[i].CompletionForecast = completionForecastOf(fact)
	}
	return claims
}

// completionForecastOf is the summary a team workload fact serves, or nil when
// it serves no completion_distribution_state.
func completionForecastOf(fact CanonicalFact) *contractsv1.ContextFabricClaimedCompletionForecast {
	state, ok := stringField(fact, "completion_distribution_state")
	if !ok || state == "" {
		return nil
	}
	computedAt, _ := stringField(fact, "computed_at")
	scope, _ := stringField(fact, "work_scope_id")
	summary := &contractsv1.ContextFabricClaimedCompletionForecast{
		WorkScopeID: scope,
		State:       contractsv1.ContextFabricCompletionDistributionState(state),
		ComputedAt:  computedAt,
	}
	number := func(name string) *int64 {
		if value, ok := integerField(fact, name); ok {
			return &value
		}
		return nil
	}
	summary.SimulationCount = number("simulation_count")
	summary.HorizonDays = number("horizon_days")
	summary.UnfinishedRuns = number("unfinished_runs")
	summary.P50Days = number("forecast_p50_days")
	summary.P85Days = number("forecast_p85_days")
	summary.P95Days = number("forecast_p95_days")
	summary.P50Date, _ = stringField(fact, "forecast_p50_date")
	summary.P85Date, _ = stringField(fact, "forecast_p85_date")
	summary.P95Date, _ = stringField(fact, "forecast_p95_date")
	return summary
}
