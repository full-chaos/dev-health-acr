package contextfabric

import (
	"fmt"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12521: the rule that yields the treemap. The result here is the shape a
// served answer has: an investment_allocation plan and one claimed fact holding
// the theme_breakdown table the provider declares.

const themeShareTestClaim = "claim_theme_share_selection"

func themeShareResultFor(kind contractsv1.ContextFabricSubjectKind, shares ...float64) InvestigationResult {
	tokens := []string{ThemeFeatureDelivery, ThemeOperational, ThemeMaintenance, ThemeQuality, ThemeRisk}
	rows := make([]ClaimedFactRow, 0, len(shares))
	for index, share := range shares {
		value, theme := share, tokens[index]
		rows = append(rows, ClaimedFactRow{Fields: map[string]ScalarValue{
			"theme": {String: &theme}, "share": {Number: &value}, "weighted_effort": {Number: &value},
		}})
	}
	result := validInvestigationResult()
	result.Cohort = nil
	result.Interpretation.Shape = ShapeSingleSubject
	result.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{
		Family:        QuestionFamilyInvestmentAllocation,
		FamilyVersion: QuestionFamilyTableVersion,
		RenderKinds:   []contractsv1.ContextFabricRenderKind{contractsv1.ContextFabricRenderKindTable, contractsv1.ContextFabricRenderKindSeries, contractsv1.ContextFabricRenderKindTreemap},
	}
	result.ClaimedFacts = []ClaimedFact{{
		ClaimID: themeShareTestClaim, Kind: FactInvestment, Field: "attribution_basis", Rows: rows,
		Subject: contractsv1.ContextFabricSubjectRef{Kind: kind, CanonicalID: "subject:one", Label: "acme/widgets"},
		Table: &contractsv1.ContextFabricClaimedFactTable{
			Field: "theme_breakdown", Shape: contractsv1.ContextFabricFactTableShapeBreakdown, Key: []string{"theme"},
			Measures: []string{"share", "weighted_effort"}, Observations: []string{"source", "attribution"},
		},
	}}
	return result
}

func treemapsOf(shapes []contractsv1.ContextFabricRenderShape) []contractsv1.ContextFabricRenderShape {
	var out []contractsv1.ContextFabricRenderShape
	for _, shape := range shapes {
		if shape.Kind == contractsv1.ContextFabricRenderKindTreemap {
			out = append(out, shape)
		}
	}
	return out
}

func skipOf(t *testing.T, event RenderShapeSelectionEvent) RenderShapeSkipReason {
	t.Helper()
	if err := event.Accounted(); err != nil {
		t.Fatalf("Accounted: %v", err)
	}
	for _, skip := range event.Skipped {
		if skip.Rule == contractsv1.ContextFabricRenderRuleInvestmentThemeShare {
			return skip.Reason
		}
	}
	return ""
}

func TestAnInvestmentAnswerGetsItsTreemap(t *testing.T) {
	t.Parallel()
	result := themeShareResultFor(contractsv1.ContextFabricSubjectRepository, 0.5, 0.25, 0.25, 0, 0)
	shapes, event := selectRenderShapes(result, nil, RenderShapeOptions{InvestmentThemeShare: true})
	treemaps := treemapsOf(shapes)
	if len(treemaps) != 1 {
		t.Fatalf("shapes %+v, want one treemap", shapes)
	}
	shape := treemaps[0]
	if shape.SelectedBy != contractsv1.ContextFabricRenderRuleInvestmentThemeShare || shape.ValueUnit != contractsv1.ContextFabricRenderValueUnitRatio || shape.Presentation != "" {
		t.Fatalf("shape %+v", shape)
	}
	if shape.Title != "Effort share by theme" || shape.AxisLabel != "Theme" || shape.ValueLabel != "Share of effort (ratio)" || shape.Series[0].Label != "Share of effort" {
		t.Fatalf("words %+v", shape)
	}
	points := shape.Series[0].Points
	if len(points) != 5 {
		t.Fatalf("points %d: a measured zero stays a part of the whole", len(points))
	}
	if points[0].Label != "Feature Delivery" || points[0].Value != 0.5 || *points[0].Source.RowIndex != 0 || points[4].Label != "Risk / Security" || points[4].Value != 0 {
		t.Fatalf("points %+v", points)
	}
	if skip := skipOf(t, event); skip != "" {
		t.Fatalf("a selected rule recorded skip %q", skip)
	}
	result.RenderShapes = shapes
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the contract refused the shape: %v", err)
	}
}

func TestATeamGetsTheTreemapToo(t *testing.T) {
	t.Parallel()
	shapes, _ := selectRenderShapes(themeShareResultFor(contractsv1.ContextFabricSubjectTeam, 0.5, 0.5), nil, RenderShapeOptions{InvestmentThemeShare: true})
	if len(treemapsOf(shapes)) != 1 {
		t.Fatalf("shapes %+v", shapes)
	}
}

// Off is the default, and SelectRenderShapes is off: every caller that does not
// ask for the rule keeps what it had.
func TestTheRuleIsOffByDefault(t *testing.T) {
	t.Parallel()
	shapes, event := SelectRenderShapes(themeShareResultFor(contractsv1.ContextFabricSubjectRepository, 0.5, 0.5), nil)
	if len(treemapsOf(shapes)) != 0 {
		t.Fatalf("shapes %+v", shapes)
	}
	if skip := skipOf(t, event); skip != RenderShapeSkipRuleDisabled {
		t.Fatalf("skip %q", skip)
	}
}

func TestNoTreemapWithoutTheRulesEvidence(t *testing.T) {
	t.Parallel()
	shares := []float64{0.5, 0.5}
	subject := contractsv1.ContextFabricSubjectRepository
	for name, plant := range map[string]struct {
		edit func(*InvestigationResult)
		want RenderShapeSkipReason
	}{
		"an unrelated question": {func(r *InvestigationResult) { r.AnswerPlan.Family = QuestionFamilySubjectInvestigation }, RenderShapeSkipNotInvestmentAllocation},
		"no plan":               {func(r *InvestigationResult) { r.AnswerPlan = nil }, RenderShapeSkipNotInvestmentAllocation},
		"not authorized": {func(r *InvestigationResult) {
			r.AnswerPlan.RenderKinds = []contractsv1.ContextFabricRenderKind{contractsv1.ContextFabricRenderKindTable}
		}, RenderShapeSkipNotPlanAuthorized},
		"a cohort answer": {func(r *InvestigationResult) { r.Cohort = &Cohort{} }, RenderShapeSkipCohortAnswer},
		"a cohort shape":  {func(r *InvestigationResult) { r.Interpretation.Shape = ShapeDiscoveredCohort }, RenderShapeSkipCohortAnswer},
		"no fact":         {func(r *InvestigationResult) { r.ClaimedFacts = nil }, RenderShapeSkipNoThemeBreakdown},
		"another kind":    {func(r *InvestigationResult) { r.ClaimedFacts[0].Kind = FactStatus }, RenderShapeSkipNoThemeBreakdown},
		"no declaration":  {func(r *InvestigationResult) { r.ClaimedFacts[0].Table = nil }, RenderShapeSkipNoThemeBreakdown},
		"another table":   {func(r *InvestigationResult) { r.ClaimedFacts[0].Table.Field = "daily_investment" }, RenderShapeSkipNoThemeBreakdown},
		"not a breakdown": {func(r *InvestigationResult) {
			r.ClaimedFacts[0].Table.Shape = contractsv1.ContextFabricFactTableShapeTimeSeries
		}, RenderShapeSkipNoThemeBreakdown},
		"share unmeasured":  {func(r *InvestigationResult) { r.ClaimedFacts[0].Table.Measures = []string{"weighted_effort"} }, RenderShapeSkipNoThemeBreakdown},
		"a series in front": {func(r *InvestigationResult) { r.ClaimedFacts[0].TimeSeriesRows = r.ClaimedFacts[0].Rows }, RenderShapeSkipNoThemeBreakdown},
		"two subjects": {func(r *InvestigationResult) {
			second := r.ClaimedFacts[0]
			second.ClaimID = "claim_theme_share_second"
			r.ClaimedFacts = append(r.ClaimedFacts, second)
		}, RenderShapeSkipSeveralThemeBreakdowns},
		"a person": {func(r *InvestigationResult) {
			r.ClaimedFacts[0].Subject.Kind = contractsv1.ContextFabricSubjectKind("person")
		}, RenderShapeSkipSubjectKindNotCharted},
		"a project": {func(r *InvestigationResult) { r.ClaimedFacts[0].Subject.Kind = contractsv1.ContextFabricSubjectProject }, RenderShapeSkipSubjectKindNotCharted},
		"an organization": {func(r *InvestigationResult) {
			r.ClaimedFacts[0].Subject.Kind = contractsv1.ContextFabricSubjectOrganization
		}, RenderShapeSkipSubjectKindNotCharted},
		"no theme": {func(r *InvestigationResult) { delete(r.ClaimedFacts[0].Rows[1].Fields, "theme") }, RenderShapeSkipThemeRowUnusable},
		"unknown theme": {func(r *InvestigationResult) {
			token := "unassigned"
			r.ClaimedFacts[0].Rows[1].Fields["theme"] = ScalarValue{String: &token}
		}, RenderShapeSkipThemeRowUnusable},
		"no share": {func(r *InvestigationResult) { delete(r.ClaimedFacts[0].Rows[1].Fields, "share") }, RenderShapeSkipThemeRowUnusable},
		"a share above one": {func(r *InvestigationResult) {
			value := 1.5
			r.ClaimedFacts[0].Rows[1].Fields["share"] = ScalarValue{Number: &value}
		}, RenderShapeSkipThemeRowUnusable},
		"a share below zero": {func(r *InvestigationResult) {
			value := -0.5
			r.ClaimedFacts[0].Rows[1].Fields["share"] = ScalarValue{Number: &value}
		}, RenderShapeSkipThemeRowUnusable},
		"one part": {func(r *InvestigationResult) {
			value := 0.0
			r.ClaimedFacts[0].Rows[1].Fields["share"] = ScalarValue{Number: &value}
		}, RenderShapeSkipFewerThanTwoShares},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := themeShareResultFor(subject, shares...)
			plant.edit(&result)
			shapes, event := selectRenderShapes(result, nil, RenderShapeOptions{InvestmentThemeShare: true})
			if len(treemapsOf(shapes)) != 0 {
				t.Fatalf("shapes %+v", shapes)
			}
			if got := skipOf(t, event); got != plant.want {
				t.Fatalf("skip %q, want %q", got, plant.want)
			}
		})
	}
}

// A family carried from an earlier turn does not make this question about one
// subject's split.
func TestACarriedFamilyDoesNotChartACohortFrame(t *testing.T) {
	t.Parallel()
	result := themeShareResultFor(contractsv1.ContextFabricSubjectRepository, 0.5, 0.5)
	result.AnswerPlan.FamilySource = QuestionFamilySourceCarried
	frame := &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionDiscoveredKind, Discovered: &DiscoveredSetExpression{MemberKind: contractsv1.ContextFabricSubjectTeam}}}
	shapes, event := selectRenderShapes(result, frame, RenderShapeOptions{InvestmentThemeShare: true})
	if len(treemapsOf(shapes)) != 0 {
		t.Fatalf("shapes %+v", shapes)
	}
	if skip := skipOf(t, event); skip != RenderShapeSkipCohortAnswer {
		t.Fatalf("skip %q", skip)
	}
	// A follow-up about the same one subject keeps its chart.
	shapes, _ = selectRenderShapes(result, nil, RenderShapeOptions{InvestmentThemeShare: true})
	if len(treemapsOf(shapes)) != 1 {
		t.Fatalf("carried on one subject: %+v", shapes)
	}
}

func TestAStoredTreemapIsRemovedWhenTheRuleIsOff(t *testing.T) {
	t.Parallel()
	result := themeShareResultFor(contractsv1.ContextFabricSubjectRepository, 0.5, 0.5)
	result.RenderShapes, _ = selectRenderShapes(result, nil, RenderShapeOptions{InvestmentThemeShare: true})
	other := result.RenderShapes[0]
	other.Kind, other.SelectedBy = contractsv1.ContextFabricRenderKindSeries, contractsv1.ContextFabricRenderRuleDatedFactTrend
	result.RenderShapes = append(result.RenderShapes, other)

	off := WithoutDisabledRenderShapes(result, RenderShapeOptions{})
	if len(off.RenderShapes) != 1 || off.RenderShapes[0].SelectedBy != contractsv1.ContextFabricRenderRuleDatedFactTrend {
		t.Fatalf("off kept %+v", off.RenderShapes)
	}
	if len(result.RenderShapes) != 2 {
		t.Fatal("the input was changed")
	}
	if on := WithoutDisabledRenderShapes(result, RenderShapeOptions{InvestmentThemeShare: true}); len(on.RenderShapes) != 2 {
		t.Fatalf("on removed a shape: %+v", on.RenderShapes)
	}
	alone := result
	alone.RenderShapes = result.RenderShapes[:1]
	if got := WithoutDisabledRenderShapes(alone, RenderShapeOptions{}); got.RenderShapes != nil {
		t.Fatalf("an answer left with no shape must carry none: %+v", got.RenderShapes)
	}
}

// The trends take what the shape bound leaves, so the treemap never pushes an
// answer over it. With the bound full of trends the treemap is still served.
func TestTheTreemapIsSelectedAheadOfTrends(t *testing.T) {
	t.Parallel()
	result := themeShareResultFor(contractsv1.ContextFabricSubjectRepository, 0.5, 0.5)
	trend := declaredTrendAnswer().ClaimedFacts[0]
	for index := range contractsv1.ContextFabricRenderShapesMaxCount {
		fact := trend
		fact.ClaimID = fmt.Sprintf("claim_trend_filling_the_bound_%d", index)
		fact.Subject.CanonicalID = fmt.Sprintf("subject:trend:%d", index)
		result.ClaimedFacts = append(result.ClaimedFacts, fact)
	}
	shapes, _ := selectRenderShapes(result, nil, RenderShapeOptions{InvestmentThemeShare: true})
	if len(shapes) != contractsv1.ContextFabricRenderShapesMaxCount {
		t.Fatalf("%d shapes, want the bound", len(shapes))
	}
	if shapes[0].Kind != contractsv1.ContextFabricRenderKindTreemap {
		t.Fatalf("first shape %q, want the treemap", shapes[0].Kind)
	}
}
