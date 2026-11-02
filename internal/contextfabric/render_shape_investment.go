package contextfabric

import (
	"reflect"
	"slices"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// RenderShapeOptions are the deployment's switches for selection. The zero
// value is what SelectRenderShapes applies.
type RenderShapeOptions struct {
	// InvestmentThemeShare turns on the investment_theme_share rule
	// (CHAOS-12521). See Config.ContextFabricInvestmentTreemapEnabled.
	InvestmentThemeShare bool
	// ComparisonSideBySide turns on the comparison_side_by_side rule
	// (CHAOS-12513). See Config.ContextFabricComparisonTableEnabled.
	ComparisonSideBySide bool
	// LandscapePosition turns on the landscape_position rule (CHAOS-12626). See
	// Config.ContextFabricLandscapeQuadrantEnabled.
	LandscapePosition bool
	// FieldScope reads the declared scope of a field. The comparison rule
	// reads it. A nil lookup scopes nothing, so that rule finds no cell.
	FieldScope FactFieldScopeLookup
	// TableCadence reads the declared cadence of a claimed table (CHAOS-11985).
	// finalizeResult states the cadence with it, and the selection counts the
	// tables that could not keep theirs. A nil lookup states none.
	TableCadence FactTableCadenceLookup
}

// The words of the treemap (CHAOS-12521). They are served, never derived from a
// model, and agreed with the seam that draws them.
const (
	themeShareTitle       = "Effort share by theme"
	themeShareAxisLabel   = "Theme"
	themeShareValueLabel  = "Share of effort (ratio)"
	themeShareSeriesLabel = "Share of effort"
	// An organization's shares are of the effort that reaches a repository. The
	// rest is the fact's unattributed_effort_share, outside the mix, so the
	// words say which effort the parts divide (CHAOS-12585).
	themeShareOrganizationValueLabel = "Share of effort linked to a repository (ratio)"
)

// investmentThemeShareShape is the part of a whole of one subject's investment.
//
// It reads the plan and the claimed facts only: the served family says the
// question is about where effort went, and the fact's own declaration says which
// rows are the parts. There is no geometry to infer, and no fallback: a question
// this rule does not fire for gets no treemap (check 10).
//
// Exactly one breakdown, on a repository, a team or the organization. Each claim
// on a fact carries that fact's rows, so several claims on one fact are one
// breakdown (CHAOS-12591), and the chart cites the first of them. A second
// breakdown is a comparison. An organization frame reaches this family through
// RouteFrameFamily (CHAOS-12585). The subject check is an allow
// list, so a person never gets this chart: the vocabulary has no such kind today,
// and a kind added later stays out until someone decides otherwise. Every row is a
// point, so a measured zero stays a part of the whole.
func investmentThemeShareShape(result InvestigationResult, frame *QuestionFrame) (*contractsv1.ContextFabricRenderShape, RenderShapeSkipReason) {
	if result.AnswerPlan == nil || result.AnswerPlan.Family != QuestionFamilyInvestmentAllocation {
		return nil, RenderShapeSkipNotInvestmentAllocation
	}
	if result.Cohort != nil || cohortRenderIntent(frame) || !familyTakesShape(result.AnswerPlan.Family, result.Interpretation.Shape) {
		return nil, RenderShapeSkipCohortAnswer
	}
	// Positive evidence: the rule fires only on this turn's own validated frame
	// holding the goal. A carried family says what an earlier turn asked, so a
	// turn with no frame gets no chart, and that includes a real follow-up on the
	// same subject (check 10). Excluding bad cases one at a time let three
	// through.
	if frame == nil || !frame.HasGoal(GoalAllocateInvestment) {
		return nil, RenderShapeSkipNotInvestmentAllocation
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
		if fact == nil {
			fact = candidate
			continue
		}
		if !sameThemeBreakdown(fact, candidate) {
			return nil, RenderShapeSkipSeveralThemeBreakdowns
		}
	}
	if fact == nil {
		return nil, RenderShapeSkipNoThemeBreakdown
	}
	switch fact.Subject.Kind {
	case contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectOrganization:
	default:
		return nil, RenderShapeSkipSubjectKindNotCharted
	}
	// The row budget can cut a table. A cut table is not the whole, and a chart of
	// what is left would present those shares as all of the effort.
	if len(fact.Rows) != contractsv1.ContextFabricInvestmentThemeCount() {
		return nil, RenderShapeSkipThemeTableCut
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
	shape := &contractsv1.ContextFabricRenderShape{
		Kind:       contractsv1.ContextFabricRenderKindTreemap,
		SelectedBy: contractsv1.ContextFabricRenderRuleInvestmentThemeShare,
		Title:      themeShareTitle,
		AxisKind:   contractsv1.ContextFabricRenderAxisCategory,
		AxisLabel:  themeShareAxisLabel,
		ValueLabel: themeShareValueLabelFor(fact.Subject.Kind),
		ValueUnit:  contractsv1.ContextFabricRenderValueUnitRatio,
		Series: []contractsv1.ContextFabricRenderSeries{{
			Key:    contractsv1.ContextFabricRenderThemeShareField,
			Label:  themeShareSeriesLabel,
			Points: points,
		}},
	}
	// The class rule: a chart never costs an answer. The contract is what a
	// result is held to, so the rule proves its shape against the contract here
	// and does not restate each clause. A shape that would refuse the whole
	// document is no chart, whatever the reason.
	proved := *shape
	proved.ShapeID = "rs_proof"
	probe := result
	probe.RenderShapes = []contractsv1.ContextFabricRenderShape{proved}
	if err := contractsv1.ValidateRenderShapesForResult(probe); err != nil {
		return nil, RenderShapeSkipThemeRowUnusable
	}
	return shape, ""
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
	if options.InvestmentThemeShare && options.ComparisonSideBySide && options.LandscapePosition {
		return result
	}
	var kept []contractsv1.ContextFabricRenderShape
	for _, shape := range result.RenderShapes {
		switch shape.SelectedBy {
		case contractsv1.ContextFabricRenderRuleInvestmentThemeShare:
			if !options.InvestmentThemeShare {
				continue
			}
		case contractsv1.ContextFabricRenderRuleComparisonSideBySide:
			if !options.ComparisonSideBySide {
				continue
			}
		case contractsv1.ContextFabricRenderRuleLandscapePosition:
			if !options.LandscapePosition {
				continue
			}
		}
		kept = append(kept, shape)
	}
	result.RenderShapes = kept
	// The quadrant's served line goes with it (CHAOS-12626).
	if !hasLandscapePosition(kept) {
		result.Limitations = withoutLimitation(result.Limitations, contractsv1.ContextFabricLandscapePositionLimitation)
	}
	return result
}

// themeShareValueLabelFor names what the shares divide for the subject's kind.
func themeShareValueLabelFor(kind contractsv1.ContextFabricSubjectKind) string {
	if kind == contractsv1.ContextFabricSubjectOrganization {
		return themeShareOrganizationValueLabel
	}
	return themeShareValueLabel
}

// sameThemeBreakdown reports that two claims carry one breakdown: one subject,
// one declaration and the same rows. That is what two claims on one fact hold,
// because the rows reach every claim from the fact (attachCanonicalRows). Any
// difference is a second breakdown, and a chart of either would hide the other.
//
// Two facts of one subject can hold equal rows, so equal content is not enough:
// the fact ids must be equal too. Two ids are two facts. A claim with no id is
// grounded over its subject's whole group (groundClaim), so beside a claim that
// names a fact it may hold another fact's rows, and the pair is two breakdowns.
// Claims that both name no fact are compared by content alone: the model writes
// fact_id, the server does not fill it, and a model may leave it out of every
// claim.
func sameThemeBreakdown(first, other *ClaimedFact) bool {
	if first.FactID != other.FactID {
		return false
	}
	return first.Subject.Kind == other.Subject.Kind && first.Subject.CanonicalID == other.Subject.CanonicalID &&
		reflect.DeepEqual(first.Table, other.Table) && reflect.DeepEqual(first.Rows, other.Rows)
}

func declaresThemeShareTable(fact *ClaimedFact) bool {
	table := fact.Table
	return table != nil && len(fact.TimeSeriesRows) == 0 &&
		table.Field == contractsv1.ContextFabricRenderThemeTableField &&
		table.Shape == contractsv1.ContextFabricFactTableShapeBreakdown &&
		table.HasMeasure(contractsv1.ContextFabricRenderThemeShareField)
}
