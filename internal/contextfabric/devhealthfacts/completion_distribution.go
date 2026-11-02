package devhealthfacts

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-go/readers"
)

// CHAOS-12607: the stored Monte Carlo completion distribution of one
// forecast, served on the team workload fact.
//
// The cumulative share is ops' read-time rule, COPIED, not re-derived: ops
// internal/queryapi/capacityforecast/distribution.go (binsToModel and
// cumulativeShare) computes the share its /plan/capacity page draws, and an
// Ask Dev chart of the same forecast must draw the same curve. A copy can
// drift, so the copy is held to the original by a differential oracle that
// EXECUTES the ops functions at a pinned ops commit on the same inputs
// (ci/forecastoracle, CHAOS-12607); a test here cannot stand in for it.

// forecastHorizonDays is ops numerical.ForecastHorizonDays: a run that needs
// this many days or more is recorded at this value and is NOT done.
const forecastHorizonDays = 365

// Completion distribution states, a closed vocabulary served as
// completion_distribution_state. Every team workload fact carries one, so
// "no distribution" is said, never inferred from an absent table.
const (
	// completionDistributionAvailable: the table is served.
	completionDistributionAvailable = "available"
	// completionDistributionNotStored: the row holds no distribution (written
	// before ops migration 101, or the run produced none). Absent is not a
	// distribution of zeros.
	completionDistributionNotStored = "not_stored"
	// completionDistributionNoRunWithinHorizon: every run reached the
	// horizon (bins below it hold no run). No table is served, because it
	// would draw a flat 0% line; unfinished_runs says how many runs there
	// were.
	completionDistributionNoRunWithinHorizon = "no_run_within_horizon"
	// completionDistributionOverLimit: more distinct days than a
	// distribution table carries. No table is served; a shorter curve would
	// be a different forecast. The percentiles stay.
	completionDistributionOverLimit = "over_limit"
	// completionDistributionScopeAmbiguous: the team holds a NULL scope and
	// an empty scope, which the workload read cannot tell apart, so neither
	// fact is given the other's forecast.
	completionDistributionScopeAmbiguous = "scope_ambiguous"
	// completionDistributionUnmatched: the forecast read held no row for this
	// fact's scope and computed_at (a newer forecast landed between the two
	// reads). Nothing is attached rather than a different forecast's detail.
	completionDistributionUnmatched = "unmatched"
	// completionDistributionMalformed: the stored days are not strictly
	// ascending, or every count is zero. No producer writes either (ops
	// builds the histogram sorted and distinct, from its runs); the table is
	// withheld rather than served out of order or as a curve of no runs, and
	// the fact keeps its percentiles.
	completionDistributionMalformed = "malformed"
)

// completionBin is one served bin: ops model.CapacityDistributionBin.
type completionBin struct {
	Value           int
	Count           int
	CumulativeShare float64
}

// completionBins is ops binsToModel for the days mode, line for line: nil
// for an empty histogram; values and counts of different lengths cut to the
// shorter; the share is the running count of the bins BELOW the horizon over
// the total of every served bin; a bin at or above the horizon adds to
// unfinished and nothing to the share. ops' histogramFromUint16 makes an
// empty pair (either array empty) nil, which is the first branch here.
func completionBins(values []uint16, counts []uint32, horizon int) (bins []completionBin, unfinished int) {
	if len(values) == 0 || len(counts) == 0 {
		return nil, 0
	}
	length := min(len(values), len(counts))
	total := 0
	for index := 0; index < length; index++ {
		total += int(counts[index])
	}
	bins = make([]completionBin, 0, length)
	finished := 0
	for index := 0; index < length; index++ {
		value, count := int(values[index]), int(counts[index])
		if horizon > 0 && value >= horizon {
			unfinished += count
		} else {
			finished += count
		}
		bins = append(bins, completionBin{Value: value, Count: count, CumulativeShare: completionShare(finished, total)})
	}
	return bins, unfinished
}

// completionShare is ops cumulativeShare: 0, not NaN, for a total of 0.
func completionShare(running, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(running) / float64(total)
}

// addCompletionForecastFields writes the forecast fields one team workload
// fact gains from its own forecast row (CHAOS-12607). A NULL percentile or
// date is absent, never 0 or "". Every key is written by name, so the
// emission reachability walk sees each one. distribution is the deployment
// switch: off, nothing about the distribution is written -- no table, no
// state, no horizon -- so nothing the fact says depends on a table it does
// not carry.
func addCompletionForecastFields(fields map[string]contextfabric.FactValue, row readers.CompletionForecastRow, distribution bool) {
	fields["simulation_count"] = contextfabric.IntegerFactValue(int64(row.SimulationCount))
	if row.HasP85Days != 0 {
		fields["forecast_p85_days"] = contextfabric.IntegerFactValue(row.P85Days)
	}
	if row.HasP95Days != 0 {
		fields["forecast_p95_days"] = contextfabric.IntegerFactValue(row.P95Days)
	}
	if row.P50Date != "" {
		fields["forecast_p50_date"] = contextfabric.StringFactValue(row.P50Date)
	}
	if row.P85Date != "" {
		fields["forecast_p85_date"] = contextfabric.StringFactValue(row.P85Date)
	}
	if row.P95Date != "" {
		fields["forecast_p95_date"] = contextfabric.StringFactValue(row.P95Date)
	}
	if !distribution {
		return
	}
	state := completionDistributionState(row)
	fields["completion_distribution_state"] = contextfabric.StringFactValue(state)
	switch state {
	case completionDistributionAvailable, completionDistributionNoRunWithinHorizon:
		bins, unfinished := completionBins(row.CompletionDaysValues, row.CompletionDaysCounts, forecastHorizonDays)
		fields["horizon_days"] = contextfabric.IntegerFactValue(forecastHorizonDays)
		fields["unfinished_runs"] = contextfabric.IntegerFactValue(int64(unfinished))
		if state == completionDistributionAvailable {
			fields["completion_distribution"] = completionDistributionTable(bins)
		}
	}
}

// completionDistributionState decides the state from the STORED lengths the
// reader reports, before any share is computed: a stored array past the
// table's bound reaches here cut to one entry past it, and shares computed
// over a cut array would be wrong, not merely short.
func completionDistributionState(row readers.CompletionForecastRow) string {
	stored := min(row.CompletionDayBins, row.CompletionCountBins)
	if stored == 0 {
		return completionDistributionNotStored
	}
	if stored > uint64(contextfabric.MaxFactDistributionRows) {
		return completionDistributionOverLimit
	}
	bins, unfinished := completionBins(row.CompletionDaysValues, row.CompletionDaysCounts, forecastHorizonDays)
	for index := 1; index < len(bins); index++ {
		if bins[index].Value <= bins[index-1].Value {
			return completionDistributionMalformed
		}
	}
	total := 0
	for _, bin := range bins {
		total += bin.Count
	}
	switch {
	case total == 0:
		// No run at all: every count zero, which no producer writes.
		return completionDistributionMalformed
	case total == unfinished:
		// Every run is at the horizon. A bin below it that no run reached
		// is not a run that finished (Codex #1940 r1), and its table would
		// be a flat 0% line.
		return completionDistributionNoRunWithinHorizon
	}
	return completionDistributionAvailable
}

// completionDistributionTable is the served `distribution` table: one row
// per bin, keyed by completion_days, ascending (ops writes the values ascending).
func completionDistributionTable(bins []completionBin) contextfabric.FactValue {
	rows := make([]contextfabric.FactValueRow, 0, len(bins))
	for _, bin := range bins {
		rows = append(rows, contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
			"completion_days":  contextfabric.IntegerFactValue(int64(bin.Value)),
			"runs":             contextfabric.IntegerFactValue(int64(bin.Count)),
			"cumulative_share": contextfabric.NumberFactValue(bin.CumulativeShare),
		}})
	}
	return contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:    contextfabric.FactTableDistribution,
		Key:      []string{"completion_days"},
		Measures: []string{"runs", "cumulative_share"},
		Rows:     rows,
	})
}

// completionForecastScope is the join key the workload read can express: it
// folds a NULL scope into "", so a NULL and an empty scope share one key.
type completionForecastScope struct{ team, scope string }

func completionForecastsByScope(rows []readers.CompletionForecastRow) map[completionForecastScope][]readers.CompletionForecastRow {
	out := make(map[completionForecastScope][]readers.CompletionForecastRow, len(rows))
	for _, row := range rows {
		key := completionForecastScope{team: row.TeamID, scope: row.WorkScopeID}
		out[key] = append(out[key], row)
	}
	return out
}

// addTeamCompletionForecastFields attaches a forecast to the workload fact
// only when exactly one forecast row has the fact's scope and the same
// computed_at, which makes it the same stored row. Two candidates are a NULL
// and an empty scope; none, or a different computed_at, is a newer forecast
// that landed between the two reads. Either way the fact gets no other
// forecast's detail, and, with the distribution switched on, says why.
func addTeamCompletionForecastFields(fields map[string]contextfabric.FactValue, candidates []readers.CompletionForecastRow, computedAt string, distribution bool) {
	switch {
	case len(candidates) == 1 && candidates[0].ComputedAt == computedAt:
		addCompletionForecastFields(fields, candidates[0], distribution)
	case !distribution:
	case len(candidates) > 1:
		fields["completion_distribution_state"] = contextfabric.StringFactValue(completionDistributionScopeAmbiguous)
	default:
		fields["completion_distribution_state"] = contextfabric.StringFactValue(completionDistributionUnmatched)
	}
}

// completionDistributionFieldNames are the fields the deployment switch
// gates: everything about the distribution, none of the row's scalars.
var completionDistributionFieldNames = []string{"completion_distribution", "completion_distribution_state", "horizon_days", "unfinished_runs"}
