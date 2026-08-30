package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4616: the `dated_fact_trend` rule is WITHDRAWN, and these tests pin
// that it stays withdrawn.
//
// The history matters more than the code, because the code is now an
// absence. A trend was shipped in CHAOS-4415 slice 1 and, on the live rig,
// drew a line across two DIFFERENT work scopes measured once each six weeks
// apart — reading as one scope changing over time. Every plotted number was
// copied faithfully and the chart still said something the data does not.
//
// Three attempts to fix it by inferring which columns are measures and which
// are dimensions were each defeated in review:
//
//  1. skip numeric columns — a numeric `team_id` (101, 202) became a plotted
//     series;
//  2. an `id`/`*_id` NAME test — a column called `year` walked through it;
//  3. read the claim's own `Field` — correct for one measure, but it
//     silently narrowed every multi-measure table to nothing.
//
// The information is not in the row table. It EXISTS at the producer —
// devhealthfacts/flow.go builds that table under a field it names
// `scope_breakdown`, and says so in its own doc comment — but it is not
// carried on the wire. Until a row table declares its shape, any trend this
// rule drew would be a server-asserted claim resting on a guess.
//
// This removes a SERVER assertion only. Consumers still draw row tables with
// their own generic visualization; nothing disappears from a reader's
// screen.

func liveFlowRows() []ClaimedFactRow {
	return []ClaimedFactRow{
		{Fields: map[string]ScalarValue{
			"day":                  renderScalarString("2026-07-20"),
			"work_scope_id":        renderScalarString("full.chaos/chaos-ops"),
			"items_completed":      renderScalarNumber(0),
			"wip_count_end_of_day": renderScalarNumber(0),
		}},
		{Fields: map[string]ScalarValue{
			"day":                  renderScalarString("2026-08-30"),
			"work_scope_id":        renderScalarString("full.chaos/dev-health-ops"),
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

// TestNoTrendIsSelectedForTheLiveMisleadingRows is the regression that
// matters: the exact rows that produced the misleading chart must produce no
// shape at all.
func TestNoTrendIsSelectedForTheLiveMisleadingRows(t *testing.T) {
	t.Parallel()
	shapes, event := SelectRenderShapes(trendFixture(liveFlowRows()))
	if len(shapes) != 0 {
		t.Fatalf("a shape was selected for the rows that produced the misleading chart: %+v", shapes)
	}
	if !skipRecorded(event, contractsv1.ContextFabricRenderRuleDatedFactTrend, RenderShapeSkipTrendRuleWithdrawn) {
		t.Errorf("the withdrawal is not recorded, so a reader cannot tell it from a rule that ran and found nothing; skipped=%+v", event.Skipped)
	}
}

// TestNoTrendIsSelectedEvenForAPerfectlyShapedTable is the sharper half. A
// single-scope, single-measure, properly dated table is exactly what a trend
// SHOULD have been, and it still selects nothing — because the rule is
// withdrawn, not merely narrowed. A future producer re-enabling it must
// delete this test deliberately.
func TestNoTrendIsSelectedEvenForAPerfectlyShapedTable(t *testing.T) {
	t.Parallel()
	rows := []ClaimedFactRow{
		{Fields: map[string]ScalarValue{
			"day": renderScalarString("2026-07-20"), "items_completed": renderScalarNumber(0),
		}},
		{Fields: map[string]ScalarValue{
			"day": renderScalarString("2026-08-30"), "items_completed": renderScalarNumber(3),
		}},
	}
	if shapes, _ := SelectRenderShapes(trendFixture(rows)); len(shapes) != 0 {
		t.Fatalf("the withdrawn rule selected a shape: %+v", shapes)
	}
}

// TestTheWithdrawalDoesNotDisturbTheCohortRules: the two shapes chris asked
// for and that are proven live must be unaffected by the withdrawal.
func TestTheWithdrawalDoesNotDisturbTheCohortRules(t *testing.T) {
	t.Parallel()
	shapes, _ := SelectRenderShapes(chrisTeamsAnswer())
	rules := map[contractsv1.ContextFabricRenderShapeRule]bool{}
	for _, shape := range shapes {
		rules[shape.SelectedBy] = true
	}
	if !rules[contractsv1.ContextFabricRenderRuleCohortAttentionScore] ||
		!rules[contractsv1.ContextFabricRenderRuleCohortDriverContribution] {
		t.Fatalf("the cohort shapes were disturbed by the trend withdrawal; got %+v", rules)
	}
	if rules[contractsv1.ContextFabricRenderRuleDatedFactTrend] {
		t.Fatal("a trend was selected")
	}
}
