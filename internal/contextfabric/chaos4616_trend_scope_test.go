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
func TestTrendStillDrawsASingleScopeSeries(t *testing.T) {
	t.Parallel()
	rows := liveFlowRows()
	// Same two rows, now genuinely the same scope observed twice.
	rows[1].Fields["work_scope_id"] = renderScalarString("full.chaos/chaos-ops")
	shapes, _ := SelectRenderShapes(trendFixture(rows))
	if len(shapes) != 1 {
		t.Fatalf("a legitimate single-scope trend was refused; got %d shapes", len(shapes))
	}
	if shapes[0].SelectedBy != contractsv1.ContextFabricRenderRuleDatedFactTrend {
		t.Fatalf("selected_by = %s, want dated_fact_trend", shapes[0].SelectedBy)
	}
}

// TestTrendIgnoresAConstantDimensionColumn: a dimension column that never
// varies is provenance, not a scope split, and must not block a trend.
func TestTrendIgnoresAConstantDimensionColumn(t *testing.T) {
	t.Parallel()
	rows := liveFlowRows()
	rows[1].Fields["work_scope_id"] = renderScalarString("full.chaos/chaos-ops")
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
