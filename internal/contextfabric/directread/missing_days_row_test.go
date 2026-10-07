package directread

import (
	"math"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func serveDaily(t *testing.T, days int, cell func(i int) map[string]contextfabric.FactValue) ServedTable {
	t.Helper()
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, days)
	var rows []contextfabric.FactValueRow
	for i := 0; i < days; i++ {
		fields := cell(i)
		fields["day"] = contextfabric.StringFactValue(start.AddDate(0, 0, i).Format("2006-01-02"))
		rows = append(rows, contextfabric.FactValueRow{Fields: fields})
	}
	value := contextfabric.TableFactValue(contextfabric.FactTable{
		Shape: contextfabric.FactTableTimeSeries, Key: []string{"day"},
		Measures: []string{"compounding_risk"}, Observations: []string{"severity"},
		Grain: contextfabric.GrainDay, Rows: rows,
	})
	decl := contextfabric.FactFieldDeclaration{Name: "daily_health", Columns: []contextfabric.FactColumnDeclaration{
		{Name: "day", Type: contextfabric.FactFieldString},
		{Name: "severity", Type: contextfabric.FactFieldString, Nullable: true},
		{Name: "compounding_risk", Type: contextfabric.FactFieldNumber, Nullable: true},
	}}
	plan := readPlan{time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}}
	table := serveTable("daily_health", decl, value, GatedFact{}, plan, nil)
	if table.ExpectedPoints == nil || table.ReturnedPoints == nil {
		t.Fatal("points not set")
	}
	return table
}

func TestServeTableNullValuedDayIsMissingNotReturned(t *testing.T) {
	nullDays := map[int]bool{3: true, 9: true, 15: true, 21: true}
	table := serveDaily(t, 31, func(i int) map[string]contextfabric.FactValue {
		if nullDays[i] {
			return map[string]contextfabric.FactValue{"compounding_risk": contextfabric.NullFactValue()}
		}
		return map[string]contextfabric.FactValue{"compounding_risk": contextfabric.NumberFactValue(0.4)}
	})
	want := []string{"2026-03-04", "2026-03-10", "2026-03-16", "2026-03-22"}
	if *table.ReturnedPoints != 27 || *table.ExpectedPoints != 31 || len(table.MissingInstants) != 4 {
		t.Fatalf("got %d/%d missing=%v", *table.ReturnedPoints, *table.ExpectedPoints, table.MissingInstants)
	}
	for i, d := range want {
		if table.MissingInstants[i] != d {
			t.Fatalf("missing[%d]=%s want %s", i, table.MissingInstants[i], d)
		}
	}
}

func TestServeTableSeverityOnlyDayIsMissingButRowStays(t *testing.T) {
	table := serveDaily(t, 3, func(i int) map[string]contextfabric.FactValue {
		if i == 1 {
			return map[string]contextfabric.FactValue{"severity": contextfabric.StringFactValue("high")}
		}
		return map[string]contextfabric.FactValue{"compounding_risk": contextfabric.NumberFactValue(0.2)}
	})
	if *table.ReturnedPoints != 2 || len(table.MissingInstants) != 1 || table.MissingInstants[0] != "2026-03-02" {
		t.Fatalf("got %d missing=%v", *table.ReturnedPoints, table.MissingInstants)
	}
	if table.RowsReturned != 3 {
		t.Fatalf("observation row dropped: rows=%d", table.RowsReturned)
	}
}

func TestServeTableZeroValuedDayStaysReturned(t *testing.T) {
	table := serveDaily(t, 2, func(i int) map[string]contextfabric.FactValue {
		return map[string]contextfabric.FactValue{"compounding_risk": contextfabric.NumberFactValue(0)}
	})
	if *table.ReturnedPoints != 2 || len(table.MissingInstants) != 0 {
		t.Fatalf("got %d missing=%v", *table.ReturnedPoints, table.MissingInstants)
	}
}

func TestServeTableNonFiniteMeasureDayIsMissing(t *testing.T) {
	table := serveDaily(t, 3, func(i int) map[string]contextfabric.FactValue {
		if i == 2 {
			return map[string]contextfabric.FactValue{"compounding_risk": contextfabric.NumberFactValue(math.Inf(1))}
		}
		if i == 0 {
			return map[string]contextfabric.FactValue{"compounding_risk": contextfabric.NumberFactValue(math.NaN())}
		}
		return map[string]contextfabric.FactValue{"compounding_risk": contextfabric.NumberFactValue(1)}
	})
	if *table.ReturnedPoints != 1 || len(table.MissingInstants) != 2 {
		t.Fatalf("got %d missing=%v", *table.ReturnedPoints, table.MissingInstants)
	}
}

func TestRowCarriesMeasureWithoutDeclaredMeasuresKeepsRowPresence(t *testing.T) {
	row := contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{"day": contextfabric.StringFactValue("2026-03-01")}}
	if !rowCarriesMeasure(row, nil) {
		t.Fatal("no declared measures must keep row presence")
	}
}
