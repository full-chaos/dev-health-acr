package contextfabric

import (
	"time"

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
// it serves no completion_distribution_state, or a computed_at it cannot read
// as an instant.
func completionForecastOf(fact CanonicalFact) *contractsv1.ContextFabricClaimedCompletionForecast {
	state, ok := stringField(fact, "completion_distribution_state")
	if !ok || state == "" {
		return nil
	}
	raw, _ := stringField(fact, "computed_at")
	computedAt, ok := forecastInstant(raw)
	if !ok {
		return nil
	}
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
	summary.TargetItems = number("target_items")
	summary.P50Days = number("forecast_p50_days")
	summary.P85Days = number("forecast_p85_days")
	summary.P95Days = number("forecast_p95_days")
	summary.P50Date = forecastDate(fact, "forecast_p50_date")
	summary.P85Date = forecastDate(fact, "forecast_p85_date")
	summary.P95Date = forecastDate(fact, "forecast_p95_date")
	return summary
}

// clickHouseInstantLayout is how ClickHouse prints a DateTime64:
// capacity_forecasts.computed_at is a DateTime64(3, 'UTC'), read with
// toString, so the text names no zone and is UTC.
const clickHouseInstantLayout = "2006-01-02 15:04:05.999999999"

// forecastInstant is a fact's computed_at as the summary serves it, an RFC
// 3339 UTC instant (lead ruling on CHAOS-12608). The text is read as UTC, never
// in this process's zone: a zone-less instant read as local time moves with
// the host, and a day counted from it can move by one. A value already in RFC
// 3339 is put in UTC. Anything else is not an instant this summary can state.
func forecastInstant(raw string) (string, bool) {
	for _, layout := range []string{clickHouseInstantLayout, time.RFC3339Nano} {
		if instant, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return instant.UTC().Format(time.RFC3339Nano), true
		}
	}
	return "", false
}

// forecastDate is a fact's date field as the summary serves it: the stored
// RFC 3339 full-date, or absent when the fact has none or another form. A
// date it cannot state is absent, as a NULL date is.
func forecastDate(fact CanonicalFact, field string) string {
	date, _ := stringField(fact, field)
	if !contractsv1.ValidContextFabricForecastDate(date) {
		return ""
	}
	return date
}

const workloadFactSource = "canonical_fact:workload"

// discloseForecastScopes (CHAOS-12726) says how many work scopes a team has
// when an answer's forecast claims fewer of them. A team is forecast under
// several scopes at once, and one fact is read per scope; a claim states one
// scope's forecast. Each forecast summary of a team gets the count of the
// team's scopes the read held, and a team whose claims cover fewer than that
// gets one service-authored limitation naming the scopes covered and the
// number left out. It reads only the facts already read: no new query. It
// discloses what was used; it does not choose a forecast source.
func discloseForecastScopes(result *InvestigationResult, bundle CanonicalFactBundle) {
	type teamScopes struct {
		label string
		read  map[string]struct{}
	}
	read := map[string]*teamScopes{}
	var order []string
	for _, fact := range bundle.Facts {
		if fact.Kind != FactWorkload || fact.Subject.Kind != SubjectTeam || completionForecastOf(fact) == nil {
			continue
		}
		scope, _ := stringField(fact, "work_scope_id")
		team := read[fact.Subject.CanonicalID]
		if team == nil {
			team = &teamScopes{label: fact.Subject.Label, read: map[string]struct{}{}}
			read[fact.Subject.CanonicalID] = team
			order = append(order, fact.Subject.CanonicalID)
		}
		team.read[scope] = struct{}{}
	}
	if len(read) == 0 {
		return
	}
	atLeast := false
	for _, source := range bundle.Coverage.Sources {
		if source.Source == workloadFactSource && source.State == SourceTruncated {
			atLeast = true
		}
	}
	covered := map[string]map[string]struct{}{}
	for index := range result.ClaimedFacts {
		claim := &result.ClaimedFacts[index]
		if claim.CompletionForecast == nil {
			continue
		}
		team := read[claim.Subject.CanonicalID]
		if team == nil {
			continue
		}
		total := int64(len(team.read))
		claim.CompletionForecast.ScopeCount = &total
		claim.CompletionForecast.ScopeCountAtLeast = atLeast
		if covered[claim.Subject.CanonicalID] == nil {
			covered[claim.Subject.CanonicalID] = map[string]struct{}{}
		}
		covered[claim.Subject.CanonicalID][claim.CompletionForecast.WorkScopeID] = struct{}{}
	}
	var sentences []string
	for _, id := range order {
		scopes := covered[id]
		team := read[id]
		if len(scopes) == 0 {
			continue
		}
		scopeName := ""
		for scope := range scopes {
			scopeName = scope
		}
		sentence := contractsv1.ContextFabricForecastScopeLimitation(team.label, scopeName, int64(len(scopes)), int64(len(team.read)), atLeast)
		if sentence != "" {
			sentences = append(sentences, sentence)
		}
	}
	if len(sentences) == 0 {
		return
	}
	composed, displaced := appendBoundedLimitations(result.Limitations, sentences)
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	result.Coverage.Partial = true
}
