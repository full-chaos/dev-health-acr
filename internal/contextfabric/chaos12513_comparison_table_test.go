package contextfabric

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12513: the rule that yields the side-by-side table. The result here is the
// shape a served comparison has: an explicit_comparison plan, two committed
// subjects, and the claims of each side.

const (
	tableMedian = "window_pr_cycle_hours_median"
	tableCount  = "window_pr_count"
	tableDaily  = "median_pr_cycle_hours"
	tableGini   = "code_ownership_gini"
)

var (
	tableAPI = contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:acme-api", Label: "acme/api"}
	tableWeb = contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:acme-web", Label: "acme/web"}
)

func tableClaim(id string, subject SubjectRef, field string, value ScalarValue) ClaimedFact {
	return ClaimedFact{ClaimID: id, Kind: FactMetrics, Subject: subject, Field: field, Value: value}
}

func tableNumber(value float64) ScalarValue { return ScalarValue{Number: &value} }

func tableInteger(value int64) ScalarValue { return ScalarValue{Integer: &value} }

// tableResult compares two repositories on two window measures.
func tableResult() InvestigationResult {
	result := validInvestigationResult()
	result.Cohort = nil
	result.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{
		Family:        QuestionFamilyExplicitComparison,
		FamilyVersion: QuestionFamilyTableVersion,
		RenderKinds:   []contractsv1.ContextFabricRenderKind{contractsv1.ContextFabricRenderKindTable, contractsv1.ContextFabricRenderKindQuadrant},
	}
	result.SubjectResolution.Committed = []SubjectRef{tableAPI, tableWeb}
	result.ClaimedFacts = []ClaimedFact{
		tableClaim("claim_api_median", tableAPI, tableMedian, tableNumber(1.95)),
		tableClaim("claim_api_count", tableAPI, tableCount, tableInteger(48)),
		tableClaim("claim_web_median", tableWeb, tableMedian, tableNumber(1.66)),
		tableClaim("claim_web_count", tableWeb, tableCount, tableInteger(82)),
	}
	return result
}

func tableFrame() *QuestionFrame {
	frame := comparisonFrame(comparisonNamedOperand("acme-api", contractsv1.ContextFabricSubjectRepository), comparisonNamedOperand("acme-web", contractsv1.ContextFabricSubjectRepository))
	return &frame
}

func tableScopes(_ FactKind, _ SubjectKind, field string) FactFieldScope {
	switch field {
	case tableMedian, tableCount:
		return FactScopeWindow
	case tableDaily, tableGini:
		return FactScopeLatestDay
	case "day_count":
		return FactScopeNotCompared
	}
	return ""
}

func tableOptions() RenderShapeOptions {
	return RenderShapeOptions{ComparisonSideBySide: true, FieldScope: tableScopes}
}

func tableOf(t *testing.T, shapes []contractsv1.ContextFabricRenderShape) *contractsv1.ContextFabricRenderShape {
	t.Helper()
	var found *contractsv1.ContextFabricRenderShape
	for index := range shapes {
		if shapes[index].Kind == contractsv1.ContextFabricRenderKindTable {
			if found != nil {
				t.Fatal("two tables")
			}
			found = &shapes[index]
		}
	}
	return found
}

func tableSkipOf(t *testing.T, event RenderShapeSelectionEvent) RenderShapeSkipReason {
	t.Helper()
	if err := event.Accounted(); err != nil {
		t.Fatalf("Accounted: %v", err)
	}
	for _, skip := range event.Skipped {
		if skip.Rule == contractsv1.ContextFabricRenderRuleComparisonSideBySide {
			return skip.Reason
		}
	}
	return ""
}

func tableWord(t *testing.T, field string) string {
	t.Helper()
	word, ok := contractsv1.ContextFabricVocabularyLabel(contractsv1.ContextFabricVocabularyFactField, field)
	if !ok {
		t.Fatalf("field %q has no word", field)
	}
	return word
}

// labelsOf reads one series as label -> value.
func labelsOf(series contractsv1.ContextFabricRenderSeries) map[string]float64 {
	out := make(map[string]float64, len(series.Points))
	for _, point := range series.Points {
		out[point.Label] = point.Value
	}
	return out
}

func TestAComparisonGetsItsTable(t *testing.T) {
	t.Parallel()
	result := tableResult()
	shapes, event := selectRenderShapes(result, tableFrame(), tableOptions())
	table := tableOf(t, shapes)
	if table == nil {
		t.Fatalf("no table; skip = %q", tableSkipOf(t, event))
	}
	if table.SelectedBy != contractsv1.ContextFabricRenderRuleComparisonSideBySide || table.AxisKind != contractsv1.ContextFabricRenderAxisCategory ||
		table.ValueUnit != "" || table.Presentation != "" || table.Title == "" || table.AxisLabel == "" || table.ValueLabel == "" {
		t.Fatalf("shape %+v", table)
	}
	if len(table.Series) != 2 || table.Series[0].Key != tableAPI.CanonicalID || table.Series[1].Key != tableWeb.CanonicalID ||
		table.Series[0].Label != tableAPI.Label || table.Series[1].Label != tableWeb.Label {
		t.Fatalf("series %+v", table.Series)
	}
	median, count := tableWord(t, tableMedian), tableWord(t, tableCount)
	if got := labelsOf(table.Series[0]); !reflect.DeepEqual(got, map[string]float64{median: 1.95, count: 48}) {
		t.Fatalf("side one %v", got)
	}
	if got := labelsOf(table.Series[1]); !reflect.DeepEqual(got, map[string]float64{median: 1.66, count: 82}) {
		t.Fatalf("side two %v", got)
	}
	source := table.Series[1].Points[1].Source
	if source.Kind != contractsv1.ContextFabricRenderSourceClaimedFactValue || source.ClaimID != "claim_web_count" || source.Field != tableCount {
		t.Fatalf("source %+v", source)
	}
	result.RenderShapes = shapes
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the contract refused the table: %v", err)
	}
	if event.ComparisonMeasuresLeftOut != 0 {
		t.Fatalf("left out %d", event.ComparisonMeasuresLeftOut)
	}
	selected := slices.ContainsFunc(event.Selected, func(s RenderShapeSelection) bool {
		return s.Rule == contractsv1.ContextFabricRenderRuleComparisonSideBySide && s.Kind == contractsv1.ContextFabricRenderKindTable && s.SeriesCount == 2 && s.PointCount == 4
	})
	if !selected {
		t.Fatalf("selected %+v", event.Selected)
	}
	if err := event.Accounted(); err != nil {
		t.Fatal(err)
	}
}

func TestSelectRenderShapesKeepsTheRuleOff(t *testing.T) {
	t.Parallel()
	shapes, event := SelectRenderShapes(tableResult(), tableFrame())
	if tableOf(t, shapes) != nil || tableSkipOf(t, event) != RenderShapeSkipRuleDisabled {
		t.Fatalf("shapes %+v, skip %q", shapes, tableSkipOf(t, event))
	}
}

// The order of the sides is the order of the question's operands, and the rows
// follow the claims. Both are the same on every read of one result.
func TestATableHoldsItsOrder(t *testing.T) {
	t.Parallel()
	result := tableResult()
	result.SubjectResolution.Committed = []SubjectRef{tableWeb, tableAPI}
	first, _ := selectRenderShapes(result, tableFrame(), tableOptions())
	table := tableOf(t, first)
	if table == nil || table.Series[0].Key != tableWeb.CanonicalID {
		t.Fatalf("table %+v", table)
	}
	if table.Series[0].Points[0].Label != tableWord(t, tableMedian) {
		t.Fatalf("rows left claim order: %+v", table.Series[0].Points)
	}
	for range 25 {
		again, _ := selectRenderShapes(result, tableFrame(), tableOptions())
		if !reflect.DeepEqual(first, again) {
			t.Fatal("selection is not deterministic")
		}
	}
}

// A measure a side lacks has no point, and the shared measure keeps the table.
func TestAGapHasNoPoint(t *testing.T) {
	t.Parallel()
	result := tableResult()
	result.ClaimedFacts = result.ClaimedFacts[:3]
	shapes, event := selectRenderShapes(result, tableFrame(), tableOptions())
	table := tableOf(t, shapes)
	if table == nil {
		t.Fatalf("no table; skip = %q", tableSkipOf(t, event))
	}
	if got := labelsOf(table.Series[1]); !reflect.DeepEqual(got, map[string]float64{tableWord(t, tableMedian): 1.66}) {
		t.Fatalf("the side with a gap reads %v", got)
	}
	if event.ComparisonMeasuresLeftOut != 0 {
		t.Fatalf("a gap is no measure left out: %d", event.ComparisonMeasuresLeftOut)
	}
}

func dailyClaim(id string, subject SubjectRef, day string, value float64) ClaimedFact {
	claim := tableClaim(id, subject, tableDaily, tableNumber(value))
	claim.Rows = []ClaimedFactRow{{Fields: map[string]ScalarValue{FactDayColumn: {String: &day}, tableDaily: tableNumber(value)}}}
	return claim
}

func TestALatestDayFieldNeedsTheSameDay(t *testing.T) {
	t.Parallel()
	daily := tableWord(t, tableDaily)
	for name, tc := range map[string]struct {
		edit    func(api, web *ClaimedFact)
		kept    bool
		leftOut int
	}{
		"the same day": {func(_, _ *ClaimedFact) {}, true, 0},
		"different days": {func(_, web *ClaimedFact) {
			day := "2026-10-30"
			web.Rows[0].Fields[FactDayColumn] = ScalarValue{String: &day}
		}, false, 1},
		"rows that prove no day": {func(_, web *ClaimedFact) { web.Rows = nil }, false, 1},
		"a row with no day": {func(_, web *ClaimedFact) {
			delete(web.Rows[0].Fields, FactDayColumn)
		}, false, 1},
		"no row with a day on either side": {func(api, web *ClaimedFact) {
			delete(api.Rows[0].Fields, FactDayColumn)
			delete(web.Rows[0].Fields, FactDayColumn)
		}, false, 1},
		"a zero that its row does not hold": {func(_, web *ClaimedFact) {
			web.Value = tableNumber(0)
			delete(web.Rows[0].Fields, tableDaily)
		}, false, 1},
		"a row that does not hold this value": {func(_, web *ClaimedFact) {
			web.Rows[0].Fields[tableDaily] = tableNumber(9)
		}, false, 1},
		"a row with no such field": {func(_, web *ClaimedFact) {
			delete(web.Rows[0].Fields, tableDaily)
		}, false, 1},
		"the day in the time series table": {func(_, web *ClaimedFact) {
			web.TimeSeriesRows, web.Rows = web.Rows, []ClaimedFactRow{{Fields: map[string]ScalarValue{}}}
		}, true, 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := tableResult()
			api, web := dailyClaim("claim_api_daily", tableAPI, "2026-10-31", 2.5), dailyClaim("claim_web_daily", tableWeb, "2026-10-31", 3.5)
			tc.edit(&api, &web)
			result.ClaimedFacts = append(result.ClaimedFacts, api, web)
			shapes, event := selectRenderShapes(result, tableFrame(), tableOptions())
			table := tableOf(t, shapes)
			if table == nil {
				t.Fatalf("the window measures keep the table; skip = %q", tableSkipOf(t, event))
			}
			if _, kept := labelsOf(table.Series[1])[daily]; kept != tc.kept {
				t.Fatalf("kept the daily measure = %v, want %v", kept, tc.kept)
			}
			if event.ComparisonMeasuresLeftOut != tc.leftOut {
				t.Fatalf("left out %d, want %d", event.ComparisonMeasuresLeftOut, tc.leftOut)
			}
		})
	}
}

// One side alone proves its day, and a measure only it holds is a gap, not a
// mismatch.
func TestALatestDayFieldOnOneSideStays(t *testing.T) {
	t.Parallel()
	result := tableResult()
	result.ClaimedFacts = append(result.ClaimedFacts, dailyClaim("claim_api_daily", tableAPI, "2026-10-31", 2.5))
	shapes, event := selectRenderShapes(result, tableFrame(), tableOptions())
	table := tableOf(t, shapes)
	if table == nil || len(table.Series[0].Points) != 3 || len(table.Series[1].Points) != 2 || event.ComparisonMeasuresLeftOut != 0 {
		t.Fatalf("table %+v, left out %d", table, event.ComparisonMeasuresLeftOut)
	}
}

func TestTwoClaimsOfOneCellLeaveItEmpty(t *testing.T) {
	t.Parallel()
	result := tableResult()
	result.ClaimedFacts = append(result.ClaimedFacts, tableClaim("claim_api_count_2", tableAPI, tableCount, tableInteger(49)))
	shapes, event := selectRenderShapes(result, tableFrame(), tableOptions())
	table := tableOf(t, shapes)
	if table == nil {
		t.Fatalf("no table; skip = %q", tableSkipOf(t, event))
	}
	if _, held := labelsOf(table.Series[0])[tableWord(t, tableCount)]; held {
		t.Fatalf("a doubled cell was filled: %+v", table.Series[0].Points)
	}
	if got := labelsOf(table.Series[1])[tableWord(t, tableCount)]; got != 82 {
		t.Fatalf("the other side lost its cell: %v", got)
	}

	// Both sides doubled leave no cell, and that measure is left out.
	result.ClaimedFacts = append(result.ClaimedFacts, tableClaim("claim_web_count_2", tableWeb, tableCount, tableInteger(83)))
	shapes, event = selectRenderShapes(result, tableFrame(), tableOptions())
	if table = tableOf(t, shapes); table == nil || len(table.Series[0].Points) != 1 || event.ComparisonMeasuresLeftOut != 1 {
		t.Fatalf("table %+v, left out %d", table, event.ComparisonMeasuresLeftOut)
	}
}

// One field under two fact kinds is two measures with one word. The first stays,
// because a label names one fact kind.
func TestOneWordKeepsOneFactKind(t *testing.T) {
	t.Parallel()
	result := tableResult()
	other := tableClaim("claim_api_other", tableAPI, tableCount, tableInteger(7))
	other.Kind = FactPullRequests
	result.ClaimedFacts = append(result.ClaimedFacts, other)
	shapes, event := selectRenderShapes(result, tableFrame(), tableOptions())
	table := tableOf(t, shapes)
	if table == nil || event.ComparisonMeasuresLeftOut != 1 || labelsOf(table.Series[0])[tableWord(t, tableCount)] != 48 {
		t.Fatalf("table %+v, left out %d", table, event.ComparisonMeasuresLeftOut)
	}
}

func TestATableHoldsAsManyRowsAsAShape(t *testing.T) {
	t.Parallel()
	result := tableResult()
	names := make([]string, 0, len(contractsv1.ContextFabricFactFieldLabels()))
	for name := range contractsv1.ContextFabricFactFieldLabels() {
		names = append(names, name)
	}
	slices.Sort(names)
	limit := contractsv1.ContextFabricRenderPointsMaxCount
	result.ClaimedFacts = nil
	for index := range limit + 2 {
		result.ClaimedFacts = append(result.ClaimedFacts,
			tableClaim("claim_api_"+names[index], tableAPI, names[index], tableInteger(int64(index))),
			tableClaim("claim_web_"+names[index], tableWeb, names[index], tableInteger(int64(index))))
	}
	options := tableOptions()
	options.FieldScope = func(FactKind, SubjectKind, string) FactFieldScope { return FactScopeWindow }
	shapes, event := selectRenderShapes(result, tableFrame(), options)
	table := tableOf(t, shapes)
	if table == nil || len(table.Series[0].Points) != limit || event.ComparisonMeasuresLeftOut != 2 {
		t.Fatalf("table %v, left out %d", table != nil, event.ComparisonMeasuresLeftOut)
	}
}

func TestATableNamesASideWithNoLabelByItsID(t *testing.T) {
	t.Parallel()
	result := tableResult()
	long := strings.Repeat("a", contractsv1.ContextFabricRenderLabelMaxLength+4)
	result.SubjectResolution.Committed[0].Label = ""
	result.SubjectResolution.Committed[1].Label = long
	shapes, event := selectRenderShapes(result, tableFrame(), tableOptions())
	table := tableOf(t, shapes)
	if table == nil {
		t.Fatalf("no table; skip = %q", tableSkipOf(t, event))
	}
	if table.Series[0].Label != tableAPI.CanonicalID || len(table.Series[1].Label) != contractsv1.ContextFabricRenderLabelMaxLength {
		t.Fatalf("series %q, %d", table.Series[0].Label, len(table.Series[1].Label))
	}
}

func TestAComparisonSkipsWithItsReason(t *testing.T) {
	t.Parallel()
	sides := func(count int) []SubjectRef {
		out := make([]SubjectRef, 0, count)
		for index := range count {
			out = append(out, SubjectRef{Kind: tableAPI.Kind, CanonicalID: "repository:" + strings.Repeat("x", index+1), Label: "x"})
		}
		return out
	}
	for name, tc := range map[string]struct {
		edit    func(*InvestigationResult, **QuestionFrame, *RenderShapeOptions)
		want    RenderShapeSkipReason
		leftOut int
	}{
		"the flag off": {func(_ *InvestigationResult, _ **QuestionFrame, o *RenderShapeOptions) { o.ComparisonSideBySide = false }, RenderShapeSkipRuleDisabled, 0},
		"no plan":      {func(r *InvestigationResult, _ **QuestionFrame, _ *RenderShapeOptions) { r.AnswerPlan = nil }, RenderShapeSkipNotExplicitComparison, 0},
		"another family": {func(r *InvestigationResult, _ **QuestionFrame, _ *RenderShapeOptions) {
			r.AnswerPlan.Family = QuestionFamilyInvestmentAllocation
		}, RenderShapeSkipNotExplicitComparison, 0},
		"no frame": {func(_ *InvestigationResult, f **QuestionFrame, _ *RenderShapeOptions) { *f = nil }, RenderShapeSkipNotExplicitComparison, 0},
		"a frame that asks no comparison": {func(_ *InvestigationResult, f **QuestionFrame, _ *RenderShapeOptions) {
			frame := goalFrame(allocationSubject(), GoalAllocateInvestment)
			*f = &frame
		}, RenderShapeSkipNotExplicitComparison, 0},
		"a plan that authorizes no table": {func(r *InvestigationResult, _ **QuestionFrame, _ *RenderShapeOptions) {
			r.AnswerPlan.RenderKinds = []contractsv1.ContextFabricRenderKind{contractsv1.ContextFabricRenderKindQuadrant}
		}, RenderShapeSkipNotPlanAuthorized, 0},
		"one side": {func(r *InvestigationResult, _ **QuestionFrame, _ *RenderShapeOptions) {
			r.SubjectResolution.Committed = r.SubjectResolution.Committed[:1]
		}, RenderShapeSkipFewerThanTwoSides, 0},
		"more sides than series": {func(r *InvestigationResult, _ **QuestionFrame, _ *RenderShapeOptions) {
			r.SubjectResolution.Committed = sides(contractsv1.ContextFabricRenderSeriesMaxCount + 1)
		}, RenderShapeSkipTooManySides, 0},
		"sides of two kinds": {func(r *InvestigationResult, _ **QuestionFrame, _ *RenderShapeOptions) {
			r.SubjectResolution.Committed[1].Kind = contractsv1.ContextFabricSubjectTeam
		}, RenderShapeSkipMixedSubjectKinds, 0},
		"no claim": {func(r *InvestigationResult, _ **QuestionFrame, _ *RenderShapeOptions) { r.ClaimedFacts = nil }, RenderShapeSkipNoSharedMeasure, 0},
		"claims on one side": {func(r *InvestigationResult, _ **QuestionFrame, _ *RenderShapeOptions) {
			r.ClaimedFacts = r.ClaimedFacts[:2]
		}, RenderShapeSkipNoSharedMeasure, 0},
		"claims on a subject that is no side": {func(r *InvestigationResult, _ **QuestionFrame, _ *RenderShapeOptions) {
			for index := range r.ClaimedFacts {
				r.ClaimedFacts[index].Subject.CanonicalID = "repository:other"
			}
		}, RenderShapeSkipNoSharedMeasure, 0},
		"a word in place of a number": {func(r *InvestigationResult, _ **QuestionFrame, _ *RenderShapeOptions) {
			word := "open"
			for index := range r.ClaimedFacts {
				r.ClaimedFacts[index].Value = ScalarValue{String: &word}
			}
		}, RenderShapeSkipNoSharedMeasure, 0},
		"a derived value": {func(r *InvestigationResult, _ **QuestionFrame, _ *RenderShapeOptions) {
			for index := range r.ClaimedFacts {
				r.ClaimedFacts[index].DerivedFrom = []contractsv1.ContextFabricDerivationInput{{FactID: "fact_one", Field: "x"}}
			}
		}, RenderShapeSkipNoSharedMeasure, 0},
		"a field with no word": {func(r *InvestigationResult, _ **QuestionFrame, o *RenderShapeOptions) {
			o.FieldScope = func(FactKind, SubjectKind, string) FactFieldScope { return FactScopeWindow }
			for index := range r.ClaimedFacts {
				r.ClaimedFacts[index].Field = "a_future_measure"
			}
		}, RenderShapeSkipNoSharedMeasure, 0},
		"a field with no scope": {func(_ *InvestigationResult, _ **QuestionFrame, o *RenderShapeOptions) {
			o.FieldScope = func(FactKind, SubjectKind, string) FactFieldScope { return "" }
		}, RenderShapeSkipNoSharedMeasure, 0},
		"a field no comparison shows": {func(_ *InvestigationResult, _ **QuestionFrame, o *RenderShapeOptions) {
			o.FieldScope = func(FactKind, SubjectKind, string) FactFieldScope { return FactScopeNotCompared }
		}, RenderShapeSkipNoSharedMeasure, 0},
		"no scopes at all": {func(_ *InvestigationResult, _ **QuestionFrame, o *RenderShapeOptions) { o.FieldScope = nil }, RenderShapeSkipNoSharedMeasure, 0},
		"sides on different days": {func(r *InvestigationResult, _ **QuestionFrame, _ *RenderShapeOptions) {
			r.ClaimedFacts = []ClaimedFact{dailyClaim("claim_api_daily", tableAPI, "2026-10-31", 2.5), dailyClaim("claim_web_daily", tableWeb, "2026-10-30", 3.5)}
		}, RenderShapeSkipNoSharedMeasure, 1},
		"a table the contract refuses": {func(r *InvestigationResult, _ **QuestionFrame, _ *RenderShapeOptions) {
			r.ClaimedFacts[2].Subject.Kind = contractsv1.ContextFabricSubjectTeam
			r.ClaimedFacts[3].Subject.Kind = contractsv1.ContextFabricSubjectTeam
		}, RenderShapeSkipComparisonUnprovable, 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, frame, options := tableResult(), tableFrame(), tableOptions()
			tc.edit(&result, &frame, &options)
			shapes, event := selectRenderShapes(result, frame, options)
			if table := tableOf(t, shapes); table != nil {
				t.Fatalf("a table was drawn: %+v", table)
			}
			if got := tableSkipOf(t, event); got != tc.want {
				t.Fatalf("skip = %q, want %q", got, tc.want)
			}
			if event.ComparisonMeasuresLeftOut != tc.leftOut {
				t.Fatalf("left out %d, want %d", event.ComparisonMeasuresLeftOut, tc.leftOut)
			}
		})
	}
}

// Either statement on this turn's frame is positive evidence.
func TestEitherFrameStatementAsksForATable(t *testing.T) {
	t.Parallel()
	named := goalFrame(allocationSubject(), GoalCompare)
	set := *tableFrame()
	set.Goals = []InvestigationGoal{GoalAllocateInvestment}
	for name, frame := range map[string]*QuestionFrame{"the compare goal": &named, "an explicit set": &set} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if !comparisonIntent(frame) {
				t.Fatal("no intent read")
			}
			if shapes, event := selectRenderShapes(tableResult(), frame, tableOptions()); tableOf(t, shapes) == nil {
				t.Fatalf("skip = %q", tableSkipOf(t, event))
			}
		})
	}
}

func TestWithoutDisabledRenderShapesReadsEachFlag(t *testing.T) {
	t.Parallel()
	shape := func(rule contractsv1.ContextFabricRenderShapeRule) contractsv1.ContextFabricRenderShape {
		return contractsv1.ContextFabricRenderShape{SelectedBy: rule}
	}
	table, treemap, trend := contractsv1.ContextFabricRenderRuleComparisonSideBySide, contractsv1.ContextFabricRenderRuleInvestmentThemeShare, contractsv1.ContextFabricRenderRuleDatedFactTrend
	result := InvestigationResult{RenderShapes: []contractsv1.ContextFabricRenderShape{shape(table), shape(treemap), shape(trend)}}
	for name, tc := range map[string]struct {
		options RenderShapeOptions
		want    []contractsv1.ContextFabricRenderShapeRule
	}{
		"both on":      {RenderShapeOptions{ComparisonSideBySide: true, InvestmentThemeShare: true}, []contractsv1.ContextFabricRenderShapeRule{table, treemap, trend}},
		"table only":   {RenderShapeOptions{ComparisonSideBySide: true}, []contractsv1.ContextFabricRenderShapeRule{table, trend}},
		"treemap only": {RenderShapeOptions{InvestmentThemeShare: true}, []contractsv1.ContextFabricRenderShapeRule{treemap, trend}},
		"both off":     {RenderShapeOptions{}, []contractsv1.ContextFabricRenderShapeRule{trend}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var got []contractsv1.ContextFabricRenderShapeRule
			for _, kept := range WithoutDisabledRenderShapes(result, tc.options).RenderShapes {
				got = append(got, kept.SelectedBy)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("kept %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAFieldScopeIsReadOffItsDeclaration(t *testing.T) {
	t.Parallel()
	lookup := factFieldScopeLookup([]FactCapability{{Kind: FactMetrics, Fields: []FactFieldDeclaration{
		{Name: tableMedian, Scope: FactScopeWindow, SubjectKinds: []SubjectKind{contractsv1.ContextFabricSubjectRepository}},
	}}})
	if got := lookup(FactMetrics, contractsv1.ContextFabricSubjectRepository, tableMedian); got != FactScopeWindow {
		t.Fatalf("scope %q", got)
	}
	for name, got := range map[string]FactFieldScope{
		"another field":   lookup(FactMetrics, contractsv1.ContextFabricSubjectRepository, tableCount),
		"another subject": lookup(FactMetrics, contractsv1.ContextFabricSubjectTeam, tableMedian),
		"another kind":    lookup(FactPullRequests, contractsv1.ContextFabricSubjectRepository, tableMedian),
		"no declarations": factFieldScopeLookup(nil)(FactMetrics, contractsv1.ContextFabricSubjectRepository, tableMedian),
	} {
		if got != "" {
			t.Fatalf("%s reads %q", name, got)
		}
	}
}

// The engine holds what the deployment configured, and a selection reads it.
func TestTheEngineCarriesTheComparisonSwitches(t *testing.T) {
	t.Parallel()
	engine, err := NewEngine(tableEngineDependencies(), EngineOptions{ServiceVersion: "table", NewResultID: resultIDSequence(), ComparisonTableEnabled: true, FactCapabilities: []FactCapability{{Kind: FactMetrics, Fields: []FactFieldDeclaration{{Name: tableMedian, Scope: FactScopeWindow}}}}})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	options := engine.renderShapeOptions
	if !options.ComparisonSideBySide || options.InvestmentThemeShare {
		t.Fatalf("options %+v", options)
	}
	if got := options.FieldScope(FactMetrics, contractsv1.ContextFabricSubjectRepository, tableMedian); got != FactScopeWindow {
		t.Fatalf("scope %q", got)
	}
}

func tableEngineDependencies() EngineDependencies {
	return EngineDependencies{
		Interpreter: &anchorProbeInterpreter{},
		Graph:       &needTurnGraph{},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return validInvestigationResult(), nil
		}),
	}
}
