package contextfabric

import (
	"slices"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// RenderShapeOptions are the deployment's switches for selection. The zero
// value is what SelectRenderShapes applies.
type RenderShapeOptions struct {
	// InvestmentThemeShare turns on the investment_theme_share rule
	// (CHAOS-12521). See Config.ContextFabricInvestmentTreemapEnabled.
	InvestmentThemeShare bool
}

// The words of the treemap (CHAOS-12521). They are served, never derived from a
// model, and agreed with the seam that draws them.
const (
	themeShareTitle       = "Effort share by theme"
	themeShareAxisLabel   = "Theme"
	themeShareValueLabel  = "Share of effort (ratio)"
	themeShareSeriesLabel = "Share of effort"
)

// investmentThemeShareShape is the part of a whole of one subject's investment.
//
// It reads the plan and the claimed facts only: the served family says the
// question is about where effort went, and the fact's own declaration says which
// rows are the parts. There is no geometry to infer, and no fallback: a question
// this rule does not fire for gets no treemap (check 10).
//
// Exactly one fact, on a repository or a team. A second fact is a comparison, and
// an organization frame does not reach this family. The subject check is an allow
// list, so a person never gets this chart: the vocabulary has no such kind today,
// and a kind added later stays out until someone decides otherwise. Every row is a
// point, so a measured zero stays a part of the whole.
func investmentThemeShareShape(result InvestigationResult, frame *QuestionFrame) (*contractsv1.ContextFabricRenderShape, RenderShapeSkipReason) {
	if result.AnswerPlan == nil || result.AnswerPlan.Family != QuestionFamilyInvestmentAllocation {
		return nil, RenderShapeSkipNotInvestmentAllocation
	}
	// A carried family reaches here on a turn that classified nothing, so the
	// family does not by itself say this question is about one subject's split.
	if result.Cohort != nil || cohortRenderIntent(frame) || !familyTakesShape(result.AnswerPlan.Family, result.Interpretation.Shape) {
		return nil, RenderShapeSkipCohortAnswer
	}
	if !planAuthorizesRenderKind(result.AnswerPlan, contractsv1.ContextFabricRenderKindTreemap) {
		return nil, RenderShapeSkipNotPlanAuthorized
	}
	var fact *ClaimedFact
	for index := range result.ClaimedFacts {
		candidate := &result.ClaimedFacts[index]
		if candidate.Kind != FactInvestment || !declaresThemeShareTable(candidate) {
			continue
		}
		if fact != nil {
			return nil, RenderShapeSkipSeveralThemeBreakdowns
		}
		fact = candidate
	}
	if fact == nil {
		return nil, RenderShapeSkipNoThemeBreakdown
	}
	switch fact.Subject.Kind {
	case contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectTeam:
	default:
		return nil, RenderShapeSkipSubjectKindNotCharted
	}
	points := make([]contractsv1.ContextFabricRenderPoint, 0, len(fact.Rows))
	above := 0
	for index, row := range fact.Rows {
		theme, _ := renderStringCell(row.Fields[contractsv1.ContextFabricRenderThemeField])
		word, known := contractsv1.ContextFabricInvestmentThemeLabel(theme)
		if !known {
			return nil, RenderShapeSkipThemeRowUnusable
		}
		share, numeric := renderNumericCell(row.Fields[contractsv1.ContextFabricRenderThemeShareField])
		if !numeric || share < 0 || share > 1 {
			return nil, RenderShapeSkipThemeRowUnusable
		}
		if share > 0 {
			above++
		}
		rowIndex := index
		points = append(points, contractsv1.ContextFabricRenderPoint{
			Label: word,
			Value: share,
			Source: contractsv1.ContextFabricRenderPointSource{
				Kind:     contractsv1.ContextFabricRenderSourceClaimedFactRow,
				ClaimID:  fact.ClaimID,
				RowIndex: &rowIndex,
				Field:    contractsv1.ContextFabricRenderThemeShareField,
			},
		})
	}
	if above < 2 {
		return nil, RenderShapeSkipFewerThanTwoShares
	}
	return &contractsv1.ContextFabricRenderShape{
		Kind:       contractsv1.ContextFabricRenderKindTreemap,
		SelectedBy: contractsv1.ContextFabricRenderRuleInvestmentThemeShare,
		Title:      themeShareTitle,
		AxisKind:   contractsv1.ContextFabricRenderAxisCategory,
		AxisLabel:  themeShareAxisLabel,
		ValueLabel: themeShareValueLabel,
		ValueUnit:  contractsv1.ContextFabricRenderValueUnitRatio,
		Series: []contractsv1.ContextFabricRenderSeries{{
			Key:    contractsv1.ContextFabricRenderThemeShareField,
			Label:  themeShareSeriesLabel,
			Points: points,
		}},
	}, ""
}

// declaresThemeShareTable is the same reading the contract's validator holds a
// treemap to: the fact's rows are its declared theme breakdown, and no time
// series sits in front of them.
// familyTakesShape reads the registry row's structural gate.
func familyTakesShape(family QuestionFamily, shape InvestigationShape) bool {
	definition, found := LookupQuestionFamily(family)
	return found && slices.Contains(definition.CompatibleShapes, shape)
}

// WithoutDisabledRenderShapes removes a stored shape the deployment has turned
// off. A flag that only gated selection would leave every answer stored while it
// was on still serving the shape, on a by-id read and on a reuse hit, to the very
// consumer the flag protects. The stored row is not touched.
func WithoutDisabledRenderShapes(result InvestigationResult, options RenderShapeOptions) InvestigationResult {
	if options.InvestmentThemeShare {
		return result
	}
	var kept []contractsv1.ContextFabricRenderShape
	for _, shape := range result.RenderShapes {
		if shape.SelectedBy != contractsv1.ContextFabricRenderRuleInvestmentThemeShare {
			kept = append(kept, shape)
		}
	}
	result.RenderShapes = kept
	return result
}

func declaresThemeShareTable(fact *ClaimedFact) bool {
	table := fact.Table
	return table != nil && len(fact.TimeSeriesRows) == 0 &&
		table.Field == contractsv1.ContextFabricRenderThemeTableField &&
		table.Shape == contractsv1.ContextFabricFactTableShapeBreakdown &&
		table.HasMeasure(contractsv1.ContextFabricRenderThemeShareField)
}
