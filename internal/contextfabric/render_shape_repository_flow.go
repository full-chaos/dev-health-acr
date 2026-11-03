package contextfabric

import (
	"slices"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The words of the sankey (CHAOS-12582). They are served, never derived from a
// model, and they are the words the contract's fixture and web's seam were
// built with (CHAOS-12578, CHAOS-12581).
const (
	repositoryFlowTitle      = "Where the team's effort went, by repository"
	repositoryFlowAxisLabel  = "Repository"
	repositoryFlowValueLabel = "Weighted effort"
)

// repositoryFlowThemeOrder is the canonical theme order, the order the product
// model names the themes in. A series is a theme, so a reader sees the themes in
// one order on every answer. TestTheRepositoryFlowThemeOrderIsTheCanon holds it
// to the contract's closed theme set.
var repositoryFlowThemeOrder = []string{ThemeFeatureDelivery, ThemeOperational, ThemeMaintenance, ThemeQuality, ThemeRisk}

// investmentRepositoryFlowShape is one team's effort flowing from its
// repositories to the themes (the CHAOS-4610 sankey).
//
// It fires on positive evidence only, all of it this turn's own: the served
// family is investment_allocation, the validated frame asks for the
// repositories under a scope anchor (children_of_scope, member kind
// repository) with the one goal allocate_investment, and the answer holds one
// team's investment claim that carries the declared repository_theme_breakdown
// table. A carried family is never evidence, so a turn with no frame gets no
// sankey (check 10). Which question reaches this family is the CHAOS-12606
// route's decision, and this rule follows it.
//
// One series per theme with effort, in canonical order, and one point per
// repository's link with effort above 0, each value the row's own. A zero link
// draws nothing and stays in the value table. Fewer than 2 or more than 64
// repositories with effort give no shape, never an "other" node. The shape is
// proved against the contract before it is returned: a chart never costs an
// answer.
func investmentRepositoryFlowShape(result InvestigationResult, frame *QuestionFrame) (*contractsv1.ContextFabricRenderShape, RenderShapeSkipReason) {
	if result.AnswerPlan == nil || result.AnswerPlan.Family != QuestionFamilyInvestmentAllocation {
		return nil, RenderShapeSkipNotInvestmentAllocation
	}
	if !repositoryFlowFrame(frame) {
		return nil, RenderShapeSkipNotRepositoryFlowQuestion
	}
	if result.Cohort != nil {
		return nil, RenderShapeSkipCohortAnswer
	}
	if !planAuthorizesRenderKind(result.AnswerPlan, contractsv1.ContextFabricRenderKindSankey) {
		return nil, RenderShapeSkipNotPlanAuthorized
	}
	var fact *ClaimedFact
	for index := range result.ClaimedFacts {
		candidate := &result.ClaimedFacts[index]
		if candidate.Kind != FactInvestment || !declaresRepositoryFlowTable(candidate) {
			continue
		}
		if fact != nil {
			return nil, RenderShapeSkipSeveralRepositoryFlowTables
		}
		fact = candidate
	}
	if fact == nil {
		return nil, RenderShapeSkipNoRepositoryFlowTable
	}
	if fact.Subject.Kind != contractsv1.ContextFabricSubjectTeam {
		return nil, RenderShapeSkipSubjectKindNotCharted
	}
	byTheme := map[string][]contractsv1.ContextFabricRenderPoint{}
	repositories := map[string]struct{}{}
	for index, row := range fact.Rows {
		theme, isTheme := renderStringCell(row.Fields[contractsv1.ContextFabricRenderThemeField])
		word, isWord := renderStringCell(row.Fields[contractsv1.ContextFabricRenderRepositoryWordField])
		repository, isRepository := renderStringCell(row.Fields[contractsv1.ContextFabricRenderRepositoryField])
		effort, numeric := renderNumericCell(row.Fields[contractsv1.ContextFabricRenderEffortField])
		if !isTheme || !isWord || !isRepository || !numeric {
			return nil, RenderShapeSkipRepositoryFlowUnprovable
		}
		if !(effort > 0) {
			continue
		}
		repositories[repository] = struct{}{}
		rowIndex := index
		byTheme[theme] = append(byTheme[theme], contractsv1.ContextFabricRenderPoint{
			Label: word,
			Value: effort,
			Source: contractsv1.ContextFabricRenderPointSource{
				Kind:     contractsv1.ContextFabricRenderSourceClaimedFactRow,
				ClaimID:  fact.ClaimID,
				RowIndex: &rowIndex,
				Field:    contractsv1.ContextFabricRenderEffortField,
			},
		})
	}
	if len(repositories) < contractsv1.ContextFabricRenderRepositoryFlowMinRepositories ||
		len(repositories) > contractsv1.ContextFabricRenderRepositoryFlowMaxRepositories {
		return nil, RenderShapeSkipRepositoryCountOutsideBounds
	}
	shape := &contractsv1.ContextFabricRenderShape{
		Kind:       contractsv1.ContextFabricRenderKindSankey,
		SelectedBy: contractsv1.ContextFabricRenderRuleInvestmentRepositoryFlow,
		Title:      repositoryFlowTitle,
		AxisKind:   contractsv1.ContextFabricRenderAxisCategory,
		AxisLabel:  repositoryFlowAxisLabel,
		ValueLabel: repositoryFlowValueLabel,
	}
	for _, theme := range repositoryFlowThemeOrder {
		points := byTheme[theme]
		if len(points) == 0 {
			continue
		}
		word, _ := contractsv1.ContextFabricInvestmentThemeLabel(theme)
		shape.Series = append(shape.Series, contractsv1.ContextFabricRenderSeries{Key: theme, Label: word, Points: points})
	}
	// A theme outside the canon has no series here, so its links are left out:
	// the contract refuses that table, and the proof below with it.
	//
	// The class rule, as the treemap's: the contract is what a result is held
	// to, so the rule proves its shape against it and does not restate each
	// clause (a link left out, a theme sum the rows disagree on, two nodes with
	// one word). A shape that would refuse the whole document is no chart.
	proved := *shape
	proved.ShapeID = "rs_proof"
	probe := result
	probe.RenderShapes = []contractsv1.ContextFabricRenderShape{proved}
	if err := contractsv1.ValidateRenderShapesForResult(probe); err != nil {
		return nil, RenderShapeSkipRepositoryFlowUnprovable
	}
	return shape, ""
}

// repositoryFlowFrame is the question the sankey answers: the repositories under
// a scope anchor, asked with the one goal allocate_investment. Any other goal
// beside it asks for something else as well, and a sankey would answer the
// nearest measurable question in its place (check 1).
func repositoryFlowFrame(frame *QuestionFrame) bool {
	if frame == nil || frame.SubjectExpression.Kind != SubjectExpressionChildrenOfScope || frame.SubjectExpression.Scoped == nil {
		return false
	}
	if frame.SubjectExpression.Scoped.MemberKind != contractsv1.ContextFabricSubjectRepository {
		return false
	}
	return len(frame.Goals) == 1 && frame.HasGoal(GoalAllocateInvestment)
}

// declaresRepositoryFlowTable is the claim's own declaration of the sankey's
// table, read the way the contract reads it: a breakdown keyed by repository
// then theme, with both efforts and the repository's word, and no time series.
func declaresRepositoryFlowTable(fact *ClaimedFact) bool {
	table := fact.Table
	return table != nil && len(fact.TimeSeriesRows) == 0 &&
		table.Field == contractsv1.ContextFabricRenderRepositoryFlowTableField &&
		table.Shape == contractsv1.ContextFabricFactTableShapeBreakdown &&
		slices.Equal(table.Key, []string{contractsv1.ContextFabricRenderRepositoryField, contractsv1.ContextFabricRenderThemeField}) &&
		table.HasMeasure(contractsv1.ContextFabricRenderEffortField) &&
		table.HasMeasure(contractsv1.ContextFabricRenderThemeEffortField) &&
		table.HasObservation(contractsv1.ContextFabricRenderRepositoryWordField)
}
