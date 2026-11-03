package answerprojection

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12582: no driver cites the team's repository_theme_breakdown claim, so
// without the sankey among the charts that cite their claim the projection left
// the rows behind and dropped the sankey. The answer projection carries the
// shape (the ticket's scope).
func TestAProjectionKeepsTheClaimItsSankeyPlots(t *testing.T) {
	t.Parallel()
	type link struct {
		repository, word, theme string
		effort, total           float64
	}
	links := []link{
		{"repository:aaa", "acme/api", "feature_delivery", 6, 7.5},
		{"repository:bbb", "acme/web", "feature_delivery", 1.5, 7.5},
		{"repository:bbb", "acme/web", "quality", 2, 2},
	}
	const claim = "claim_repository_flow_projection"
	result := richResultWithCoverageDetails()
	result.Drivers = nil
	fact := contractsv1.ContextFabricClaimedFact{
		ClaimID: claim, Kind: contractsv1.ContextFabricFactInvestment, Field: contractsv1.ContextFabricRenderRepositoryFlowTableField,
		// A claim names a detail table with a null value (CHAOS-12587).
		Value:   contractsv1.ContextFabricScalarValue{Null: true},
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:platform", Label: "Platform"},
		Table: &contractsv1.ContextFabricClaimedFactTable{
			Field: contractsv1.ContextFabricRenderRepositoryFlowTableField, Shape: contractsv1.ContextFabricFactTableShapeBreakdown,
			Key: []string{"repository", "theme"}, Measures: []string{"weighted_effort", "theme_weighted_effort"}, Observations: []string{"repository_label"},
		},
	}
	series := map[string]*contractsv1.ContextFabricRenderSeries{
		"feature_delivery": {Key: "feature_delivery", Label: "Feature Delivery"},
		"quality":          {Key: "quality", Label: "Quality / Reliability"},
	}
	for index, l := range links {
		repository, word, theme, effort, total := l.repository, l.word, l.theme, l.effort, l.total
		fact.Rows = append(fact.Rows, contractsv1.ContextFabricClaimedFactRow{Fields: map[string]contractsv1.ContextFabricScalarValue{
			"repository": {String: &repository}, "repository_label": {String: &word}, "theme": {String: &theme},
			"weighted_effort": {Number: &effort}, "theme_weighted_effort": {Number: &total},
		}})
		row := index
		series[theme].Points = append(series[theme].Points, contractsv1.ContextFabricRenderPoint{Label: word, Value: effort, Source: contractsv1.ContextFabricRenderPointSource{
			Kind: contractsv1.ContextFabricRenderSourceClaimedFactRow, ClaimID: claim, RowIndex: &row, Field: "weighted_effort",
		}})
	}
	result.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{fact}
	result.RenderShapes = []contractsv1.ContextFabricRenderShape{{
		ShapeID: "rs_1", Kind: contractsv1.ContextFabricRenderKindSankey, SelectedBy: contractsv1.ContextFabricRenderRuleInvestmentRepositoryFlow,
		Title: "Where the team's effort went, by repository", AxisKind: contractsv1.ContextFabricRenderAxisCategory, AxisLabel: "Repository",
		ValueLabel: "Weighted effort",
		Series:     []contractsv1.ContextFabricRenderSeries{*series["feature_delivery"], *series["quality"]},
	}}
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the fixture's sankey does not validate: %v", err)
	}
	projection := Project(result, Budget{})
	if len(projection.RenderShapes) != 1 || projection.RenderShapes[0].Kind != contractsv1.ContextFabricRenderKindSankey {
		t.Fatalf("projected shapes %+v, want the sankey", projection.RenderShapes)
	}
	if len(projection.KeyFacts) != 1 || projection.KeyFacts[0].ClaimID != claim || len(projection.KeyFacts[0].Rows) != len(links) {
		t.Fatalf("projected facts %+v, want the sankey's claim with its rows", projection.KeyFacts)
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("the projection does not validate: %v", err)
	}
}
