package contextfabric

import (
	"fmt"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12582: the rule that yields the team Repository -> Theme sankey. The
// result here is the shape a served answer has: an investment_allocation plan,
// and one team's investment claim that names the declared
// repository_theme_breakdown detail table (CHAOS-12580, CHAOS-12587).

const repositoryFlowTestClaim = "claim_repository_flow_selection"

type repositoryFlowLink struct {
	repository, word, theme string
	effort                  float64
}

// repositoryFlowLinks: three repositories, four themes with effort, one zero link
// and the table's row order (by repository, then theme) unlike the canon's.
func repositoryFlowLinks() []repositoryFlowLink {
	return []repositoryFlowLink{
		{"repository:aaa", "acme/api", ThemeQuality, 2},
		{"repository:aaa", "acme/api", ThemeFeatureDelivery, 6},
		{"repository:bbb", "acme/web", ThemeFeatureDelivery, 1.5},
		{"repository:bbb", "acme/web", ThemeOperational, 0},
		{"repository:ccc", "acme/infra", ThemeOperational, 0.5},
		{"repository:ccc", "acme/infra", ThemeRisk, 0.25},
	}
}

func repositoryFlowClaimFor(claimID string, links []repositoryFlowLink) ClaimedFact {
	totals := map[string]float64{}
	for _, link := range links {
		totals[link.theme] += link.effort
	}
	rows := make([]ClaimedFactRow, 0, len(links))
	for _, link := range links {
		repository, word, theme, effort, total := link.repository, link.word, link.theme, link.effort, totals[link.theme]
		rows = append(rows, ClaimedFactRow{Fields: map[string]ScalarValue{
			"repository": {String: &repository}, "repository_label": {String: &word}, "theme": {String: &theme},
			"weighted_effort": {Number: &effort}, "theme_weighted_effort": {Number: &total},
		}})
	}
	return ClaimedFact{
		ClaimID: claimID, Kind: FactInvestment, Field: contractsv1.ContextFabricRenderRepositoryFlowTableField, Rows: rows,
		Value:   ScalarValue{Null: true},
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:platform", Label: "Platform"},
		Table: &contractsv1.ContextFabricClaimedFactTable{
			Field: contractsv1.ContextFabricRenderRepositoryFlowTableField, Shape: contractsv1.ContextFabricFactTableShapeBreakdown,
			Key:      []string{"repository", "theme"},
			Measures: []string{"weighted_effort", "theme_weighted_effort"}, Observations: []string{"repository_label"},
		},
	}
}

func repositoryFlowResult(links []repositoryFlowLink) InvestigationResult {
	result := validInvestigationResult()
	result.Cohort = nil
	result.Interpretation.Shape = ShapeSingleSubject
	result.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{
		Family:        QuestionFamilyInvestmentAllocation,
		FamilyVersion: QuestionFamilyTableVersion,
		RenderKinds: []contractsv1.ContextFabricRenderKind{
			contractsv1.ContextFabricRenderKindTable, contractsv1.ContextFabricRenderKindSeries,
			contractsv1.ContextFabricRenderKindTreemap, contractsv1.ContextFabricRenderKindSankey,
		},
	}
	result.ClaimedFacts = []ClaimedFact{repositoryFlowClaimFor(repositoryFlowTestClaim, links)}
	return result
}

// repositoryFlowFrameOf is the validated frame of "how did the Platform team's
// effort split across its repositories": the repositories under the team, with
// the one goal allocate_investment.
func repositoryFlowFrameOf(goals ...InvestigationGoal) *QuestionFrame {
	if len(goals) == 0 {
		goals = []InvestigationGoal{GoalAllocateInvestment}
	}
	frame := goalFrame(SubjectExpression{Kind: SubjectExpressionChildrenOfScope, Scoped: &ScopedSetExpression{
		AnchorTerms: []string{"Platform"}, MemberKind: contractsv1.ContextFabricSubjectRepository,
	}}, goals...)
	return &frame
}

var sankeyOn = RenderShapeOptions{InvestmentRepositoryFlow: true}

func sankeysOf(shapes []contractsv1.ContextFabricRenderShape) []contractsv1.ContextFabricRenderShape {
	var out []contractsv1.ContextFabricRenderShape
	for _, shape := range shapes {
		if shape.Kind == contractsv1.ContextFabricRenderKindSankey {
			out = append(out, shape)
		}
	}
	return out
}

func repositoryFlowSkipOf(t *testing.T, event RenderShapeSelectionEvent) RenderShapeSkipReason {
	t.Helper()
	if err := event.Accounted(); err != nil {
		t.Fatalf("Accounted: %v", err)
	}
	for _, skip := range event.Skipped {
		if skip.Rule == contractsv1.ContextFabricRenderRuleInvestmentRepositoryFlow {
			return skip.Reason
		}
	}
	return ""
}

func TestATeamsRepositoryQuestionGetsItsSankey(t *testing.T) {
	t.Parallel()
	result := repositoryFlowResult(repositoryFlowLinks())
	shapes, event := selectRenderShapes(result, repositoryFlowFrameOf(), sankeyOn)
	sankeys := sankeysOf(shapes)
	if len(sankeys) != 1 {
		t.Fatalf("shapes %+v, want one sankey", shapes)
	}
	shape := sankeys[0]
	if shape.SelectedBy != contractsv1.ContextFabricRenderRuleInvestmentRepositoryFlow || shape.ValueUnit != "" || shape.Presentation != "" ||
		shape.AxisKind != contractsv1.ContextFabricRenderAxisCategory {
		t.Fatalf("shape %+v", shape)
	}
	if shape.Title != "Where the team's effort went, by repository" || shape.AxisLabel != "Repository" || shape.ValueLabel != "Weighted effort" {
		t.Fatalf("words %+v", shape)
	}
	// One series per theme with effort, in the canon's order, whatever the
	// table's row order; one point per link with effort, the row's own value.
	want := []struct {
		key, label string
		points     []string
	}{
		{ThemeFeatureDelivery, "Feature Delivery", []string{"acme/api=6@1", "acme/web=1.5@2"}},
		{ThemeOperational, "Operational / Support", []string{"acme/infra=0.5@4"}},
		{ThemeQuality, "Quality / Reliability", []string{"acme/api=2@0"}},
		{ThemeRisk, "Risk / Security", []string{"acme/infra=0.25@5"}},
	}
	if len(shape.Series) != len(want) {
		t.Fatalf("series %+v, want %d", shape.Series, len(want))
	}
	for index, series := range shape.Series {
		if series.Key != want[index].key || series.Label != want[index].label || len(series.Points) != len(want[index].points) {
			t.Fatalf("series %d = %+v, want %+v", index, series, want[index])
		}
		for p, point := range series.Points {
			got := fmt.Sprintf("%s=%v@%d", point.Label, point.Value, *point.Source.RowIndex)
			if got != want[index].points[p] || point.Source.ClaimID != repositoryFlowTestClaim || point.Source.Field != "weighted_effort" ||
				point.Source.Kind != contractsv1.ContextFabricRenderSourceClaimedFactRow {
				t.Fatalf("series %s point %d = %s %+v, want %s", series.Key, p, got, point.Source, want[index].points[p])
			}
		}
	}
	if skip := repositoryFlowSkipOf(t, event); skip != "" {
		t.Fatalf("a selected rule recorded skip %q", skip)
	}
	result.RenderShapes = shapes
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the contract refused the shape: %v", err)
	}
}

func TestTheSankeyRuleIsOffByDefault(t *testing.T) {
	t.Parallel()
	shapes, event := selectRenderShapes(repositoryFlowResult(repositoryFlowLinks()), repositoryFlowFrameOf(), RenderShapeOptions{})
	if len(sankeysOf(shapes)) != 0 || repositoryFlowSkipOf(t, event) != RenderShapeSkipRuleDisabled {
		t.Fatalf("off: shapes %+v, skip %q", shapes, repositoryFlowSkipOf(t, event))
	}
	if shapes, _ := SelectRenderShapes(repositoryFlowResult(repositoryFlowLinks()), repositoryFlowFrameOf()); len(sankeysOf(shapes)) != 0 {
		t.Fatal("SelectRenderShapes (the zero options) drew a sankey")
	}
}

// Check 1 and check 10: the sankey answers one question, and every other
// question, however near, gets none.
func TestNoSankeyWithoutTheRulesEvidence(t *testing.T) {
	t.Parallel()
	named := goalFrame(allocationSubject(), GoalAllocateInvestment)
	teams := goalFrame(SubjectExpression{Kind: SubjectExpressionChildrenOfScope, Scoped: &ScopedSetExpression{
		AnchorTerms: []string{"Platform"}, MemberKind: contractsv1.ContextFabricSubjectTeam,
	}}, GoalAllocateInvestment)
	cases := map[string]struct {
		frame *QuestionFrame
		edit  func(*InvestigationResult)
		want  RenderShapeSkipReason
	}{
		"no frame (a carried family)":       {nil, nil, RenderShapeSkipNotRepositoryFlowQuestion},
		"one team's own split, not by repo": {&named, nil, RenderShapeSkipNotRepositoryFlowQuestion},
		"the teams under a scope":           {&teams, nil, RenderShapeSkipNotRepositoryFlowQuestion},
		"repositories grouped by another kind": {func() *QuestionFrame {
			frame := *repositoryFlowFrameOf()
			frame.SubjectExpression.Kind = SubjectExpressionGroupedMembers
			return &frame
		}(), nil, RenderShapeSkipNotRepositoryFlowQuestion},
		"a second goal beside allocation": {repositoryFlowFrameOf(GoalAllocateInvestment, GoalCompare), nil, RenderShapeSkipNotRepositoryFlowQuestion},
		"another goal alone":              {repositoryFlowFrameOf(GoalCompare), nil, RenderShapeSkipNotRepositoryFlowQuestion},
		"another family": {repositoryFlowFrameOf(), func(r *InvestigationResult) {
			r.AnswerPlan.Family = QuestionFamilyDiscoveredCohortRanking
		}, RenderShapeSkipNotInvestmentAllocation},
		"no plan": {repositoryFlowFrameOf(), func(r *InvestigationResult) { r.AnswerPlan = nil }, RenderShapeSkipNotInvestmentAllocation},
		"a cohort answer": {repositoryFlowFrameOf(), func(r *InvestigationResult) {
			r.Cohort = &Cohort{Kind: contractsv1.ContextFabricSubjectRepository, Complete: true, Rationale: "fixture"}
		}, RenderShapeSkipCohortAnswer},
		"a plan without the sankey": {repositoryFlowFrameOf(), func(r *InvestigationResult) {
			r.AnswerPlan.RenderKinds = r.AnswerPlan.RenderKinds[:3]
		}, RenderShapeSkipNotPlanAuthorized},
		"no claim with the table": {repositoryFlowFrameOf(), func(r *InvestigationResult) {
			r.ClaimedFacts[0].Table.Field = "theme_breakdown"
		}, RenderShapeSkipNoRepositoryFlowTable},
		"the table on a workload claim": {repositoryFlowFrameOf(), func(r *InvestigationResult) {
			r.ClaimedFacts[0].Kind = FactWorkload
		}, RenderShapeSkipNoRepositoryFlowTable},
		"the table keyed another way": {repositoryFlowFrameOf(), func(r *InvestigationResult) {
			r.ClaimedFacts[0].Table.Key = []string{"theme", "repository"}
		}, RenderShapeSkipNoRepositoryFlowTable},
		"a time series in front": {repositoryFlowFrameOf(), func(r *InvestigationResult) {
			r.ClaimedFacts[0].TimeSeriesRows = r.ClaimedFacts[0].Rows[:1]
		}, RenderShapeSkipNoRepositoryFlowTable},
		"two teams' tables": {repositoryFlowFrameOf(), func(r *InvestigationResult) {
			second := repositoryFlowClaimFor("claim_second_team", repositoryFlowLinks())
			second.Subject.CanonicalID = "team:other"
			r.ClaimedFacts = append(r.ClaimedFacts, second)
		}, RenderShapeSkipSeveralRepositoryFlowTables},
		"a project's table": {repositoryFlowFrameOf(), func(r *InvestigationResult) {
			r.ClaimedFacts[0].Subject.Kind = contractsv1.ContextFabricSubjectProject
		}, RenderShapeSkipSubjectKindNotCharted},
		"a row with no word": {repositoryFlowFrameOf(), func(r *InvestigationResult) {
			delete(r.ClaimedFacts[0].Rows[0].Fields, "repository_label")
		}, RenderShapeSkipRepositoryFlowUnprovable},
		// A table cut to its first rows, as the answer-size lever cuts a
		// breakdown: operational keeps only its zero link, so the theme's effort
		// has no link with effort left.
		"a cut table": {repositoryFlowFrameOf(), func(r *InvestigationResult) {
			r.ClaimedFacts[0].Rows = r.ClaimedFacts[0].Rows[:4]
		}, RenderShapeSkipRepositoryFlowUnprovable},
		"a theme outside the canon": {repositoryFlowFrameOf(), func(r *InvestigationResult) {
			growth := "growth"
			r.ClaimedFacts[0].Rows[0].Fields["theme"] = ScalarValue{String: &growth}
		}, RenderShapeSkipRepositoryFlowUnprovable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := repositoryFlowResult(repositoryFlowLinks())
			if tc.edit != nil {
				tc.edit(&result)
			}
			shapes, event := selectRenderShapes(result, tc.frame, sankeyOn)
			if len(sankeysOf(shapes)) != 0 {
				t.Fatalf("drew a sankey: %+v", sankeysOf(shapes))
			}
			if got := repositoryFlowSkipOf(t, event); got != tc.want {
				t.Fatalf("skip = %q, want %q", got, tc.want)
			}
		})
	}
}

// Between 2 and 64 repositories with effort; past 64 no shape, never an "other"
// node. A repository whose only links are zero is not one of them.
func TestTheSankeyDrawsTwoToSixtyFourRepositories(t *testing.T) {
	t.Parallel()
	linksOf := func(repositories int) []repositoryFlowLink {
		var links []repositoryFlowLink
		for index := range repositories {
			links = append(links, repositoryFlowLink{fmt.Sprintf("repository:r%02d", index), fmt.Sprintf("acme/r%02d", index), ThemeFeatureDelivery, 1})
		}
		return links
	}
	for _, tc := range []struct {
		repositories int
		drawn        bool
	}{{1, false}, {2, true}, {64, true}, {65, false}} {
		shapes, event := selectRenderShapes(repositoryFlowResult(linksOf(tc.repositories)), repositoryFlowFrameOf(), sankeyOn)
		if drawn := len(sankeysOf(shapes)) == 1; drawn != tc.drawn {
			t.Fatalf("%d repositories: drawn = %v, want %v (skip %q)", tc.repositories, drawn, tc.drawn, repositoryFlowSkipOf(t, event))
		}
		if !tc.drawn && repositoryFlowSkipOf(t, event) != RenderShapeSkipRepositoryCountOutsideBounds {
			t.Fatalf("%d repositories: skip %q", tc.repositories, repositoryFlowSkipOf(t, event))
		}
	}
	zeroOnly := append(linksOf(2), repositoryFlowLink{"repository:zero", "acme/zero", ThemeRisk, 0})
	shapes, _ := selectRenderShapes(repositoryFlowResult(zeroOnly), repositoryFlowFrameOf(), sankeyOn)
	if len(sankeysOf(shapes)) != 1 || len(sankeysOf(shapes)[0].Series) != 1 || len(sankeysOf(shapes)[0].Series[0].Points) != 2 {
		t.Fatalf("a zero link drew: %+v", shapes)
	}
}

// The ticket's acceptance: a treemap answer never shares a claim with the
// sankey. One claim holds one table, so with both rules on the two charts cite
// two claims.
func TestTheTreemapAndTheSankeyNeverShareAClaim(t *testing.T) {
	t.Parallel()
	result := repositoryFlowResult(repositoryFlowLinks())
	theme := themeShareResultFor(contractsv1.ContextFabricSubjectTeam, 0.6, 0.2, 0.1, 0.1).ClaimedFacts[0]
	theme.Subject = result.ClaimedFacts[0].Subject
	result.ClaimedFacts = append(result.ClaimedFacts, theme)
	shapes, _ := selectRenderShapes(result, repositoryFlowFrameOf(), RenderShapeOptions{InvestmentRepositoryFlow: true, InvestmentThemeShare: true})
	if len(sankeysOf(shapes)) != 1 {
		t.Fatalf("shapes %+v, want a sankey", shapes)
	}
	cited := map[contractsv1.ContextFabricRenderKind]map[string]struct{}{}
	for _, shape := range shapes {
		if cited[shape.Kind] == nil {
			cited[shape.Kind] = map[string]struct{}{}
		}
		for _, series := range shape.Series {
			for _, point := range series.Points {
				cited[shape.Kind][point.Source.ClaimID] = struct{}{}
			}
		}
	}
	for claim := range cited[contractsv1.ContextFabricRenderKindSankey] {
		if _, shared := cited[contractsv1.ContextFabricRenderKindTreemap][claim]; shared {
			t.Fatalf("the treemap and the sankey both cite %q", claim)
		}
	}
	result.RenderShapes = shapes
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the contract refused the shapes: %v", err)
	}
}

func TestAStoredSankeyIsRemovedWhenTheRuleIsOff(t *testing.T) {
	t.Parallel()
	result := repositoryFlowResult(repositoryFlowLinks())
	result.RenderShapes, _ = selectRenderShapes(result, repositoryFlowFrameOf(), sankeyOn)
	if len(result.RenderShapes) != 1 {
		t.Fatalf("shapes %+v", result.RenderShapes)
	}
	everyOtherSwitch := RenderShapeOptions{InvestmentThemeShare: true, ComparisonSideBySide: true, CompletionForecast: true}
	if off := WithoutDisabledRenderShapes(result, everyOtherSwitch); off.RenderShapes != nil {
		t.Fatalf("the sankey switch off kept %+v", off.RenderShapes)
	}
	everySwitch := everyOtherSwitch
	everySwitch.InvestmentRepositoryFlow = true
	if on := WithoutDisabledRenderShapes(result, everySwitch); len(on.RenderShapes) != 1 {
		t.Fatalf("on removed the sankey: %+v", on.RenderShapes)
	}
	if on := WithoutDisabledRenderShapes(result, sankeyOn); len(on.RenderShapes) != 1 {
		t.Fatalf("the sankey switch alone removed the sankey: %+v", on.RenderShapes)
	}
	if len(result.RenderShapes) != 1 {
		t.Fatal("the input was changed")
	}
}

// The plan addition on CHAOS-12582: an answer stored with the switch off is not
// reused once it is on.
func TestTheSankeySwitchJoinsTheReuseKey(t *testing.T) {
	t.Parallel()
	off := ReuseQuestionFamilyVersion(RenderShapeOptions{InvestmentThemeShare: true})
	on := ReuseQuestionFamilyVersion(RenderShapeOptions{InvestmentThemeShare: true, InvestmentRepositoryFlow: true})
	if off == on {
		t.Fatalf("the sankey switch does not reach the reuse key: %q either way", on)
	}
	if want := QuestionFamilyTableVersion + "+render:investment_repository_flow,investment_theme_share"; on != want {
		t.Fatalf("on = %q, want %q", on, want)
	}
}

func TestTheRepositoryFlowThemeOrderIsTheCanon(t *testing.T) {
	t.Parallel()
	if len(repositoryFlowThemeOrder) != contractsv1.ContextFabricInvestmentThemeCount() {
		t.Fatalf("%d themes in the order, %d in the canon", len(repositoryFlowThemeOrder), contractsv1.ContextFabricInvestmentThemeCount())
	}
	seen := map[string]bool{}
	for _, theme := range repositoryFlowThemeOrder {
		if _, known := contractsv1.ContextFabricInvestmentThemeLabel(theme); !known || seen[theme] {
			t.Fatalf("theme %q is not in the canon, or repeats", theme)
		}
		seen[theme] = true
	}
}

// The served plan is what authorizes the kind: investment_allocation's registry
// row names sankey, so a served plan of that family carries it.
func TestTheInvestmentAllocationFamilyAuthorizesTheSankey(t *testing.T) {
	t.Parallel()
	definition, found := LookupQuestionFamily(QuestionFamilyInvestmentAllocation)
	if !found {
		t.Fatal("no investment_allocation row")
	}
	authorized := false
	for _, kind := range definition.RenderKinds {
		authorized = authorized || kind == contractsv1.ContextFabricRenderKindSankey
	}
	if !authorized {
		t.Fatalf("investment_allocation's render kinds %v do not name sankey", definition.RenderKinds)
	}
}
