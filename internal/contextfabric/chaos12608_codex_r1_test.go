package contextfabric

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Codex #1949 r1: two claims of one stored forecast are one forecast; a day
// count past 2^53 is no point; the deployment switch gates stored forecast
// shapes and summaries at the serving doors.

func percentilesOf(shapes []contractsv1.ContextFabricRenderShape) []contractsv1.ContextFabricRenderShape {
	var out []contractsv1.ContextFabricRenderShape
	for _, shape := range shapes {
		if shape.SelectedBy == contractsv1.ContextFabricRenderRuleCompletionForecastPercentiles {
			out = append(out, shape)
		}
	}
	return out
}

// secondClaimOf is another claim of the same fact: same team, same summary.
func secondClaimOf(claim ClaimedFact, claimID string) ClaimedFact {
	second := claim
	second.ClaimID = claimID
	p85 := int64(30)
	second.Field, second.Value = "forecast_p85_days", ScalarValue{Integer: &p85}
	return second
}

func TestTwoClaimsOfOneForecastDrawOneCurve(t *testing.T) {
	rows := forecastDistributionRows([]int64{5, 12, 365}, []float64{0.2, 0.8, 0.8})
	first := forecastClaim(forecastTestClaim, rows)
	result := forecastResult(first, secondClaimOf(first, "claim_forecast_chaos_p85"))
	shapes, event := selectRenderShapes(result, forecastFrame(ObligationCompletion), forecastOn)
	if reason := forecastSkipOf(t, event); reason != "" {
		t.Fatalf("skipped: %s; two claims of one forecast are one scope", reason)
	}
	curves := forecastsOf(shapes)
	if len(curves) != 1 || curves[0].Series[0].Points[0].Source.ClaimID != forecastTestClaim {
		t.Fatalf("curves = %+v, want one, drawn from the first claim", curves)
	}

	other := secondClaimOf(first, "claim_forecast_other_scope")
	summary := *other.CompletionForecast
	summary.WorkScopeID = "scope-b"
	other.CompletionForecast = &summary
	shapes, event = selectRenderShapes(forecastResult(first, other), forecastFrame(ObligationCompletion), forecastOn)
	if reason := forecastSkipOf(t, event); reason != RenderShapeSkipMoreThanOneScope || len(forecastsOf(shapes)) != 0 {
		t.Fatalf("two scopes: skip = %q, curves = %d; want no curve", reason, len(forecastsOf(shapes)))
	}
}

func TestTwoClaimsOfOneForecastGetOneRow(t *testing.T) {
	first := forecastClaim(forecastTestClaim, nil)
	first.Rows, first.Table, first.TimeSeriesRows, first.TimeSeriesTable = nil, nil, nil, nil
	other := secondClaimOf(first, "claim_forecast_other_scope")
	summary := *other.CompletionForecast
	summary.WorkScopeID = "scope-b"
	other.CompletionForecast = &summary
	result := forecastResult(first, secondClaimOf(first, "claim_forecast_chaos_p85"), other)
	shapes, _ := selectRenderShapes(result, forecastFrame(ObligationCompletion), forecastOn)
	tables := percentilesOf(shapes)
	if len(tables) != 1 {
		t.Fatalf("percentile tables = %d, want 1", len(tables))
	}
	var labels []string
	for _, series := range tables[0].Series {
		labels = append(labels, series.Label)
	}
	if strings.Join(labels, ",") != "scope-a,scope-b" {
		t.Fatalf("series = %v, want one row per stored forecast", labels)
	}

	// The contract holds it too: two rows of one forecast are refused.
	duplicate := tables[0]
	duplicate.ShapeID = "rs_1"
	repeat := duplicate.Series[0]
	repeat.Key = "claim_forecast_chaos_p85"
	repeat.Points = append([]contractsv1.ContextFabricRenderPoint(nil), repeat.Points...)
	for index := range repeat.Points {
		repeat.Points[index].Source.ClaimID = "claim_forecast_chaos_p85"
	}
	duplicate.Series = append([]contractsv1.ContextFabricRenderSeries{repeat}, duplicate.Series...)
	result.RenderShapes = []contractsv1.ContextFabricRenderShape{duplicate}
	if err := contractsv1.ValidateRenderShapesForResult(result); err == nil || !strings.Contains(err.Error(), "has two series") {
		t.Fatalf("two rows of one forecast: Validate = %v", err)
	}
}

func TestADayCountPastExactIntegersIsNoPoint(t *testing.T) {
	claim := forecastClaim(forecastTestClaim, nil)
	claim.Rows, claim.Table, claim.TimeSeriesRows, claim.TimeSeriesTable = nil, nil, nil, nil
	huge := contractsv1.ContextFabricRenderPointExactIntegerBound + 1
	claim.CompletionForecast.P50Days = &huge
	result := forecastResult(claim)
	shapes, _ := selectRenderShapes(result, forecastFrame(ObligationCompletion), forecastOn)
	tables := percentilesOf(shapes)
	if len(tables) != 1 {
		t.Fatalf("percentile tables = %d, want 1", len(tables))
	}
	for _, point := range tables[0].Series[0].Points {
		if point.Source.Field == contractsv1.ContextFabricForecastSummaryP50Days {
			t.Fatalf("a point for %d days: %+v", huge, point)
		}
	}

	// The contract refuses a hand-made point that rounds onto the summary.
	shape := tables[0]
	shape.ShapeID = "rs_1"
	word, _ := contractsv1.ContextFabricForecastPercentileLabel(contractsv1.ContextFabricForecastSummaryP50Days)
	shape.Series = []contractsv1.ContextFabricRenderSeries{{Key: shape.Series[0].Key, Label: shape.Series[0].Label, Points: []contractsv1.ContextFabricRenderPoint{{
		Label: word, Value: float64(huge),
		Source: contractsv1.ContextFabricRenderPointSource{Kind: contractsv1.ContextFabricRenderSourceClaimedFactForecastSummary, ClaimID: forecastTestClaim, Field: contractsv1.ContextFabricForecastSummaryP50Days},
	}}}}
	result.RenderShapes = []contractsv1.ContextFabricRenderShape{shape}
	if err := contractsv1.ValidateRenderShapesForResult(result); err == nil || !strings.Contains(err.Error(), "too large to plot") {
		t.Fatalf("a point for %d days: Validate = %v", huge, err)
	}
}

func TestTheServingDoorsGateStoredForecasts(t *testing.T) {
	rows := forecastDistributionRows([]int64{5, 12, 365}, []float64{0.2, 0.8, 0.8})
	result := forecastResult(forecastClaim(forecastTestClaim, rows))
	shapes, _ := selectRenderShapes(result, forecastFrame(ObligationCompletion), forecastOn)
	if len(forecastsOf(shapes)) != 1 {
		t.Fatal("no curve to gate: this measured nothing")
	}
	result.RenderShapes = shapes

	// Every other switch on, the forecast's off: the shortcut for "all on"
	// must not keep it.
	off := WithoutDisabledRenderShapes(result, RenderShapeOptions{InvestmentThemeShare: true, ComparisonSideBySide: true})
	if len(forecastsOf(off.RenderShapes)) != 0 {
		t.Fatalf("switch off: %d curves served", len(forecastsOf(off.RenderShapes)))
	}
	if off.ClaimedFacts[0].CompletionForecast != nil {
		t.Fatal("switch off: the stored summary is served")
	}
	if result.ClaimedFacts[0].CompletionForecast == nil {
		t.Fatal("the stored row was written: the doors work on a copy")
	}

	on := WithoutDisabledRenderShapes(result, RenderShapeOptions{InvestmentThemeShare: true, ComparisonSideBySide: true, CompletionForecast: true})
	if len(forecastsOf(on.RenderShapes)) != 1 || on.ClaimedFacts[0].CompletionForecast == nil {
		t.Fatalf("switch on: curves = %d, summary = %v", len(forecastsOf(on.RenderShapes)), on.ClaimedFacts[0].CompletionForecast)
	}
}
