package contextfabric

import (
	"math"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Which rows a cut keeps (team-lead ruling 2026-09-24): a dated series keeps
// its newest days whichever way it is listed, a ranking its highest order_by
// values, anything else its source-order prefix. Kept rows stay in the
// table's own order.
func TestCHAOS6558KeepFactTableRowsCutsFromTheRightEnd(t *testing.T) {
	t.Parallel()
	day := func(value string, count int64) contractsv1.ContextFabricClaimedFactRow {
		return contractsv1.ContextFabricClaimedFactRow{Fields: map[string]contractsv1.ContextFabricScalarValue{
			"day": {String: &value}, "count": {Integer: &count},
		}}
	}
	numberDay := func(value string, number float64) contractsv1.ContextFabricClaimedFactRow {
		return contractsv1.ContextFabricClaimedFactRow{Fields: map[string]contractsv1.ContextFabricScalarValue{
			"day": {String: &value}, "count": {Number: &number},
		}}
	}
	days := func(rows []contractsv1.ContextFabricClaimedFactRow) []string {
		out := []string{}
		for _, row := range rows {
			out = append(out, *row.Fields["day"].String)
		}
		return out
	}
	series := &contractsv1.ContextFabricClaimedFactTable{Field: "f", Shape: contractsv1.ContextFabricFactTableShapeTimeSeries, Key: []string{"day"}, Measures: []string{"count"}}
	ranking := &contractsv1.ContextFabricClaimedFactTable{Field: "f", Shape: contractsv1.ContextFabricFactTableShapeRanking, Key: []string{"day"}, Measures: []string{"count"}, OrderBy: "count"}
	breakdown := &contractsv1.ContextFabricClaimedFactTable{Field: "f", Shape: contractsv1.ContextFabricFactTableShapeBreakdown, Key: []string{"day"}, Measures: []string{"count"}}
	ascending := []contractsv1.ContextFabricClaimedFactRow{day("2026-09-01", 5), day("2026-09-02", 9), day("2026-09-03", 1), day("2026-09-04", 7)}
	descending := []contractsv1.ContextFabricClaimedFactRow{day("2026-09-04", 7), day("2026-09-03", 1), day("2026-09-02", 9), day("2026-09-01", 5)}
	rfc := []contractsv1.ContextFabricClaimedFactRow{day("2026-09-03T00:00:00Z", 1), day("2026-09-01T00:00:00Z", 5), day("2026-09-04T00:00:00Z", 7), day("2026-09-02T00:00:00Z", 9)}
	undated := []contractsv1.ContextFabricClaimedFactRow{day("2026-09-01", 5), day("not-a-day", 9), day("2026-09-03", 1), day("2026-09-04", 7)}
	for _, testCase := range []struct {
		name  string
		rows  []contractsv1.ContextFabricClaimedFactRow
		table *contractsv1.ContextFabricClaimedFactTable
		want  []string
		cap   int
	}{
		{"series listed oldest-first keeps its newest days", ascending, series, []string{"2026-09-03", "2026-09-04"}, 0},
		{"series listed newest-first keeps its newest days", descending, series, []string{"2026-09-04", "2026-09-03"}, 0},
		{"series in no order keeps its newest days in table order", rfc, series, []string{"2026-09-03T00:00:00Z", "2026-09-04T00:00:00Z"}, 0},
		{"series separates instants one nanosecond apart", []contractsv1.ContextFabricClaimedFactRow{day("2026-09-01T00:00:00.000000001Z", 1), day("2026-09-01T00:00:00.000000002Z", 2), day("2026-08-01T00:00:00Z", 3)}, series, []string{"2026-09-01T00:00:00.000000002Z"}, 1},
		{"ranking keeps its highest order_by values", ascending, ranking, []string{"2026-09-02", "2026-09-04"}, 0},
		{"ranking separates int64 ranks float64 cannot", []contractsv1.ContextFabricClaimedFactRow{day("2026-09-01", 9007199254740992), day("2026-09-02", 9007199254740993), day("2026-09-03", 1)}, ranking, []string{"2026-09-02"}, 1},
		{"ranking mixes integer and number exactly", []contractsv1.ContextFabricClaimedFactRow{numberDay("2026-09-01", 9007199254740992), day("2026-09-02", 9007199254740993), day("2026-09-03", 1)}, ranking, []string{"2026-09-02"}, 1},
		{"ranking with a non-finite number falls back to the prefix", []contractsv1.ContextFabricClaimedFactRow{day("2026-09-01", 1), numberDay("2026-09-02", math.Inf(1)), day("2026-09-03", 9)}, ranking, []string{"2026-09-01", "2026-09-02"}, 0},
		{"breakdown keeps its source-order prefix", ascending, breakdown, []string{"2026-09-01", "2026-09-02"}, 0},
		{"undeclared keeps its source-order prefix", ascending, nil, []string{"2026-09-01", "2026-09-02"}, 0},
		{"a series row that is not dated falls back to the prefix", undated, series, []string{"2026-09-01", "not-a-day"}, 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			before := days(testCase.rows)
			perTable := testCase.cap
			if perTable == 0 {
				perTable = 2
			}
			kept, _ := keepFactTableRows(testCase.rows, testCase.table, perTable)
			got := days(kept)
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("kept %v, want %v", got, testCase.want)
			}
			if !reflect.DeepEqual(days(testCase.rows), before) {
				t.Fatalf("input rows were reordered: %v -> %v", before, days(testCase.rows))
			}
		})
	}
}
