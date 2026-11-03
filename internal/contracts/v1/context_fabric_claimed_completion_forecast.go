package v1

import (
	"fmt"
	"regexp"
	"strings"
	"time"
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
	State ContextFabricCompletionDistributionState `json:"state"`
	// ComputedAt is the forecast's computed_at as an RFC 3339 UTC instant,
	// "2026-11-02T04:00:00Z" (lead ruling on CHAOS-12608). ClickHouse's own
	// text, "2026-11-02 04:00:00.000", names no zone: a browser reads it as
	// local time, and a date counted from it can move by one day.
	ComputedAt string `json:"computed_at"`
	// SimulationCount, HorizonDays and UnfinishedRuns are the run's size, the
	// day at which a run is stopped, not done, and how many runs stopped there.
	SimulationCount *int64 `json:"simulation_count,omitempty"`
	HorizonDays     *int64 `json:"horizon_days,omitempty"`
	UnfinishedRuns  *int64 `json:"unfinished_runs,omitempty"`
	// TargetItems (CHAOS-12634) is the item target the run forecast: the
	// scope's own target, or its backlog, as ops' worker falls back to it.
	// Absent when the stored row has none, never 0 for none.
	TargetItems *int64 `json:"target_items,omitempty"`
	P50Days     *int64 `json:"forecast_p50_days,omitempty"`
	P85Days     *int64 `json:"forecast_p85_days,omitempty"`
	P95Days     *int64 `json:"forecast_p95_days,omitempty"`
	// P50Date, P85Date and P95Date are RFC 3339 full-dates, "2026-11-20":
	// the stored dates, which name a UTC day.
	P50Date string `json:"forecast_p50_date,omitempty"`
	P85Date string `json:"forecast_p85_date,omitempty"`
	P95Date string `json:"forecast_p95_date,omitempty"`
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

// The forms of the summary's instant and dates (lead ruling on CHAOS-12608),
// stated in every published schema as the same patterns.
const (
	ContextFabricForecastComputedAtPattern = `^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?Z$`
	ContextFabricForecastDatePattern       = `^[0-9]{4}-[0-9]{2}-[0-9]{2}$`
)

var (
	forecastComputedAtForm = regexp.MustCompile(ContextFabricForecastComputedAtPattern)
	forecastDateForm       = regexp.MustCompile(ContextFabricForecastDatePattern)
)

// ValidContextFabricForecastComputedAt reports an RFC 3339 UTC instant in the
// summary's form: the pattern, and a real time.
func ValidContextFabricForecastComputedAt(value string) bool {
	if !forecastComputedAtForm.MatchString(value) {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

// ValidContextFabricForecastDate reports an RFC 3339 full-date: the pattern,
// and a real day.
func ValidContextFabricForecastDate(value string) bool {
	if !forecastDateForm.MatchString(value) {
		return false
	}
	_, err := time.Parse(time.DateOnly, value)
	return err == nil
}

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
	if !stringLengthBetween(f.ComputedAt, 1, ContextFabricClaimedFieldMaxLength) || !ValidContextFabricForecastComputedAt(f.ComputedAt) {
		return fmt.Errorf("completion_forecast computed_at is not an RFC 3339 UTC instant")
	}
	if len(f.WorkScopeID) > ContextFabricClaimedFieldMaxLength || strings.TrimSpace(f.WorkScopeID) != f.WorkScopeID {
		return fmt.Errorf("completion_forecast text violates v1 bounds")
	}
	for _, date := range []string{f.P50Date, f.P85Date, f.P95Date} {
		if date != "" && !ValidContextFabricForecastDate(date) {
			return fmt.Errorf("completion_forecast date is not an RFC 3339 full-date")
		}
	}
	for _, value := range []*int64{f.SimulationCount, f.HorizonDays, f.UnfinishedRuns, f.TargetItems, f.P50Days, f.P85Days, f.P95Days} {
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
