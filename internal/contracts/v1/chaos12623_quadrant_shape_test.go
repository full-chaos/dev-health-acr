package v1

import (
	"fmt"
	"strings"
	"testing"
)

// CHAOS-12623: the wire shape of the landscape quadrant (the CHAOS-4611 quadrant).
// Two series, churn then delivery units, one point per team, each value one
// team claim's own, all on one day. Each test plants the defect one clause exists
// to catch, and matches its own message.

// quadrantTeam is one team of the fixture: its claims on both measures carry
// the one area_breakdown row of its landscape fact.
type quadrantTeam struct {
	id, label, day string
	churn, units   int64
}

type quadrantFixture struct {
	claims []ContextFabricClaimedFact
	shape  ContextFabricRenderShape
}

func landscapeAreaTable() *ContextFabricClaimedFactTable {
	return &ContextFabricClaimedFactTable{
		Field: "area_breakdown", Shape: ContextFabricFactTableShapeBreakdown, Key: []string{"as_of_day"},
		Measures: []string{"identity_count", "churn_loc_30d", "delivery_units_30d"},
	}
}

func newQuadrantFixture(teams []quadrantTeam) *quadrantFixture {
	f := &quadrantFixture{}
	xWord, _ := ContextFabricVocabularyLabel(ContextFabricVocabularyFactField, "churn_loc_30d")
	yWord, _ := ContextFabricVocabularyLabel(ContextFabricVocabularyFactField, "delivery_units_30d")
	x := ContextFabricRenderSeries{Key: "churn_loc_30d", Label: xWord}
	y := ContextFabricRenderSeries{Key: "delivery_units_30d", Label: yWord}
	for index, team := range teams {
		day, churn, units := team.day, team.churn, team.units
		row := ContextFabricClaimedFactRow{Fields: map[string]ContextFabricScalarValue{
			"as_of_day": {String: &day}, "identity_count": {Integer: int64Ptr(3)},
			"churn_loc_30d": {Integer: &churn}, "delivery_units_30d": {Integer: &units},
		}}
		subject := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: team.id, Label: team.label}
		for _, measure := range []struct {
			field  string
			value  *int64
			series *ContextFabricRenderSeries
		}{{"churn_loc_30d", &churn, &x}, {"delivery_units_30d", &units, &y}} {
			claimID := fmt.Sprintf("claim_quadrant_%d_%s", index, measure.field)
			f.claims = append(f.claims, ContextFabricClaimedFact{
				ClaimID: claimID, Kind: ContextFabricFactLandscape, Field: measure.field, Subject: subject,
				Value: ContextFabricScalarValue{Integer: measure.value}, Rows: []ContextFabricClaimedFactRow{row}, Table: landscapeAreaTable(),
			})
			measure.series.Points = append(measure.series.Points, ContextFabricRenderPoint{Label: team.label, Value: float64(*measure.value), Source: ContextFabricRenderPointSource{
				Kind: ContextFabricRenderSourceClaimedFactValue, ClaimID: claimID, Field: measure.field,
			}})
		}
	}
	f.shape = ContextFabricRenderShape{
		ShapeID: "rs_landscape_position", Kind: ContextFabricRenderKindQuadrant, SelectedBy: ContextFabricRenderRuleLandscapePosition,
		Title: "Churn and delivery units, 30 days to Nov 2", AxisKind: ContextFabricRenderAxisCategory,
		AxisLabel: xWord, ValueLabel: yWord, Series: []ContextFabricRenderSeries{x, y},
	}
	return f
}

func (f *quadrantFixture) result() ContextFabricInvestigationResult {
	return ContextFabricInvestigationResult{ClaimedFacts: f.claims}
}

func (f *quadrantFixture) validate() error {
	return validateRenderShapes([]ContextFabricRenderShape{f.shape}, renderShapeSourcesFromResult(f.result()))
}

func quadrantTeams() []quadrantTeam {
	return []quadrantTeam{
		{"team:platform", "Platform", "2026-11-02", 2400, 31},
		{"team:payments", "Payments", "2026-11-02", 900, 12},
		{"team:mobile", "Mobile", "2026-11-02", 0, 4},
	}
}

func TestAQuadrantOfTeamsOnOneDayIsAccepted(t *testing.T) {
	t.Parallel()
	f := newQuadrantFixture(quadrantTeams())
	if err := f.validate(); err != nil {
		t.Fatalf("three teams on one day, raw values, one claim each: %v", err)
	}
}

func TestAQuadrantIsRefused(t *testing.T) {
	t.Parallel()
	for name, plant := range map[string]struct {
		edit func(*quadrantFixture)
		want string
	}{
		// A rule and a kind no other clause binds, so only this rule's own
		// pairing can refuse them.
		"a quadrant under another rule": {func(f *quadrantFixture) { f.shape.SelectedBy = ContextFabricRenderRuleDatedFactTrend }, "selected_by"},
		"the rule on another kind":      {func(f *quadrantFixture) { f.shape.Kind = ContextFabricRenderKindBurndown }, "selected_by"},
		"a time axis":                   {func(f *quadrantFixture) { f.shape.AxisKind = ContextFabricRenderAxisTime }, "a quadrant needs a category axis"},
		"a value unit":                  {func(f *quadrantFixture) { f.shape.ValueUnit = ContextFabricRenderValueUnitRatio }, "declares no value_unit"},
		"one series":                    {func(f *quadrantFixture) { f.shape.Series = f.shape.Series[:1] }, "a quadrant holds the series"},
		"the axes swapped": {func(f *quadrantFixture) {
			f.shape.Series[0], f.shape.Series[1] = f.shape.Series[1], f.shape.Series[0]
		}, "a quadrant holds the series"},
		"a normalized x":      {func(f *quadrantFixture) { f.shape.Series[0].Key = "x_norm" }, "a quadrant holds the series"},
		"a normalized y":      {func(f *quadrantFixture) { f.shape.Series[1].Key = "y_norm" }, "a quadrant holds the series"},
		"another series word": {func(f *quadrantFixture) { f.shape.Series[1].Label = "Throughput" }, "served words of its two fields"},
		"another axis word":   {func(f *quadrantFixture) { f.shape.AxisLabel = "Churn" }, "served words of its two fields"},
		"one team": {func(f *quadrantFixture) {
			f.shape.Series[0].Points = f.shape.Series[0].Points[:1]
			f.shape.Series[1].Points = f.shape.Series[1].Points[:1]
		}, "2 or more teams"},
		"a team with no y": {func(f *quadrantFixture) { f.shape.Series[1].Points = f.shape.Series[1].Points[:2] }, "2 or more teams"},
		"the points out of order": {func(f *quadrantFixture) {
			points := f.shape.Series[1].Points
			points[0], points[1] = points[1], points[0]
		}, "a point is one team on both axes"},
		"a point citing a row": {func(f *quadrantFixture) {
			f.shape.Series[0].Points[0].Source = ContextFabricRenderPointSource{Kind: ContextFabricRenderSourceClaimedFactRow, ClaimID: "claim_quadrant_0_churn_loc_30d", RowIndex: intPtr(0), Field: "churn_loc_30d"}
		}, "must cite a"},
		"x citing the y claim": {func(f *quadrantFixture) {
			f.shape.Series[0].Points[0].Source.ClaimID = "claim_quadrant_0_delivery_units_30d"
			f.shape.Series[0].Points[0].Source.Field = "delivery_units_30d"
			f.shape.Series[0].Points[0].Value = 31
		}, "a stated landscape claim of"},
		"another fact kind": {func(f *quadrantFixture) { f.claims[0].Kind = ContextFabricFactFlow }, "a stated landscape claim of"},
		"a derived claim": {func(f *quadrantFixture) {
			f.claims[0].DerivedFrom = []ContextFabricDerivationInput{{FactID: "fact_one", Field: "churn_loc_30d"}}
		}, "derived"},
		"a project's claims": {func(f *quadrantFixture) {
			f.claims[0].Subject.Kind, f.claims[1].Subject.Kind = ContextFabricSubjectProject, ContextFabricSubjectProject
		}, "a quadrant places teams"},
		"x and y of two teams": {func(f *quadrantFixture) {
			f.shape.Series[1].Points[0].Source.ClaimID = "claim_quadrant_1_delivery_units_30d"
			f.shape.Series[1].Points[0].Value = 12
		}, "claims about two teams"},
		"one team twice": {func(f *quadrantFixture) {
			f.shape.Series[0].Points[1].Source.ClaimID = "claim_quadrant_0_churn_loc_30d"
			f.shape.Series[0].Points[1].Value = 2400
			f.shape.Series[1].Points[1].Source.ClaimID = "claim_quadrant_0_delivery_units_30d"
			f.shape.Series[1].Points[1].Value = 31
		}, "is already a point"},
		"a negative value": {func(f *quadrantFixture) {
			*f.claims[2].Value.Integer = -1
			f.shape.Series[0].Points[1].Value = -1
		}, "0 or more"},
		"no area_breakdown row": {func(f *quadrantFixture) { f.claims[0].Rows, f.claims[0].Table = nil, nil }, "no area_breakdown row"},
		"another table": {func(f *quadrantFixture) {
			f.claims[0].Table = landscapeAreaTable()
			f.claims[0].Table.Field = "map_coordinates"
		}, "no area_breakdown row"},
		"a table keyed otherwise": {func(f *quadrantFixture) {
			f.claims[0].Table = landscapeAreaTable()
			f.claims[0].Table.Key = []string{"team_id"}
		}, "no area_breakdown row"},
		"two rows": {func(f *quadrantFixture) {
			f.claims[0].Rows = append(f.claims[0].Rows, f.claims[0].Rows[0])
		}, "no area_breakdown row"},
		"an empty day": {func(f *quadrantFixture) {
			day := ""
			f.claims[0].Rows = []ContextFabricClaimedFactRow{{Fields: map[string]ContextFabricScalarValue{"as_of_day": {String: &day}}}}
		}, "no area_breakdown row"},
		"a row with no day": {func(f *quadrantFixture) {
			f.claims[0].Rows = []ContextFabricClaimedFactRow{{Fields: map[string]ContextFabricScalarValue{"identity_count": {Integer: int64Ptr(3)}}}}
		}, "no area_breakdown row"},
		"a team on another day": {func(f *quadrantFixture) {
			day := "2026-10-07"
			for _, index := range []int{4, 5} {
				f.claims[index].Rows = []ContextFabricClaimedFactRow{{Fields: map[string]ContextFabricScalarValue{"as_of_day": {String: &day}}}}
			}
		}, "teams measured on one day"},
		"x and y of one team on two days": {func(f *quadrantFixture) {
			day := "2026-10-07"
			f.claims[1].Rows = []ContextFabricClaimedFactRow{{Fields: map[string]ContextFabricScalarValue{"as_of_day": {String: &day}}}}
		}, "teams measured on one day"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newQuadrantFixture(quadrantTeams())
			plant.edit(f)
			err := f.validate()
			if err == nil || !strings.Contains(err.Error(), plant.want) {
				t.Fatalf("validate() = %v, want an error containing %q", err, plant.want)
			}
		})
	}
}

// A quadrant reaches the projection: the projected key facts carry the same
// claims and rows, and the shape validates there too.
func TestAQuadrantResolvesOnTheProjectionPath(t *testing.T) {
	t.Parallel()
	f := newQuadrantFixture(quadrantTeams())
	var projection ContextFabricAnswerProjection
	for _, claim := range f.claims {
		projection.KeyFacts = append(projection.KeyFacts, ContextFabricProjectedFact{
			ClaimID: claim.ClaimID, Kind: claim.Kind, Field: claim.Field, Subject: claim.Subject, Value: claim.Value, Rows: claim.Rows, Table: claim.Table,
		})
	}
	if err := validateRenderShapes([]ContextFabricRenderShape{f.shape}, renderShapeSourcesFromProjection(projection)); err != nil {
		t.Fatalf("the projection refused the quadrant: %v", err)
	}
}
