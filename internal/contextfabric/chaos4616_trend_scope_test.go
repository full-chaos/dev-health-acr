package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4616 (Urgent): a trend may only plot rows that are observations of
// the SAME thing.
//
// Found by inspecting a live trend on the kiac rig, not by any test — every
// fixture and the golden example used a single-scope row table, so nothing
// in the suite could have caught it. The live `flow` fact for team
// `fullchaos` carried exactly two rows:
//
//	day=2026-07-20  work_scope_id=full.chaos/chaos-ops        wip_count_end_of_day=0
//	day=2026-08-30  work_scope_id=full.chaos/dev-health-ops   wip_count_end_of_day=1
//
// Two DIFFERENT work scopes, measured once each, six weeks apart. The rule
// keyed only on "a distinct same-shaped date column plus numeric columns"
// and drew a line rising 0 -> 1, which reads as one scope changing over
// time. Every plotted number was copied faithfully and the chart still said
// something the data does not — this contract's own defect, reached through
// the AXIS instead of through a value.

// liveFlowRows reproduces that live fact exactly.
func liveFlowRows() []ClaimedFactRow {
	return []ClaimedFactRow{
		{Fields: map[string]ScalarValue{
			"day":                  renderScalarString("2026-07-20"),
			"work_scope_id":        renderScalarString("full.chaos/chaos-ops"),
			"provider":             renderScalarString("gitlab"),
			"items_completed":      renderScalarNumber(0),
			"wip_count_end_of_day": renderScalarNumber(0),
		}},
		{Fields: map[string]ScalarValue{
			"day":                  renderScalarString("2026-08-30"),
			"work_scope_id":        renderScalarString("full.chaos/dev-health-ops"),
			"provider":             renderScalarString("gitlab"),
			"items_completed":      renderScalarNumber(0),
			"wip_count_end_of_day": renderScalarNumber(1),
		}},
	}
}

func trendFixture(rows []ClaimedFactRow) InvestigationResult {
	return InvestigationResult{
		Interpretation: InterpretedQuestion{Shape: contractsv1.ContextFabricShapeSingleSubject},
		ClaimedFacts: []ClaimedFact{{
			ClaimID: "claim-fullchaos-flow",
			Kind:    "flow",
			Subject: SubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:fullchaos", Label: "fullchaos"},
			Field:   "items_completed",
			Rows:    rows,
		}},
	}
}

// TestTrendRefusesRowsFromDifferentScopes is the red-first proof, built from
// the live rows verbatim.
func TestTrendRefusesRowsFromDifferentScopes(t *testing.T) {
	t.Parallel()
	shapes, event := SelectRenderShapes(trendFixture(liveFlowRows()))
	if len(shapes) != 0 {
		t.Fatalf("a trend was drawn across two work scopes measured once each: %+v", shapes[0].Series)
	}
	if !skipRecorded(event, contractsv1.ContextFabricRenderRuleDatedFactTrend, RenderShapeSkipMixedScopeRows) {
		t.Errorf("the rule declined without saying why; skipped=%+v", event.Skipped)
	}
}

// TestTrendStillDrawsASingleScopeSeries is the control. Without it the fix
// above would be satisfied by a rule that refuses every trend.
//
// One scope, one measure (the claim's own Field), and every other column
// constant -- the shape a genuine time series has.
func TestTrendStillDrawsASingleScopeSeries(t *testing.T) {
	t.Parallel()
	rows := []ClaimedFactRow{
		{Fields: map[string]ScalarValue{
			"day": renderScalarString("2026-07-20"), "provider": renderScalarString("gitlab"),
			"work_scope_id": renderScalarString("full.chaos/chaos-ops"), "items_completed": renderScalarNumber(0),
		}},
		{Fields: map[string]ScalarValue{
			"day": renderScalarString("2026-08-30"), "provider": renderScalarString("gitlab"),
			"work_scope_id": renderScalarString("full.chaos/chaos-ops"), "items_completed": renderScalarNumber(3),
		}},
	}
	shapes, _ := SelectRenderShapes(trendFixture(rows))
	if len(shapes) != 1 {
		t.Fatalf("a legitimate single-scope trend was refused; got %d shapes", len(shapes))
	}
	if shapes[0].SelectedBy != contractsv1.ContextFabricRenderRuleDatedFactTrend {
		t.Fatalf("selected_by = %s, want dated_fact_trend", shapes[0].SelectedBy)
	}
	// Exactly ONE series, and it is the claim's own Field. A trend states
	// one measure over time; several measures is a comparison, which is a
	// different claim with its own designed rule (filed separately).
	if len(shapes[0].Series) != 1 || shapes[0].Series[0].Key != "items_completed" {
		t.Fatalf("series = %+v, want exactly the claim's own Field", shapes[0].Series)
	}
}

// TestTrendRefusesASecondMeasure pins the narrowing the field-driven rule
// deliberately accepts: a table carrying a measure the claim does NOT name
// is a breakdown or a comparison, and this rule declines rather than
// guessing which of the two columns the answer is about.
func TestTrendRefusesASecondMeasure(t *testing.T) {
	t.Parallel()
	rows := []ClaimedFactRow{
		{Fields: map[string]ScalarValue{
			"day":             renderScalarString("2026-07-20"),
			"items_completed": renderScalarNumber(0), "items_started": renderScalarNumber(4),
		}},
		{Fields: map[string]ScalarValue{
			"day":             renderScalarString("2026-08-30"),
			"items_completed": renderScalarNumber(3), "items_started": renderScalarNumber(9),
		}},
	}
	if shapes, _ := SelectRenderShapes(trendFixture(rows)); len(shapes) != 0 {
		t.Fatalf("a trend was drawn for a table carrying a measure the claim does not name: %+v", shapes[0].Series)
	}
}

// TestTrendIgnoresAConstantDimensionColumn: a dimension column that never
// varies is provenance, not a scope split, and must not block a trend.
func TestTrendIgnoresAConstantDimensionColumn(t *testing.T) {
	t.Parallel()
	rows := liveFlowRows()
	rows[1].Fields["work_scope_id"] = renderScalarString("full.chaos/chaos-ops")
	// The live rows carry a second measure (wip_count_end_of_day) that the
	// claim does not name, so drop it to isolate what this test is about.
	delete(rows[0].Fields, "wip_count_end_of_day")
	delete(rows[1].Fields, "wip_count_end_of_day")
	rows[1].Fields["items_completed"] = renderScalarNumber(3)
	// `provider` is identical on both rows already; add another constant.
	rows[0].Fields["team_name"] = renderScalarString("fullchaos")
	rows[1].Fields["team_name"] = renderScalarString("fullchaos")
	if shapes, _ := SelectRenderShapes(trendFixture(rows)); len(shapes) != 1 {
		t.Fatalf("a constant dimension column blocked a legitimate trend; got %d shapes", len(shapes))
	}
}

// TestTrendRefusesAVaryingBooleanDimension: the scope identity is every
// non-date, non-plotted column, not just strings.
func TestTrendRefusesAVaryingBooleanDimension(t *testing.T) {
	t.Parallel()
	rows := liveFlowRows()
	rows[1].Fields["work_scope_id"] = renderScalarString("full.chaos/chaos-ops")
	yes, no := true, false
	rows[0].Fields["is_backfill"] = ScalarValue{Boolean: &no}
	rows[1].Fields["is_backfill"] = ScalarValue{Boolean: &yes}
	if shapes, _ := SelectRenderShapes(trendFixture(rows)); len(shapes) != 0 {
		t.Fatalf("a trend was drawn across a varying boolean dimension: %+v", shapes[0].Series)
	}
}

// codex round 1 on this PR, P1 (EXECUTED). The scope check skipped any
// NUMERIC column, so a numeric dimension — a team id, a year — was treated
// as a plottable series instead. The rows below are two different teams
// measured once each, and the rule drew "team_id over time" alongside the
// measure, which is both nonsense on its own and the very scope split this
// PR exists to refuse.
//
// I named this case in the review prompt and then did not close it; codex
// executed it and found it open.
func TestTrendRefusesANumericDimensionColumn(t *testing.T) {
	t.Parallel()
	rows := []ClaimedFactRow{
		{Fields: map[string]ScalarValue{
			"day": renderScalarString("2026-07-20"), "team_id": renderScalarNumber(101),
			"items_completed": renderScalarNumber(0),
		}},
		{Fields: map[string]ScalarValue{
			"day": renderScalarString("2026-08-30"), "team_id": renderScalarNumber(202),
			"items_completed": renderScalarNumber(1),
		}},
	}
	shapes, event := SelectRenderShapes(trendFixture(rows))
	if len(shapes) != 0 {
		t.Fatalf("a trend was drawn across two numeric team ids: %+v", shapes[0].Series)
	}
	if !skipRecorded(event, contractsv1.ContextFabricRenderRuleDatedFactTrend, RenderShapeSkipMixedScopeRows) {
		t.Errorf("declined without naming the scope split; skipped=%+v", event.Skipped)
	}
}

// Same finding's second half: an identifier present on one row and absent on
// another keyed as "absent" both ways, so the split was invisible.
func TestTrendRefusesAnIdentifierPresentOnOnlySomeRows(t *testing.T) {
	t.Parallel()
	rows := []ClaimedFactRow{
		{Fields: map[string]ScalarValue{
			"day": renderScalarString("2026-07-20"), "team_id": renderScalarNumber(101),
			"items_completed": renderScalarNumber(0),
		}},
		{Fields: map[string]ScalarValue{
			"day": renderScalarString("2026-08-30"), "items_completed": renderScalarNumber(1),
		}},
	}
	if shapes, _ := SelectRenderShapes(trendFixture(rows)); len(shapes) != 0 {
		t.Fatalf("a trend was drawn across a present/absent identifier: %+v", shapes[0].Series)
	}
}

// codex P2 (EXECUTED): the skip reason must describe what actually stopped
// the rule. A dated, mixed-scope table with NO numeric column has nothing to
// plot at all, so "no dated rows" is the honest reason -- reporting a scope
// split for a table that could never have been a trend sends a reader after
// the wrong producer.
func TestTrendReportsFieldNotPlottableWhenTheClaimsFieldIsNotAColumn(t *testing.T) {
	t.Parallel()
	rows := []ClaimedFactRow{
		{Fields: map[string]ScalarValue{
			"day": renderScalarString("2026-07-20"), "work_scope_id": renderScalarString("a"),
		}},
		{Fields: map[string]ScalarValue{
			"day": renderScalarString("2026-08-30"), "work_scope_id": renderScalarString("b"),
		}},
	}
	_, event := SelectRenderShapes(trendFixture(rows))
	if skipRecorded(event, contractsv1.ContextFabricRenderRuleDatedFactTrend, RenderShapeSkipMixedScopeRows) {
		t.Errorf("blamed a scope split for a table whose measure is absent; skipped=%+v", event.Skipped)
	}
	if !skipRecorded(event, contractsv1.ContextFabricRenderRuleDatedFactTrend, RenderShapeSkipFieldNotPlottable) {
		t.Errorf("the reason does not name the actual problem; skipped=%+v", event.Skipped)
	}
}

// codex P2 (EXECUTED): a refused mixed-scope fact was invisible whenever ANY
// other fact produced a trend, because the reason was only recorded when the
// rule produced nothing at all. A silent refusal is undiagnosable from the
// run's own artifacts, which is the failure mode this file's telemetry
// exists to prevent.
func TestMixedScopeIsReportedEvenWhenAnotherFactTrends(t *testing.T) {
	t.Parallel()
	good := ClaimedFact{
		ClaimID: "claim-good", Kind: "flow",
		Subject: SubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:g", Label: "g"},
		Field:   "items_completed",
		Rows: []ClaimedFactRow{
			{Fields: map[string]ScalarValue{"day": renderScalarString("2026-07-20"), "items_completed": renderScalarNumber(1)}},
			{Fields: map[string]ScalarValue{"day": renderScalarString("2026-08-30"), "items_completed": renderScalarNumber(2)}},
		},
	}
	mixed := ClaimedFact{
		ClaimID: "claim-mixed", Kind: "flow",
		Subject: SubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:m", Label: "m"},
		Field:   "items_completed", Rows: liveFlowRows(),
	}
	result := InvestigationResult{
		Interpretation: InterpretedQuestion{Shape: contractsv1.ContextFabricShapeSingleSubject},
		ClaimedFacts:   []ClaimedFact{good, mixed},
	}
	shapes, event := SelectRenderShapes(result)
	if len(shapes) != 1 {
		t.Fatalf("expected the good fact's trend, got %d shapes", len(shapes))
	}
	if !skipRecorded(event, contractsv1.ContextFabricRenderRuleDatedFactTrend, RenderShapeSkipMixedScopeRows) {
		t.Fatalf("the refused mixed-scope fact left no trace; skipped=%+v", event.Skipped)
	}
}
