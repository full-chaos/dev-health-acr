package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The words of the sunburst (CHAOS-12554). They are served, never derived from
// a model. The point labels are the registry's words (subcategory:<token> and
// the theme words); these name the chart and its rings only. The inner ring
// states the one derivation it holds: each theme's share is the sum of its
// subcategories' shares.
const (
	subcategoryShareTitle          = "Effort share by theme and subcategory"
	subcategoryShareAxisLabel      = "Subcategory"
	subcategoryShareInnerRingLabel = "Theme share, sum of subcategories"
)

// investmentSubcategoryShareShape is the nested part of a whole of one
// subject's investment: an inner ring of themes and one ring per theme of its
// subcategories, all from one claim's subcategory_breakdown table.
//
// Positive evidence only, as for the treemap: this turn's own validated frame
// holds allocate_investment, and a claim names subcategory_breakdown, so it
// carries that declared detail table (attachDetailTable). A claim gets the
// table only when the question asked for that breakdown, so the claim is the
// intent. A carried family is never evidence (check 10).
//
// Exactly one breakdown, of a team or a repository: the subjects the ops
// breakdown entries serve. The whole table or no chart: a table the row budget
// cut lacks a subcategory, and a chart of the rest would present those shares
// as all of the effort. Every row is a point, so a measured zero stays a part.
func investmentSubcategoryShareShape(result InvestigationResult, frame *QuestionFrame) (*contractsv1.ContextFabricRenderShape, RenderShapeSkipReason) {
	if result.AnswerPlan == nil || result.AnswerPlan.Family != QuestionFamilyInvestmentAllocation {
		return nil, RenderShapeSkipNotInvestmentAllocation
	}
	if result.Cohort != nil || ((cohortRenderIntent(frame) || !familyTakesShape(result.AnswerPlan.Family, result.Interpretation.Shape)) && !scopeAllocationAnswer(frame)) {
		return nil, RenderShapeSkipCohortAnswer
	}
	if frame == nil || !frame.HasGoal(GoalAllocateInvestment) {
		return nil, RenderShapeSkipNotInvestmentAllocation
	}
	if !planAuthorizesRenderKind(result.AnswerPlan, contractsv1.ContextFabricRenderKindSunburst) {
		return nil, RenderShapeSkipNotPlanAuthorized
	}
	var fact *ClaimedFact
	for index := range result.ClaimedFacts {
		candidate := &result.ClaimedFacts[index]
		if candidate.Kind != FactInvestment || candidate.Field != contractsv1.ContextFabricRenderSubcategoryTableField || !declaresSubcategoryShareTable(candidate) {
			continue
		}
		if fact == nil {
			fact = candidate
			continue
		}
		if !sameThemeBreakdown(fact, candidate) {
			return nil, RenderShapeSkipSeveralSubcategoryBreakdowns
		}
	}
	if fact == nil {
		return nil, RenderShapeSkipNoSubcategoryBreakdown
	}
	switch fact.Subject.Kind {
	case contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectTeam:
	default:
		return nil, RenderShapeSkipSubjectKindNotCharted
	}
	vocabulary := contractsv1.ContextFabricInvestmentSubcategoryVocabulary()
	if len(fact.Rows) != len(vocabulary) {
		return nil, RenderShapeSkipSubcategoryTableCut
	}
	inner := contractsv1.ContextFabricRenderSeries{Key: contractsv1.ContextFabricRenderSunburstThemeSeriesKey, Label: subcategoryShareInnerRingLabel}
	rings := map[string]*contractsv1.ContextFabricRenderSeries{}
	above := 0
	for index, row := range fact.Rows {
		subcategory, _ := renderStringCell(row.Fields[contractsv1.ContextFabricRenderSubcategoryField])
		theme, _ := renderStringCell(row.Fields[contractsv1.ContextFabricRenderThemeField])
		own, canonical := contractsv1.ContextFabricInvestmentSubcategoryTheme(subcategory)
		themeWord, knownTheme := contractsv1.ContextFabricInvestmentThemeLabel(theme)
		word, worded := contractsv1.ContextFabricVocabularyLabel(contractsv1.ContextFabricVocabularySubcategory, subcategory)
		if !canonical || own != theme || !knownTheme || !worded {
			return nil, RenderShapeSkipSubcategoryRowUnusable
		}
		share, numeric := renderNumericCell(row.Fields[contractsv1.ContextFabricRenderThemeShareField])
		total, totalNumeric := renderNumericCell(row.Fields[contractsv1.ContextFabricRenderThemeTotalField])
		if !numeric || !totalNumeric || share < 0 || share > 1 || total < 0 || total > 1 {
			return nil, RenderShapeSkipSubcategoryRowUnusable
		}
		if share > 0 {
			above++
		}
		rowIndex := index
		ring, seen := rings[theme]
		if !seen {
			// The inner ring cites the first row of each theme: every row of a
			// theme carries the same theme_share, and the contract holds that.
			ring = &contractsv1.ContextFabricRenderSeries{Key: theme, Label: themeWord}
			rings[theme] = ring
			inner.Points = append(inner.Points, contractsv1.ContextFabricRenderPoint{
				Label: themeWord,
				Value: total,
				Source: contractsv1.ContextFabricRenderPointSource{
					Kind: contractsv1.ContextFabricRenderSourceClaimedFactRow, ClaimID: fact.ClaimID, RowIndex: &rowIndex,
					Field: contractsv1.ContextFabricRenderThemeTotalField,
				},
			})
		}
		ring.Points = append(ring.Points, contractsv1.ContextFabricRenderPoint{
			Label: word,
			Value: share,
			Source: contractsv1.ContextFabricRenderPointSource{
				Kind: contractsv1.ContextFabricRenderSourceClaimedFactRow, ClaimID: fact.ClaimID, RowIndex: &rowIndex,
				Field: contractsv1.ContextFabricRenderThemeShareField,
			},
		})
	}
	if above < 2 {
		return nil, RenderShapeSkipFewerThanTwoShares
	}
	// The rings follow the inner ring's order, which is the table's.
	series := make([]contractsv1.ContextFabricRenderSeries, 0, 1+len(rings))
	series = append(series, inner)
	for _, point := range inner.Points {
		theme, _ := renderStringCell(fact.Rows[*point.Source.RowIndex].Fields[contractsv1.ContextFabricRenderThemeField])
		series = append(series, *rings[theme])
	}
	shape := &contractsv1.ContextFabricRenderShape{
		Kind:       contractsv1.ContextFabricRenderKindSunburst,
		SelectedBy: contractsv1.ContextFabricRenderRuleInvestmentSubcategoryShare,
		Title:      subcategoryShareTitle,
		AxisKind:   contractsv1.ContextFabricRenderAxisCategory,
		AxisLabel:  subcategoryShareAxisLabel,
		ValueLabel: themeShareValueLabel,
		ValueUnit:  contractsv1.ContextFabricRenderValueUnitRatio,
		Series:     series,
	}
	// A chart never costs an answer: the rule proves its shape against the
	// contract, which holds the part sums, and a shape it refuses is no chart.
	proved := *shape
	proved.ShapeID = "rs_proof"
	probe := result
	probe.RenderShapes = []contractsv1.ContextFabricRenderShape{proved}
	if err := contractsv1.ValidateRenderShapesForResult(probe); err != nil {
		return nil, RenderShapeSkipSubcategoryShapeUnprovable
	}
	return shape, ""
}

// declaresSubcategoryShareTable is the reading the contract's validator holds a
// sunburst to: the claim's rows are its declared subcategory breakdown, keyed by
// subcategory, with the theme observation and both measures, and no time
// series sits in front of them.
func declaresSubcategoryShareTable(fact *ClaimedFact) bool {
	table := fact.Table
	return table != nil && len(fact.TimeSeriesRows) == 0 &&
		table.Field == contractsv1.ContextFabricRenderSubcategoryTableField &&
		table.Shape == contractsv1.ContextFabricFactTableShapeBreakdown &&
		len(table.Key) == 1 && table.Key[0] == contractsv1.ContextFabricRenderSubcategoryField &&
		table.HasObservation(contractsv1.ContextFabricRenderThemeField) &&
		table.HasMeasure(contractsv1.ContextFabricRenderThemeShareField) &&
		table.HasMeasure(contractsv1.ContextFabricRenderThemeTotalField)
}
