package v1

import (
	"fmt"
	"strings"
)

// renderShapeSources is the set of numbers a document actually contains,
// indexed the way ContextFabricRenderPointSource addresses them. Both the
// canonical result and the answer projection build one from their OWN
// content and hand it to validateRenderShapes, so the resolution rule is
// written once and cannot drift between the two surfaces.
//
// It holds only what a source kind can name. Anything a shape points at
// that is not in here does not exist in the document the shape travels in,
// which is the whole point: a chart may not cite a number the reader cannot
// also see.
type renderShapeSources struct {
	// memberScore maps a cohort member's canonical id to its Score. A
	// member present with a nil score is a DIFFERENT thing from a member
	// that is absent -- an unranked member cannot be plotted, and saying
	// so precisely is what keeps "no score" distinguishable from "no such
	// team" in the rejection message.
	memberScore map[string]*float64
	// driverWeight maps canonical id -> signal -> WeightContributed.
	driverWeight map[string]map[string]float64
	// factRows maps a claim id to that claim's row table.
	factRows map[string][]ContextFabricClaimedFactRow
	// themeShareClaims names the claims whose factRows are their declared
	// theme_breakdown table. A claim can carry a time series beside that table
	// and factRows then holds the series, so rows alone do not say which table
	// a treemap would plot.
	themeShareClaims map[string]struct{}
}

// holdsThemeShareTable reports that a claim's renderable rows are a declared
// breakdown table of the theme share field, with share among its measures.
func holdsThemeShareTable(table *ContextFabricClaimedFactTable, timeSeriesRows []ContextFabricClaimedFactRow) bool {
	if table == nil || len(timeSeriesRows) > 0 {
		return false
	}
	if table.Field != ContextFabricRenderThemeTableField || table.Shape != ContextFabricFactTableShapeBreakdown {
		return false
	}
	for _, measure := range table.Measures {
		if measure == ContextFabricRenderThemeShareField {
			return true
		}
	}
	return false
}

// validateRenderPointSourceShape checks an address is WELL FORMED before
// anything tries to resolve it. Separating shape from resolution keeps the
// failure honest: "this address is malformed" and "this address names
// something the document does not carry" are different defects, and a
// resolver that conflated them would report a missing team for a source
// that never named one.
func validateRenderPointSourceShape(source ContextFabricRenderPointSource) error {
	if source.SubjectCanonicalID != "" && !stringLengthBetween(source.SubjectCanonicalID, 1, 256) {
		return fmt.Errorf("render point source subject id violates v1 bounds")
	}
	if source.Signal != "" && !stringLengthBetween(source.Signal, 1, ContextFabricRenderLabelMaxLength) {
		return fmt.Errorf("render point source signal violates v1 bounds")
	}
	if source.ClaimID != "" && !stringLengthBetween(source.ClaimID, 8, 256) {
		return fmt.Errorf("render point source claim id violates v1 bounds")
	}
	if source.Field != "" && !stringLengthBetween(source.Field, 1, ContextFabricRenderSourceFieldMaxLength) {
		return fmt.Errorf("render point source field violates v1 bounds")
	}
	if source.RowIndex != nil && *source.RowIndex < 0 {
		return fmt.Errorf("render point source row index violates v1 bounds")
	}
	return nil
}

func (s renderShapeSources) resolve(source ContextFabricRenderPointSource) (float64, error) {
	if err := validateRenderPointSourceShape(source); err != nil {
		return 0, err
	}
	switch source.Kind {
	case ContextFabricRenderSourceCohortMemberScore:
		if source.SubjectCanonicalID == "" || source.Signal != "" || source.ClaimID != "" || source.RowIndex != nil || source.Field != "" {
			return 0, fmt.Errorf("render point source %q carries the wrong address fields", source.Kind)
		}
		score, ok := s.memberScore[source.SubjectCanonicalID]
		if !ok {
			return 0, fmt.Errorf("render point cites cohort member %q, which this answer does not carry", source.SubjectCanonicalID)
		}
		if score == nil {
			return 0, fmt.Errorf("render point cites cohort member %q, which carries no score", source.SubjectCanonicalID)
		}
		return *score, nil
	case ContextFabricRenderSourceCohortDriverWeight:
		if source.SubjectCanonicalID == "" || source.Signal == "" || source.ClaimID != "" || source.RowIndex != nil || source.Field != "" {
			return 0, fmt.Errorf("render point source %q carries the wrong address fields", source.Kind)
		}
		signals, ok := s.driverWeight[source.SubjectCanonicalID]
		if !ok {
			return 0, fmt.Errorf("render point cites cohort member %q, which this answer does not carry", source.SubjectCanonicalID)
		}
		weight, ok := signals[source.Signal]
		if !ok {
			return 0, fmt.Errorf("render point cites driver %q on member %q, which this answer does not carry", source.Signal, source.SubjectCanonicalID)
		}
		return weight, nil
	case ContextFabricRenderSourceClaimedFactRow:
		if source.ClaimID == "" || source.RowIndex == nil || source.Field == "" || source.SubjectCanonicalID != "" || source.Signal != "" {
			return 0, fmt.Errorf("render point source %q carries the wrong address fields", source.Kind)
		}
		rows, ok := s.factRows[source.ClaimID]
		if !ok {
			return 0, fmt.Errorf("render point cites claim %q, which this answer does not carry", source.ClaimID)
		}
		index := *source.RowIndex
		if index < 0 || index >= len(rows) {
			return 0, fmt.Errorf("render point cites row %d of claim %q, which has %d rows", index, source.ClaimID, len(rows))
		}
		cell, ok := rows[index].Fields[source.Field]
		if !ok {
			return 0, fmt.Errorf("render point cites field %q of claim %q row %d, which the row does not carry", source.Field, source.ClaimID, index)
		}
		return renderCellValue(cell, source, index)
	default:
		return 0, fmt.Errorf("render point source kind is not a member of the closed vocabulary")
	}
}

// validateRenderShapes is the CHAOS-4415 guard that makes a chart a claimed
// fact rather than a picture beside one.
//
// Every Point.Value must EXACTLY equal the number its own Source resolves
// to inside the same document. Exact float equality is deliberate and is
// the load-bearing part: a builder that copies a number verbatim always
// passes, and a builder that rounds, rescales, aggregates, interpolates,
// or invents one always fails. There is no tolerance to argue about,
// because there is no legitimate arithmetic for a shape to do -- if a
// derived number is wanted on a chart, a producer computes it into a
// canonical fact first, where it gets provenance and coverage of its own.
//
// This is the sibling of internal/contextfabric's Rows discipline (rows are
// attached from the cited canonical fact, never model-authored). A model
// never authors a render shape at all: shapes are selected and built by
// internal/contextfabric.SelectRenderShapes AFTER synthesis validation, so
// there is no draft field for a model to fill. This function is what makes
// that structural rather than conventional -- a shape that reached the wire
// by any other route cannot resolve, so it cannot validate.
func validateRenderShapes(shapes []ContextFabricRenderShape, sources renderShapeSources) error {
	if len(shapes) > ContextFabricRenderShapesMaxCount {
		return fmt.Errorf("render shapes exceed v1 bounds")
	}
	shapeIDs := make(map[string]struct{}, len(shapes))
	for _, shape := range shapes {
		if !stringLengthBetween(shape.ShapeID, 1, 256) || strings.TrimSpace(shape.ShapeID) != shape.ShapeID {
			return fmt.Errorf("render shape id violates v1 bounds")
		}
		if _, exists := shapeIDs[shape.ShapeID]; exists {
			return fmt.Errorf("render shape ids must be unique")
		}
		shapeIDs[shape.ShapeID] = struct{}{}
		if !validContextFabricRenderKind(shape.Kind) {
			return fmt.Errorf("render shape kind is not a member of the closed vocabulary")
		}
		if !validContextFabricRenderShapeRule(shape.SelectedBy) {
			return fmt.Errorf("render shape selected_by is not a member of the closed vocabulary")
		}
		// Presentation belongs to "series" alone. Requiring it there and
		// forbidding it elsewhere keeps a consumer's switch total: it
		// never has to decide what a "stacked_bars" sankey would mean.
		if shape.Kind == ContextFabricRenderKindSeries {
			if !validContextFabricRenderPresentation(shape.Presentation) {
				return fmt.Errorf("render shape presentation is not a member of the closed vocabulary")
			}
		} else if shape.Presentation != "" {
			return fmt.Errorf("render shape presentation is only valid on a series shape")
		}
		if !validContextFabricRenderAxisKind(shape.AxisKind) {
			return fmt.Errorf("render shape axis kind is not a member of the closed vocabulary")
		}
		if shape.ValueUnit != "" && !validContextFabricRenderValueUnit(shape.ValueUnit) {
			return fmt.Errorf("render shape value_unit is not a member of the closed vocabulary")
		}
		if !stringLengthBetween(shape.Title, 1, ContextFabricRenderLabelMaxLength) ||
			!stringLengthBetween(shape.AxisLabel, 1, ContextFabricRenderLabelMaxLength) ||
			!stringLengthBetween(shape.ValueLabel, 1, ContextFabricRenderLabelMaxLength) {
			return fmt.Errorf("render shape labels violate v1 bounds")
		}
		if len(shape.Series) == 0 || len(shape.Series) > ContextFabricRenderSeriesMaxCount {
			return fmt.Errorf("render shape series count violates v1 bounds")
		}
		seriesKeys := make(map[string]struct{}, len(shape.Series))
		for _, series := range shape.Series {
			if !stringLengthBetween(series.Key, 1, ContextFabricRenderLabelMaxLength) ||
				!stringLengthBetween(series.Label, 1, ContextFabricRenderLabelMaxLength) {
				return fmt.Errorf("render series identity violates v1 bounds")
			}
			if _, exists := seriesKeys[series.Key]; exists {
				return fmt.Errorf("render series keys must be unique within a shape")
			}
			seriesKeys[series.Key] = struct{}{}
			if len(series.Points) == 0 || len(series.Points) > ContextFabricRenderPointsMaxCount {
				return fmt.Errorf("render series point count violates v1 bounds")
			}
			// Point labels are the axis positions of ONE series, so a
			// repeat inside a series would plot two values at one
			// position. Across series a repeat is expected and required
			// -- that is exactly how a stacked bar aligns its parts.
			pointLabels := make(map[string]struct{}, len(series.Points))
			for _, point := range series.Points {
				if !stringLengthBetween(point.Label, 1, ContextFabricRenderLabelMaxLength) {
					return fmt.Errorf("render point label violates v1 bounds")
				}
				if _, exists := pointLabels[point.Label]; exists {
					return fmt.Errorf("render point labels must be unique within a series")
				}
				pointLabels[point.Label] = struct{}{}
				resolved, err := sources.resolve(point.Source)
				if err != nil {
					return fmt.Errorf("render shape %q: %w", shape.ShapeID, err)
				}
				if resolved != point.Value {
					return fmt.Errorf("render shape %q point %q claims %v but its cited source carries %v -- a chart number is never re-derived", shape.ShapeID, point.Label, point.Value, resolved)
				}
			}
		}
		if err := validateTreemapShape(shape, sources); err != nil {
			return err
		}
		if err := validateRenderValueUnit(shape); err != nil {
			return err
		}
	}
	return nil
}

// validateTreemapShape holds a treemap to what a part of a whole means
// (CHAOS-4610). Every point has already resolved to its source. What is left is
// that the points are ALL the parts of ONE whole: one claim's declared table,
// every row once, the same measure, each labelled with the served word of its own
// row's theme, and a real split. A row left out makes the chart a different claim,
// a label that is not its row's theme puts a share on the wrong name, and one part
// above 0 is no split: a chart of it says nothing the sentence does not.
//
// The rule and the kind name each other. A treemap has no other rule, so a
// consumer that reads the kind can rely on these clauses.
func validateTreemapShape(shape ContextFabricRenderShape, sources renderShapeSources) error {
	if (shape.Kind == ContextFabricRenderKindTreemap) != (shape.SelectedBy == ContextFabricRenderRuleInvestmentThemeShare) {
		return fmt.Errorf("render shape %q: selected_by %q cannot yield kind %q", shape.ShapeID, shape.SelectedBy, shape.Kind)
	}
	if shape.Kind != ContextFabricRenderKindTreemap {
		return nil
	}
	if shape.AxisKind != ContextFabricRenderAxisCategory {
		return fmt.Errorf("render shape %q: a treemap needs a category axis", shape.ShapeID)
	}
	if len(shape.Series) != 1 {
		return fmt.Errorf("render shape %q: a treemap holds one series", shape.ShapeID)
	}
	if shape.ValueUnit != ContextFabricRenderValueUnitRatio {
		return fmt.Errorf("render shape %q: a treemap needs a value_unit of %q", shape.ShapeID, ContextFabricRenderValueUnitRatio)
	}
	points := shape.Series[0].Points
	claim := points[0].Source.ClaimID
	if _, declared := sources.themeShareClaims[claim]; !declared {
		return fmt.Errorf("render shape %q: a treemap plots a declared %q table", shape.ShapeID, ContextFabricRenderThemeTableField)
	}
	table := sources.factRows[claim]
	if len(points) != len(table) {
		return fmt.Errorf("render shape %q: a treemap plots every row of its table, and has %d of %d", shape.ShapeID, len(points), len(table))
	}
	rows := make(map[int]struct{}, len(points))
	above := 0
	for _, point := range points {
		source := point.Source
		// The kind is what makes RowIndex safe to read below: the resolver
		// has already refused a claimed_fact_row source with no row index.
		if source.Kind != ContextFabricRenderSourceClaimedFactRow || source.Field != ContextFabricRenderThemeShareField {
			return fmt.Errorf("render shape %q point %q: a treemap point must cite the %q field of a claimed fact row", shape.ShapeID, point.Label, ContextFabricRenderThemeShareField)
		}
		if source.ClaimID != claim {
			return fmt.Errorf("render shape %q point %q: a treemap plots one claim", shape.ShapeID, point.Label)
		}
		if _, repeated := rows[*source.RowIndex]; repeated {
			return fmt.Errorf("render shape %q point %q: a treemap plots each row once", shape.ShapeID, point.Label)
		}
		rows[*source.RowIndex] = struct{}{}
		theme := table[*source.RowIndex].Fields[ContextFabricRenderThemeField].String
		if theme == nil {
			return fmt.Errorf("render shape %q point %q: its row carries no %q", shape.ShapeID, point.Label, ContextFabricRenderThemeField)
		}
		if word, _ := ContextFabricInvestmentThemeLabel(*theme); word != point.Label {
			return fmt.Errorf("render shape %q point %q: a treemap label must be the served word of its row's theme %q", shape.ShapeID, point.Label, *theme)
		}
		if point.Value < 0 {
			return fmt.Errorf("render shape %q point %q: a treemap value is below 0", shape.ShapeID, point.Label)
		}
		if point.Value > 0 {
			above++
		}
	}
	if above < 2 {
		return fmt.Errorf("render shape %q: a treemap needs at least 2 values above 0, and has %d", shape.ShapeID, above)
	}
	if len(points) != ContextFabricInvestmentThemeCount() {
		return fmt.Errorf("render shape %q: a treemap plots every canonical theme, and has %d of %d", shape.ShapeID, len(points), ContextFabricInvestmentThemeCount())
	}
	return nil
}

// validateRenderValueUnit holds every value of a shape to the unit it declares.
// The unit is a claim about the numbers, and a consumer formats by it.
func validateRenderValueUnit(shape ContextFabricRenderShape) error {
	if shape.ValueUnit != ContextFabricRenderValueUnitRatio {
		return nil
	}
	for _, series := range shape.Series {
		for _, point := range series.Points {
			if point.Value < 0 || point.Value > 1 {
				return fmt.Errorf("render shape %q point %q: %v is outside a ratio's range of 0 to 1", shape.ShapeID, point.Label, point.Value)
			}
		}
	}
	return nil
}

// renderCellValue unwraps a row cell to the number a point may plot, or
// refuses it. See ContextFabricRenderPointExactIntegerBound for why a large
// integer is a refusal rather than a cast.
func renderCellValue(cell ContextFabricScalarValue, source ContextFabricRenderPointSource, index int) (float64, error) {
	switch {
	case cell.Number != nil:
		return *cell.Number, nil
	case cell.Integer != nil:
		if *cell.Integer > ContextFabricRenderPointExactIntegerBound || *cell.Integer < -ContextFabricRenderPointExactIntegerBound {
			return 0, fmt.Errorf("render point cites field %q of claim %q row %d, whose integer value is too large to plot without losing the distinction between adjacent values", source.Field, source.ClaimID, index)
		}
		return float64(*cell.Integer), nil
	default:
		return 0, fmt.Errorf("render point cites field %q of claim %q row %d, which is not a number", source.Field, source.ClaimID, index)
	}
}

// ContextFabricRenderKindVocabulary and ContextFabricRenderPresentationVocabulary
// are the closed vocabularies the validators below check against, exported so a
// log declaration reads the same list instead of copying it.
func ContextFabricRenderKindVocabulary() []ContextFabricRenderKind {
	return []ContextFabricRenderKind{
		ContextFabricRenderKindSeries, ContextFabricRenderKindTable, ContextFabricRenderKindQuadrant,
		ContextFabricRenderKindTreemap, ContextFabricRenderKindSunburst, ContextFabricRenderKindSankey,
		ContextFabricRenderKindBurndown, ContextFabricRenderKindForecast,
	}
}

func ContextFabricRenderPresentationVocabulary() []ContextFabricRenderPresentation {
	return []ContextFabricRenderPresentation{
		ContextFabricRenderPresentationBars, ContextFabricRenderPresentationStackedBars, ContextFabricRenderPresentationLine,
	}
}

// ContextFabricRenderValueUnitVocabulary is the closed unit vocabulary.
func ContextFabricRenderValueUnitVocabulary() []ContextFabricRenderValueUnit {
	return []ContextFabricRenderValueUnit{ContextFabricRenderValueUnitRatio}
}

func validContextFabricRenderKind(kind ContextFabricRenderKind) bool {
	for _, member := range ContextFabricRenderKindVocabulary() {
		if member == kind {
			return true
		}
	}
	return false
}

func validContextFabricRenderValueUnit(unit ContextFabricRenderValueUnit) bool {
	for _, member := range ContextFabricRenderValueUnitVocabulary() {
		if member == unit {
			return true
		}
	}
	return false
}

func validContextFabricRenderPresentation(presentation ContextFabricRenderPresentation) bool {
	for _, member := range ContextFabricRenderPresentationVocabulary() {
		if member == presentation {
			return true
		}
	}
	return false
}

func validContextFabricRenderAxisKind(kind ContextFabricRenderAxisKind) bool {
	switch kind {
	case ContextFabricRenderAxisCategory, ContextFabricRenderAxisTime:
		return true
	default:
		return false
	}
}

func validContextFabricRenderShapeRule(rule ContextFabricRenderShapeRule) bool {
	switch rule {
	case ContextFabricRenderRuleCohortAttentionScore, ContextFabricRenderRuleCohortDriverContribution,
		ContextFabricRenderRuleDatedFactTrend, ContextFabricRenderRuleInvestmentThemeShare:
		return true
	default:
		return false
	}
}

// renderShapeSourcesFromResult indexes the CANONICAL result's own cohort
// and claimed facts.
func renderShapeSourcesFromResult(result ContextFabricInvestigationResult) renderShapeSources {
	sources := renderShapeSources{
		memberScore:  map[string]*float64{},
		driverWeight: map[string]map[string]float64{},
		factRows:     map[string][]ContextFabricClaimedFactRow{},

		themeShareClaims: map[string]struct{}{},
	}
	if result.Cohort != nil {
		for _, member := range result.Cohort.Members {
			sources.memberScore[member.Subject.CanonicalID] = member.Score
			signals := make(map[string]float64, len(member.Drivers))
			for _, driver := range member.Drivers {
				signals[driver.Signal] = driver.WeightContributed
			}
			sources.driverWeight[member.Subject.CanonicalID] = signals
		}
	}
	for _, fact := range result.ClaimedFacts {
		sources.factRows[fact.ClaimID] = fact.renderableRows()
		if holdsThemeShareTable(fact.Table, fact.TimeSeriesRows) {
			sources.themeShareClaims[fact.ClaimID] = struct{}{}
		}
	}
	return sources
}

// renderShapeSourcesFromProjection indexes the PROJECTED cohort and key
// facts. The projected cohort member carries Score but NOT drivers (the
// projection narrows it), so a driver-weight source can only resolve
// against the projected ranking table -- which is why answerprojection
// drops a shape whose sources the projection no longer carries and declares
// the drop, rather than shipping a shape that cannot validate.
func renderShapeSourcesFromProjection(projection ContextFabricAnswerProjection) renderShapeSources {
	sources := renderShapeSources{
		memberScore:  map[string]*float64{},
		driverWeight: map[string]map[string]float64{},
		factRows:     map[string][]ContextFabricClaimedFactRow{},

		themeShareClaims: map[string]struct{}{},
	}
	if projection.Cohort != nil {
		for _, member := range projection.Cohort.Members {
			sources.memberScore[member.Subject.CanonicalID] = member.Score
		}
		// The projected cohort's driver numbers survive as ranking-table
		// cells (driver_<n>_signal / driver_<n>_weight_contributed), so
		// they are re-indexed from there rather than declared missing:
		// the number a driver-weight source names IS present in the
		// projection, one hop away, and a reader can see it.
		for _, row := range projection.Cohort.RankingTable {
			id, ok := RankingRowSubjectID(row)
			if !ok {
				continue
			}
			signals, exists := sources.driverWeight[id]
			if !exists {
				signals = map[string]float64{}
				sources.driverWeight[id] = signals
			}
			for key, value := range row.Fields {
				name, isSignal := strings.CutPrefix(key, "driver_")
				if !isSignal || !strings.HasSuffix(name, "_signal") || value.String == nil {
					continue
				}
				weightKey := "driver_" + strings.TrimSuffix(name, "_signal") + "_weight_contributed"
				weight, ok := row.Fields[weightKey]
				if !ok || weight.Number == nil {
					continue
				}
				signals[*value.String] = *weight.Number
			}
		}
	}
	for _, fact := range projection.KeyFacts {
		sources.factRows[fact.ClaimID] = fact.renderableRows()
		if holdsThemeShareTable(fact.Table, fact.TimeSeriesRows) {
			sources.themeShareClaims[fact.ClaimID] = struct{}{}
		}
	}
	return sources
}

// ValidateRenderShapesForResult is the exported entry point a PRODUCER of
// render shapes uses to prove its own output before attaching it.
//
// It is the same check ContextFabricInvestigationResult.Validate runs, made
// callable on its own so the selector in internal/contextfabric can be
// tested against the resolve-and-compare rule directly, without also having
// to construct a fully valid investigation result. Keeping one
// implementation behind two entry points is the point: a second, laxer copy
// for producers is exactly how a chart number would eventually stop being
// checked.
func ValidateRenderShapesForResult(result ContextFabricInvestigationResult) error {
	return validateRenderShapes(result.RenderShapes, renderShapeSourcesFromResult(result))
}
