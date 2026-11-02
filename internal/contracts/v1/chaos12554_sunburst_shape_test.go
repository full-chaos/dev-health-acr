package v1

import (
	"math"
	"slices"
	"strings"
	"testing"
)

// CHAOS-12554: the wire shape of the investment sunburst. Two rings over one
// claimed subcategory_breakdown table: the inner ring cites each theme's
// theme_share, and one ring per theme cites each subcategory's share. Each test
// plants the defect one clause exists to catch, and matches its own message.

const subcategoryClaim = "claim_subcategory_share_fixture"

// sunburstFixture is one subcategory table and the shape that plots it.
type sunburstFixture struct {
	rows  []ContextFabricClaimedFactRow
	table *ContextFabricClaimedFactTable
	shape ContextFabricRenderShape
	// field and value are the claim's own, which a claimed_fact_value source
	// reads.
	field string
	value ContextFabricScalarValue
}

// subcategoryShares gives every canonical subcategory a share, in vocabulary
// order; a subcategory missing from shares gets 0.
func newSunburstFixture(shares map[string]float64) *sunburstFixture {
	vocabulary := ContextFabricInvestmentSubcategoryVocabulary()
	totals := map[string]float64{}
	for _, subcategory := range vocabulary {
		theme, _ := ContextFabricInvestmentSubcategoryTheme(subcategory)
		totals[theme] += shares[subcategory]
	}
	f := &sunburstFixture{field: "subcategory_breakdown", table: &ContextFabricClaimedFactTable{
		Field: "subcategory_breakdown", Shape: ContextFabricFactTableShapeBreakdown, Key: []string{"subcategory"},
		Measures: []string{"share", "theme_share"}, Observations: []string{"theme"},
	}}
	inner := ContextFabricRenderSeries{Key: "themes", Label: "Themes"}
	rings := map[string]*ContextFabricRenderSeries{}
	var order []string
	for index, subcategory := range vocabulary {
		theme, _ := ContextFabricInvestmentSubcategoryTheme(subcategory)
		subcategory, themeToken := subcategory, theme
		f.rows = append(f.rows, ContextFabricClaimedFactRow{Fields: map[string]ContextFabricScalarValue{
			"subcategory": {String: &subcategory},
			"theme":       {String: &themeToken},
			"share":       {Number: float64Ptr(shares[subcategory])},
			"theme_share": {Number: float64Ptr(totals[theme])},
		}})
		word, _ := ContextFabricInvestmentThemeLabel(theme)
		ring, known := rings[theme]
		if !known {
			order = append(order, theme)
			ring = &ContextFabricRenderSeries{Key: theme, Label: word}
			rings[theme] = ring
			inner.Points = append(inner.Points, ContextFabricRenderPoint{Label: word, Value: totals[theme], Source: ContextFabricRenderPointSource{
				Kind: ContextFabricRenderSourceClaimedFactRow, ClaimID: subcategoryClaim, RowIndex: intPtr(index), Field: "theme_share",
			}})
		}
		label, _ := ContextFabricVocabularyLabel(ContextFabricVocabularySubcategory, subcategory)
		ring.Points = append(ring.Points, ContextFabricRenderPoint{Label: label, Value: shares[subcategory], Source: ContextFabricRenderPointSource{
			Kind: ContextFabricRenderSourceClaimedFactRow, ClaimID: subcategoryClaim, RowIndex: intPtr(index), Field: "share",
		}})
	}
	series := []ContextFabricRenderSeries{inner}
	for _, theme := range order {
		series = append(series, *rings[theme])
	}
	f.shape = ContextFabricRenderShape{
		ShapeID: "rs_subcategory_share", Kind: ContextFabricRenderKindSunburst, SelectedBy: ContextFabricRenderRuleInvestmentSubcategoryShare,
		Title: "Effort share by theme and subcategory", AxisKind: ContextFabricRenderAxisCategory, AxisLabel: "Subcategory",
		ValueLabel: "Share of effort (ratio)", ValueUnit: ContextFabricRenderValueUnitRatio, Series: series,
	}
	return f
}

func (f *sunburstFixture) result() ContextFabricInvestigationResult {
	return ContextFabricInvestigationResult{ClaimedFacts: []ContextFabricClaimedFact{{
		ClaimID: subcategoryClaim, Kind: "investment", Field: f.field, Value: f.value, Rows: f.rows, Table: f.table,
		Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectRepository, CanonicalID: "repo:acme/widgets", Label: "acme/widgets"},
	}}}
}

func (f *sunburstFixture) validate() error {
	return validateRenderShapes([]ContextFabricRenderShape{f.shape}, renderShapeSourcesFromResult(f.result()))
}

// series returns the shape's series keyed key.
func (f *sunburstFixture) series(key string) *ContextFabricRenderSeries {
	for index := range f.shape.Series {
		if f.shape.Series[index].Key == key {
			return &f.shape.Series[index]
		}
	}
	panic("no series " + key)
}

// row returns the table row of a subcategory.
func (f *sunburstFixture) row(subcategory string) *ContextFabricClaimedFactRow {
	for index := range f.rows {
		if *f.rows[index].Fields["subcategory"].String == subcategory {
			return &f.rows[index]
		}
	}
	panic("no row " + subcategory)
}

func sunburstShares() map[string]float64 {
	return map[string]float64{
		"feature_delivery.customer": 0.25, "feature_delivery.roadmap": 0.15, "maintenance.debt": 0.1,
		"operational.support": 0.2, "quality.bugfix": 0.2, "quality.testing": 0.05, "risk.security": 0.05,
	}
}

func TestASunburstOfOneSubcategoryTableIsAccepted(t *testing.T) {
	t.Parallel()
	f := newSunburstFixture(sunburstShares())
	if err := f.validate(); err != nil {
		t.Fatalf("a measured zero is a point, and this shape is valid: %v", err)
	}
	if len(f.shape.Series) != 6 || len(f.shape.Series[0].Points) != 5 {
		t.Fatalf("the fixture is not two rings over five themes: %d series", len(f.shape.Series))
	}
}

func TestASunburstIsRefused(t *testing.T) {
	t.Parallel()
	for name, plant := range map[string]struct {
		edit func(*sunburstFixture)
		want string
	}{
		"a sunburst under another rule": {func(f *sunburstFixture) { f.shape.SelectedBy = ContextFabricRenderRuleDatedFactTrend }, "selected_by"},
		"the rule on another kind": {func(f *sunburstFixture) {
			f.shape.Kind, f.shape.Presentation = ContextFabricRenderKindSeries, ContextFabricRenderPresentationBars
		}, "selected_by"},
		"a time axis":         {func(f *sunburstFixture) { f.shape.AxisKind = ContextFabricRenderAxisTime }, "a sunburst needs a category axis"},
		"no ratio unit":       {func(f *sunburstFixture) { f.shape.ValueUnit = "" }, "a sunburst needs a value_unit"},
		"an undeclared table": {func(f *sunburstFixture) { f.table.Field = "theme_breakdown" }, "plots a declared"},
		"a cut table": {func(f *sunburstFixture) {
			f.rows = f.rows[:len(f.rows)-1]
			ring := f.series("risk")
			ring.Points = ring.Points[:len(ring.Points)-1]
		}, "every canonical subcategory, and its table has 14 of 15"},
		"a subcategory that is not canonical": {func(f *sunburstFixture) {
			other := "risk.privacy"
			f.row("risk.vulnerability").Fields["subcategory"] = ContextFabricScalarValue{String: &other}
		}, "is not canonical"},
		"a row under another theme": {func(f *sunburstFixture) {
			other := "quality"
			f.row("risk.vulnerability").Fields["theme"] = ContextFabricScalarValue{String: &other}
		}, "not its own"},
		"a repeated subcategory": {func(f *sunburstFixture) {
			again := "risk.security"
			f.row("risk.vulnerability").Fields["subcategory"] = ContextFabricScalarValue{String: &again}
		}, "repeats subcategory"},
		"a row with no theme share": {func(f *sunburstFixture) { delete(f.row("risk.vulnerability").Fields, "theme_share") }, "is not a number"},
		"two theme shares for one theme": {func(f *sunburstFixture) {
			f.row("risk.vulnerability").Fields["theme_share"] = ContextFabricScalarValue{Number: float64Ptr(0.06)}
		}, "carry different theme shares"},
		"a theme with no ring":             {func(f *sunburstFixture) { f.shape.Series = f.shape.Series[:5] }, "one series per theme, 6 series, and has 5"},
		"a ring keyed by no theme":         {func(f *sunburstFixture) { f.series("risk").Key = "security" }, "the inner ring or a theme"},
		"a ring with another theme's word": {func(f *sunburstFixture) { f.series("risk").Label = "Quality / Reliability" }, "served word of its theme"},
		"an inner ring short of a theme": {func(f *sunburstFixture) {
			inner := f.series("themes")
			inner.Points = inner.Points[:4]
		}, "inner ring plots 5 themes, and has 4 points"},
		"an inner ring with a theme twice": {func(f *sunburstFixture) {
			inner := f.series("themes")
			// The risk point cites a quality row instead: quality twice, risk never.
			inner.Points[4].Source.RowIndex = intPtr(10)
			inner.Points[4].Value = *f.rows[10].Fields["theme_share"].Number
			inner.Points[4].Label = "Quality / Reliability twice"
		}, "plots theme \"quality\" twice"},
		"an inner ring label that is not its theme's": {func(f *sunburstFixture) {
			inner := f.series("themes")
			inner.Points[0].Label, inner.Points[1].Label = inner.Points[1].Label, inner.Points[0].Label
		}, "inner ring label must be the served word"},
		"an inner ring that cites a share": {func(f *sunburstFixture) {
			point := &f.series("themes").Points[0]
			point.Source.Field = "share"
			point.Value = *f.rows[*point.Source.RowIndex].Fields["share"].Number
		}, "must cite the \"theme_share\" field"},
		"a ring short of a subcategory": {func(f *sunburstFixture) {
			ring := f.series("risk")
			ring.Points = ring.Points[:2]
		}, "has 3 subcategories, and its ring has 2 points"},
		"a ring that cites theme shares": {func(f *sunburstFixture) {
			point := &f.series("risk").Points[0]
			point.Source.Field = "theme_share"
			point.Value = *f.rows[*point.Source.RowIndex].Fields["theme_share"].Number
		}, "must cite the \"share\" field"},
		"a subcategory under another theme's ring": {func(f *sunburstFixture) {
			quality, risk := f.series("quality"), f.series("risk")
			quality.Points[0], risk.Points[0] = risk.Points[0], quality.Points[0]
		}, "sits under theme"},
		"a subcategory twice": {func(f *sunburstFixture) {
			ring := f.series("risk")
			ring.Points[1].Source.RowIndex = ring.Points[0].Source.RowIndex
			ring.Points[1].Value = ring.Points[0].Value
		}, "plots each subcategory once"},
		"a point that cites the claim's value": {func(f *sunburstFixture) {
			f.field, f.value = "share", ContextFabricScalarValue{Number: float64Ptr(0.05)}
			point := &f.series("risk").Points[1]
			point.Source = ContextFabricRenderPointSource{Kind: ContextFabricRenderSourceClaimedFactValue, ClaimID: subcategoryClaim, Field: "share"}
			point.Value = 0.05
		}, "must cite the \"share\" field of a claimed fact row"},
		"a label that is not the subcategory's word": {func(f *sunburstFixture) { f.series("risk").Points[0].Label = "Compliance work" }, "served word of its subcategory"},
		"a sum off its theme share": {func(f *sunburstFixture) {
			for _, row := range f.rows {
				if *row.Fields["theme"].String == "quality" {
					row.Fields["theme_share"] = ContextFabricScalarValue{Number: float64Ptr(0.25 * (1 + 1e-9))}
				}
			}
			f.series("themes").Points[3].Value = 0.25 * (1 + 1e-9)
		}, "add up to"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSunburstFixture(sunburstShares())
			plant.edit(f)
			err := f.validate()
			if err == nil || !strings.Contains(err.Error(), plant.want) {
				t.Fatalf("validate() = %v, want an error containing %q", err, plant.want)
			}
		})
	}
}

// The sum check takes 1e-12 relative, and nothing else does: a theme share 1e-13
// off the sum of its subcategories passes, as float rounding does on real data.
func TestASunburstSumTakesTheNamedTolerance(t *testing.T) {
	t.Parallel()
	if ContextFabricRenderShareSumTolerance != 1e-12 {
		t.Fatalf("tolerance = %v, want the ruled 1e-12", ContextFabricRenderShareSumTolerance)
	}
	f := newSunburstFixture(sunburstShares())
	near := 0.25 * (1 + 1e-13)
	for _, row := range f.rows {
		if *row.Fields["theme"].String == "quality" {
			row.Fields["theme_share"] = ContextFabricScalarValue{Number: float64Ptr(near)}
		}
	}
	f.series("themes").Points[3].Value = near
	if err := f.validate(); err != nil {
		t.Fatalf("a theme share 1e-13 relative off its sum is float noise: %v", err)
	}
	// Relative, not absolute: on a theme of 1e-6, 1e-15 is 1e-9 relative, and is
	// refused although it is far under 1e-12 absolute.
	tiny := newSunburstFixture(map[string]float64{"risk.security": 1e-6, "quality.bugfix": 0.5})
	off := 1e-6 + 1e-15
	for _, row := range tiny.rows {
		if *row.Fields["theme"].String == "risk" {
			row.Fields["theme_share"] = ContextFabricScalarValue{Number: float64Ptr(off)}
		}
	}
	tiny.series("themes").Points[4].Value = off
	if err := tiny.validate(); err == nil || !strings.Contains(err.Error(), "add up to") {
		t.Fatalf("validate() = %v, want a relative refusal of a small theme", err)
	}
	// The plotted number is still exact: a point 1e-13 off its own cell is refused.
	f.series("themes").Points[3].Value = 0.25
	if err := f.validate(); err == nil || !strings.Contains(err.Error(), "never re-derived") {
		t.Fatalf("validate() = %v, want an exact-copy refusal for the plotted theme share", err)
	}
}

// A NaN on either side of the part-sum check fails it: no stored number is NaN,
// and a comparison that passed on one would let any sum through.
func TestPartsSumToWholeFailsOnNaN(t *testing.T) {
	t.Parallel()
	if partsSumToWhole(math.NaN(), 0.5) || partsSumToWhole(0.5, math.NaN()) {
		t.Fatal("partsSumToWhole passed a NaN")
	}
	if !partsSumToWhole(0.5, 0.5) || !partsSumToWhole(0, 0) {
		t.Fatal("partsSumToWhole refused an exact sum")
	}
}

func TestASunburstNeedsTwoSubcategoriesAboveZero(t *testing.T) {
	t.Parallel()
	f := newSunburstFixture(map[string]float64{"quality.bugfix": 1})
	if err := f.validate(); err == nil || !strings.Contains(err.Error(), "at least 2 subcategories above 0, and has 1") {
		t.Fatalf("validate() = %v, want a refusal of a one-part split", err)
	}
}

func TestASunburstRefusesANegativeShare(t *testing.T) {
	t.Parallel()
	shares := sunburstShares()
	shares["risk.compliance"] = -0.05
	shares["risk.security"] = 0.1
	f := newSunburstFixture(shares)
	if err := f.validate(); err == nil || !strings.Contains(err.Error(), "a sunburst value is below 0") {
		t.Fatalf("validate() = %v, want a refusal of a negative share", err)
	}
}

func TestASunburstOverTwoClaimsIsRefused(t *testing.T) {
	t.Parallel()
	f := newSunburstFixture(sunburstShares())
	result := f.result()
	second := result.ClaimedFacts[0]
	second.ClaimID = "claim_subcategory_share_other"
	result.ClaimedFacts = append(result.ClaimedFacts, second)
	f.series("risk").Points[0].Source.ClaimID = second.ClaimID
	err := validateRenderShapes([]ContextFabricRenderShape{f.shape}, renderShapeSourcesFromResult(result))
	if err == nil || !strings.Contains(err.Error(), "a sunburst plots one claim") {
		t.Fatalf("validate() = %v, want a one-claim refusal", err)
	}
}

func TestASunburstResolvesOnTheProjectionPath(t *testing.T) {
	t.Parallel()
	f := newSunburstFixture(sunburstShares())
	projection := ContextFabricAnswerProjection{KeyFacts: []ContextFabricProjectedFact{{ClaimID: subcategoryClaim, Rows: f.rows, Table: f.table}}}
	if err := validateRenderShapes([]ContextFabricRenderShape{f.shape}, renderShapeSourcesFromProjection(projection)); err != nil {
		t.Fatalf("the projection carries the table, so the shape resolves: %v", err)
	}
	projection.KeyFacts[0].Table = nil
	if err := validateRenderShapes([]ContextFabricRenderShape{f.shape}, renderShapeSourcesFromProjection(projection)); err == nil || !strings.Contains(err.Error(), "plots a declared") {
		t.Fatalf("validate() = %v, want the undeclared-table refusal on the projection path too", err)
	}
}

// Only a subcategory_breakdown breakdown keyed by subcategory, with both shares
// as measures and the theme as an observation, is the declared table.
func TestOnlyTheDeclaredSubcategoryTableIsHeld(t *testing.T) {
	t.Parallel()
	for name, edit := range map[string]func(*ContextFabricClaimedFactTable){
		"another field":           func(table *ContextFabricClaimedFactTable) { table.Field = "theme_breakdown" },
		"another shape":           func(table *ContextFabricClaimedFactTable) { table.Shape = ContextFabricFactTableShapeTimeSeries },
		"another key":             func(table *ContextFabricClaimedFactTable) { table.Key = []string{"theme"} },
		"no share measure":        func(table *ContextFabricClaimedFactTable) { table.Measures = []string{"theme_share"} },
		"no theme share measure":  func(table *ContextFabricClaimedFactTable) { table.Measures = []string{"share"} },
		"no theme observation":    func(table *ContextFabricClaimedFactTable) { table.Observations = nil },
		"a second key column too": func(table *ContextFabricClaimedFactTable) { table.Key = []string{"subcategory", "theme"} },
	} {
		t.Run(name, func(t *testing.T) {
			f := newSunburstFixture(sunburstShares())
			if !holdsSubcategoryShareTable(f.table, nil) {
				t.Fatal("the fixture's table is the declared table")
			}
			edit(f.table)
			if holdsSubcategoryShareTable(f.table, nil) {
				t.Fatalf("%s: holdsSubcategoryShareTable = true, want false", name)
			}
		})
	}
	f := newSunburstFixture(sunburstShares())
	if holdsSubcategoryShareTable(f.table, f.rows) {
		t.Fatal("with a time series beside it the claim's rows are the series, so the table is not held")
	}
}

// The canonical list is the ops taxonomy registry's, and every theme of it is a
// canonical theme with at least one subcategory. Its words are in the registry.
func TestTheSubcategoryVocabularyIsTheTaxonomy(t *testing.T) {
	t.Parallel()
	want := []string{
		"feature_delivery.customer", "feature_delivery.enablement", "feature_delivery.roadmap",
		"maintenance.debt", "maintenance.refactor", "maintenance.upgrade",
		"operational.incident_response", "operational.on_call", "operational.support",
		"quality.bugfix", "quality.reliability", "quality.testing",
		"risk.compliance", "risk.security", "risk.vulnerability",
	}
	vocabulary := ContextFabricInvestmentSubcategoryVocabulary()
	if !slices.Equal(vocabulary[:], want) {
		t.Fatalf("subcategories = %v, want the taxonomy's %v", vocabulary, want)
	}
	perTheme := map[string]int{}
	for _, subcategory := range vocabulary {
		theme, ok := ContextFabricInvestmentSubcategoryTheme(subcategory)
		if !ok || !strings.HasPrefix(subcategory, theme+".") {
			t.Fatalf("subcategory %q: theme %q, %v", subcategory, theme, ok)
		}
		if _, canonical := ContextFabricInvestmentThemeLabel(theme); !canonical {
			t.Fatalf("subcategory %q sits under %q, which is not a canonical theme", subcategory, theme)
		}
		perTheme[theme]++
		if _, worded := ContextFabricVocabularyLabel(ContextFabricVocabularySubcategory, subcategory); !worded {
			t.Fatalf("subcategory %q has no registry word", subcategory)
		}
	}
	if len(perTheme) != ContextFabricInvestmentThemeCount() {
		t.Fatalf("the subcategories cover %d themes, want every one of %d", len(perTheme), ContextFabricInvestmentThemeCount())
	}
	if _, ok := ContextFabricInvestmentSubcategoryTheme("risk"); ok {
		t.Fatal("a theme token is not a subcategory")
	}
	if !validContextFabricRenderShapeRule(ContextFabricRenderRuleInvestmentSubcategoryShare) {
		t.Fatal("the rule is not in the closed vocabulary")
	}
}
