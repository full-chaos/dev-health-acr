package answerprojection

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12521: an investment answer need not have a driver, and no driver cites
// its fact, so the projection left the rows behind and dropped the chart.
func TestAProjectionKeepsTheClaimItsTreemapPlots(t *testing.T) {
	t.Parallel()
	share := func(value float64) *float64 { return &value }
	themes := []string{"feature_delivery", "operational"}
	result := richResultWithCoverageDetails()
	result.Drivers = nil
	result.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{{
		ClaimID: "claim_theme_share_projection", Kind: contractsv1.ContextFabricFactInvestment, Field: "attribution_basis",
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repo:one", Label: "one"},
		Table: &contractsv1.ContextFabricClaimedFactTable{
			Field: "theme_breakdown", Shape: contractsv1.ContextFabricFactTableShapeBreakdown, Key: []string{"theme"}, Measures: []string{"share"},
		},
		Rows: []contractsv1.ContextFabricClaimedFactRow{
			{Fields: map[string]contractsv1.ContextFabricScalarValue{"theme": {String: &themes[0]}, "share": {Number: share(0.5)}}},
			{Fields: map[string]contractsv1.ContextFabricScalarValue{"theme": {String: &themes[1]}, "share": {Number: share(0.5)}}},
		},
	}}
	point := func(index int, label string) contractsv1.ContextFabricRenderPoint {
		return contractsv1.ContextFabricRenderPoint{Label: label, Value: 0.5, Source: contractsv1.ContextFabricRenderPointSource{
			Kind: contractsv1.ContextFabricRenderSourceClaimedFactRow, ClaimID: "claim_theme_share_projection", RowIndex: &index, Field: "share",
		}}
	}
	result.RenderShapes = []contractsv1.ContextFabricRenderShape{{
		ShapeID: "rs_1", Kind: contractsv1.ContextFabricRenderKindTreemap, SelectedBy: contractsv1.ContextFabricRenderRuleInvestmentThemeShare,
		Title: "Effort share by theme", AxisKind: contractsv1.ContextFabricRenderAxisCategory, AxisLabel: "Theme", ValueLabel: "Share of effort (ratio)",
		ValueUnit: contractsv1.ContextFabricRenderValueUnitRatio,
		Series:    []contractsv1.ContextFabricRenderSeries{{Key: "share", Label: "Share of effort", Points: []contractsv1.ContextFabricRenderPoint{point(0, "Feature Delivery"), point(1, "Operational / Support")}}},
	}}
	projection := Project(result, Budget{})
	if len(projection.RenderShapes) != 1 || projection.RenderShapes[0].Kind != contractsv1.ContextFabricRenderKindTreemap {
		t.Fatalf("projected shapes %+v", projection.RenderShapes)
	}
	// Carrying the claim is for the treemap alone. An uncited claim stays out
	// when no chart plots it, and when another kind of chart does.
	series := result.RenderShapes[0]
	series.Kind, series.Presentation, series.SelectedBy = contractsv1.ContextFabricRenderKindSeries, contractsv1.ContextFabricRenderPresentationBars, contractsv1.ContextFabricRenderRuleDatedFactTrend
	result.RenderShapes = []contractsv1.ContextFabricRenderShape{series}
	if plain := Project(result, Budget{}); len(plain.KeyFacts) != 0 {
		t.Fatalf("an uncited claim was carried for a series: %+v", plain.KeyFacts)
	}
	result.RenderShapes = nil
	if plain := Project(result, Budget{}); len(plain.KeyFacts) != 0 {
		t.Fatalf("an uncited claim was carried: %+v", plain.KeyFacts)
	}
}
