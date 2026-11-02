package v1

import "fmt"

// validateQuadrantShape holds a quadrant to what teams' positions on one day mean
// (CHAOS-12623; the CHAOS-4611 quadrant). Every point has already resolved to its
// source and equals it exactly. What is left:
//
//   - the rule and the kind name each other;
//   - a category axis and no value_unit: the two measures have two units, and the
//     words of each carry it;
//   - exactly two series, x then y, keyed by the one allowed field pair
//     (ContextFabricRenderLandscapeXField, ContextFabricRenderLandscapeYField) and
//     labelled with each field's served word, which are also the axis words;
//   - the same point labels in the same order in both series, at least 2 of them:
//     a point is one team;
//   - every point cites a claimed_fact_value of its series' field, on the
//     landscape kind (the resolver refuses a derived one), about a team; the x and y of one label cite
//     claims about one team, and no team is two points;
//   - every cited claim carries its fact's area_breakdown row, and all of them
//     name one as_of_day: a quadrant of different days places teams that were
//     never measured together;
//   - values are 0 or more.
func validateQuadrantShape(shape ContextFabricRenderShape, sources renderShapeSources) error {
	if (shape.Kind == ContextFabricRenderKindQuadrant) != (shape.SelectedBy == ContextFabricRenderRuleLandscapePosition) {
		return fmt.Errorf("render shape %q: selected_by %q cannot yield kind %q", shape.ShapeID, shape.SelectedBy, shape.Kind)
	}
	if shape.Kind != ContextFabricRenderKindQuadrant {
		return nil
	}
	if shape.AxisKind != ContextFabricRenderAxisCategory {
		return fmt.Errorf("render shape %q: a quadrant needs a category axis", shape.ShapeID)
	}
	if shape.ValueUnit != "" {
		return fmt.Errorf("render shape %q: a quadrant's two measures have two units, so it declares no value_unit", shape.ShapeID)
	}
	if len(shape.Series) != 2 || shape.Series[0].Key != ContextFabricRenderLandscapeXField || shape.Series[1].Key != ContextFabricRenderLandscapeYField {
		return fmt.Errorf("render shape %q: a quadrant holds the series %q then %q", shape.ShapeID, ContextFabricRenderLandscapeXField, ContextFabricRenderLandscapeYField)
	}
	x, y := shape.Series[0], shape.Series[1]
	xWord, _ := ContextFabricVocabularyLabel(ContextFabricVocabularyFactField, x.Key)
	yWord, _ := ContextFabricVocabularyLabel(ContextFabricVocabularyFactField, y.Key)
	if x.Label != xWord || y.Label != yWord || shape.AxisLabel != xWord || shape.ValueLabel != yWord {
		return fmt.Errorf("render shape %q: a quadrant's series and axis words are the served words of its two fields", shape.ShapeID)
	}
	if len(x.Points) < 2 || len(x.Points) != len(y.Points) {
		return fmt.Errorf("render shape %q: a quadrant places 2 or more teams, each with an x and a y", shape.ShapeID)
	}
	day := ""
	teams := make(map[string]struct{}, len(x.Points))
	for index := range x.Points {
		xPoint, yPoint := x.Points[index], y.Points[index]
		if xPoint.Label != yPoint.Label {
			return fmt.Errorf("render shape %q: point %d is %q on x and %q on y; a point is one team on both axes", shape.ShapeID, index, xPoint.Label, yPoint.Label)
		}
		xTeam, xDay, err := quadrantPointClaim(shape.ShapeID, xPoint, x.Key, sources)
		if err != nil {
			return err
		}
		yTeam, yDay, err := quadrantPointClaim(shape.ShapeID, yPoint, y.Key, sources)
		if err != nil {
			return err
		}
		if xTeam != yTeam {
			return fmt.Errorf("render shape %q point %q: its x and y cite claims about two teams", shape.ShapeID, xPoint.Label)
		}
		if _, repeated := teams[xTeam]; repeated {
			return fmt.Errorf("render shape %q point %q: team %q is already a point", shape.ShapeID, xPoint.Label, xTeam)
		}
		teams[xTeam] = struct{}{}
		for _, pointDay := range []string{xDay, yDay} {
			if day == "" {
				day = pointDay
			}
			if pointDay != day {
				return fmt.Errorf("render shape %q point %q: its claims speak for %s, and another point's for %s; a quadrant places teams measured on one day", shape.ShapeID, xPoint.Label, pointDay, day)
			}
		}
	}
	return nil
}

// quadrantPointClaim checks one point's cited claim and returns its team and the
// day its value speaks for.
func quadrantPointClaim(shapeID string, point ContextFabricRenderPoint, field string, sources renderShapeSources) (string, string, error) {
	// The kind is what makes claimValues safe to read: the resolver has already
	// refused a claimed_fact_value source this answer lacks.
	if point.Source.Kind != ContextFabricRenderSourceClaimedFactValue {
		return "", "", fmt.Errorf("render shape %q point %q: a quadrant point must cite a %q source", shapeID, point.Label, ContextFabricRenderSourceClaimedFactValue)
	}
	claim := sources.claimValues[point.Source.ClaimID]
	// The resolver has already refused a derived claim, which holds no stored
	// value.
	if claim.field != field || claim.kind != ContextFabricFactLandscape {
		return "", "", fmt.Errorf("render shape %q point %q: a quadrant point cites a stated landscape claim of %q", shapeID, point.Label, field)
	}
	if claim.subject.Kind != ContextFabricSubjectTeam {
		return "", "", fmt.Errorf("render shape %q point %q: a quadrant places teams, and the claim is about a %s", shapeID, point.Label, claim.subject.Kind)
	}
	if point.Value < 0 {
		return "", "", fmt.Errorf("render shape %q point %q: a landscape measure is 0 or more", shapeID, point.Label)
	}
	day, dated := sources.landscapeDays[point.Source.ClaimID]
	if !dated {
		return "", "", fmt.Errorf("render shape %q point %q: its claim carries no %s row to say which day it speaks for", shapeID, point.Label, ContextFabricRenderLandscapeTableField)
	}
	return claim.subject.CanonicalID, day, nil
}

// landscapeRowDay is the as_of_day of a claim's one area_breakdown row, when the
// claim carries that declared table and exactly one row of it. The point's own
// check holds the claim to the landscape kind.
func landscapeRowDay(table *ContextFabricClaimedFactTable, rows []ContextFabricClaimedFactRow) (string, bool) {
	if table == nil || table.Field != ContextFabricRenderLandscapeTableField || len(rows) != 1 {
		return "", false
	}
	if len(table.Key) != 1 || table.Key[0] != ContextFabricRenderLandscapeDayField {
		return "", false
	}
	day := rows[0].Fields[ContextFabricRenderLandscapeDayField].String
	if day == nil || *day == "" {
		return "", false
	}
	return *day, true
}
