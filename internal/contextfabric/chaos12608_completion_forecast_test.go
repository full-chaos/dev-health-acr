package contextfabric

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12608: the completion_forecast rule draws one team's completion curve
// from the claimed workload fact's distribution, every row once, each point
// cited to its own row through claimed_fact_table_row.

const forecastTestClaim = "claim_forecast_chaos"

func forecastDistributionRows(days []int64, shares []float64) []ClaimedFactRow {
	rows := make([]ClaimedFactRow, 0, len(days))
	for index := range days {
		day, share, runs := days[index], shares[index], int64(10)
		rows = append(rows, ClaimedFactRow{Fields: map[string]ScalarValue{
			"completion_days": {Integer: &day}, "runs": {Integer: &runs}, "cumulative_share": {Number: &share},
		}})
	}
	return rows
}

func forecastClaim(claimID string, rows []ClaimedFactRow) ClaimedFact {
	p50 := int64(12)
	day := "2026-11-01"
	backlog := int64(40)
	return ClaimedFact{
		ClaimID: claimID, Kind: FactWorkload, Field: "forecast_p50_days", Value: ScalarValue{Integer: &p50},
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:chaos", Label: "CHAOS"},
		Rows:    rows,
		Table: &contractsv1.ContextFabricClaimedFactTable{
			Field: "completion_distribution", Shape: contractsv1.ContextFabricFactTableShapeDistribution,
			Key: []string{"completion_days"}, Measures: []string{"runs", "cumulative_share"},
		},
		// The daily series rides beside the distribution, as #1940 serves it:
		// a claimed_fact_row source would resolve into these rows.
		TimeSeriesRows: []ClaimedFactRow{{Fields: map[string]ScalarValue{"day": {String: &day}, "backlog_size": {Integer: &backlog}}}},
		TimeSeriesTable: &contractsv1.ContextFabricClaimedFactTable{
			Field: "daily_workload", Shape: contractsv1.ContextFabricFactTableShapeTimeSeries, Key: []string{"day"}, Measures: []string{"backlog_size"},
		},
	}
}

func forecastResult(claims ...ClaimedFact) InvestigationResult {
	result := validInvestigationResult()
	result.Cohort = nil
	result.Interpretation.Shape = ShapeSingleSubject
	result.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{
		Family:        QuestionFamilySubjectInvestigation,
		FamilyVersion: QuestionFamilyTableVersion,
		RenderKinds:   []contractsv1.ContextFabricRenderKind{contractsv1.ContextFabricRenderKindTable, contractsv1.ContextFabricRenderKindSeries, contractsv1.ContextFabricRenderKindForecast},
	}
	result.ClaimedFacts = claims
	return result
}

func forecastFrame(obligations ...AnswerObligation) *QuestionFrame {
	frame := goalFrame(allocationSubject(), GoalAssessState)
	frame.Obligations = obligations
	return &frame
}

func forecastsOf(shapes []contractsv1.ContextFabricRenderShape) []contractsv1.ContextFabricRenderShape {
	var out []contractsv1.ContextFabricRenderShape
	for _, shape := range shapes {
		if shape.Kind == contractsv1.ContextFabricRenderKindForecast {
			out = append(out, shape)
		}
	}
	return out
}

func forecastSkipOf(t *testing.T, event RenderShapeSelectionEvent) RenderShapeSkipReason {
	t.Helper()
	if err := event.Accounted(); err != nil {
		t.Fatalf("Accounted: %v", err)
	}
	for _, skip := range event.Skipped {
		if skip.Rule == contractsv1.ContextFabricRenderRuleCompletionForecast {
			return skip.Reason
		}
	}
	return ""
}

var forecastOn = RenderShapeOptions{CompletionForecast: true}

func TestAForecastQuestionGetsItsCompletionCurve(t *testing.T) {
	days := []int64{5, 12, 30, 365}
	shares := []float64{0.01, 0.4, 0.81, 0.81}
	result := forecastResult(forecastClaim(forecastTestClaim, forecastDistributionRows(days, shares)))
	shapes, event := selectRenderShapes(result, forecastFrame(ObligationCompletion), forecastOn)
	if reason := forecastSkipOf(t, event); reason != "" {
		t.Fatalf("skipped: %s", reason)
	}
	forecasts := forecastsOf(shapes)
	if len(forecasts) != 1 {
		t.Fatalf("forecast shapes = %d, want 1", len(forecasts))
	}
	shape := forecasts[0]
	if shape.SelectedBy != contractsv1.ContextFabricRenderRuleCompletionForecast || shape.AxisKind != contractsv1.ContextFabricRenderAxisCategory || shape.ValueUnit != contractsv1.ContextFabricRenderValueUnitRatio {
		t.Fatalf("shape = %+v", shape)
	}
	points := shape.Series[0].Points
	if len(points) != len(days) {
		t.Fatalf("points = %d, want every row (%d)", len(points), len(days))
	}
	for index, point := range points {
		if point.Label != strconv.FormatInt(days[index], 10) || point.Value != shares[index] {
			t.Errorf("point %d = %q %v, want %d %v", index, point.Label, point.Value, days[index], shares[index])
		}
		if point.Source.Kind != contractsv1.ContextFabricRenderSourceClaimedFactTableRow || *point.Source.RowIndex != index {
			t.Errorf("point %d source = %+v", index, point.Source)
		}
	}
	result.RenderShapes = shapes
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the selected forecast does not validate: %v", err)
	}
	// Remaining work asks for the same curve.
	if forecasts := forecastsOf(func() []contractsv1.ContextFabricRenderShape {
		s, _ := selectRenderShapes(result, forecastFrame(ObligationRemainingWork), forecastOn)
		return s
	}()); len(forecasts) != 1 {
		t.Fatalf("remaining work: forecast shapes = %d, want 1", len(forecasts))
	}
}

func TestNoCurveWithoutTheRulesEvidence(t *testing.T) {
	rows := forecastDistributionRows([]int64{5, 12}, []float64{0.5, 1})
	other := forecastClaim("claim_forecast_other", forecastDistributionRows([]int64{3}, []float64{1}))
	other.Subject.CanonicalID = "team:chaos"
	noDistribution := forecastClaim(forecastTestClaim, rows)
	noDistribution.Rows, noDistribution.Table = nil, nil
	badRow := forecastClaim(forecastTestClaim, forecastDistributionRows([]int64{5, 12}, []float64{0.5, 1.5}))
	unauthorized := forecastResult(forecastClaim(forecastTestClaim, rows))
	unauthorized.AnswerPlan.RenderKinds = []contractsv1.ContextFabricRenderKind{contractsv1.ContextFabricRenderKindSeries}
	cohortFrame := &QuestionFrame{
		SubjectExpression: SubjectExpression{Kind: SubjectExpressionDiscoveredKind, Discovered: &DiscoveredSetExpression{MemberKind: contractsv1.ContextFabricSubjectTeam}},
		Obligations:       []AnswerObligation{ObligationCompletion},
	}
	for _, tc := range []struct {
		name    string
		result  InvestigationResult
		frame   *QuestionFrame
		options RenderShapeOptions
		want    RenderShapeSkipReason
	}{
		{"the rule is off by default", forecastResult(forecastClaim(forecastTestClaim, rows)), forecastFrame(ObligationCompletion), RenderShapeOptions{}, RenderShapeSkipRuleDisabled},
		{"no frame this turn", forecastResult(forecastClaim(forecastTestClaim, rows)), nil, forecastOn, RenderShapeSkipNotForecastIntent},
		{"a state question", forecastResult(forecastClaim(forecastTestClaim, rows)), forecastFrame(ObligationState), forecastOn, RenderShapeSkipNotForecastIntent},
		{"the plan does not authorize it", unauthorized, forecastFrame(ObligationCompletion), forecastOn, RenderShapeSkipNotPlanAuthorized},
		{"two scopes carry one", forecastResult(forecastClaim(forecastTestClaim, rows), other), forecastFrame(ObligationCompletion), forecastOn, RenderShapeSkipMoreThanOneScope},
		{"no served distribution", forecastResult(noDistribution), forecastFrame(ObligationCompletion), forecastOn, RenderShapeSkipNoCompletionDistribution},
		{"a share out of range", forecastResult(badRow), forecastFrame(ObligationCompletion), forecastOn, RenderShapeSkipDistributionUnusable},
		{"a cohort question", forecastResult(forecastClaim(forecastTestClaim, rows)), cohortFrame, forecastOn, RenderShapeSkipCohortAnswer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shapes, event := selectRenderShapes(tc.result, tc.frame, tc.options)
			if len(forecastsOf(shapes)) != 0 {
				t.Fatal("a forecast was drawn")
			}
			if got := forecastSkipOf(t, event); got != tc.want {
				t.Fatalf("skip = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTheContractHoldsAForecastToItsWholeDistribution: each defect a forecast
// could carry is refused by the validator every result passes through.
func TestTheContractHoldsAForecastToItsWholeDistribution(t *testing.T) {
	days := []int64{5, 12, 30}
	result := forecastResult(forecastClaim(forecastTestClaim, forecastDistributionRows(days, []float64{0.2, 0.6, 1})))
	shapes, _ := selectRenderShapes(result, forecastFrame(ObligationCompletion), forecastOn)
	good := forecastsOf(shapes)[0]
	good.ShapeID = "rs_1"
	for _, tc := range []struct {
		name  string
		plant func(shape *contractsv1.ContextFabricRenderShape)
		want  string
	}{
		{"cited through the time series", func(shape *contractsv1.ContextFabricRenderShape) {
			for index := range shape.Series[0].Points {
				shape.Series[0].Points[index].Source.Kind = contractsv1.ContextFabricRenderSourceClaimedFactRow
			}
		}, ""},
		{"a row left out", func(shape *contractsv1.ContextFabricRenderShape) {
			shape.Series[0].Points = shape.Series[0].Points[:2]
		}, "a forecast plots every row"},
		{"a label that is not its row's day", func(shape *contractsv1.ContextFabricRenderShape) {
			shape.Series[0].Points[1].Label = "13"
		}, "is not its row's completion day"},
		{"rows out of order", func(shape *contractsv1.ContextFabricRenderShape) {
			points := shape.Series[0].Points
			points[0], points[1] = points[1], points[0]
		}, "a forecast point cites row"},
		{"another rule", func(shape *contractsv1.ContextFabricRenderShape) {
			shape.SelectedBy = contractsv1.ContextFabricRenderRuleDatedFactTrend
		}, "cannot yield kind"},
		{"a time axis", func(shape *contractsv1.ContextFabricRenderShape) {
			shape.AxisKind = contractsv1.ContextFabricRenderAxisTime
		}, "category axis"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planted := good
			planted.Series = []contractsv1.ContextFabricRenderSeries{{Key: good.Series[0].Key, Label: good.Series[0].Label, Points: append([]contractsv1.ContextFabricRenderPoint(nil), good.Series[0].Points...)}}
			tc.plant(&planted)
			probe := result
			probe.RenderShapes = []contractsv1.ContextFabricRenderShape{planted}
			err := contractsv1.ValidateRenderShapesForResult(probe)
			if err == nil {
				t.Fatal("the planted forecast validated")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	probe := result
	probe.RenderShapes = []contractsv1.ContextFabricRenderShape{good}
	if err := contractsv1.ValidateRenderShapesForResult(probe); err != nil {
		t.Fatalf("the good forecast: %v", err)
	}
}

// TestAForecastCarriesTheWholeHorizonAndASeriesStillStopsAt64: the point cap
// is the kind's own -- a forecast holds 366 points, a series 64.
func TestAForecastCarriesTheWholeHorizonAndASeriesStillStopsAt64(t *testing.T) {
	if got := contractsv1.ContextFabricRenderPointsMax(contractsv1.ContextFabricRenderKindForecast); got != 366 {
		t.Fatalf("forecast point cap = %d", got)
	}
	if got := contractsv1.ContextFabricRenderPointsMax(contractsv1.ContextFabricRenderKindSeries); got != contractsv1.ContextFabricRenderPointsMaxCount {
		t.Fatalf("series point cap = %d", got)
	}
	days := make([]int64, 366)
	shares := make([]float64, 366)
	for index := range days {
		days[index], shares[index] = int64(index), float64(index+1)/366
	}
	result := forecastResult(forecastClaim(forecastTestClaim, forecastDistributionRows(days, shares)))
	shapes, event := selectRenderShapes(result, forecastFrame(ObligationCompletion), forecastOn)
	if reason := forecastSkipOf(t, event); reason != "" || len(forecastsOf(shapes)) != 1 || len(forecastsOf(shapes)[0].Series[0].Points) != 366 {
		t.Fatalf("a 366-row distribution: skip %q, shapes %d", reason, len(forecastsOf(shapes)))
	}
}

// TestARowCutThatWithholdsTheDistributionTakesTheCurveWithIt: the row-cut
// lever re-finalizes each candidate, and finalizing re-selects the shapes from
// the cut claims. A distribution the cut withholds whole (CHAOS-12607) leaves
// no table to draw, so the candidate carries no forecast and still validates.
func TestARowCutThatWithholdsTheDistributionTakesTheCurveWithIt(t *testing.T) {
	days := make([]int64, 40)
	shares := make([]float64, 40)
	for index := range days {
		days[index], shares[index] = int64(index+1), float64(index+1)/40
	}
	result := forecastResult(forecastClaim(forecastTestClaim, forecastDistributionRows(days, shares)))
	shapes, _ := selectRenderShapes(result, forecastFrame(ObligationCompletion), forecastOn)
	if len(forecastsOf(shapes)) != 1 {
		t.Fatal("no forecast before the cut")
	}
	cut, _, tables, applied := applyFactRowTruncation(result, 10, 41)
	if !applied || tables.DistributionWithheld != 1 || cut.ClaimedFacts[0].Rows != nil {
		t.Fatalf("cut: applied %t, withheld %d, rows %d", applied, tables.DistributionWithheld, len(cut.ClaimedFacts[0].Rows))
	}
	reselected, event := selectRenderShapes(cut, forecastFrame(ObligationCompletion), forecastOn)
	if len(forecastsOf(reselected)) != 0 {
		t.Fatal("a forecast was drawn from a withheld distribution")
	}
	if got := forecastSkipOf(t, event); got != RenderShapeSkipNoCompletionDistribution {
		t.Fatalf("skip = %q", got)
	}
	cut.RenderShapes = reselected
	if err := contractsv1.ValidateRenderShapesForResult(cut); err != nil {
		t.Fatalf("the cut answer's shapes: %v", err)
	}
	// Only the distribution was withheld: its own sentence, and no per-table
	// sentence, whose counts describe tables that keep rows.
	if !slices.Contains(cut.Limitations, contractsv1.ContextFabricDistributionWithheldLimitation) {
		t.Fatalf("limitations = %q, want the withheld-distribution sentence", cut.Limitations)
	}
	for _, limitation := range cut.Limitations {
		if contractsv1.IsContextFabricFactRowTruncationLimitation(limitation) {
			t.Fatalf("a per-table sentence for a cut that kept no table at its cap: %q", limitation)
		}
	}
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(contractsv1.ContextFabricDistributionWithheldLimitation) {
		t.Fatal("the withheld-distribution sentence is not recognised as the service's")
	}

	// Beside a breakdown the cut shortens, both sentences, the per-table one
	// counting the breakdown's rows only.
	breakdown := forecastClaim("claim_breakdown_beside", forecastDistributionRows(days, shares))
	breakdown.Table = &contractsv1.ContextFabricClaimedFactTable{Field: "team_breakdown", Shape: contractsv1.ContextFabricFactTableShapeBreakdown, Key: []string{"completion_days"}, Measures: []string{"runs", "cumulative_share"}}
	breakdown.TimeSeriesRows, breakdown.TimeSeriesTable = nil, nil
	both := forecastResult(forecastClaim(forecastTestClaim, forecastDistributionRows(days, shares)), breakdown)
	cutBoth, served, tablesBoth, appliedBoth := applyFactRowTruncation(both, 10, 81)
	if !appliedBoth || tablesBoth.DistributionWithheld != 1 || tablesBoth.SourcePrefix != 1 || served != 11 {
		t.Fatalf("both: applied %t, rules %+v, served %d", appliedBoth, tablesBoth, served)
	}
	want, _ := contractsv1.ContextFabricFactRowTruncationLimitation(11, 41, 10)
	if !slices.Contains(cutBoth.Limitations, want) || !slices.Contains(cutBoth.Limitations, contractsv1.ContextFabricDistributionWithheldLimitation) {
		t.Fatalf("limitations = %q, want %q and the withheld-distribution sentence", cutBoth.Limitations, want)
	}
}
