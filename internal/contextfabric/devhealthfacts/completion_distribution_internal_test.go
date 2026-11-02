package devhealthfacts

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-go/readers"
)

// CHAOS-12607: the completion forecast fields of one team workload fact.

func forecastRow(values []uint16, counts []uint32) readers.CompletionForecastRow {
	return readers.CompletionForecastRow{
		TeamID: "CHAOS", HasWorkScope: 1, WorkScopeID: "scope-a", ComputedAt: "2026-11-02 04:00:12.672",
		SimulationCount: 10000, BacklogSize: 4,
		HasP50Days: 1, P50Days: 114, HasP85Days: 1, P85Days: 208, HasP95Days: 1, P95Days: 281,
		P50Date: "2027-02-24", P85Date: "2027-05-29", P95Date: "2027-08-11",
		CompletionDayBins: uint64(len(values)), CompletionCountBins: uint64(len(counts)),
		CompletionDaysValues: values, CompletionDaysCounts: counts,
	}
}

func stateOf(t *testing.T, fields map[string]contextfabric.FactValue) string {
	t.Helper()
	state, ok := fields["completion_distribution_state"]
	if !ok || state.String == nil {
		t.Fatalf("completion_distribution_state absent: %v", fields)
	}
	return *state.String
}

func TestACompletionDistributionIsServedWithOpsShares(t *testing.T) {
	fields := completionForecastFields(forecastRow([]uint16{5, 12, 365}, []uint32{100, 8000, 1900}))
	if got := stateOf(t, fields); got != completionDistributionAvailable {
		t.Fatalf("state = %q", got)
	}
	table := fields["completion_distribution"].Table
	if table == nil || table.Shape != contextfabric.FactTableDistribution || len(table.Rows) != 3 {
		t.Fatalf("table = %+v", table)
	}
	if err := fields["completion_distribution"].Validate(); err != nil {
		t.Fatalf("table Validate() = %v", err)
	}
	// The horizon bin is served with its runs and adds nothing to the share.
	wantShares := []float64{100.0 / 10000, 8100.0 / 10000, 8100.0 / 10000}
	for index, row := range table.Rows {
		if got := *row.Fields["cumulative_share"].Number; got != wantShares[index] {
			t.Errorf("row %d share = %v, want %v", index, got, wantShares[index])
		}
	}
	if got := *fields["unfinished_runs"].Integer; got != 1900 {
		t.Errorf("unfinished_runs = %d, want 1900", got)
	}
	if got := *fields["horizon_days"].Integer; got != forecastHorizonDays {
		t.Errorf("horizon_days = %d", got)
	}
	for _, name := range []string{"forecast_p85_days", "forecast_p95_days", "forecast_p50_date", "forecast_p85_date", "forecast_p95_date", "simulation_count"} {
		if _, ok := fields[name]; !ok {
			t.Errorf("%s absent", name)
		}
	}
}

func TestEveryCompletionDistributionStateWithholdsWhatItCannotServe(t *testing.T) {
	over := make([]uint16, contextfabric.MaxFactDistributionRows+1)
	overCounts := make([]uint32, contextfabric.MaxFactDistributionRows+1)
	for index := range over {
		over[index], overCounts[index] = uint16(index), 1
	}
	overRow := forecastRow(over, overCounts)
	overRow.CompletionDayBins, overRow.CompletionCountBins = 400, 400

	cases := []struct {
		name      string
		row       readers.CompletionForecastRow
		want      string
		unfinshed int64
	}{
		{"no arrays (before migration 101)", forecastRow(nil, nil), completionDistributionNotStored, -1},
		{"values without counts", forecastRow([]uint16{5}, nil), completionDistributionNotStored, -1},
		{"every run at the horizon", forecastRow([]uint16{365}, []uint32{10000}), completionDistributionNoRunWithinHorizon, 10000},
		{"past the table bound", overRow, completionDistributionOverLimit, -1},
		{"days not ascending", forecastRow([]uint16{12, 5}, []uint32{1, 1}), completionDistributionMalformed, -1},
		{"a repeated day", forecastRow([]uint16{5, 5}, []uint32{1, 1}), completionDistributionMalformed, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := completionForecastFields(tc.row)
			if got := stateOf(t, fields); got != tc.want {
				t.Fatalf("state = %q, want %q", got, tc.want)
			}
			if _, ok := fields["completion_distribution"]; ok {
				t.Fatal("a table was served: this state serves none (a 0% line, a cut curve, or rows out of order)")
			}
			unfinished, ok := fields["unfinished_runs"]
			if tc.unfinshed < 0 && ok {
				t.Fatalf("unfinished_runs = %v, want absent", unfinished)
			}
			if tc.unfinshed >= 0 && (!ok || *unfinished.Integer != tc.unfinshed) {
				t.Fatalf("unfinished_runs = %v, want %d", unfinished, tc.unfinshed)
			}
			if _, ok := fields["forecast_p95_days"]; !ok {
				t.Fatal("the percentiles must stay whatever the distribution state")
			}
		})
	}
}

func TestANullPercentileOrDateIsAbsent(t *testing.T) {
	row := forecastRow(nil, nil)
	row.HasP85Days, row.P85Days, row.HasP95Days, row.P95Days = 0, 0, 0, 0
	row.P85Date, row.P95Date = "", ""
	fields := completionForecastFields(row)
	for _, name := range []string{"forecast_p85_days", "forecast_p95_days", "forecast_p85_date", "forecast_p95_date"} {
		if value, ok := fields[name]; ok {
			t.Errorf("%s = %+v, want absent: no percentile is not a percentile of 0", name, value)
		}
	}
	if _, ok := fields["forecast_p50_date"]; !ok {
		t.Error("forecast_p50_date absent, want present")
	}
}

func TestAForecastJoinsOnlyItsOwnStoredRow(t *testing.T) {
	row := forecastRow([]uint16{5}, []uint32{10000})
	cases := []struct {
		name       string
		candidates []readers.CompletionForecastRow
		computedAt string
		want       string
	}{
		{"the same row", []readers.CompletionForecastRow{row}, row.ComputedAt, completionDistributionAvailable},
		{"a newer forecast landed between the reads", []readers.CompletionForecastRow{row}, "2026-11-01 04:00:00.000", completionDistributionUnmatched},
		{"no forecast row", nil, row.ComputedAt, completionDistributionUnmatched},
		{"a NULL and an empty scope", []readers.CompletionForecastRow{row, row}, row.ComputedAt, completionDistributionScopeAmbiguous},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := teamCompletionForecastFields(tc.candidates, tc.computedAt)
			if got := stateOf(t, fields); got != tc.want {
				t.Fatalf("state = %q, want %q", got, tc.want)
			}
			if tc.want != completionDistributionAvailable && len(fields) != 1 {
				t.Fatalf("fields = %v, want only the state: no other forecast's detail", fields)
			}
		})
	}
	nullScope, emptyScope := row, row
	nullScope.HasWorkScope, nullScope.WorkScopeID = 0, ""
	emptyScope.WorkScopeID = ""
	byScope := completionForecastsByScope([]readers.CompletionForecastRow{nullScope, emptyScope})
	if got := len(byScope[completionForecastScope{team: "CHAOS", scope: ""}]); got != 2 {
		t.Fatalf("a NULL and an empty scope share %d candidates, want 2 (the workload read folds them)", got)
	}
}
