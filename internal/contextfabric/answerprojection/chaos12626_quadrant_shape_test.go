package answerprojection

import (
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12626: the quadrant crosses into the projection with the claims it plots
// and its served line, or it is dropped, counted, and its line goes with it.

// canonicalWithQuadrant places two teams. No driver cites a claim: the quadrant is
// their citation.
func canonicalWithQuadrant(t *testing.T) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	xWord, yWord := comparisonWord(t, "churn_loc_30d"), comparisonWord(t, "delivery_units_30d")
	x := contractsv1.ContextFabricRenderSeries{Key: "churn_loc_30d", Label: xWord}
	y := contractsv1.ContextFabricRenderSeries{Key: "delivery_units_30d", Label: yWord}
	table := &contractsv1.ContextFabricClaimedFactTable{
		Field: "area_breakdown", Shape: contractsv1.ContextFabricFactTableShapeBreakdown, Key: []string{"as_of_day"},
		Measures: []string{"identity_count", "churn_loc_30d", "delivery_units_30d"},
	}
	result := richResult()
	result.ClaimedFacts = nil
	result.Drivers = nil
	for index, name := range []string{"platform", "payments"} {
		team := subject(contractsv1.ContextFabricSubjectTeam, "team:"+name, name)
		day, identities, churn, units := "2026-11-02", int64(3), int64(900+index), int64(12+index)
		rows := []contractsv1.ContextFabricClaimedFactRow{{Fields: map[string]contractsv1.ContextFabricScalarValue{
			"as_of_day": {String: &day}, "identity_count": {Integer: &identities},
			"churn_loc_30d": {Integer: &churn}, "delivery_units_30d": {Integer: &units},
		}}}
		for _, measure := range []struct {
			field  string
			value  *int64
			series *contractsv1.ContextFabricRenderSeries
		}{{"churn_loc_30d", &churn, &x}, {"delivery_units_30d", &units, &y}} {
			claim := "claim_quadrant_" + name + "_" + measure.field
			result.ClaimedFacts = append(result.ClaimedFacts, contractsv1.ContextFabricClaimedFact{
				ClaimID: claim, Kind: contractsv1.ContextFabricFactLandscape, Subject: team, Field: measure.field,
				Value: contractsv1.ContextFabricScalarValue{Integer: measure.value}, Rows: rows, Table: table,
			})
			measure.series.Points = append(measure.series.Points, contractsv1.ContextFabricRenderPoint{
				Label: name, Value: float64(*measure.value),
				Source: contractsv1.ContextFabricRenderPointSource{Kind: contractsv1.ContextFabricRenderSourceClaimedFactValue, ClaimID: claim, Field: measure.field},
			})
		}
	}
	result.RenderShapes = []contractsv1.ContextFabricRenderShape{{
		ShapeID: "rs_quadrant", Kind: contractsv1.ContextFabricRenderKindQuadrant, SelectedBy: contractsv1.ContextFabricRenderRuleLandscapePosition,
		Title: "Churn and delivery units, 30 days to Nov 2, 2026", AxisKind: contractsv1.ContextFabricRenderAxisCategory,
		AxisLabel: xWord, ValueLabel: yWord, Series: []contractsv1.ContextFabricRenderSeries{x, y},
	}}
	result.Limitations = append(result.Limitations, contractsv1.ContextFabricLandscapePositionLimitation)
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the quadrant fixture must be a valid canonical shape: %v", err)
	}
	return result
}

func TestProjectionCarriesAQuadrantItsClaimsAndItsLine(t *testing.T) {
	t.Parallel()
	projection := Project(canonicalWithQuadrant(t), Budget{})
	if len(projection.RenderShapes) != 1 || len(projection.KeyFacts) != 4 || projection.ProjectionBudget.RenderShapesOmitted != 0 {
		t.Fatalf("shapes = %d, key facts = %d, omitted = %d; want the quadrant and its 4 claims", len(projection.RenderShapes), len(projection.KeyFacts), projection.ProjectionBudget.RenderShapesOmitted)
	}
	if !slices.Contains(projection.Limitations, contractsv1.ContextFabricLandscapePositionLimitation) {
		t.Fatalf("the quadrant's line is missing: %v", projection.Limitations)
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("the projection must validate: %v", err)
	}
}

// The budget keeps one team's claims. The quadrant cannot be checked, so it is
// dropped and counted, and its line, which speaks of it, goes too.
func TestProjectionDropsAQuadrantWithItsLine(t *testing.T) {
	t.Parallel()
	projection := Project(canonicalWithQuadrant(t), Budget{MaxFacts: 2})
	if len(projection.KeyFacts) != 2 {
		t.Fatalf("key facts = %d, want one team's 2 for this plant to mean anything", len(projection.KeyFacts))
	}
	if len(projection.RenderShapes) != 0 || projection.ProjectionBudget.RenderShapesOmitted != 1 {
		t.Fatalf("shapes = %d, omitted = %d; want the quadrant dropped and counted", len(projection.RenderShapes), projection.ProjectionBudget.RenderShapesOmitted)
	}
	if slices.Contains(projection.Limitations, contractsv1.ContextFabricLandscapePositionLimitation) {
		t.Fatalf("the line stayed with no quadrant: %v", projection.Limitations)
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("the projection must stay valid after the drop: %v", err)
	}
}
