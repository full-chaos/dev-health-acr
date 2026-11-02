package contextfabric

import (
	"slices"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The words of the quadrant (CHAOS-12626, the CHAOS-4611 quadrant). They are
// served, never derived from a model. The axis and series words are the served
// words of the two fields, which the contract holds them to.
const (
	landscapeTitlePrefix = "Churn and delivery units, 30 days to "
	// landscapeMinIdentities is the person floor: a team of one identity is one
	// person's numbers, so it gives no point (lead ruling 5 on CHAOS-4611).
	landscapeMinIdentities = 2
	// landscapeIdentityField is the row cell the floor reads.
	landscapeIdentityField = "identity_count"
)

// landscapeSide is one team's point: its x and y claims, on one fact, and the
// day and cells of the one area_breakdown row they carry.
type landscapeSide struct {
	label        string
	day          string
	x, y         ClaimedFact
	xCell, yCell float64
}

// landscapePositionShape is where teams stand on churn and delivery units on one
// day, for a comparison of teams that asks for landscape facts.
//
// It reads the plan, the interpretation's own fact requirements, the committed
// subjects and the claimed facts only: a number reaches the chart because the
// answer already claims it, as for the comparison table (option A, CHAOS-12513).
// It fires only when the plan authorizes a quadrant, the question asks for
// landscape facts, and 2 or more committed teams each claim both measures from
// one fact on one shared day. Every other comparison gets no quadrant (check 10).
//
// A side gives no point when it does not claim both measures from one fact, when
// it claims one of them twice, when its row is not the declared area_breakdown
// row holding those same values, or when its row holds fewer than 2 identities.
// Such a side is counted (the second return), never drawn as zero (check 12). The
// sides that give a point must all speak for one day: a quadrant of different
// days places teams that were never measured together, so it is refused whole.
func landscapePositionShape(result InvestigationResult) (*contractsv1.ContextFabricRenderShape, int, RenderShapeSkipReason) {
	if result.AnswerPlan == nil || !slices.Contains(result.AnswerPlan.RenderKinds, contractsv1.ContextFabricRenderKindQuadrant) {
		return nil, 0, RenderShapeSkipNotPlanAuthorized
	}
	if !asksForLandscape(result.Interpretation.FactRequirements) {
		return nil, 0, RenderShapeSkipNoLandscapeRequirement
	}
	sides := result.SubjectResolution.Committed
	switch {
	case len(sides) < 2:
		return nil, 0, RenderShapeSkipFewerThanTwoSides
	case len(sides) > contractsv1.ContextFabricRenderPointsMaxCount:
		return nil, 0, RenderShapeSkipTooManySides
	}
	for _, side := range sides {
		if side.Kind != contractsv1.ContextFabricSubjectTeam {
			return nil, 0, RenderShapeSkipSubjectKindNotCharted
		}
	}
	placed := make([]landscapeSide, 0, len(sides))
	leftOut := 0
	for _, side := range sides {
		point, ok := landscapeSideOf(result.ClaimedFacts, side)
		if !ok {
			leftOut++
			continue
		}
		placed = append(placed, point)
	}
	if len(placed) < 2 {
		return nil, leftOut, RenderShapeSkipFewerThanTwoPoints
	}
	for _, side := range placed[1:] {
		if side.day != placed[0].day {
			return nil, leftOut, RenderShapeSkipLandscapeDaysDiffer
		}
	}
	shape := landscapeShape(placed)
	// The class rule, as for the treemap and the table: a chart never costs an
	// answer. A shape the contract would refuse is no chart.
	proved := *shape
	proved.ShapeID = "rs_proof"
	probe := result
	probe.RenderShapes = []contractsv1.ContextFabricRenderShape{proved}
	if err := contractsv1.ValidateRenderShapesForResult(probe); err != nil {
		return nil, leftOut, RenderShapeSkipQuadrantUnprovable
	}
	return shape, leftOut, ""
}

// asksForLandscape reads the interpretation's own requirements, not the facts
// read: a status answer reads landscape facts by composition, and that is no
// question about where teams stand.
func asksForLandscape(requirements []FactRequirement) bool {
	for _, requirement := range requirements {
		if requirement.Kind == FactLandscape {
			return true
		}
	}
	return false
}

// landscapeSideOf finds one team's point among the claims, or reports there is
// none.
func landscapeSideOf(claims []ClaimedFact, side SubjectRef) (landscapeSide, bool) {
	var x, y []ClaimedFact
	for _, claim := range claims {
		// A canonical id carries its kind ("team:..."), so one id is one subject.
		if claim.Kind != FactLandscape || claim.Subject.CanonicalID != side.CanonicalID || len(claim.DerivedFrom) > 0 {
			continue
		}
		switch claim.Field {
		case contractsv1.ContextFabricRenderLandscapeXField:
			x = append(x, claim)
		case contractsv1.ContextFabricRenderLandscapeYField:
			y = append(y, claim)
		}
	}
	// Two claims of one measure: which one is right is not this rule's call.
	if len(x) != 1 || len(y) != 1 {
		return landscapeSide{}, false
	}
	// The same identity rule as the treemap's (CHAOS-12591): one fact, one row.
	if !sameThemeBreakdown(&x[0], &y[0]) {
		return landscapeSide{}, false
	}
	day, identities, cells, ok := landscapeRowOf(x[0])
	if !ok || identities < landscapeMinIdentities {
		return landscapeSide{}, false
	}
	xCell, xHeld := cells[contractsv1.ContextFabricRenderLandscapeXField]
	yCell, yHeld := cells[contractsv1.ContextFabricRenderLandscapeYField]
	if !xHeld || !yHeld || !claimHolds(x[0], xCell) || !claimHolds(y[0], yCell) {
		return landscapeSide{}, false
	}
	return landscapeSide{label: x[0].Subject.Label, day: day, x: x[0], y: y[0], xCell: xCell, yCell: yCell}, true
}

// landscapeRowOf reads a claim's one declared area_breakdown row: its day, its
// identity count, and the integer cells of the measures the table declares.
func landscapeRowOf(claim ClaimedFact) (string, int64, map[string]float64, bool) {
	table := claim.Table
	if table == nil || table.Field != contractsv1.ContextFabricRenderLandscapeTableField || len(claim.Rows) != 1 ||
		len(table.Key) != 1 || table.Key[0] != contractsv1.ContextFabricRenderLandscapeDayField {
		return "", 0, nil, false
	}
	fields := claim.Rows[0].Fields
	// A cell that is not a string reads as "".
	day, _ := renderStringCell(fields[contractsv1.ContextFabricRenderLandscapeDayField])
	identities := fields[landscapeIdentityField].Integer
	if day == "" || identities == nil {
		return "", 0, nil, false
	}
	cells := map[string]float64{}
	for _, measure := range table.Measures {
		cell := fields[measure]
		if cell.Integer == nil {
			continue
		}
		if value, numeric := renderNumericCell(cell); numeric {
			cells[measure] = value
		}
	}
	return day, *identities, cells, true
}

// claimHolds reports that the claim states the row's own integer cell.
func claimHolds(claim ClaimedFact, cell float64) bool {
	if claim.Value.Integer == nil {
		return false
	}
	stated, numeric := renderNumericCell(claim.Value)
	return numeric && stated == cell
}

// landscapeShape draws the x series then the y series, one point per side in
// committed order, which is the order of the question's own operands. The words
// are the fields' served words; a field with none gives an empty word, which the
// contract refuses, so the shape is never served.
func landscapeShape(sides []landscapeSide) *contractsv1.ContextFabricRenderShape {
	xWord, _ := contractsv1.ContextFabricVocabularyLabel(contractsv1.ContextFabricVocabularyFactField, contractsv1.ContextFabricRenderLandscapeXField)
	yWord, _ := contractsv1.ContextFabricVocabularyLabel(contractsv1.ContextFabricVocabularyFactField, contractsv1.ContextFabricRenderLandscapeYField)
	x := contractsv1.ContextFabricRenderSeries{Key: contractsv1.ContextFabricRenderLandscapeXField, Label: xWord}
	y := contractsv1.ContextFabricRenderSeries{Key: contractsv1.ContextFabricRenderLandscapeYField, Label: yWord}
	for _, side := range sides {
		x.Points = append(x.Points, landscapePoint(side.label, side.x, side.xCell))
		y.Points = append(y.Points, landscapePoint(side.label, side.y, side.yCell))
	}
	return &contractsv1.ContextFabricRenderShape{
		Kind:       contractsv1.ContextFabricRenderKindQuadrant,
		SelectedBy: contractsv1.ContextFabricRenderRuleLandscapePosition,
		Title:      landscapeTitlePrefix + landscapeDayWords(sides[0].day),
		AxisKind:   contractsv1.ContextFabricRenderAxisCategory,
		AxisLabel:  xWord,
		ValueLabel: yWord,
		Series:     []contractsv1.ContextFabricRenderSeries{x, y},
	}
}

func landscapePoint(label string, claim ClaimedFact, value float64) contractsv1.ContextFabricRenderPoint {
	return contractsv1.ContextFabricRenderPoint{
		Label: label,
		Value: value,
		Source: contractsv1.ContextFabricRenderPointSource{
			Kind:    contractsv1.ContextFabricRenderSourceClaimedFactValue,
			ClaimID: claim.ClaimID,
			Field:   claim.Field,
		},
	}
}

// landscapeDayWords states the day as a reader says it, "Nov 2, 2026". The
// year stays: a team's newest day can be months old. A day that is not a date
// is stated as stored.
func landscapeDayWords(day string) string {
	parsed, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return day
	}
	return parsed.Format("Jan 2, 2006")
}

// withLandscapeLimitation states the quadrant's served line exactly while the
// shapes hold the quadrant. Finalization runs again on every pass, and a later
// pass can draw no quadrant, so the line is removed first and added back only
// with the shape. It returns how many model caveats the line displaced, as
// appendBoundedLimitations counts them.
func withLandscapeLimitation(limitations []string, shapes []contractsv1.ContextFabricRenderShape) ([]string, int) {
	limitations = withoutLimitation(limitations, contractsv1.ContextFabricLandscapePositionLimitation)
	if !hasLandscapePosition(shapes) {
		return limitations, 0
	}
	return appendBoundedLimitations(limitations, []string{contractsv1.ContextFabricLandscapePositionLimitation})
}

// withoutLimitation removes every copy of one sentence.
func withoutLimitation(limitations []string, sentence string) []string {
	if !slices.Contains(limitations, sentence) {
		return limitations
	}
	kept := make([]string, 0, len(limitations))
	for _, limitation := range limitations {
		if limitation != sentence {
			kept = append(kept, limitation)
		}
	}
	return kept
}

// hasLandscapePosition reports that the shapes hold the quadrant.
func hasLandscapePosition(shapes []contractsv1.ContextFabricRenderShape) bool {
	for _, shape := range shapes {
		if shape.SelectedBy == contractsv1.ContextFabricRenderRuleLandscapePosition {
			return true
		}
	}
	return false
}
