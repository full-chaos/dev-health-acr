package contextfabric

import (
	"strconv"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The words of the completion forecast (CHAOS-12608). They are served, never
// derived from a model, and agreed with the seam that draws them.
const (
	completionForecastTitle       = "Completion forecast"
	completionForecastAxisLabel   = "Days to complete"
	completionForecastValueLabel  = "Share of runs finished"
	completionForecastSeriesLabel = "Share of runs finished"
)

// completionForecastShape is the completion_forecast rule (CHAOS-12608,
// CHAOS-4613 rulings 3 and 5): the Monte Carlo completion curve of ONE team
// forecast, drawn from the claimed team workload fact's completion
// distribution, every row once, in row order. It is positive-evidence only:
// this turn's own validated frame must ask for completion or remaining work,
// the plan must authorize the forecast kind, and exactly one claimed team
// workload fact must carry a distribution. A team with several scopes that
// carry one gets no curve, and the claims carry each scope's percentiles: the
// answer does not choose a scope for the reader. A distribution the producer
// did not serve (no run finished within the horizon, over its cap, malformed,
// not stored) is no claim table here, so it is never drawn as a flat line.
func completionForecastShape(result InvestigationResult, frame *QuestionFrame) (*contractsv1.ContextFabricRenderShape, RenderShapeSkipReason) {
	if result.Cohort != nil || cohortRenderIntent(frame) {
		return nil, RenderShapeSkipCohortAnswer
	}
	if frame == nil || !(frame.HasObligation(ObligationCompletion) || frame.HasObligation(ObligationRemainingWork)) {
		return nil, RenderShapeSkipNotForecastIntent
	}
	if !planAuthorizesRenderKind(result.AnswerPlan, contractsv1.ContextFabricRenderKindForecast) {
		return nil, RenderShapeSkipNotPlanAuthorized
	}
	var fact *ClaimedFact
	for index := range result.ClaimedFacts {
		candidate := &result.ClaimedFacts[index]
		if candidate.Kind != FactWorkload || candidate.Subject.Kind != contractsv1.ContextFabricSubjectTeam || !declaresCompletionDistribution(candidate) {
			continue
		}
		if fact != nil && fact.ClaimID != candidate.ClaimID {
			return nil, RenderShapeSkipMoreThanOneScope
		}
		fact = candidate
	}
	if fact == nil {
		return nil, RenderShapeSkipNoCompletionDistribution
	}
	if len(fact.Rows) > contractsv1.ContextFabricRenderPointsMax(contractsv1.ContextFabricRenderKindForecast) {
		return nil, RenderShapeSkipDistributionUnusable
	}
	points := make([]contractsv1.ContextFabricRenderPoint, 0, len(fact.Rows))
	for index, row := range fact.Rows {
		day := row.Fields[contractsv1.ContextFabricRenderForecastAxisField]
		share, numeric := renderNumericCell(row.Fields[contractsv1.ContextFabricRenderForecastShareField])
		if day.Integer == nil || !numeric || share < 0 || share > 1 {
			return nil, RenderShapeSkipDistributionUnusable
		}
		rowIndex := index
		points = append(points, contractsv1.ContextFabricRenderPoint{
			Label: strconv.FormatInt(*day.Integer, 10),
			Value: share,
			Source: contractsv1.ContextFabricRenderPointSource{
				Kind:     contractsv1.ContextFabricRenderSourceClaimedFactTableRow,
				ClaimID:  fact.ClaimID,
				RowIndex: &rowIndex,
				Field:    contractsv1.ContextFabricRenderForecastShareField,
			},
		})
	}
	shape := &contractsv1.ContextFabricRenderShape{
		Kind:       contractsv1.ContextFabricRenderKindForecast,
		SelectedBy: contractsv1.ContextFabricRenderRuleCompletionForecast,
		Title:      completionForecastTitle,
		AxisKind:   contractsv1.ContextFabricRenderAxisCategory,
		AxisLabel:  completionForecastAxisLabel,
		ValueLabel: completionForecastValueLabel,
		ValueUnit:  contractsv1.ContextFabricRenderValueUnitRatio,
		Series: []contractsv1.ContextFabricRenderSeries{{
			Key:    contractsv1.ContextFabricRenderForecastShareField,
			Label:  completionForecastSeriesLabel,
			Points: points,
		}},
	}
	// A chart never costs an answer: the shape is proved against the contract
	// here, as the treemap's is.
	proved := *shape
	proved.ShapeID = "rs_proof"
	probe := result
	probe.RenderShapes = []contractsv1.ContextFabricRenderShape{proved}
	if err := contractsv1.ValidateRenderShapesForResult(probe); err != nil {
		return nil, RenderShapeSkipDistributionUnusable
	}
	return shape, ""
}

// declaresCompletionDistribution is the reading the contract's validator holds
// a forecast to: the claim's rows are its declared completion distribution.
func declaresCompletionDistribution(fact *ClaimedFact) bool {
	table := fact.Table
	if table == nil || len(fact.Rows) == 0 || table.Field != contractsv1.ContextFabricRenderForecastTableField || table.Shape != contractsv1.ContextFabricFactTableShapeDistribution {
		return false
	}
	return len(table.Key) == 1 && table.Key[0] == contractsv1.ContextFabricRenderForecastAxisField && table.HasMeasure(contractsv1.ContextFabricRenderForecastShareField)
}
