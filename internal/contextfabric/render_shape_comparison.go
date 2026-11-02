package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The words of the table (CHAOS-12513). They are served, never derived from a
// model, and agreed with the seam that draws them.
const (
	comparisonTitle      = "Side by side"
	comparisonAxisLabel  = "Measure"
	comparisonValueLabel = "Value"
)

// FactFieldScopeLookup returns the declared scope of one field of a fact, or ""
// for a field with no declaration.
type FactFieldScopeLookup func(kind FactKind, subject SubjectKind, field string) FactFieldScope

// factFieldScopeLookup reads the scopes off the fact declarations. With no
// declarations no field has a scope, so no cell exists and the rule skips.
func factFieldScopeLookup(capabilities []FactCapability) FactFieldScopeLookup {
	byKind := make(map[FactKind]FactCapability, len(capabilities))
	for _, capability := range capabilities {
		byKind[capability.Kind] = capability
	}
	return func(kind FactKind, subject SubjectKind, field string) FactFieldScope {
		declaration, _ := byKind[kind].FieldDeclaration(field, subject)
		return declaration.Scope
	}
}

// comparisonMeasure is one row of the table: one field of one fact kind, with the
// claim of each side that holds it.
type comparisonMeasure struct {
	kind  FactKind
	field string
	word  string
	scope FactFieldScope
	cells map[string]ClaimedFact
	// doubled names the sides with two claims of this measure. Which one is
	// right is not this rule's call, so that cell stays empty.
	doubled map[string]struct{}
}

// comparisonSideBySideShape is the side by side of an explicit comparison. It
// returns the shape, or the reason there is none, and how many claimed measures
// it left out as not comparable.
//
// It reads the plan, the committed subjects and the claimed facts only: a number
// reaches the table because the answer already claims it (the lead's option A,
// CHAOS-12513). There is no fallback and no read of its own, so a turn whose
// claims do not hold one measure for every side gets no table (check 10).
//
// A cell needs a declared scope. A window field speaks for the window both sides
// were read over. A latest_day field speaks for one subject's newest stored day,
// and that day can differ, so it stays only when every side that holds it proves
// the same day. A measure a side lacks has no point: the cell is empty, never zero.
func comparisonSideBySideShape(result InvestigationResult, frame *QuestionFrame, scopeOf FactFieldScopeLookup) (*contractsv1.ContextFabricRenderShape, int, RenderShapeSkipReason) {
	if result.AnswerPlan == nil || result.AnswerPlan.Family != QuestionFamilyExplicitComparison || !comparisonIntent(frame) {
		return nil, 0, RenderShapeSkipNotExplicitComparison
	}
	if !planAuthorizesRenderKind(result.AnswerPlan, contractsv1.ContextFabricRenderKindTable) {
		return nil, 0, RenderShapeSkipNotPlanAuthorized
	}
	sides := result.SubjectResolution.Committed
	switch {
	case len(sides) < 2:
		return nil, 0, RenderShapeSkipFewerThanTwoSides
	case len(sides) > contractsv1.ContextFabricRenderSeriesMaxCount:
		return nil, 0, RenderShapeSkipTooManySides
	}
	for _, side := range sides[1:] {
		if side.Kind != sides[0].Kind {
			return nil, 0, RenderShapeSkipMixedSubjectKinds
		}
	}
	measures, leftOut := comparableMeasures(result.ClaimedFacts, sides, scopeOf)
	shared := false
	for _, measure := range measures {
		shared = shared || len(measure.cells) == len(sides)
	}
	if !shared {
		return nil, leftOut, RenderShapeSkipNoSharedMeasure
	}
	shape := comparisonShape(sides, measures)
	// The class rule, as for the treemap: a chart never costs an answer. A shape
	// that would refuse the whole document is no chart, whatever the reason.
	proved := *shape
	proved.ShapeID = "rs_proof"
	probe := result
	probe.RenderShapes = []contractsv1.ContextFabricRenderShape{proved}
	if err := contractsv1.ValidateRenderShapesForResult(probe); err != nil {
		return nil, leftOut, RenderShapeSkipComparisonUnprovable
	}
	return shape, leftOut, ""
}

// comparisonIntent reports that this turn's own validated frame asks for a
// comparison. A carried family says what an earlier turn asked, so a turn with no
// frame gets no table.
func comparisonIntent(frame *QuestionFrame) bool {
	return frame != nil && (frame.HasGoal(GoalCompare) || frame.SubjectExpression.Kind == SubjectExpressionExplicitSet)
}

// comparableMeasures groups the claims by measure, in claim order, and keeps each
// measure a table can state. It returns the count it left out.
func comparableMeasures(claims []ClaimedFact, sides []SubjectRef, scopeOf FactFieldScopeLookup) ([]*comparisonMeasure, int) {
	onSide := make(map[string]struct{}, len(sides))
	for _, side := range sides {
		onSide[side.CanonicalID] = struct{}{}
	}
	type measureKey struct {
		kind  FactKind
		field string
	}
	byKey := map[measureKey]*comparisonMeasure{}
	var ordered []*comparisonMeasure
	for _, claim := range claims {
		id := claim.Subject.CanonicalID
		if _, side := onSide[id]; !side {
			continue
		}
		word, scope, usable := comparisonCell(claim, scopeOf)
		if !usable {
			continue
		}
		key := measureKey{claim.Kind, claim.Field}
		measure := byKey[key]
		if measure == nil {
			measure = &comparisonMeasure{kind: claim.Kind, field: claim.Field, word: word, scope: scope, cells: map[string]ClaimedFact{}, doubled: map[string]struct{}{}}
			byKey[key] = measure
			ordered = append(ordered, measure)
		}
		if _, repeated := measure.cells[id]; repeated {
			measure.doubled[id] = struct{}{}
		}
		measure.cells[id] = claim
	}
	kept := make([]*comparisonMeasure, 0, len(ordered))
	words := make(map[string]struct{}, len(ordered))
	leftOut := 0
	for _, measure := range ordered {
		for id := range measure.doubled {
			delete(measure.cells, id)
		}
		_, wordTaken := words[measure.word]
		if len(measure.cells) == 0 || wordTaken || len(kept) == contractsv1.ContextFabricRenderPointsMaxCount ||
			(measure.scope == FactScopeLatestDay && !sameLatestDay(measure.cells)) {
			leftOut++
			continue
		}
		words[measure.word] = struct{}{}
		kept = append(kept, measure)
	}
	return kept, leftOut
}

// comparisonCell reports what a claim brings to a cell: a stored number, in a
// field with a served word and a span this table can state.
func comparisonCell(claim ClaimedFact, scopeOf FactFieldScopeLookup) (string, FactFieldScope, bool) {
	if len(claim.DerivedFrom) > 0 || scopeOf == nil {
		return "", "", false
	}
	if _, numeric := renderNumericCell(claim.Value); !numeric {
		return "", "", false
	}
	word, labeled := contractsv1.ContextFabricVocabularyLabel(contractsv1.ContextFabricVocabularyFactField, claim.Field)
	scope := scopeOf(claim.Kind, claim.Subject.Kind, claim.Field)
	return word, scope, labeled && (scope == FactScopeWindow || scope == FactScopeLatestDay)
}

// sameLatestDay reports that every cell proves one day.
func sameLatestDay(cells map[string]ClaimedFact) bool {
	shared := ""
	for _, claim := range cells {
		day, proved := claimLatestDay(claim)
		if !proved || (shared != "" && day != shared) {
			return false
		}
		shared = day
	}
	return true
}

// claimLatestDay is the day a latest_day value speaks for: the day of the claim's
// own newest row, when that row holds this same value. The row travels in the
// document, so a reader can check the day. A claim whose rows prove no day gets no
// cell, because a day this rule cannot show is a day it cannot compare.
func claimLatestDay(claim ClaimedFact) (string, bool) {
	rows := claim.TimeSeriesRows
	if len(rows) == 0 {
		rows = claim.Rows
	}
	if len(rows) == 0 {
		return "", false
	}
	newest := rows[0].Fields
	day, dated := renderStringCell(newest[FactDayColumn])
	held, numeric := renderNumericCell(newest[claim.Field])
	stated, _ := renderNumericCell(claim.Value)
	return day, dated && numeric && held == stated
}

// comparisonShape draws one series per side, in committed order, which is the
// order of the question's own operands, and one point per measure the side holds.
func comparisonShape(sides []SubjectRef, measures []*comparisonMeasure) *contractsv1.ContextFabricRenderShape {
	series := make([]contractsv1.ContextFabricRenderSeries, 0, len(sides))
	for _, side := range sides {
		points := make([]contractsv1.ContextFabricRenderPoint, 0, len(measures))
		for _, measure := range measures {
			claim, holds := measure.cells[side.CanonicalID]
			if !holds {
				continue
			}
			value, _ := renderNumericCell(claim.Value)
			points = append(points, contractsv1.ContextFabricRenderPoint{
				Label: measure.word,
				Value: value,
				Source: contractsv1.ContextFabricRenderPointSource{
					Kind:    contractsv1.ContextFabricRenderSourceClaimedFactValue,
					ClaimID: claim.ClaimID,
					Field:   claim.Field,
				},
			})
		}
		label := side.Label
		if label == "" {
			label = side.CanonicalID
		}
		series = append(series, contractsv1.ContextFabricRenderSeries{Key: side.CanonicalID, Label: clampRenderLabel(label), Points: points})
	}
	return &contractsv1.ContextFabricRenderShape{
		Kind:       contractsv1.ContextFabricRenderKindTable,
		SelectedBy: contractsv1.ContextFabricRenderRuleComparisonSideBySide,
		Title:      comparisonTitle,
		AxisKind:   contractsv1.ContextFabricRenderAxisCategory,
		AxisLabel:  comparisonAxisLabel,
		ValueLabel: comparisonValueLabel,
		Series:     series,
	}
}
