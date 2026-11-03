package answerprojection

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12554: an investment answer need not have a driver, so no driver cites
// the claim a sunburst plots. The chart is the citation, as for the treemap.
func TestAProjectionKeepsTheClaimItsSunburstPlots(t *testing.T) {
	t.Parallel()
	const claimID = "claim_subcategory_share_projection"
	shares := map[string]float64{"feature_delivery.customer": 0.5, "quality.bugfix": 0.3, "risk.security": 0.2}
	vocabulary := contractsv1.ContextFabricInvestmentSubcategoryVocabulary()
	totals := map[string]float64{}
	for _, subcategory := range vocabulary {
		theme, _ := contractsv1.ContextFabricInvestmentSubcategoryTheme(subcategory)
		totals[theme] += shares[subcategory]
	}
	claim := contractsv1.ContextFabricClaimedFact{
		ClaimID: claimID, Kind: contractsv1.ContextFabricFactInvestment, Field: "subcategory_breakdown", Value: contractsv1.ContextFabricScalarValue{Null: true},
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repo:one", Label: "one"},
		Table: &contractsv1.ContextFabricClaimedFactTable{Field: "subcategory_breakdown", Shape: contractsv1.ContextFabricFactTableShapeBreakdown,
			Key: []string{"subcategory"}, Measures: []string{"share", "theme_share"}, Observations: []string{"theme"}},
	}
	inner := contractsv1.ContextFabricRenderSeries{Key: "themes", Label: "Theme share, sum of subcategories"}
	rings := map[string]*contractsv1.ContextFabricRenderSeries{}
	var order []string
	cite := func(index int, field string) contractsv1.ContextFabricRenderPointSource {
		return contractsv1.ContextFabricRenderPointSource{Kind: contractsv1.ContextFabricRenderSourceClaimedFactRow, ClaimID: claimID, RowIndex: &index, Field: field}
	}
	for index, subcategory := range vocabulary {
		theme, _ := contractsv1.ContextFabricInvestmentSubcategoryTheme(subcategory)
		key, token, share, total := subcategory, theme, shares[subcategory], totals[theme]
		claim.Rows = append(claim.Rows, contractsv1.ContextFabricClaimedFactRow{Fields: map[string]contractsv1.ContextFabricScalarValue{
			"subcategory": {String: &key}, "theme": {String: &token}, "share": {Number: &share}, "theme_share": {Number: &total},
		}})
		word, _ := contractsv1.ContextFabricInvestmentThemeLabel(theme)
		ring, known := rings[theme]
		if !known {
			order = append(order, theme)
			ring = &contractsv1.ContextFabricRenderSeries{Key: theme, Label: word}
			rings[theme] = ring
			inner.Points = append(inner.Points, contractsv1.ContextFabricRenderPoint{Label: word, Value: total, Source: cite(index, "theme_share")})
		}
		label, _ := contractsv1.ContextFabricVocabularyLabel(contractsv1.ContextFabricVocabularySubcategory, subcategory)
		ring.Points = append(ring.Points, contractsv1.ContextFabricRenderPoint{Label: label, Value: share, Source: cite(index, "share")})
	}
	series := []contractsv1.ContextFabricRenderSeries{inner}
	for _, theme := range order {
		series = append(series, *rings[theme])
	}
	result := richResultWithCoverageDetails()
	result.Drivers = nil
	result.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{claim}
	result.RenderShapes = []contractsv1.ContextFabricRenderShape{{
		ShapeID: "rs_1", Kind: contractsv1.ContextFabricRenderKindSunburst, SelectedBy: contractsv1.ContextFabricRenderRuleInvestmentSubcategoryShare,
		Title: "Effort share by theme and subcategory", AxisKind: contractsv1.ContextFabricRenderAxisCategory, AxisLabel: "Subcategory",
		ValueLabel: "Share of effort (ratio)", ValueUnit: contractsv1.ContextFabricRenderValueUnitRatio, Series: series,
	}}
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the fixture is not a valid sunburst: %v", err)
	}
	projection := Project(result, Budget{})
	if len(projection.RenderShapes) != 1 || projection.RenderShapes[0].Kind != contractsv1.ContextFabricRenderKindSunburst {
		t.Fatalf("projected shapes %+v: the sunburst was dropped", projection.RenderShapes)
	}
	if len(projection.KeyFacts) != 1 || projection.KeyFacts[0].ClaimID != claimID {
		t.Fatalf("key facts %+v, want the claim the sunburst plots", projection.KeyFacts)
	}
}
