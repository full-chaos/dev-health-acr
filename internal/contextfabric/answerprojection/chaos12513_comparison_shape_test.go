package answerprojection

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12513: a side-by-side comparison crosses into the projection whole, or it
// is dropped and counted. A table with one side cut is a different claim.

const (
	comparisonCountField  = "window_pr_count"
	comparisonMedianField = "window_pr_cycle_hours_median"
)

func comparisonWord(t *testing.T, field string) string {
	t.Helper()
	word, ok := contractsv1.ContextFabricVocabularyLabel(contractsv1.ContextFabricVocabularyFactField, field)
	if !ok {
		t.Fatalf("field %q has no word", field)
	}
	return word
}

func comparisonValuePoint(t *testing.T, claim contractsv1.ContextFabricClaimedFact, value float64) contractsv1.ContextFabricRenderPoint {
	t.Helper()
	return contractsv1.ContextFabricRenderPoint{
		Label: comparisonWord(t, claim.Field), Value: value,
		Source: contractsv1.ContextFabricRenderPointSource{
			Kind: contractsv1.ContextFabricRenderSourceClaimedFactValue, ClaimID: claim.ClaimID, Field: claim.Field,
		},
	}
}

// canonicalWithComparison gives each side's claims their own driver, because a
// claim reaches the key facts through a driver that cites it.
func canonicalWithComparison(t *testing.T) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	api := subject(contractsv1.ContextFabricSubjectRepository, "repository:acme-api", "acme-api")
	web := subject(contractsv1.ContextFabricSubjectRepository, "repository:acme-web", "acme-web")
	apiCount, webCount := int64(48), int64(82)
	apiMedian, webMedian := 1.95, 1.66
	claims := []contractsv1.ContextFabricClaimedFact{
		{ClaimID: "claim_api_count", Kind: "metrics", Subject: api, Field: comparisonCountField, Value: contractsv1.ContextFabricScalarValue{Integer: &apiCount}},
		{ClaimID: "claim_api_median", Kind: "metrics", Subject: api, Field: comparisonMedianField, Value: contractsv1.ContextFabricScalarValue{Number: &apiMedian}},
		{ClaimID: "claim_web_count", Kind: "metrics", Subject: web, Field: comparisonCountField, Value: contractsv1.ContextFabricScalarValue{Integer: &webCount}},
		{ClaimID: "claim_web_median", Kind: "metrics", Subject: web, Field: comparisonMedianField, Value: contractsv1.ContextFabricScalarValue{Number: &webMedian}},
	}
	result := richResult()
	result.ClaimedFacts = claims
	result.Drivers = []contractsv1.ContextFabricDriverJudgment{
		driver("driver_api", contractsv1.ContextFabricDriverPrincipal, "work", "acme-api", []string{"claim_api_count", "claim_api_median"}, api),
		driver("driver_web", contractsv1.ContextFabricDriverPrincipal, "work", "acme-web", []string{"claim_web_count", "claim_web_median"}, web),
	}
	result.RenderShapes = []contractsv1.ContextFabricRenderShape{{
		ShapeID: "rs_comparison", Kind: contractsv1.ContextFabricRenderKindTable,
		SelectedBy: contractsv1.ContextFabricRenderRuleComparisonSideBySide,
		Title:      "Side by side", AxisKind: contractsv1.ContextFabricRenderAxisCategory,
		AxisLabel: "Measure", ValueLabel: "Value",
		Series: []contractsv1.ContextFabricRenderSeries{
			{Key: api.CanonicalID, Label: api.Label, Points: []contractsv1.ContextFabricRenderPoint{
				comparisonValuePoint(t, claims[0], 48), comparisonValuePoint(t, claims[1], 1.95),
			}},
			{Key: web.CanonicalID, Label: web.Label, Points: []contractsv1.ContextFabricRenderPoint{
				comparisonValuePoint(t, claims[2], 82), comparisonValuePoint(t, claims[3], 1.66),
			}},
		},
	}}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the comparison fixture must be a valid canonical shape: %v", err)
	}
	return result
}

func TestProjectionCarriesAComparisonItsKeyFactsHold(t *testing.T) {
	t.Parallel()
	projection := Project(canonicalWithComparison(t), Budget{})
	if len(projection.RenderShapes) != 1 || projection.ProjectionBudget.RenderShapesOmitted != 0 {
		t.Fatalf("shapes = %d, omitted = %d, want the one shape carried", len(projection.RenderShapes), projection.ProjectionBudget.RenderShapesOmitted)
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("the projection must validate: %v", err)
	}
}

// The budget keeps one side's claims and cuts the other's. Every cell of the kept
// side still resolves, so only a whole-shape drop keeps the table honest.
func TestProjectionDropsAComparisonWithOneSideCut(t *testing.T) {
	t.Parallel()
	projection := Project(canonicalWithComparison(t), Budget{MaxFacts: 2})
	if len(projection.KeyFacts) != 2 {
		t.Fatalf("key facts = %d, want one side's 2 for this plant to mean anything", len(projection.KeyFacts))
	}
	if len(projection.RenderShapes) != 0 {
		t.Fatalf("a comparison with one side cut survived: %+v", projection.RenderShapes)
	}
	if projection.ProjectionBudget.RenderShapesOmitted != 1 {
		t.Fatalf("render_shapes_omitted = %d, want 1", projection.ProjectionBudget.RenderShapesOmitted)
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("the projection must stay valid after the drop: %v", err)
	}
}

// Each plant is a cell a reader of the projection could not check.
func TestProjectionDropsAComparisonCellItCannotCheck(t *testing.T) {
	t.Parallel()
	big, negative := contractsv1.ContextFabricRenderPointExactIntegerBound+1, -contractsv1.ContextFabricRenderPointExactIntegerBound-1
	for name, plant := range map[string]func(*contractsv1.ContextFabricInvestigationResult){
		"a number the claim does not state": func(r *contractsv1.ContextFabricInvestigationResult) {
			r.RenderShapes[0].Series[0].Points[1].Value = 2
		},
		"an integer the claim does not state": func(r *contractsv1.ContextFabricInvestigationResult) {
			r.RenderShapes[0].Series[0].Points[0].Value = 49
		},
		"a field the claim does not hold": func(r *contractsv1.ContextFabricInvestigationResult) {
			r.RenderShapes[0].Series[0].Points[0].Source.Field = comparisonMedianField
		},
		"an integer past the exact bound": func(r *contractsv1.ContextFabricInvestigationResult) {
			r.ClaimedFacts[0].Value = contractsv1.ContextFabricScalarValue{Integer: &big}
			r.RenderShapes[0].Series[0].Points[0].Value = float64(big)
		},
		"a negative integer past the exact bound": func(r *contractsv1.ContextFabricInvestigationResult) {
			r.ClaimedFacts[0].Value = contractsv1.ContextFabricScalarValue{Integer: &negative}
			r.RenderShapes[0].Series[0].Points[0].Value = float64(negative)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := canonicalWithComparison(t)
			plant(&result)
			projection := Project(result, Budget{})
			if len(projection.RenderShapes) != 0 || projection.ProjectionBudget.RenderShapesOmitted != 1 {
				t.Fatalf("shapes = %d, omitted = %d, want the shape dropped and counted", len(projection.RenderShapes), projection.ProjectionBudget.RenderShapesOmitted)
			}
		})
	}
}
