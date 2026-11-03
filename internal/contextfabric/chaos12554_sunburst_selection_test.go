package contextfabric

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12554: the rule that yields the sunburst. The answer here is the shape
// a served one has: an investment_allocation plan and a claim that names the
// declared subcategory_breakdown detail table, which carries its 15 rows.

const sunburstTestClaim = "claim_subcategory_share_selection"

func sunburstOptions() RenderShapeOptions {
	return RenderShapeOptions{InvestmentSubcategoryShare: true, InvestmentThemeShare: true}
}

// sunburstShares is a split with measured zeros: a subcategory not named is 0.
func sunburstShares() map[string]float64 {
	return map[string]float64{
		"feature_delivery.customer": 0.25, "feature_delivery.roadmap": 0.15, "maintenance.debt": 0.1,
		"operational.support": 0.2, "quality.bugfix": 0.2, "quality.testing": 0.05, "risk.security": 0.05,
	}
}

// subcategoryRows is the table the provider serves: every canonical
// subcategory in vocabulary order, and each theme's share the sum of its share
// cells in that order.
func subcategoryRows(shares map[string]float64) []ClaimedFactRow {
	vocabulary := contractsv1.ContextFabricInvestmentSubcategoryVocabulary()
	totals := map[string]float64{}
	for _, subcategory := range vocabulary {
		theme, _ := contractsv1.ContextFabricInvestmentSubcategoryTheme(subcategory)
		totals[theme] += shares[subcategory]
	}
	rows := make([]ClaimedFactRow, 0, len(vocabulary))
	for _, subcategory := range vocabulary {
		theme, _ := contractsv1.ContextFabricInvestmentSubcategoryTheme(subcategory)
		key, token, share, total := subcategory, theme, shares[subcategory], totals[theme]
		rows = append(rows, ClaimedFactRow{Fields: map[string]ScalarValue{
			"subcategory": {String: &key}, "theme": {String: &token}, "share": {Number: &share}, "theme_share": {Number: &total},
		}})
	}
	return rows
}

func subcategoryClaim(kind contractsv1.ContextFabricSubjectKind, rows []ClaimedFactRow) ClaimedFact {
	return ClaimedFact{
		ClaimID: sunburstTestClaim, Kind: FactInvestment, Field: "subcategory_breakdown", Value: ScalarValue{Null: true}, Rows: rows,
		Subject: contractsv1.ContextFabricSubjectRef{Kind: kind, CanonicalID: "subject:one", Label: "acme/widgets"},
		Table: &contractsv1.ContextFabricClaimedFactTable{
			Field: "subcategory_breakdown", Shape: contractsv1.ContextFabricFactTableShapeBreakdown, Key: []string{"subcategory"},
			Measures: []string{"share", "theme_share"}, Observations: []string{"theme"},
		},
	}
}

func sunburstResultFor(kind contractsv1.ContextFabricSubjectKind, shares map[string]float64) InvestigationResult {
	result := themeShareResultFor(kind)
	result.AnswerPlan.RenderKinds = append(result.AnswerPlan.RenderKinds, contractsv1.ContextFabricRenderKindSunburst)
	result.ClaimedFacts = []ClaimedFact{subcategoryClaim(kind, subcategoryRows(shares))}
	return result
}

func sunburstsOf(shapes []contractsv1.ContextFabricRenderShape) []contractsv1.ContextFabricRenderShape {
	var out []contractsv1.ContextFabricRenderShape
	for _, shape := range shapes {
		if shape.Kind == contractsv1.ContextFabricRenderKindSunburst {
			out = append(out, shape)
		}
	}
	return out
}

func sunburstSkipOf(t *testing.T, event RenderShapeSelectionEvent) RenderShapeSkipReason {
	t.Helper()
	if err := event.Accounted(); err != nil {
		t.Fatalf("Accounted: %v", err)
	}
	for _, skip := range event.Skipped {
		if skip.Rule == contractsv1.ContextFabricRenderRuleInvestmentSubcategoryShare {
			return skip.Reason
		}
	}
	return ""
}

func TestCHAOS12554AClaimThatNamesTheTableGetsItsSunburst(t *testing.T) {
	t.Parallel()
	for _, kind := range []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectTeam} {
		result := sunburstResultFor(kind, sunburstShares())
		shapes, event := selectRenderShapes(result, themeShareFrame(), sunburstOptions())
		sunbursts := sunburstsOf(shapes)
		if len(sunbursts) != 1 {
			t.Fatalf("%s: shapes %+v, skip %q: want one sunburst", kind, shapes, sunburstSkipOf(t, event))
		}
		shape := sunbursts[0]
		if shape.SelectedBy != contractsv1.ContextFabricRenderRuleInvestmentSubcategoryShare || shape.ValueUnit != contractsv1.ContextFabricRenderValueUnitRatio ||
			shape.Title != "Effort share by theme and subcategory" || shape.AxisLabel != "Subcategory" || shape.ValueLabel != "Share of effort (ratio)" {
			t.Fatalf("shape %+v", shape)
		}
		if len(shape.Series) != 6 || shape.Series[0].Key != "themes" || len(shape.Series[0].Points) != 5 || shape.Series[0].Label != "Theme share, sum of subcategories" {
			t.Fatalf("series %+v", shape.Series)
		}
		rows := result.ClaimedFacts[0].Rows
		points := 0
		for _, series := range shape.Series {
			for _, point := range series.Points {
				points++
				cell := rows[*point.Source.RowIndex].Fields[point.Source.Field].Number
				if cell == nil || *cell != point.Value || point.Source.ClaimID != sunburstTestClaim {
					t.Fatalf("point %+v is not an exact copy of its cell", point)
				}
				subcategory := *rows[*point.Source.RowIndex].Fields["subcategory"].String
				theme := *rows[*point.Source.RowIndex].Fields["theme"].String
				want, _ := contractsv1.ContextFabricVocabularyLabel(contractsv1.ContextFabricVocabularySubcategory, subcategory)
				if series.Key == "themes" {
					want, _ = contractsv1.ContextFabricInvestmentThemeLabel(theme)
				}
				if point.Label != want || want == "" {
					t.Fatalf("point %q, want the registry word %q", point.Label, want)
				}
			}
		}
		if points != 20 {
			t.Fatalf("points %d, want 5 themes and 15 subcategories: a measured zero stays a part", points)
		}
		if treemaps := treemapsOf(shapes); len(treemaps) != 0 {
			t.Fatalf("a treemap beside the sunburst: %+v", treemaps)
		}
		result.RenderShapes = shapes
		if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
			t.Fatalf("the contract refused the shape: %v", err)
		}
	}
}

func TestCHAOS12554TheSunburstNeedsTheNamedClaimAndThisTurnsGoal(t *testing.T) {
	t.Parallel()
	frameWithout := goalFrame(allocationSubject())
	for name, tc := range map[string]struct {
		edit  func(*InvestigationResult)
		frame *QuestionFrame
		opts  *RenderShapeOptions
		want  RenderShapeSkipReason
	}{
		"the switch is off":                          {opts: &RenderShapeOptions{InvestmentThemeShare: true}, want: RenderShapeSkipRuleDisabled},
		"no validated frame":                         {frame: nil, want: RenderShapeSkipNotInvestmentAllocation},
		"a frame without the goal":                   {frame: &frameWithout, want: RenderShapeSkipNotInvestmentAllocation},
		"a claim on another field carries the table": {edit: func(r *InvestigationResult) { r.ClaimedFacts[0].Field = "theme_quality" }, want: RenderShapeSkipNoSubcategoryBreakdown},
		"a carried family with no claim":             {edit: func(r *InvestigationResult) { r.ClaimedFacts = nil }, want: RenderShapeSkipNoSubcategoryBreakdown},
		"the plan does not authorize it": {edit: func(r *InvestigationResult) {
			r.AnswerPlan.RenderKinds = r.AnswerPlan.RenderKinds[:len(r.AnswerPlan.RenderKinds)-1]
		}, want: RenderShapeSkipNotPlanAuthorized},
		"another family": {edit: func(r *InvestigationResult) { r.AnswerPlan.Family = QuestionFamilyUnclassified }, want: RenderShapeSkipNotInvestmentAllocation},
		"an organization": {edit: func(r *InvestigationResult) {
			r.ClaimedFacts[0].Subject.Kind = contractsv1.ContextFabricSubjectOrganization
		}, want: RenderShapeSkipSubjectKindNotCharted},
		"a cut table": {edit: func(r *InvestigationResult) { r.ClaimedFacts[0].Rows = r.ClaimedFacts[0].Rows[:14] }, want: RenderShapeSkipSubcategoryTableCut},
		"a token with no registry word": {edit: func(r *InvestigationResult) {
			other := "quality.flaky"
			r.ClaimedFacts[0].Rows[10].Fields["subcategory"] = ScalarValue{String: &other}
		}, want: RenderShapeSkipSubcategoryRowUnusable},
		"a theme share that is not its subcategories' sum": {edit: func(r *InvestigationResult) {
			for _, row := range r.ClaimedFacts[0].Rows {
				if *row.Fields["theme"].String == "quality" {
					off := *row.Fields["theme_share"].Number + 1e-3
					row.Fields["theme_share"] = ScalarValue{Number: &off}
				}
			}
		}, want: RenderShapeSkipSubcategoryShapeUnprovable},
		"a single share": {edit: func(r *InvestigationResult) {
			r.ClaimedFacts[0].Rows = subcategoryRows(map[string]float64{"quality.bugfix": 1})
		}, want: RenderShapeSkipFewerThanTwoShares},
		"two subjects' tables": {edit: func(r *InvestigationResult) {
			second := subcategoryClaim(contractsv1.ContextFabricSubjectRepository, subcategoryRows(sunburstShares()))
			second.ClaimID, second.Subject.CanonicalID = "claim_subcategory_share_other", "subject:two"
			r.ClaimedFacts = append(r.ClaimedFacts, second)
		}, want: RenderShapeSkipSeveralSubcategoryBreakdowns},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := sunburstResultFor(contractsv1.ContextFabricSubjectRepository, sunburstShares())
			if tc.edit != nil {
				tc.edit(&result)
			}
			frame := tc.frame
			if frame == nil && !strings.HasPrefix(name, "no validated") {
				frame = themeShareFrame()
			}
			options := sunburstOptions()
			if tc.opts != nil {
				options = *tc.opts
			}
			shapes, event := selectRenderShapes(result, frame, options)
			if len(sunburstsOf(shapes)) != 0 {
				t.Fatalf("a sunburst was drawn: %+v", shapes)
			}
			if got := sunburstSkipOf(t, event); got != tc.want {
				t.Fatalf("skip %q, want %q", got, tc.want)
			}
		})
	}
}

// Two claims on one fact can carry the theme table and the subcategory table.
// The sunburst's inner ring is the theme split, so the answer draws it alone.
func TestCHAOS12554TheTreemapAndTheSunburstAreNeverTogether(t *testing.T) {
	t.Parallel()
	result := sunburstResultFor(contractsv1.ContextFabricSubjectRepository, sunburstShares())
	treemapClaim := themeShareResultFor(contractsv1.ContextFabricSubjectRepository, 0.5, 0.25, 0.25).ClaimedFacts[0]
	result.ClaimedFacts = append([]ClaimedFact{treemapClaim}, result.ClaimedFacts...)
	shapes, event := selectRenderShapes(result, themeShareFrame(), sunburstOptions())
	if len(sunburstsOf(shapes)) != 1 || len(treemapsOf(shapes)) != 0 {
		t.Fatalf("shapes %+v: want the sunburst alone", shapes)
	}
	if got := skipOf(t, event); got != RenderShapeSkipSubcategoryBreakdownDrawn {
		t.Fatalf("treemap skip %q, want %q", got, RenderShapeSkipSubcategoryBreakdownDrawn)
	}
	// With the sunburst switch off, the same answer draws its treemap.
	shapes, event = selectRenderShapes(result, themeShareFrame(), RenderShapeOptions{InvestmentThemeShare: true})
	if len(sunburstsOf(shapes)) != 0 || len(treemapsOf(shapes)) != 1 {
		t.Fatalf("switch off: shapes %+v, skip %q", shapes, sunburstSkipOf(t, event))
	}
}

func TestCHAOS12554AStoredSunburstIsRemovedWhenTheSwitchIsOff(t *testing.T) {
	t.Parallel()
	result := sunburstResultFor(contractsv1.ContextFabricSubjectTeam, sunburstShares())
	result.RenderShapes, _ = selectRenderShapes(result, themeShareFrame(), sunburstOptions())
	if len(sunburstsOf(result.RenderShapes)) != 1 {
		t.Fatalf("no sunburst to store: %+v", result.RenderShapes)
	}
	for name, tc := range map[string]struct {
		options RenderShapeOptions
		kept    int
	}{
		"off":                     {RenderShapeOptions{InvestmentThemeShare: true, ComparisonSideBySide: true, LandscapePosition: true, CompletionForecast: true, InvestmentRepositoryFlow: true}, 0},
		"everything off":          {RenderShapeOptions{}, 0},
		"on":                      {RenderShapeOptions{InvestmentSubcategoryShare: true}, 1},
		"on with every other one": {RenderShapeOptions{InvestmentSubcategoryShare: true, InvestmentThemeShare: true, ComparisonSideBySide: true, LandscapePosition: true, CompletionForecast: true, InvestmentRepositoryFlow: true}, 1},
	} {
		if got := len(sunburstsOf(WithoutDisabledRenderShapes(result, tc.options).RenderShapes)); got != tc.kept {
			t.Errorf("%s: kept %d sunbursts, want %d", name, got, tc.kept)
		}
	}
	if len(result.RenderShapes) != 1 {
		t.Fatal("the input was changed")
	}
}

// The sunburst switch joins the reuse key by its rule name, and with it off the
// key is what it was before the switch existed.
func TestCHAOS12554TheReuseKeyNamesTheSunburstSwitch(t *testing.T) {
	t.Parallel()
	if got, want := ReuseQuestionFamilyVersion(RenderShapeOptions{InvestmentSubcategoryShare: true}), QuestionFamilyTableVersion+"+render:investment_subcategory_share"; got != want {
		t.Fatalf("sunburst on: %q, want %q", got, want)
	}
	// Off: the forms before this switch, pinned as literals.
	for _, tc := range []struct {
		options RenderShapeOptions
		want    string
	}{
		{RenderShapeOptions{}, "question-family.v8"},
		{RenderShapeOptions{InvestmentThemeShare: true}, "question-family.v8+render:investment_theme_share"},
		{RenderShapeOptions{InvestmentThemeShare: true, ComparisonSideBySide: true, CompletionForecast: true, LandscapePosition: true}, "question-family.v8+render:comparison_side_by_side,completion_forecast,investment_theme_share,landscape_position"},
	} {
		if got := ReuseQuestionFamilyVersion(tc.options); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.options, got, tc.want)
		}
	}
	seen := map[string]int{}
	for mask := 0; mask < 64; mask++ {
		options := RenderShapeOptions{InvestmentThemeShare: mask&1 != 0, ComparisonSideBySide: mask&2 != 0, CompletionForecast: mask&4 != 0, LandscapePosition: mask&8 != 0, InvestmentSubcategoryShare: mask&16 != 0, InvestmentRepositoryFlow: mask&32 != 0}
		version := ReuseQuestionFamilyVersion(options)
		if earlier, repeated := seen[version]; repeated {
			t.Errorf("switch masks %06b and %06b share the reuse version %q", earlier, mask, version)
		}
		seen[version] = mask
	}
}

// The length half of the 64-set check. With the sunburst switch on, a readable
// form can pass the question_family_version column's 128 characters (migration
// 0036); the length rule of CHAOS-12582 keys such a form by its digest.
func TestCHAOS12554EveryReuseFormFitsTheColumn(t *testing.T) {
	for mask := 0; mask < 64; mask++ {
		options := RenderShapeOptions{InvestmentThemeShare: mask&1 != 0, ComparisonSideBySide: mask&2 != 0, CompletionForecast: mask&4 != 0, LandscapePosition: mask&8 != 0, InvestmentSubcategoryShare: mask&16 != 0, InvestmentRepositoryFlow: mask&32 != 0}
		if version := ReuseQuestionFamilyVersion(options); len(version) > 128 {
			t.Errorf("%+v: %d characters, past the column's 128", options, len(version))
		}
	}
}
