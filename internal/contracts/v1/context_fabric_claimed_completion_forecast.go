package v1

import (
	"fmt"
	"strings"
)

// ContextFabricClaimedCompletionForecast (CHAOS-12608) is the completion
// forecast of a claimed team workload fact, as that fact serves it: the same
// stored capacity_forecasts row the claim is grounded on (CHAOS-12607). It is
// set by the server from the claim's grounding fact, never written by a model
// (jsonschema "-", as ContextFabricClaimedInvestmentMix), and only when the
// deployment serves the forecast's distribution (the fact then carries its
// completion_distribution_state).
//
// It exists because a claim states one field: the curve's markers, its
// horizon and the P50/P85/P95 of every claimed scope must be cited cells of
// the answer, and on the claim is where a reader finds them beside the rows
// they describe. A NULL percentile or date is absent, never 0 or "".
type ContextFabricClaimedCompletionForecast struct {
	// WorkScopeID is the forecast's work scope, absent when the fact names none.
	WorkScopeID string `json:"work_scope_id,omitempty"`
	// State is the fact's completion_distribution_state.
	State      ContextFabricCompletionDistributionState `json:"state"`
	ComputedAt string                                   `json:"computed_at"`
	// SimulationCount, HorizonDays and UnfinishedRuns are the run's size, the
	// day at which a run is stopped, not done, and how many runs stopped there.
	SimulationCount *int64 `json:"simulation_count,omitempty"`
	HorizonDays     *int64 `json:"horizon_days,omitempty"`
	UnfinishedRuns  *int64 `json:"unfinished_runs,omitempty"`
	P50Days         *int64 `json:"forecast_p50_days,omitempty"`
	P85Days         *int64 `json:"forecast_p85_days,omitempty"`
	P95Days         *int64 `json:"forecast_p95_days,omitempty"`
	P50Date         string `json:"forecast_p50_date,omitempty"`
	P85Date         string `json:"forecast_p85_date,omitempty"`
	P95Date         string `json:"forecast_p95_date,omitempty"`
}

// ContextFabricCompletionDistributionState is the closed vocabulary of a
// team workload fact's completion_distribution_state (CHAOS-12607).
type ContextFabricCompletionDistributionState string

const (
	ContextFabricCompletionDistributionAvailable          ContextFabricCompletionDistributionState = "available"
	ContextFabricCompletionDistributionNotStored          ContextFabricCompletionDistributionState = "not_stored"
	ContextFabricCompletionDistributionNoRunWithinHorizon ContextFabricCompletionDistributionState = "no_run_within_horizon"
	ContextFabricCompletionDistributionOverLimit          ContextFabricCompletionDistributionState = "over_limit"
	ContextFabricCompletionDistributionMalformed          ContextFabricCompletionDistributionState = "malformed"
	ContextFabricCompletionDistributionScopeAmbiguous     ContextFabricCompletionDistributionState = "scope_ambiguous"
	ContextFabricCompletionDistributionUnmatched          ContextFabricCompletionDistributionState = "unmatched"
)

// ContextFabricCompletionDistributionStates lists the closed vocabulary.
func ContextFabricCompletionDistributionStates() []ContextFabricCompletionDistributionState {
	return []ContextFabricCompletionDistributionState{
		ContextFabricCompletionDistributionAvailable, ContextFabricCompletionDistributionNotStored,
		ContextFabricCompletionDistributionNoRunWithinHorizon, ContextFabricCompletionDistributionOverLimit,
		ContextFabricCompletionDistributionMalformed, ContextFabricCompletionDistributionScopeAmbiguous,
		ContextFabricCompletionDistributionUnmatched,
	}
}

// ValidContextFabricCompletionDistributionState reports a member.
func ValidContextFabricCompletionDistributionState(state ContextFabricCompletionDistributionState) bool {
	for _, known := range ContextFabricCompletionDistributionStates() {
		if state == known {
			return true
		}
	}
	return false
}

// Completion forecast summary field names: the cited fields of a
// claimed_fact_forecast_summary point source, and the fact's own names.
const (
	ContextFabricForecastSummaryP50Days         = "forecast_p50_days"
	ContextFabricForecastSummaryP85Days         = "forecast_p85_days"
	ContextFabricForecastSummaryP95Days         = "forecast_p95_days"
	ContextFabricForecastSummarySimulationCount = "simulation_count"
	ContextFabricForecastSummaryHorizonDays     = "horizon_days"
	ContextFabricForecastSummaryUnfinishedRuns  = "unfinished_runs"
)

// Number returns the summary's value of a numeric field, and whether the
// summary carries one. A field the summary has no value for is not 0.
func (f ContextFabricClaimedCompletionForecast) Number(field string) (int64, bool) {
	var value *int64
	switch field {
	case ContextFabricForecastSummaryP50Days:
		value = f.P50Days
	case ContextFabricForecastSummaryP85Days:
		value = f.P85Days
	case ContextFabricForecastSummaryP95Days:
		value = f.P95Days
	case ContextFabricForecastSummarySimulationCount:
		value = f.SimulationCount
	case ContextFabricForecastSummaryHorizonDays:
		value = f.HorizonDays
	case ContextFabricForecastSummaryUnfinishedRuns:
		value = f.UnfinishedRuns
	}
	if value == nil {
		return 0, false
	}
	return *value, true
}

func (f ContextFabricClaimedCompletionForecast) validate() error {
	if !ValidContextFabricCompletionDistributionState(f.State) {
		return fmt.Errorf("completion_forecast state %q is not a member of the closed vocabulary", f.State)
	}
	if !stringLengthBetween(f.ComputedAt, 1, ContextFabricClaimedFieldMaxLength) || strings.TrimSpace(f.ComputedAt) != f.ComputedAt {
		return fmt.Errorf("completion_forecast computed_at violates v1 bounds")
	}
	for _, text := range []string{f.WorkScopeID, f.P50Date, f.P85Date, f.P95Date} {
		if len(text) > ContextFabricClaimedFieldMaxLength || strings.TrimSpace(text) != text {
			return fmt.Errorf("completion_forecast text violates v1 bounds")
		}
	}
	for _, value := range []*int64{f.SimulationCount, f.HorizonDays, f.UnfinishedRuns, f.P50Days, f.P85Days, f.P95Days} {
		if value != nil && *value < 0 {
			return fmt.Errorf("completion_forecast numbers must not be negative")
		}
	}
	return nil
}

// validateClaimedCompletionForecast admits a forecast summary only on a team
// workload claim: it describes that fact's stored forecast.
func validateClaimedCompletionForecast(kind ContextFabricFactKind, subject ContextFabricSubjectRef, forecast *ContextFabricClaimedCompletionForecast) error {
	if forecast == nil {
		return nil
	}
	if kind != ContextFabricFactWorkload || subject.Kind != ContextFabricSubjectTeam {
		return fmt.Errorf("completion_forecast is set on a %q %q claim; it describes a team workload forecast only", subject.Kind, kind)
	}
	return forecast.validate()
}
