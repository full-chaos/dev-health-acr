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
	themes := []string{"feature_delivery", "operational", "maintenance", "quality", "risk"}
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
			{Fields: map[string]contractsv1.ContextFabricScalarValue{"theme": {String: &themes[2]}, "share": {Number: share(0)}}},
			{Fields: map[string]contractsv1.ContextFabricScalarValue{"theme": {String: &themes[3]}, "share": {Number: share(0)}}},
			{Fields: map[string]contractsv1.ContextFabricScalarValue{"theme": {String: &themes[4]}, "share": {Number: share(0)}}},
		},
	}}
	point := func(index int, label string, value float64) contractsv1.ContextFabricRenderPoint {
		return contractsv1.ContextFabricRenderPoint{Label: label, Value: value, Source: contractsv1.ContextFabricRenderPointSource{
			Kind: contractsv1.ContextFabricRenderSourceClaimedFactRow, ClaimID: "claim_theme_share_projection", RowIndex: &index, Field: "share",
		}}
	}
	result.RenderShapes = []contractsv1.ContextFabricRenderShape{{
		ShapeID: "rs_1", Kind: contractsv1.ContextFabricRenderKindTreemap, SelectedBy: contractsv1.ContextFabricRenderRuleInvestmentThemeShare,
		Title: "Effort share by theme", AxisKind: contractsv1.ContextFabricRenderAxisCategory, AxisLabel: "Theme", ValueLabel: "Share of effort (ratio)",
		ValueUnit: contractsv1.ContextFabricRenderValueUnitRatio,
		Series:    []contractsv1.ContextFabricRenderSeries{{Key: "share", Label: "Share of effort", Points: []contractsv1.ContextFabricRenderPoint{point(0, "Feature Delivery", 0.5), point(1, "Operational / Support", 0.5), point(2, "Maintenance / Tech Debt", 0), point(3, "Quality / Reliability", 0), point(4, "Risk / Security", 0)}}},
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

// CHAOS-12591: several claims on one fact each carry its rows, and the treemap
// cites the first. The projection keeps that chart and still validates.
func TestAProjectionKeepsATreemapOfOneOfSeveralClaimsOnAFact(t *testing.T) {
	t.Parallel()
	share := func(value float64) *float64 { return &value }
	themes := []string{"feature_delivery", "operational", "maintenance", "quality", "risk"}
	shares := []float64{0.5, 0.5, 0, 0, 0}
	result := richResultWithCoverageDetails()
	result.Drivers = nil
	result.ClaimedFacts = nil
	basis, units := "pr_ref_share", int64(3)
	for _, claim := range []struct {
		id, field string
		value     contractsv1.ContextFabricScalarValue
	}{
		{"claim_theme_share_projection", "attribution_basis", contractsv1.ContextFabricScalarValue{String: &basis}},
		{"claim_theme_share_delivery", "theme_feature_delivery", contractsv1.ContextFabricScalarValue{Number: share(0.5)}},
		{"claim_theme_share_units", "work_unit_count", contractsv1.ContextFabricScalarValue{Integer: &units}},
	} {
		rows := make([]contractsv1.ContextFabricClaimedFactRow, len(themes))
		for index := range themes {
			rows[index] = contractsv1.ContextFabricClaimedFactRow{Fields: map[string]contractsv1.ContextFabricScalarValue{"theme": {String: &themes[index]}, "share": {Number: share(shares[index])}}}
		}
		result.ClaimedFacts = append(result.ClaimedFacts, contractsv1.ContextFabricClaimedFact{
			ClaimID: claim.id, Kind: contractsv1.ContextFabricFactInvestment, Field: claim.field, Value: claim.value, FactID: "fact_projection_one",
			Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repo:one", Label: "one"},
			Table: &contractsv1.ContextFabricClaimedFactTable{
				Field: "theme_breakdown", Shape: contractsv1.ContextFabricFactTableShapeBreakdown, Key: []string{"theme"}, Measures: []string{"share"},
			},
			Rows: rows,
		})
	}
	labels := []string{"Feature Delivery", "Operational / Support", "Maintenance / Tech Debt", "Quality / Reliability", "Risk / Security"}
	points := make([]contractsv1.ContextFabricRenderPoint, len(labels))
	for index := range labels {
		rowIndex := index
		points[index] = contractsv1.ContextFabricRenderPoint{Label: labels[index], Value: shares[index], Source: contractsv1.ContextFabricRenderPointSource{
			Kind: contractsv1.ContextFabricRenderSourceClaimedFactRow, ClaimID: "claim_theme_share_projection", RowIndex: &rowIndex, Field: "share",
		}}
	}
	result.RenderShapes = []contractsv1.ContextFabricRenderShape{{
		ShapeID: "rs_1", Kind: contractsv1.ContextFabricRenderKindTreemap, SelectedBy: contractsv1.ContextFabricRenderRuleInvestmentThemeShare,
		Title: "Effort share by theme", AxisKind: contractsv1.ContextFabricRenderAxisCategory, AxisLabel: "Theme", ValueLabel: "Share of effort (ratio)",
		ValueUnit: contractsv1.ContextFabricRenderValueUnitRatio,
		Series:    []contractsv1.ContextFabricRenderSeries{{Key: "share", Label: "Share of effort", Points: points}},
	}}
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the result refuses its treemap: %v", err)
	}
	projection := Project(result, Budget{})
	if len(projection.RenderShapes) != 1 || projection.RenderShapes[0].Kind != contractsv1.ContextFabricRenderKindTreemap {
		t.Fatalf("projected shapes %+v", projection.RenderShapes)
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("the projection does not validate: %v", err)
	}
}
