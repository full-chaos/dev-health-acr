package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4637 (S6): render selection reads the PLAN and the producer's
// DECLARED table shape. The geometry inference is gone.
//
// Every test here asserts NON-VACUITY FIRST -- that the fixture actually
// reaches the guarded path -- before asserting the property. Three fixtures
// in the preceding slice were green against the exact defect they claimed to
// pin because they never reached it, and every one was caught by mutating
// the fix back rather than by reading. Green and discriminating are
// different properties.

// declaredReadinessTable is the declaration the CHAOS-4645 readiness
// producer emits for its team/project daily series.
func declaredReadinessTable() *contractsv1.ContextFabricClaimedFactTable {
	return &contractsv1.ContextFabricClaimedFactTable{
		Field:    "daily_readiness",
		Shape:    contractsv1.ContextFabricFactTableShapeTimeSeries,
		Key:      []string{"day"},
		Measures: []string{"coverage_ratio"},
	}
}

// declaredTrendAnswer is chrisTeamsAnswer with its readiness table
// DECLARED. chrisTeamsAnswer's own fact already carries a perfectly shaped
// dated table and gets no trend, because nothing declares it one; this is
// the same document with the producer's statement attached.
func declaredTrendAnswer() InvestigationResult {
	result := chrisTeamsAnswer()
	result.ClaimedFacts[0].Table = declaredReadinessTable()
	return result
}

// TestADeclaredTimeSeriesIsSelectedAsATrend is the way back for the
// withdrawn capability, and the acceptance headline of this slice: the same
// answer that got two shapes before now gets three, and the third is the
// trend -- selected because a producer SAID the table was a time series, not
// because the columns looked like one.
func TestADeclaredTimeSeriesIsSelectedAsATrend(t *testing.T) {
	t.Parallel()
	// Non-vacuity: the undeclared twin must genuinely get NO trend, or
	// this test proves nothing about the declaration.
	if before := shapeByRule(mustSelect(t, chrisTeamsAnswer()), contractsv1.ContextFabricRenderRuleDatedFactTrend); before != nil {
		t.Fatalf("the UNDECLARED fixture already selects a trend, so the declaration is not what this test measures: %+v", before)
	}

	result := declaredTrendAnswer()
	shapes, event := SelectRenderShapes(result)
	trend := shapeByRule(shapes, contractsv1.ContextFabricRenderRuleDatedFactTrend)
	if trend == nil {
		t.Fatalf("a DECLARED time_series was not charted; shapes=%+v skipped=%+v", shapes, event.Skipped)
	}
	if trend.Kind != contractsv1.ContextFabricRenderKindSeries ||
		trend.Presentation != contractsv1.ContextFabricRenderPresentationLine ||
		trend.AxisKind != contractsv1.ContextFabricRenderAxisTime {
		t.Errorf("trend is not a time-axis line: kind=%q presentation=%q axis=%q", trend.Kind, trend.Presentation, trend.AxisKind)
	}
	// The axis is the DECLARED key column, not a column that happened to
	// parse as a date.
	if trend.AxisLabel != "day" {
		t.Errorf("axis label = %q, want the declared key column %q", trend.AxisLabel, "day")
	}
	if len(trend.Series) != 1 || trend.Series[0].Key != "coverage_ratio" {
		t.Fatalf("want exactly one series keyed on the claim's own declared measure; got %+v", trend.Series)
	}
	// Points in INSTANT order, and every value copied verbatim.
	labels := make([]string, 0, len(trend.Series[0].Points))
	for _, point := range trend.Series[0].Points {
		labels = append(labels, point.Label)
	}
	if strings.Join(labels, ",") != "2026-08-03,2026-08-18,2026-08-30" {
		t.Errorf("points are not in instant order: %v", labels)
	}
	// Every plotted number must RESOLVE, by exact equality, against the
	// row cell its own source names -- the same gate the served document
	// passes through. A chart is a claimed fact; a point that does not
	// resolve is a rejected document, not a rendering warning.
	result.RenderShapes = shapes
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the selected trend does not survive the render-shape validator: %v", err)
	}
	if err := event.Accounted(); err != nil {
		t.Errorf("selector accounting: %v", err)
	}
}

// TestTheTrendPlotsOnlyTheClaimsOwnDeclaredMeasure. A declared time_series
// may carry several measures; a trend plots ONE. Plotting several on one
// value axis would silently assert they are commensurable, which nothing on
// the wire says -- that is CHAOS-4625's own designed comparison shape.
func TestTheTrendPlotsOnlyTheClaimsOwnDeclaredMeasure(t *testing.T) {
	t.Parallel()
	result := declaredTrendAnswer()
	// A second measure, present on every row, alongside the claimed one.
	for i := range result.ClaimedFacts[0].Rows {
		result.ClaimedFacts[0].Rows[i].Fields["open_findings"] = renderScalarNumber(float64(3 - i))
	}
	result.ClaimedFacts[0].Table.Measures = []string{"coverage_ratio", "open_findings"}
	// Non-vacuity: the second measure is genuinely plottable on its own
	// terms -- present and numeric on every row -- so its absence from the
	// chart is a DECISION, not an accident of the fixture.
	for i, row := range result.ClaimedFacts[0].Rows {
		if _, numeric := renderNumericCell(row.Fields["open_findings"]); !numeric {
			t.Fatalf("row %d's second measure is not numeric; the fixture cannot show the rule chose to omit it", i)
		}
	}

	trend := shapeByRule(mustSelect(t, result), contractsv1.ContextFabricRenderRuleDatedFactTrend)
	if trend == nil {
		t.Fatal("a multi-measure declared time_series produced no trend at all")
	}
	if len(trend.Series) != 1 {
		t.Fatalf("a trend carries %d series; a trend plots exactly one measure, and several on one value axis asserts a commensurability nothing declared", len(trend.Series))
	}
	if trend.Series[0].Key != result.ClaimedFacts[0].Field {
		t.Errorf("series key = %q, want the claim's own field %q", trend.Series[0].Key, result.ClaimedFacts[0].Field)
	}
}

// TestAClaimWhoseFieldIsNotADeclaredMeasureIsRefused. The claim names what
// it is about; if that is not one of the table's declared measures there is
// no single measure to plot, and the rule refuses rather than picking one.
func TestAClaimWhoseFieldIsNotADeclaredMeasureIsRefused(t *testing.T) {
	t.Parallel()
	result := declaredTrendAnswer()
	result.ClaimedFacts[0].Field = "coverage_ratio_pct"
	// Non-vacuity: everything else about this fixture is still chartable.
	if result.ClaimedFacts[0].Table.Shape != contractsv1.ContextFabricFactTableShapeTimeSeries {
		t.Fatal("fixture no longer declares a time_series, so the refusal would not be about the measure")
	}
	if result.ClaimedFacts[0].Table.HasMeasure(result.ClaimedFacts[0].Field) {
		t.Fatal("fixture's field IS a declared measure; the guarded path is not reached")
	}

	shapes, event := SelectRenderShapes(result)
	if shapeByRule(shapes, contractsv1.ContextFabricRenderRuleDatedFactTrend) != nil {
		t.Fatal("a claim whose field names no declared measure was charted anyway")
	}
	if !skipRecorded(event, contractsv1.ContextFabricRenderRuleDatedFactTrend, RenderShapeSkipClaimFieldNotAMeasure) {
		t.Errorf("the refusal does not name the measure as the reason; skipped=%+v", event.Skipped)
	}
}

// TestThePlanRefusesATrendItDidNotAuthorize is North Star check 10 as a
// property of the PLAN rather than of the row shape: a question that planned
// only a table gets no chart, however chartable the data is.
func TestThePlanRefusesATrendItDidNotAuthorize(t *testing.T) {
	t.Parallel()
	authorized := declaredTrendAnswer()
	// Non-vacuity: without the plan this document DOES produce a trend, so
	// the refusal below is attributable to the plan and to nothing else.
	if shapeByRule(mustSelect(t, authorized), contractsv1.ContextFabricRenderRuleDatedFactTrend) == nil {
		t.Fatal("the plan-free fixture selects no trend; the plan cannot be shown to be what refuses it")
	}

	result := declaredTrendAnswer()
	result.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{
		Family:        contractsv1.ContextFabricQuestionFamilyGroupedCohortStatus,
		FamilySource:  contractsv1.ContextFabricQuestionFamilySourceFallback,
		FamilyVersion: "v1",
		RenderKinds:   []contractsv1.ContextFabricRenderKind{contractsv1.ContextFabricRenderKindTable},
	}
	shapes, event := SelectRenderShapes(result)
	if len(shapes) != 0 {
		t.Fatalf("the plan authorized only `table` and %d shape(s) were selected: %+v", len(shapes), shapes)
	}
	if !skipRecorded(event, contractsv1.ContextFabricRenderRuleDatedFactTrend, RenderShapeSkipNotPlanAuthorized) {
		t.Errorf("the trend refusal does not name the plan; skipped=%+v", event.Skipped)
	}
	if err := event.Accounted(); err != nil {
		t.Errorf("selector accounting: %v", err)
	}
}

// TestEveryRuleExitRecordsExactlyOneOutcome is CHAOS-4621: the structural
// invariant, over a matrix that reaches every rule and every outcome.
//
// The same defect class -- a refusal that leaves no trace, or records the
// wrong reason, or goes invisible once another rule produces a shape -- was
// closed FOUR times case by case in the 4415/4616 work. This is the
// invariant that makes the fifth impossible rather than unlucky.
func TestEveryRuleExitRecordsExactlyOneOutcome(t *testing.T) {
	t.Parallel()
	noPlanKinds := func(kinds ...contractsv1.ContextFabricRenderKind) *contractsv1.ContextFabricAnswerPlan {
		return &contractsv1.ContextFabricAnswerPlan{
			Family: contractsv1.ContextFabricQuestionFamilyGroupedCohortStatus, FamilySource: contractsv1.ContextFabricQuestionFamilySourceFallback,
			FamilyVersion: "v1", RenderKinds: kinds,
		}
	}
	singleSubject := func() InvestigationResult {
		r := declaredTrendAnswer()
		r.Interpretation.Shape = contractsv1.ContextFabricShapeSingleSubject
		return r
	}
	unrankedCohort := func() InvestigationResult {
		r := declaredTrendAnswer()
		r.Cohort.Members[0].RankingComputed = false
		r.Cohort.Members[0].Score = nil
		r.Cohort.Members[0].AttentionRank = 0
		r.Cohort.Members[0].Drivers = nil
		return r
	}
	noDrivers := func() InvestigationResult {
		r := declaredTrendAnswer()
		r.Cohort.Members[0].Score = renderFloat(0)
		r.Cohort.Members[0].Drivers = nil
		return r
	}
	planBlocked := func() InvestigationResult {
		r := declaredTrendAnswer()
		r.AnswerPlan = noPlanKinds(contractsv1.ContextFabricRenderKindTable)
		return r
	}
	undeclared := func() InvestigationResult { return chrisTeamsAnswer() }
	breakdownOnly := func() InvestigationResult {
		r := declaredTrendAnswer()
		r.ClaimedFacts[0].Table.Shape = contractsv1.ContextFabricFactTableShapeBreakdown
		return r
	}
	fieldNotMeasure := func() InvestigationResult {
		r := declaredTrendAnswer()
		r.ClaimedFacts[0].Field = "not_a_measure"
		return r
	}
	unresolvableRoles := func() InvestigationResult {
		r := declaredTrendAnswer()
		r.ClaimedFacts[0].Table.Measures = []string{"coverage_ratio", "severity"}
		for i := range r.ClaimedFacts[0].Rows {
			r.ClaimedFacts[0].Rows[i].Fields["severity"] = renderScalarString("elevated")
		}
		return r
	}
	// The DECLARED axis column does not parse as an instant. Breaking the
	// MEASURE instead would now trip unresolvable_measure_roles first --
	// which the matrix's own non-vacuity check caught when this case was
	// written that way, and is exactly what that check is for.
	unplottable := func() InvestigationResult {
		r := declaredTrendAnswer()
		for i := range r.ClaimedFacts[0].Rows {
			r.ClaimedFacts[0].Rows[i].Fields["day"] = renderScalarString("week " + string(rune('a'+i)))
		}
		return r
	}

	// The matrix is checked for COVERAGE as well as for the invariant. A
	// matrix that never reaches a skip reason would satisfy the invariant
	// vacuously, which is exactly the shape of the failure this test
	// exists to stop.
	cases := []struct {
		name   string
		result func() InvestigationResult
		expect RenderShapeSkipReason
	}{
		{"trend selected, cohort selected", declaredTrendAnswer, ""},
		{"not cohort intent", singleSubject, RenderShapeSkipNotCohortIntent},
		{"no ranked member", unrankedCohort, RenderShapeSkipNoRankedMember},
		{"no drivers", noDrivers, RenderShapeSkipNoDrivers},
		{"plan authorizes only table", planBlocked, RenderShapeSkipNotPlanAuthorized},
		{"undeclared table", undeclared, RenderShapeSkipNoDeclaredTable},
		{"declared, not a time series", breakdownOnly, RenderShapeSkipNoTimeSeriesTable},
		{"claim field is not a measure", fieldNotMeasure, RenderShapeSkipClaimFieldNotAMeasure},
		{"a declared measure is not a quantity", unresolvableRoles, RenderShapeSkipUnresolvableMeasureRoles},
		{"declared measure is not plottable", unplottable, RenderShapeSkipNoPlottableMeasure},
	}
	reached := map[RenderShapeSkipReason]bool{}
	for _, testCase := range cases {
		_, event := SelectRenderShapes(testCase.result())
		if err := event.Accounted(); err != nil {
			t.Errorf("%s: %v (selected=%+v skipped=%+v)", testCase.name, err, event.Selected, event.Skipped)
		}
		for _, skip := range event.Skipped {
			reached[skip.Reason] = true
		}
		if testCase.expect != "" && !reached[testCase.expect] {
			t.Errorf("%s: expected reason %q was never recorded; skipped=%+v", testCase.name, testCase.expect, event.Skipped)
		}
	}
	// Non-vacuity of the matrix itself: every closed reason this selector
	// can produce must be REACHED by some case above. A reason nothing
	// reaches is a reason nothing has ever proven correct.
	for _, reason := range []RenderShapeSkipReason{
		RenderShapeSkipNotCohortIntent, RenderShapeSkipNoRankedMember, RenderShapeSkipNoDrivers,
		RenderShapeSkipNotPlanAuthorized, RenderShapeSkipNoDeclaredTable, RenderShapeSkipNoTimeSeriesTable,
		RenderShapeSkipClaimFieldNotAMeasure, RenderShapeSkipUnresolvableMeasureRoles,
		RenderShapeSkipNoPlottableMeasure,
	} {
		if !reached[reason] {
			t.Errorf("no case in this matrix ever produces %q, so the invariant is satisfied vacuously for it", reason)
		}
	}
}

// TestTheAccountingInvariantActuallyDetectsALostOutcome proves Accounted is
// discriminating rather than a function that returns nil. Without this, the
// matrix above would pass against an Accounted that never fails.
func TestTheAccountingInvariantActuallyDetectsALostOutcome(t *testing.T) {
	t.Parallel()
	_, event := SelectRenderShapes(declaredTrendAnswer())
	if err := event.Accounted(); err != nil {
		t.Fatalf("a healthy selection is already reported as unaccounted: %v", err)
	}
	// A rule that recorded nothing at all -- the exact shape of the four
	// defects CHAOS-4621 was filed for.
	lost := RenderShapeSelectionEvent{Shape: event.Shape, Selected: []RenderShapeSelection{event.Selected[0]}}
	if err := lost.Accounted(); err == nil {
		t.Fatal("an event in which two rules recorded no outcome at all was reported as accounted")
	}
	// A rule that recorded two reasons.
	doubled := event
	doubled.Skipped = append(append([]RenderShapeSkip{}, event.Skipped...),
		RenderShapeSkip{Rule: contractsv1.ContextFabricRenderRuleCohortAttentionScore, Reason: RenderShapeSkipNoDrivers})
	if err := doubled.Accounted(); err == nil {
		t.Fatal("a rule that both selected a shape and recorded a skip was reported as accounted")
	}
}

// TestTheDeclarationDescribesTheFieldWhoseRowsWereServed is the
// anti-divergence pin, and the reason canonicalRowsField exists.
//
// A fact can carry a legacy breakdown AND a CHAOS-4645 time series. The
// CHAOS-4645 ruling is that the LEGACY field's rows are what a claim
// serves. If the declaration were computed separately it could describe the
// OTHER field -- a time_series declaration over breakdown rows -- and the
// wire would be lying in a way nothing downstream could detect. Instead a
// trend would be drawn across two work scopes: the original CHAOS-4616
// defect, reintroduced through the declaration rather than through the
// geometry.
func TestTheDeclarationDescribesTheFieldWhoseRowsWereServed(t *testing.T) {
	t.Parallel()
	subject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CHAOS", Label: "CHAOS"}
	fact := CanonicalFact{
		Kind: FactFlow, Subject: subject,
		Fields: map[string]FactValue{
			"scope_breakdown": TableFactValue(FactTable{
				Shape: FactTableBreakdown, Key: []string{"provider", "work_scope_id"},
				Measures: []string{"items_completed"}, Observations: []string{"day"},
				Rows: []FactValueRow{
					{Fields: map[string]FactValue{"provider": StringFactValue("github"), "work_scope_id": StringFactValue("a"), "day": StringFactValue("2026-07-20"), "items_completed": IntegerFactValue(0)}},
					{Fields: map[string]FactValue{"provider": StringFactValue("github"), "work_scope_id": StringFactValue("b"), "day": StringFactValue("2026-08-30"), "items_completed": IntegerFactValue(1)}},
				},
			}),
			"daily_flow": TableFactValue(FactTable{
				Shape: FactTableTimeSeries, Key: []string{"day"}, Measures: []string{"items_completed"},
				Rows: []FactValueRow{
					{Fields: map[string]FactValue{"day": StringFactValue("2026-07-20"), "items_completed": IntegerFactValue(0)}},
					{Fields: map[string]FactValue{"day": StringFactValue("2026-08-30"), "items_completed": IntegerFactValue(1)}},
				},
			}),
		},
	}
	// Non-vacuity: this really is the dual-table case, and both
	// declarations really are valid.
	for _, field := range []string{"scope_breakdown", "daily_flow"} {
		if err := fact.Fields[field].Validate(); err != nil {
			t.Fatalf("fixture field %q is not a valid declared table: %v", field, err)
		}
	}

	claims := []ClaimedFact{{Kind: FactFlow, Subject: subject, Field: "items_completed"}}
	got, _, _, truncated := attachCanonicalRows(claims, []CanonicalFact{fact})
	if truncated {
		t.Fatal("the dual-table fact failed closed; CHAOS-4645's resolution regressed")
	}
	if got[0].Table == nil {
		t.Fatal("rows were served with no declaration at all")
	}
	if got[0].Table.Field != "scope_breakdown" {
		t.Fatalf("declaration names field %q but the served rows came from scope_breakdown", got[0].Table.Field)
	}
	if got[0].Table.Shape != contractsv1.ContextFabricFactTableShapeBreakdown {
		t.Fatalf("declaration says shape %q over rows that are a breakdown", got[0].Table.Shape)
	}
	// CHAOS-4682 (§5.1 P2, orchestrator ruling 2026-09-01): Table/Rows keep
	// serving the legacy field, UNCHANGED from the assertions above -- but
	// the fact's OTHER field, daily_flow (a genuine time_series), now ALSO
	// reaches the wire through the ADDITIVE TimeSeriesTable/TimeSeriesRows
	// pair. Before P2 this pair did not exist and daily_flow's rows never
	// left the producer at all.
	if got[0].TimeSeriesTable == nil {
		t.Fatal("the fact's time_series field (daily_flow) was not attached to the additive pair -- P2 regressed")
	}
	if got[0].TimeSeriesTable.Field != "daily_flow" {
		t.Fatalf("time_series_table names field %q, want daily_flow", got[0].TimeSeriesTable.Field)
	}
	if got[0].TimeSeriesTable.Shape != contractsv1.ContextFabricFactTableShapeTimeSeries {
		t.Fatalf("time_series_table declares shape %q, want time_series", got[0].TimeSeriesTable.Shape)
	}
	if len(got[0].TimeSeriesRows) != 2 {
		t.Fatalf("time_series_rows = %d rows, want the 2 daily_flow rows", len(got[0].TimeSeriesRows))
	}
	// And therefore: a trend NOW renders, off the additive pair -- this is
	// the P2 unlock itself, the exact reversal of this test's own
	// pre-CHAOS-4682 name. The two-scope breakdown still cannot be charted
	// (Table/Rows are still a breakdown, unchanged), but the fact as a
	// whole is no longer chart-blind.
	result := InvestigationResult{
		Interpretation: InterpretedQuestion{Shape: contractsv1.ContextFabricShapeSingleSubject},
		ClaimedFacts: []ClaimedFact{{
			ClaimID: "claim_flow_dual", Kind: FactFlow, Subject: subject, Field: "items_completed",
			Rows: got[0].Rows, Table: got[0].Table,
			TimeSeriesRows: got[0].TimeSeriesRows, TimeSeriesTable: got[0].TimeSeriesTable,
		}},
	}
	shapes, event := SelectRenderShapes(result)
	trend := shapeByRule(shapes, contractsv1.ContextFabricRenderRuleDatedFactTrend)
	if trend == nil {
		t.Fatalf("the dual-table fact's time_series was not charted; shapes=%+v skipped=%+v", shapes, event.Skipped)
	}
	if len(trend.Series) != 1 || len(trend.Series[0].Points) != 2 {
		t.Fatalf("trend = %+v, want one series with the 2 daily_flow points", trend)
	}
	// Every plotted number must RESOLVE, by exact equality, against the
	// row cell its own source names -- the render-shape validator's own
	// gate, and the one that would catch a RowIndex resolved against the
	// WRONG array (Table's Rows instead of TimeSeriesRows).
	result.RenderShapes = shapes
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the dual-table trend does not survive the render-shape validator: %v", err)
	}
}

// TestAnUndeclaredFieldGetsNoDeclaration: a pre-CHAOS-4633 producer's rows
// still travel, and still carry no declaration -- so they are still never
// charted, and nothing that renders today stops rendering.
func TestAnUndeclaredFieldGetsNoDeclaration(t *testing.T) {
	t.Parallel()
	subject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CHAOS", Label: "CHAOS"}
	fact := CanonicalFact{Kind: FactFlow, Subject: subject, Fields: map[string]FactValue{
		"legacy_rows": {Rows: []FactValueRow{
			{Fields: map[string]FactValue{"day": StringFactValue("2026-07-20"), "items_completed": IntegerFactValue(0)}},
			{Fields: map[string]FactValue{"day": StringFactValue("2026-08-30"), "items_completed": IntegerFactValue(1)}},
		}},
	}}
	got, rowsCount, _, _ := attachCanonicalRows([]ClaimedFact{{Kind: FactFlow, Subject: subject}}, []CanonicalFact{fact})
	if rowsCount != 2 {
		t.Fatalf("legacy rows count = %d, want 2 -- the undeclared table must still travel", rowsCount)
	}
	if got[0].Table != nil {
		t.Fatalf("an undeclared field produced a declaration out of nowhere: %+v", got[0].Table)
	}
}

func mustSelect(t *testing.T, result InvestigationResult) []contractsv1.ContextFabricRenderShape {
	t.Helper()
	shapes, _ := SelectRenderShapes(result)
	return shapes
}

// TestTheProductionSinkReportsTheAccountingAndTheTrendLoss asserts the
// PRODUCTION sink's own bytes, not a struct field.
//
// CHAOS-4085's lesson is why: CommitAffirmationTelemetry was optional,
// nothing in production implemented it, every retraction failed a type
// assertion, and the whole event disappeared while the tests passed. An
// invariant that is only checkable by reading the source is not diagnosable
// from a run's own artifacts, which is exactly what acr/AGENTS.md forbids --
// so the accounting verdict has to be IN THE LOG LINE, and this reads the
// line.
func TestTheProductionSinkReportsTheAccountingAndTheTrendLoss(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&buf, nil)))
	_, event := SelectRenderShapes(declaredTrendAnswer())
	// Non-vacuity: this is a selection that really did produce shapes and
	// really is accounted, so "ok" below is a measurement rather than a
	// default.
	if len(event.Selected) == 0 {
		t.Fatal("the fixture selected nothing; render_shape_accounting=ok would say nothing about a real selection")
	}
	if err := event.Accounted(); err != nil {
		t.Fatalf("the fixture is not accounted, so the sink cannot be shown to report ok: %v", err)
	}
	telemetry.RecordRenderShapeSelection(context.Background(), storage.Principal{OrgID: "org_4637"}, event)
	logged := buf.String()
	for _, want := range []string{
		"render_shape_accounting=ok",
		"render_shape_trends_omitted=0",
		"render_shape_rule=dated_fact_trend",
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("the production sink never emitted %q; logged:\n%s", want, logged)
		}
	}

	// And a violation must be VISIBLE, or the field is decoration.
	buf.Reset()
	broken := RenderShapeSelectionEvent{Shape: event.Shape, Selected: []RenderShapeSelection{event.Selected[0]}}
	telemetry.RecordRenderShapeSelection(context.Background(), storage.Principal{OrgID: "org_4637"}, broken)
	if !strings.Contains(buf.String(), "render_shape_accounting=violated") {
		t.Errorf("an event with two rules recording no outcome logged no violation; logged:\n%s", buf.String())
	}
}

// TestASoleTimeSeriesFieldReachesTheWireDeclaredAndCharts is the mirror of
// the dual-table test above, and it is the case that actually puts a chart
// back on the screen.
//
// Where a fact carries exactly ONE rows-shaped field and that field is a
// declared time_series, the declaration reaches the wire and the trend
// fires. This is not hypothetical: `readiness` and `workload` declare
// `{time_series}` and NOTHING else for a TEAM subject, and `metrics`
// declares `{time_series}` and nothing else for a REPOSITORY subject
// (fact_registry capability tables, CHAOS-4645) -- so those three
// subject-kind paths are exactly this shape.
func TestASoleTimeSeriesFieldReachesTheWireDeclaredAndCharts(t *testing.T) {
	t.Parallel()
	subject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CHAOS", Label: "CHAOS"}
	fact := CanonicalFact{
		Kind: FactWorkload, Subject: subject,
		Fields: map[string]FactValue{
			// The scalar siblings the model actually sees: modelFacingFacts
			// drops every Rows-shaped field before synthesis, which is why
			// producers emit the latest day's values under the SAME names
			// as the table's measures -- and why a claim's Field can be a
			// declared measure at all.
			"backlog_size": IntegerFactValue(31),
			"daily_workload": TableFactValue(FactTable{
				Shape: FactTableTimeSeries, Key: []string{"day"},
				Measures: []string{"backlog_size", "throughput_mean"},
				Rows: []FactValueRow{
					{Fields: map[string]FactValue{"day": StringFactValue("2026-08-03"), "backlog_size": IntegerFactValue(18), "throughput_mean": NumberFactValue(2.5)}},
					{Fields: map[string]FactValue{"day": StringFactValue("2026-08-18"), "backlog_size": IntegerFactValue(24), "throughput_mean": NumberFactValue(3.1)}},
					{Fields: map[string]FactValue{"day": StringFactValue("2026-08-30"), "backlog_size": IntegerFactValue(31), "throughput_mean": NumberFactValue(3.4)}},
				},
			}),
		},
	}
	if err := fact.Fields["daily_workload"].Validate(); err != nil {
		t.Fatalf("fixture table is not a valid declaration: %v", err)
	}

	claims := []ClaimedFact{{ClaimID: "claim_workload_team", Kind: FactWorkload, Subject: subject, Field: "backlog_size"}}
	got, _, _, truncated := attachCanonicalRows(claims, []CanonicalFact{fact})
	if truncated {
		t.Fatal("a single-table fact failed closed")
	}
	// Non-vacuity: the declaration really arrived, and really is the one
	// the rows came from.
	if got[0].Table == nil || got[0].Table.Field != "daily_workload" {
		t.Fatalf("the sole time_series field did not reach the wire declared: %+v", got[0].Table)
	}
	if got[0].Table.Shape != contractsv1.ContextFabricFactTableShapeTimeSeries {
		t.Fatalf("declared shape = %q, want time_series", got[0].Table.Shape)
	}

	result := InvestigationResult{
		Interpretation: InterpretedQuestion{Shape: contractsv1.ContextFabricShapeSingleSubject},
		ClaimedFacts:   []ClaimedFact{got[0]},
	}
	shapes, event := SelectRenderShapes(result)
	trend := shapeByRule(shapes, contractsv1.ContextFabricRenderRuleDatedFactTrend)
	if trend == nil {
		t.Fatalf("a sole declared time_series produced no trend; skipped=%+v", event.Skipped)
	}
	if len(trend.Series) != 1 || trend.Series[0].Key != "backlog_size" {
		t.Fatalf("want one series on the claim's own measure; got %+v", trend.Series)
	}
	result.RenderShapes = shapes
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the trend's points do not resolve against the rows they cite: %v", err)
	}
}

// TestEveryTrendStageHasAReason is the totality half of the CHAOS-4621
// invariant, at the level the invariant can actually be broken.
//
// `Accounted` proves an EVENT is well formed. It cannot prove the code that
// builds the event has a reason for every path, and the sweep for this slice
// found exactly that hole: a fact reaching trendStageSelected while the shape
// budget was spent produced a skip with the EMPTY reason, because
// trendStageReasons has no entry for a stage that is not a refusal. Nothing
// reachable today triggers it, so no scenario test would have caught it --
// only enumerating the stages does.
func TestEveryTrendStageHasAReason(t *testing.T) {
	t.Parallel()
	for stage := trendStageNoDeclaredTable; stage <= trendStageSelected; stage++ {
		if stage == trendStageSelected {
			// Not a refusal: it must NOT have an entry, and the caller
			// handles it explicitly.
			if _, exists := trendStageReasons[stage]; exists {
				t.Errorf("trendStageSelected has a skip reason; a selection is not a refusal")
			}
			continue
		}
		reason, exists := trendStageReasons[stage]
		if !exists || reason == "" {
			t.Errorf("trend stage %d has no skip reason, so a rule exiting through it would record a skip with no reason", stage)
		}
	}
}

// TestTheTrendRuleRecordsAReasonWhenTheShapeBudgetIsSpent is the reachable
// proof of the same hole: with no room left, the rule must still say why it
// produced nothing.
func TestTheTrendRuleRecordsAReasonWhenTheShapeBudgetIsSpent(t *testing.T) {
	t.Parallel()
	result := declaredTrendAnswer()
	// Non-vacuity: with room, this fixture DOES select a trend, so the
	// difference below is the budget and nothing else.
	if trend := shapeByRule(mustSelect(t, result), contractsv1.ContextFabricRenderRuleDatedFactTrend); trend == nil {
		t.Fatal("the fixture selects no trend even with room; the budget cannot be shown to be what refuses it")
	}
	shapes, omitted, reason := datedFactTrendShapes(result, contractsv1.ContextFabricRenderShapesMaxCount)
	if len(shapes) != 0 {
		t.Fatalf("shapes were selected with no budget left: %+v", shapes)
	}
	if omitted == 0 {
		t.Error("the trend the budget had no room for was dropped with no count recorded")
	}
	if reason != RenderShapeSkipShapeBudgetSpent {
		t.Fatalf("reason = %q, want %q -- an exit with no reason is the exact defect CHAOS-4621 was filed about", reason, RenderShapeSkipShapeBudgetSpent)
	}
}
