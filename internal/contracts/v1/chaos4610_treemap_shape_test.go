package v1

import (
	"strings"
	"testing"
)

// CHAOS-4610: the wire shape of the first treemap. A treemap carries the series
// payload: one series of part-of-whole points, each a verbatim copy of one row
// of one claimed fact. Each test plants the defect one clause exists to catch.

const themeShareClaim = "claim_theme_share_fixture"

var themeShareTokens = []string{"feature_delivery", "operational", "maintenance", "quality", "risk"}

func themeShareResult(shares ...float64) ContextFabricInvestigationResult {
	rows := make([]ContextFabricClaimedFactRow, 0, len(shares))
	for index, share := range shares {
		rows = append(rows, ContextFabricClaimedFactRow{Fields: map[string]ContextFabricScalarValue{
			"theme":           {String: &themeShareTokens[index]},
			"share":           {Number: float64Ptr(share)},
			"weighted_effort": {Number: float64Ptr(share)},
		}})
	}
	return ContextFabricInvestigationResult{ClaimedFacts: []ContextFabricClaimedFact{{
		ClaimID: themeShareClaim, Kind: "investment", Field: "theme_feature_delivery", Rows: rows, Table: themeShareTable(),
		Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectRepository, CanonicalID: "repo:acme/widgets", Label: "acme/widgets"},
	}}}
}

// themeShareTable is the declaration the real producer serves.
func themeShareTable() *ContextFabricClaimedFactTable {
	return &ContextFabricClaimedFactTable{
		Field: "theme_breakdown", Shape: ContextFabricFactTableShapeBreakdown, Key: []string{"theme"},
		Measures: []string{"share", "weighted_effort"}, Observations: []string{"source", "attribution"},
	}
}

func themeShareShape(shares ...float64) ContextFabricRenderShape {
	points := make([]ContextFabricRenderPoint, 0, len(shares))
	labels := []string{"Feature Delivery", "Operational / Support", "Maintenance / Tech Debt", "Quality / Reliability", "Risk / Security"}
	for index, share := range shares {
		points = append(points, ContextFabricRenderPoint{Label: labels[index], Value: share, Source: ContextFabricRenderPointSource{
			Kind: ContextFabricRenderSourceClaimedFactRow, ClaimID: themeShareClaim, RowIndex: intPtr(index), Field: "share",
		}})
	}
	return ContextFabricRenderShape{
		ShapeID: "rs_theme_share", Kind: ContextFabricRenderKindTreemap, SelectedBy: ContextFabricRenderRuleInvestmentThemeShare,
		Title: "Effort share by theme", AxisKind: ContextFabricRenderAxisCategory, AxisLabel: "Theme",
		ValueLabel: "Share of effort (ratio)", ValueUnit: ContextFabricRenderValueUnitRatio,
		Series: []ContextFabricRenderSeries{{Key: "share", Label: "Share of effort", Points: points}},
	}
}

func validateThemeShare(shape ContextFabricRenderShape, result ContextFabricInvestigationResult) error {
	return validateRenderShapes([]ContextFabricRenderShape{shape}, renderShapeSourcesFromResult(result))
}

func TestATreemapOfOneShareTableIsAccepted(t *testing.T) {
	t.Parallel()
	shares := []float64{0.5, 0.25, 0.25, 0, 0}
	if err := validateThemeShare(themeShareShape(shares...), themeShareResult(shares...)); err != nil {
		t.Fatalf("a measured zero is a point, and this shape is valid: %v", err)
	}
}

func TestATreemapIsRefused(t *testing.T) {
	t.Parallel()
	shares := []float64{0.5, 0.25, 0.25}
	for name, plant := range map[string]struct {
		edit func(*ContextFabricRenderShape)
		want string
	}{
		"a treemap under another rule": {func(s *ContextFabricRenderShape) { s.SelectedBy = ContextFabricRenderRuleDatedFactTrend }, "selected_by"},
		"the rule on another kind": {func(s *ContextFabricRenderShape) {
			s.Kind, s.Presentation = ContextFabricRenderKindSeries, ContextFabricRenderPresentationBars
		}, "selected_by"},
		"a time axis": {func(s *ContextFabricRenderShape) { s.AxisKind = ContextFabricRenderAxisTime }, "category"},
		"two series": {func(s *ContextFabricRenderShape) {
			s.Series = append(s.Series, ContextFabricRenderSeries{Key: "b", Label: "b", Points: s.Series[0].Points})
		}, "one series"},
		"no unit":       {func(s *ContextFabricRenderShape) { s.ValueUnit = "" }, "value_unit"},
		"unknown unit":  {func(s *ContextFabricRenderShape) { s.ValueUnit = "percent" }, "value_unit"},
		"another field": {func(s *ContextFabricRenderShape) { renderPointField(s, "weighted_effort") }, "must cite"},
		"two points on one row": {func(s *ContextFabricRenderShape) {
			s.Series[0].Points[1].Source.RowIndex = intPtr(0)
			s.Series[0].Points[1].Value = 0.5
		}, "each row once"},
		"a point from outside the table": {func(s *ContextFabricRenderShape) {
			s.Series[0].Points[1].Source = ContextFabricRenderPointSource{Kind: ContextFabricRenderSourceCohortMemberScore, SubjectCanonicalID: "team:a"}
			s.Series[0].Points[1].Value = 0.25
		}, "must cite"},
		"a row left out": {func(s *ContextFabricRenderShape) { s.Series[0].Points = s.Series[0].Points[:2] }, "every row"},
		"a label on the wrong row": {func(s *ContextFabricRenderShape) {
			s.Series[0].Points[0].Label, s.Series[0].Points[1].Label = s.Series[0].Points[1].Label, s.Series[0].Points[0].Label
		}, "served word"},
		"a raw token as the label": {func(s *ContextFabricRenderShape) { s.Series[0].Points[0].Label = "feature_delivery" }, "served word"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			shape := themeShareShape(shares...)
			plant.edit(&shape)
			result := themeShareResult(shares...)
			result.Cohort = &ContextFabricCohort{Members: []ContextFabricCohortMember{{
				Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:a", Label: "a"}, Score: float64Ptr(0.25),
			}}}
			err := validateThemeShare(shape, result)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), plant.want) {
				t.Fatalf("refused for another reason: %v", err)
			}
		})
	}
}

// renderPointField leaves every value in range, so only the field clause can
// refuse the shape.
func renderPointField(shape *ContextFabricRenderShape, field string) {
	for index := range shape.Series[0].Points {
		shape.Series[0].Points[index].Source.Field = field
	}
}

// A treemap plots the declared table and no other rows of the claim. A claim can
// carry a time series beside it, and its rows then resolve to the series.
func TestATreemapPlotsOnlyTheDeclaredShareTable(t *testing.T) {
	t.Parallel()
	shares := []float64{0.5, 0.5}
	for name, plant := range map[string]func(*ContextFabricClaimedFact){
		"no declared table":  func(fact *ContextFabricClaimedFact) { fact.Table = nil },
		"another table":      func(fact *ContextFabricClaimedFact) { fact.Table.Field = "daily_investment" },
		"not a breakdown":    func(fact *ContextFabricClaimedFact) { fact.Table.Shape = ContextFabricFactTableShapeTimeSeries },
		"share not measured": func(fact *ContextFabricClaimedFact) { fact.Table.Measures = []string{"weighted_effort"} },
		"a series in front": func(fact *ContextFabricClaimedFact) {
			fact.TimeSeriesRows = fact.Rows
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := themeShareResult(shares...)
			plant(&result.ClaimedFacts[0])
			if err := validateThemeShare(themeShareShape(shares...), result); err == nil || !strings.Contains(err.Error(), "declared") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

// The row decides the word. A row with no theme, or with a token outside the
// closed set, has no word to plot under.
func TestATreemapRowNeedsACanonicalTheme(t *testing.T) {
	t.Parallel()
	shares := []float64{0.5, 0.5}
	for name, cell := range map[string]ContextFabricScalarValue{
		"no theme":         {},
		"an unknown one":   {String: stringPtrForTreemap("unassigned")},
		"a number instead": {Number: float64Ptr(1)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := themeShareResult(shares...)
			if cell.String == nil && cell.Number == nil {
				delete(result.ClaimedFacts[0].Rows[0].Fields, "theme")
			} else {
				result.ClaimedFacts[0].Rows[0].Fields["theme"] = cell
			}
			if err := validateThemeShare(themeShareShape(shares...), result); err == nil || !strings.Contains(err.Error(), "theme") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func stringPtrForTreemap(value string) *string { return &value }

// The agreed shape passes on both paths a shape is checked on.
func TestATreemapResolvesOnTheProjectionPath(t *testing.T) {
	t.Parallel()
	shares := []float64{0.5, 0.25, 0.25, 0, 0}
	result := themeShareResult(shares...)
	projection := ContextFabricAnswerProjection{KeyFacts: []ContextFabricProjectedFact{{ClaimID: themeShareClaim, Rows: result.ClaimedFacts[0].Rows, Table: themeShareTable()}}}
	if err := validateRenderShapes([]ContextFabricRenderShape{themeShareShape(shares...)}, renderShapeSourcesFromProjection(projection)); err != nil {
		t.Fatal(err)
	}
	projection.KeyFacts[0].Rows = projection.KeyFacts[0].Rows[:3]
	if err := validateRenderShapes([]ContextFabricRenderShape{themeShareShape(shares[:2]...)}, renderShapeSourcesFromProjection(projection)); err == nil {
		t.Fatal("a shape of part of the projected rows was accepted")
	}
}

// A part of a whole is a share of one table. Two claims are two wholes.
func TestATreemapOverTwoClaimsIsRefused(t *testing.T) {
	t.Parallel()
	shares := []float64{0.5, 0.5}
	result := themeShareResult(shares...)
	other := result.ClaimedFacts[0]
	other.ClaimID = "claim_theme_share_other"
	result.ClaimedFacts = append(result.ClaimedFacts, other)
	shape := themeShareShape(shares...)
	shape.Series[0].Points[1].Source.ClaimID = other.ClaimID
	if err := validateThemeShare(shape, result); err == nil || !strings.Contains(err.Error(), "one claim") {
		t.Fatalf("got %v", err)
	}
}

func TestATreemapNeedsTwoPartsAboveZero(t *testing.T) {
	t.Parallel()
	shares := []float64{1, 0, 0}
	err := validateThemeShare(themeShareShape(shares...), themeShareResult(shares...))
	if err == nil || !strings.Contains(err.Error(), "above 0") {
		t.Fatalf("one part is no split: %v", err)
	}
}

func TestATreemapRefusesANegativeShare(t *testing.T) {
	t.Parallel()
	shares := []float64{0.75, 0.5, -0.25}
	err := validateThemeShare(themeShareShape(shares...), themeShareResult(shares...))
	if err == nil || !strings.Contains(err.Error(), "below 0") {
		t.Fatalf("got %v", err)
	}
}

// The unit is a claim about the numbers, on any kind that states it.
func TestARatioAboveOneIsRefused(t *testing.T) {
	t.Parallel()
	shares := []float64{1.5, 0.25}
	if err := validateThemeShare(themeShareShape(shares...), themeShareResult(shares...)); err == nil || !strings.Contains(err.Error(), "ratio") {
		t.Fatalf("treemap: %v", err)
	}
	for _, score := range []float64{46.7, -0.5} {
		series := scoreShape(score)
		series.ValueUnit = ContextFabricRenderValueUnitRatio
		result := renderShapeResultFixture(t)
		result.Cohort.Members[0].Score = float64Ptr(score)
		if err := validateRenderShapes([]ContextFabricRenderShape{series}, renderShapeSourcesFromResult(result)); err == nil || !strings.Contains(err.Error(), "ratio") {
			t.Fatalf("series at %v: %v", score, err)
		}
	}
}

// On a series no other clause reads the unit, so this reaches the vocabulary.
func TestAnUnknownUnitIsRefusedOnAnyKind(t *testing.T) {
	t.Parallel()
	series := scoreShape(46.7)
	series.ValueUnit = "percent"
	err := validateRenderShapes([]ContextFabricRenderShape{series}, renderShapeSourcesFromResult(renderShapeResultFixture(t)))
	if err == nil || !strings.Contains(err.Error(), "closed vocabulary") {
		t.Fatalf("got %v", err)
	}
}

func TestAShapeWithoutAUnitIsUnchanged(t *testing.T) {
	t.Parallel()
	if err := validateRenderShapes([]ContextFabricRenderShape{scoreShape(46.7)}, renderShapeSourcesFromResult(renderShapeResultFixture(t))); err != nil {
		t.Fatalf("a series without a unit stays valid: %v", err)
	}
}

// The vocabularies are closed, and a consumer reads them.
func TestTheVocabularyHoldsTheRuleAndTheUnit(t *testing.T) {
	t.Parallel()
	if !validContextFabricRenderShapeRule(ContextFabricRenderRuleInvestmentThemeShare) {
		t.Fatal("rule is not a member")
	}
	if got := ContextFabricRenderValueUnitVocabulary(); len(got) != 1 || got[0] != ContextFabricRenderValueUnitRatio {
		t.Fatalf("units %v", got)
	}
}
