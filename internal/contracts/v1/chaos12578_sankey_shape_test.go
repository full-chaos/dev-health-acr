package v1

import (
	"fmt"
	"strings"
	"testing"
)

// CHAOS-12578: the wire shape of a team's Repository -> Theme sankey (the
// CHAOS-4610 sankey). One series per theme with effort, one point per
// (repository, theme) link with effort above 0, all from one claimed
// repository_theme_breakdown table. Each test plants the defect one clause exists
// to catch, and matches its own message.

const repositoryFlowClaim = "claim_repository_flow_fixture"

// sankeyLinkSpec is one row of the fixture table, in table order.
type sankeyLinkSpec struct {
	repository, word, theme string
	effort                  float64
}

// sankeyFixture is one repository_theme_breakdown table and the shape that plots it.
type sankeyFixture struct {
	rows  []ContextFabricClaimedFactRow
	table *ContextFabricClaimedFactTable
	shape ContextFabricRenderShape
	// field and value are the claim's own, which a claimed_fact_value source
	// reads; field defaults to the table's name.
	field string
	value ContextFabricScalarValue
}

func newSankeyFixture(links []sankeyLinkSpec) *sankeyFixture {
	totals := map[string]float64{}
	for _, link := range links {
		totals[link.theme] += link.effort
	}
	f := &sankeyFixture{table: &ContextFabricClaimedFactTable{
		Field: "repository_theme_breakdown", Shape: ContextFabricFactTableShapeBreakdown, Key: []string{"repository", "theme"},
		Measures: []string{"weighted_effort", "theme_weighted_effort"}, Observations: []string{"repository_label"},
	}}
	series := map[string]*ContextFabricRenderSeries{}
	var order []string
	for index, link := range links {
		repository, word, theme := link.repository, link.word, link.theme
		f.rows = append(f.rows, ContextFabricClaimedFactRow{Fields: map[string]ContextFabricScalarValue{
			"repository":            {String: &repository},
			"repository_label":      {String: &word},
			"theme":                 {String: &theme},
			"weighted_effort":       {Number: float64Ptr(link.effort)},
			"theme_weighted_effort": {Number: float64Ptr(totals[theme])},
		}})
		if link.effort <= 0 {
			continue
		}
		themeWord, _ := ContextFabricInvestmentThemeLabel(theme)
		one, known := series[theme]
		if !known {
			order = append(order, theme)
			one = &ContextFabricRenderSeries{Key: theme, Label: themeWord}
			series[theme] = one
		}
		one.Points = append(one.Points, ContextFabricRenderPoint{Label: word, Value: link.effort, Source: ContextFabricRenderPointSource{
			Kind: ContextFabricRenderSourceClaimedFactRow, ClaimID: repositoryFlowClaim, RowIndex: intPtr(index), Field: "weighted_effort",
		}})
	}
	f.shape = ContextFabricRenderShape{
		ShapeID: "rs_repository_flow", Kind: ContextFabricRenderKindSankey, SelectedBy: ContextFabricRenderRuleInvestmentRepositoryFlow,
		Title: "Where the team's effort went, by repository", AxisKind: ContextFabricRenderAxisCategory, AxisLabel: "Repository",
		ValueLabel: "Weighted effort",
	}
	for _, theme := range order {
		f.shape.Series = append(f.shape.Series, *series[theme])
	}
	return f
}

func (f *sankeyFixture) result() ContextFabricInvestigationResult {
	return ContextFabricInvestigationResult{ClaimedFacts: []ContextFabricClaimedFact{{
		ClaimID: repositoryFlowClaim, Kind: "investment", Field: f.claimField(), Value: f.value, Rows: f.rows, Table: f.table,
		Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_platform", Label: "Platform"},
	}}}
}

func (f *sankeyFixture) claimField() string {
	if f.field == "" {
		return "repository_theme_breakdown"
	}
	return f.field
}

func (f *sankeyFixture) validate() error {
	return validateRenderShapes([]ContextFabricRenderShape{f.shape}, renderShapeSourcesFromResult(f.result()))
}

func (f *sankeyFixture) series(key string) *ContextFabricRenderSeries {
	for index := range f.shape.Series {
		if f.shape.Series[index].Key == key {
			return &f.shape.Series[index]
		}
	}
	panic("no series " + key)
}

// setThemeTotal gives every row of a theme the same team total.
func (f *sankeyFixture) setThemeTotal(theme string, total float64) {
	for index := range f.rows {
		if *f.rows[index].Fields["theme"].String == theme {
			f.rows[index].Fields["theme_weighted_effort"] = ContextFabricScalarValue{Number: float64Ptr(total)}
		}
	}
}

func setRowString(row ContextFabricClaimedFactRow, field, value string) {
	row.Fields[field] = ContextFabricScalarValue{String: &value}
}

// sankeyLinks: three repositories, four themes with effort, one zero link.
func sankeyLinks() []sankeyLinkSpec {
	return []sankeyLinkSpec{
		{"repository:aaa", "acme/web", "feature_delivery", 40},
		{"repository:aaa", "acme/web", "quality", 10},
		{"repository:bbb", "acme/api", "feature_delivery", 25},
		{"repository:bbb", "acme/api", "maintenance", 0},
		{"repository:bbb", "acme/api", "operational", 5},
		{"repository:ccc", "acme/infra", "maintenance", 12.5},
		{"repository:ccc", "acme/infra", "quality", 2.5},
	}
}

func TestASankeyOfOneTeamTableIsAccepted(t *testing.T) {
	t.Parallel()
	f := newSankeyFixture(sankeyLinks())
	if err := f.validate(); err != nil {
		t.Fatalf("a zero link stays out of the chart, and this shape is valid: %v", err)
	}
	if len(f.shape.Series) != 4 {
		t.Fatalf("the fixture is not four themes with effort: %d series", len(f.shape.Series))
	}
}

func TestASankeyIsRefused(t *testing.T) {
	t.Parallel()
	for name, plant := range map[string]struct {
		edit func(*sankeyFixture)
		want string
	}{
		"a sankey under another rule": {func(f *sankeyFixture) { f.shape.SelectedBy = ContextFabricRenderRuleDatedFactTrend }, "selected_by"},
		"the rule on another kind": {func(f *sankeyFixture) {
			f.shape.Kind, f.shape.Presentation = ContextFabricRenderKindSeries, ContextFabricRenderPresentationBars
		}, "selected_by"},
		"a time axis":              {func(f *sankeyFixture) { f.shape.AxisKind = ContextFabricRenderAxisTime }, "a sankey needs a category axis"},
		"a ratio unit":             {func(f *sankeyFixture) { f.shape.ValueUnit = ContextFabricRenderValueUnitRatio }, "which has no value_unit"},
		"an undeclared table":      {func(f *sankeyFixture) { f.table.Field = "theme_breakdown" }, "a sankey plots a declared"},
		"a row with no word":       {func(f *sankeyFixture) { delete(f.rows[0].Fields, "repository_label") }, "lacks a repository, its word"},
		"a row with no repository": {func(f *sankeyFixture) { delete(f.rows[3].Fields, "repository") }, "lacks a repository, its word"},
		"a point citing the claim's value, not a row": {func(f *sankeyFixture) {
			// It resolves (the claim's own field is weighted_effort, its value 5),
			// and is still not a link: only the source kind is wrong.
			f.field, f.value = "weighted_effort", ContextFabricScalarValue{Number: float64Ptr(5)}
			f.series("operational").Points[0].Source = ContextFabricRenderPointSource{Kind: ContextFabricRenderSourceClaimedFactValue, ClaimID: repositoryFlowClaim, Field: "weighted_effort"}
		}, `must cite the "weighted_effort" field`},
		"a person node":             {func(f *sankeyFixture) { setRowString(f.rows[0], "repository", "person:alice") }, "node is not a repository"},
		"an empty repository id":    {func(f *sankeyFixture) { setRowString(f.rows[0], "repository", "repository:") }, "node is not a repository"},
		"a theme outside the canon": {func(f *sankeyFixture) { setRowString(f.rows[3], "theme", "growth") }, "not a canonical theme"},
		"a blank word":              {func(f *sankeyFixture) { setRowString(f.rows[3], "repository_label", "  ") }, "has no word"},
		"one repository, two words": {func(f *sankeyFixture) { setRowString(f.rows[1], "repository_label", "acme/web2") }, "named by two words"},
		"two repositories, one word": {func(f *sankeyFixture) {
			setRowString(f.rows[5], "repository_label", "acme/web")
			setRowString(f.rows[6], "repository_label", "acme/web")
		}, "two nodes share the word"},
		"a repository named as a theme": {func(f *sankeyFixture) {
			word, _ := ContextFabricInvestmentThemeLabel("quality")
			setRowString(f.rows[5], "repository_label", word)
			setRowString(f.rows[6], "repository_label", word)
		}, "two nodes share the word"},
		"a link twice": {func(f *sankeyFixture) { setRowString(f.rows[1], "theme", "feature_delivery") }, "a link appears twice"},
		"a negative effort": {func(f *sankeyFixture) {
			f.rows[3].Fields["weighted_effort"] = ContextFabricScalarValue{Number: float64Ptr(-1)}
		}, "negative effort"},
		"rows that disagree on a theme": {func(f *sankeyFixture) {
			f.rows[0].Fields["theme_weighted_effort"] = ContextFabricScalarValue{Number: float64Ptr(66)}
		}, "disagree on the team's effort"},
		"a theme with effort and no link": {func(f *sankeyFixture) {
			risk := "risk"
			f.rows = append(f.rows, ContextFabricClaimedFactRow{Fields: map[string]ContextFabricScalarValue{
				"repository": {String: stringPtrForTreemap("repository:aaa")}, "repository_label": {String: stringPtrForTreemap("acme/web")},
				"theme": {String: &risk}, "weighted_effort": {Number: float64Ptr(0)}, "theme_weighted_effort": {Number: float64Ptr(3)},
			}})
		}, "has effort and no link"},
		"a series dropped": {func(f *sankeyFixture) {
			f.shape.Series = f.shape.Series[:len(f.shape.Series)-1]
		}, "one series per theme with effort"},
		"a series that is not a theme with effort": {func(f *sankeyFixture) { f.series("operational").Key = "risk" }, "a sankey series is a theme with effort"},
		"a theme's label that is not its word":     {func(f *sankeyFixture) { f.series("quality").Label = "Bugs" }, "its served word"},
		"a point on another field": {func(f *sankeyFixture) {
			f.series("operational").Points[0].Source.Field = "theme_weighted_effort"
			f.series("operational").Points[0].Value = 5
		}, `must cite the "weighted_effort" field`},
		"a link plotted twice": {func(f *sankeyFixture) {
			f.series("quality").Points = append(f.series("quality").Points, f.series("feature_delivery").Points[1])
		}, "plots each link once"},
		"a link under another theme": {func(f *sankeyFixture) {
			// feature_delivery is checked first, so the moved link is met there.
			delivery := f.series("feature_delivery")
			delivery.Points = append(delivery.Points, f.series("quality").Points[1])
			f.series("quality").Points = f.series("quality").Points[:1]
		}, "is not its series"},
		"a label that is not the repository's word": {func(f *sankeyFixture) { f.series("operational").Points[0].Label = "api" }, "the served word of its repository"},
		"a zero link drawn": {func(f *sankeyFixture) {
			maintenance := f.series("maintenance")
			maintenance.Points = append(maintenance.Points, ContextFabricRenderPoint{Label: "acme/api", Value: 0, Source: ContextFabricRenderPointSource{
				Kind: ContextFabricRenderSourceClaimedFactRow, ClaimID: repositoryFlowClaim, RowIndex: intPtr(3), Field: "weighted_effort",
			}})
		}, "carries effort above 0"},
		"links that do not add up to the theme": {func(f *sankeyFixture) { f.setThemeTotal("feature_delivery", 66) }, "its links add up to"},
		"a link left out": {func(f *sankeyFixture) {
			f.series("quality").Points = f.series("quality").Points[:1]
			f.setThemeTotal("quality", 10)
		}, "plots every link with effort"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSankeyFixture(sankeyLinks())
			plant.edit(f)
			err := f.validate()
			if err == nil || !strings.Contains(err.Error(), plant.want) {
				t.Fatalf("validate() = %v, want %q", err, plant.want)
			}
		})
	}
}

// Between 2 and 64 repositories with effort: one has no flow to show, and past
// 64 the shape is refused, never folded into an "other" node.
func TestASankeyDrawsTwoToSixtyFourRepositories(t *testing.T) {
	t.Parallel()
	themes := []string{"feature_delivery", "maintenance", "operational", "quality", "risk"}
	links := func(repositories int) []sankeyLinkSpec {
		var out []sankeyLinkSpec
		for index := range repositories {
			out = append(out, sankeyLinkSpec{fmt.Sprintf("repository:%03d", index), fmt.Sprintf("acme/r%03d", index), themes[index%len(themes)], float64(index + 1)})
		}
		return out
	}
	for repositories, want := range map[int]string{1: "a sankey draws 2 to 64 repositories", 2: "", 64: "", 65: "a sankey draws 2 to 64 repositories"} {
		t.Run(fmt.Sprint(repositories), func(t *testing.T) {
			err := newSankeyFixture(links(repositories)).validate()
			if want == "" && err != nil {
				t.Fatalf("%d repositories: %v", repositories, err)
			}
			if want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
				t.Fatalf("%d repositories: validate() = %v, want %q", repositories, err, want)
			}
		})
	}
}

// The per-theme sum takes the shared named tolerance: float noise passes, a real
// gap does not.
func TestASankeySumTakesTheNamedTolerance(t *testing.T) {
	t.Parallel()
	f := newSankeyFixture(sankeyLinks())
	f.setThemeTotal("feature_delivery", 65*(1+1e-15))
	if err := f.validate(); err != nil {
		t.Fatalf("a sum within %v relative is the same whole: %v", ContextFabricRenderShareSumTolerance, err)
	}
	f.setThemeTotal("feature_delivery", 65*(1+1e-9))
	if err := f.validate(); err == nil || !strings.Contains(err.Error(), "its links add up to") {
		t.Fatalf("validate() = %v, want the sum refusal past the tolerance", err)
	}
}

func TestASankeyOverTwoClaimsIsRefused(t *testing.T) {
	t.Parallel()
	f := newSankeyFixture(sankeyLinks())
	result := f.result()
	second := result.ClaimedFacts[0]
	second.ClaimID = "claim_repository_flow_other"
	result.ClaimedFacts = append(result.ClaimedFacts, second)
	f.series("operational").Points[0].Source.ClaimID = second.ClaimID
	err := validateRenderShapes([]ContextFabricRenderShape{f.shape}, renderShapeSourcesFromResult(result))
	if err == nil || !strings.Contains(err.Error(), "a sankey plots one claim") {
		t.Fatalf("validate() = %v, want a one-claim refusal", err)
	}
}

func TestASankeyResolvesOnTheProjectionPath(t *testing.T) {
	t.Parallel()
	f := newSankeyFixture(sankeyLinks())
	projection := ContextFabricAnswerProjection{KeyFacts: []ContextFabricProjectedFact{{ClaimID: repositoryFlowClaim, Rows: f.rows, Table: f.table}}}
	if err := validateRenderShapes([]ContextFabricRenderShape{f.shape}, renderShapeSourcesFromProjection(projection)); err != nil {
		t.Fatalf("the projection carries the table, so the shape resolves: %v", err)
	}
	projection.KeyFacts[0].Table = nil
	if err := validateRenderShapes([]ContextFabricRenderShape{f.shape}, renderShapeSourcesFromProjection(projection)); err == nil || !strings.Contains(err.Error(), "plots a declared") {
		t.Fatalf("validate() = %v, want the undeclared-table refusal on the projection path too", err)
	}
}

// Only a repository_theme_breakdown breakdown keyed by repository then theme,
// with both efforts as measures and the repository's word as an observation, is
// the declared table.
func TestOnlyTheDeclaredRepositoryFlowTableIsHeld(t *testing.T) {
	t.Parallel()
	for name, edit := range map[string]func(*ContextFabricClaimedFactTable){
		"another field":         func(table *ContextFabricClaimedFactTable) { table.Field = "theme_breakdown" },
		"another shape":         func(table *ContextFabricClaimedFactTable) { table.Shape = ContextFabricFactTableShapeTimeSeries },
		"one key column":        func(table *ContextFabricClaimedFactTable) { table.Key = []string{"repository"} },
		"keys in another order": func(table *ContextFabricClaimedFactTable) { table.Key = []string{"theme", "repository"} },
		"another first key":     func(table *ContextFabricClaimedFactTable) { table.Key = []string{"subject", "theme"} },
		"no link effort":        func(table *ContextFabricClaimedFactTable) { table.Measures = []string{"theme_weighted_effort"} },
		"no theme effort":       func(table *ContextFabricClaimedFactTable) { table.Measures = []string{"weighted_effort"} },
		"no repository word":    func(table *ContextFabricClaimedFactTable) { table.Observations = nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := newSankeyFixture(sankeyLinks())
			if !holdsRepositoryFlowTable(f.table, nil) {
				t.Fatal("the fixture's table is the declared table")
			}
			edit(f.table)
			if holdsRepositoryFlowTable(f.table, nil) {
				t.Fatalf("%s: holdsRepositoryFlowTable = true, want false", name)
			}
		})
	}
	f := newSankeyFixture(sankeyLinks())
	if holdsRepositoryFlowTable(f.table, f.rows) {
		t.Fatal("with a time series beside it the claim's rows are the series, so the table is not held")
	}
}

// A sankey's repository node is the subject the minter makes: one prefix, owned
// by the contract.
func TestTheSankeyRepositoryPrefixIsTheSubjectPrefix(t *testing.T) {
	t.Parallel()
	if ContextFabricRepositoryCanonicalIDPrefix != "repository:" {
		t.Fatalf("prefix = %q", ContextFabricRepositoryCanonicalIDPrefix)
	}
}
