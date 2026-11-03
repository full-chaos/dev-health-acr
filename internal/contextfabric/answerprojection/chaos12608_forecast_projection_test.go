package answerprojection

import (
	"strconv"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12608 (Codex #1949 r1): a completion curve and a forecast percentiles
// table cross into the projection with the claim they plot and its forecast
// summary. The projection's own resolver knew neither source kind, so both
// shapes were dropped and counted, and no driver brought their claim.

const forecastProjectionClaim = "claim_forecast_projection"

func canonicalWithForecast(t *testing.T, rule contractsv1.ContextFabricRenderShapeRule) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	team := subject(contractsv1.ContextFabricSubjectTeam, "team:chaos", "CHAOS")
	days, shares := []int64{5, 12, 365}, []float64{0.2, 0.8, 0.8}
	rows := make([]contractsv1.ContextFabricClaimedFactRow, 0, len(days))
	for index := range days {
		day, share, runs := days[index], shares[index], int64(10)
		rows = append(rows, contractsv1.ContextFabricClaimedFactRow{Fields: map[string]contractsv1.ContextFabricScalarValue{
			contractsv1.ContextFabricRenderForecastAxisField: {Integer: &day}, "runs": {Integer: &runs},
			contractsv1.ContextFabricRenderForecastShareField: {Number: &share},
		}})
	}
	p50, p85, horizon := int64(12), int64(30), int64(365)
	claim := contractsv1.ContextFabricClaimedFact{
		ClaimID: forecastProjectionClaim, Kind: contractsv1.ContextFabricFactWorkload, Subject: team,
		Field: contractsv1.ContextFabricForecastSummaryP50Days, Value: contractsv1.ContextFabricScalarValue{Integer: &p50},
		Rows: rows,
		Table: &contractsv1.ContextFabricClaimedFactTable{
			Field: contractsv1.ContextFabricRenderForecastTableField, Shape: contractsv1.ContextFabricFactTableShapeDistribution,
			Key: []string{contractsv1.ContextFabricRenderForecastAxisField}, Measures: []string{"runs", contractsv1.ContextFabricRenderForecastShareField},
		},
		CompletionForecast: &contractsv1.ContextFabricClaimedCompletionForecast{
			WorkScopeID: "scope-a", State: contractsv1.ContextFabricCompletionDistributionAvailable, ComputedAt: "2026-11-02T04:00:00Z",
			HorizonDays: &horizon, P50Days: &p50, P85Days: &p85,
		},
	}
	shape := contractsv1.ContextFabricRenderShape{ShapeID: "rs_forecast", SelectedBy: rule, AxisKind: contractsv1.ContextFabricRenderAxisCategory}
	switch rule {
	case contractsv1.ContextFabricRenderRuleCompletionForecast:
		points := make([]contractsv1.ContextFabricRenderPoint, 0, len(days))
		for index := range days {
			row := index
			points = append(points, contractsv1.ContextFabricRenderPoint{
				Label: strconv.FormatInt(days[index], 10), Value: shares[index],
				Source: contractsv1.ContextFabricRenderPointSource{
					Kind: contractsv1.ContextFabricRenderSourceClaimedFactTableRow, ClaimID: claim.ClaimID,
					RowIndex: &row, Field: contractsv1.ContextFabricRenderForecastShareField,
				},
			})
		}
		shape.Kind, shape.ValueUnit = contractsv1.ContextFabricRenderKindForecast, contractsv1.ContextFabricRenderValueUnitRatio
		shape.Title, shape.AxisLabel, shape.ValueLabel = "Completion forecast", "Days to complete", "Share of runs finished"
		shape.Series = []contractsv1.ContextFabricRenderSeries{{Key: contractsv1.ContextFabricRenderForecastShareField, Label: "Share of runs finished", Points: points}}
	case contractsv1.ContextFabricRenderRuleCompletionForecastPercentiles:
		var points []contractsv1.ContextFabricRenderPoint
		for field, value := range map[string]int64{contractsv1.ContextFabricForecastSummaryP50Days: p50, contractsv1.ContextFabricForecastSummaryP85Days: p85} {
			word, ok := contractsv1.ContextFabricForecastPercentileLabel(field)
			if !ok {
				t.Fatalf("field %q has no word", field)
			}
			points = append(points, contractsv1.ContextFabricRenderPoint{
				Label: word, Value: float64(value),
				Source: contractsv1.ContextFabricRenderPointSource{Kind: contractsv1.ContextFabricRenderSourceClaimedFactForecastSummary, ClaimID: claim.ClaimID, Field: field},
			})
		}
		shape.Kind = contractsv1.ContextFabricRenderKindTable
		shape.Title, shape.AxisLabel, shape.ValueLabel = "Completion forecast percentiles", "Percentile", "Days to complete"
		shape.Series = []contractsv1.ContextFabricRenderSeries{{Key: claim.ClaimID, Label: "scope-a", Points: points}}
	}
	result := richResult()
	result.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{claim}
	result.Drivers = nil
	result.RenderShapes = []contractsv1.ContextFabricRenderShape{shape}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the %s fixture must be a valid canonical shape: %v", rule, err)
	}
	return result
}

func TestProjectionCarriesAForecastShapeAndTheClaimItPlots(t *testing.T) {
	t.Parallel()
	for _, rule := range []contractsv1.ContextFabricRenderShapeRule{
		contractsv1.ContextFabricRenderRuleCompletionForecast,
		contractsv1.ContextFabricRenderRuleCompletionForecastPercentiles,
	} {
		t.Run(string(rule), func(t *testing.T) {
			t.Parallel()
			projection := Project(canonicalWithForecast(t, rule), Budget{})
			if len(projection.RenderShapes) != 1 || projection.ProjectionBudget.RenderShapesOmitted != 0 {
				t.Fatalf("shapes = %d, omitted = %d, want the one shape carried", len(projection.RenderShapes), projection.ProjectionBudget.RenderShapesOmitted)
			}
			if len(projection.KeyFacts) != 1 || projection.KeyFacts[0].ClaimID != forecastProjectionClaim {
				t.Fatalf("key facts = %+v, want the claim the shape plots", projection.KeyFacts)
			}
			fact := projection.KeyFacts[0]
			if fact.CompletionForecast == nil || len(fact.Rows) != 3 || fact.Table == nil {
				t.Fatalf("the projected claim lost its forecast: summary %+v, %d rows, table %+v", fact.CompletionForecast, len(fact.Rows), fact.Table)
			}
			if err := projection.Validate(); err != nil {
				t.Fatalf("the projection must validate: %v", err)
			}
		})
	}
}

// A shape whose summary number the projection cannot check is dropped whole
// and counted.
func TestProjectionDropsAPercentileItCannotCheck(t *testing.T) {
	t.Parallel()
	result := canonicalWithForecast(t, contractsv1.ContextFabricRenderRuleCompletionForecastPercentiles)
	result.RenderShapes[0].Series[0].Points[0].Value++
	projection := Project(result, Budget{})
	if len(projection.RenderShapes) != 0 || projection.ProjectionBudget.RenderShapesOmitted != 1 {
		t.Fatalf("shapes = %d, omitted = %d, want the shape dropped and counted", len(projection.RenderShapes), projection.ProjectionBudget.RenderShapesOmitted)
	}
}
