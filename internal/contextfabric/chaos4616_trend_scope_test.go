package contextfabric

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4616: the `dated_fact_trend` rule was WITHDRAWN, and these tests
// pinned that it stayed withdrawn. CHAOS-4637 (S6) brings it back
// DECLARATION-DRIVEN, and these tests now pin the stronger property: the
// rows that produced the misleading chart cannot be charted even now the
// rule exists again -- and cannot even be DECLARED in a way that would let
// them be.
//
// This file was written with its own way back named in it (see
// TestUndeclaredRowsAreNeverCharted's comment, unchanged below): "a future
// producer gated on ... a declared row-table shape (CHAOS-4627) could
// legitimately select dated_fact_trend with this test still green". That is
// what happened. Nothing here was relaxed to let it happen.
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
	return trendFixtureDeclared(rows, nil)
}

func trendFixtureDeclared(rows []ClaimedFactRow, table *contractsv1.ContextFabricClaimedFactTable) InvestigationResult {
	return InvestigationResult{
		Interpretation: InterpretedQuestion{Shape: contractsv1.ContextFabricShapeSingleSubject},
		ClaimedFacts: []ClaimedFact{{
			ClaimID: "claim-fullchaos-flow",
			Kind:    "flow",
			Subject: SubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:fullchaos", Label: "fullchaos"},
			Field:   "items_completed",
			Rows:    rows,
			Table:   table,
		}},
	}
}

// TestNoTrendIsSelectedForTheLiveMisleadingRows is the regression that
// matters, now in its strongest form: the exact rows that produced the
// misleading chart, carrying the HONEST declaration flow.go emits for them,
// still produce no shape -- and the recorded reason names the declaration
// rather than a rule-wide withdrawal.
//
// Non-vacuity is asserted FIRST, deliberately. A fixture that never reaches
// the guarded path is green against the defect it claims to pin, which is a
// failure mode this project has hit repeatedly (lane-4636 shipped three
// such fixtures and caught all three only by mutating the fix back). So:
// the declaration must actually be attached, and the rows must actually be
// the two-scope live rows, before the absence of a shape means anything.
func TestNoTrendIsSelectedForTheLiveMisleadingRows(t *testing.T) {
	t.Parallel()
	declared := &contractsv1.ContextFabricClaimedFactTable{
		Field: "scope_breakdown",
		Shape: contractsv1.ContextFabricFactTableShapeBreakdown,
		Key:   []string{"work_scope_id"},
		Measures: []string{
			"day", "items_completed", "wip_count_end_of_day",
		},
	}
	fixture := trendFixtureDeclared(liveFlowRows(), declared)
	// Non-vacuity: the fixture reaches the guarded path.
	if fixture.ClaimedFacts[0].Table == nil {
		t.Fatal("fixture carries no declaration, so this proves nothing about a declared table")
	}
	if len(fixture.ClaimedFacts[0].Rows) != 2 {
		t.Fatalf("fixture carries %d rows, want the 2 live rows", len(fixture.ClaimedFacts[0].Rows))
	}
	first, _ := renderStringCell(fixture.ClaimedFacts[0].Rows[0].Fields["work_scope_id"])
	second, _ := renderStringCell(fixture.ClaimedFacts[0].Rows[1].Fields["work_scope_id"])
	if first == second {
		t.Fatalf("both fixture rows carry work_scope_id %q; the defect needs two DIFFERENT scopes", first)
	}

	shapes, event := SelectRenderShapes(fixture, frameForRenderFixture(fixture))
	if len(shapes) != 0 {
		t.Fatalf("a shape was selected for the rows that produced the misleading chart: %+v", shapes)
	}
	if !skipRecorded(event, contractsv1.ContextFabricRenderRuleDatedFactTrend, RenderShapeSkipNoTimeSeriesTable) {
		t.Errorf("the refusal does not name the declaration, so a reader cannot tell it from a rule that found no tables at all; skipped=%+v", event.Skipped)
	}
	if err := event.Accounted(); err != nil {
		t.Errorf("selector accounting: %v", err)
	}
}

// TestTheMisleadingRowsCannotBeDeclaredATimeSeries is the half that makes
// the fix structural rather than defensive.
//
// The live defect was two DIFFERENT work scopes plotted as one thing
// changing over time. A producer cannot declare those rows a time_series
// even if it wants to: the identity of such a row needs (provider,
// work_scope_id), a time_series declares exactly ONE key column, and a
// declaration a producer cannot satisfy fails at the producer, in the
// producer's own test -- not at a renderer three stages later. The wrong
// state is unrepresentable rather than merely guarded against.
func TestTheMisleadingRowsCannotBeDeclaredATimeSeries(t *testing.T) {
	t.Parallel()
	dishonest := FactTable{
		Shape:    FactTableTimeSeries,
		Key:      []string{"provider", "work_scope_id"},
		Measures: []string{"day", "items_completed"},
		Rows: []FactValueRow{
			{Fields: map[string]FactValue{
				"provider": StringFactValue("github"), "work_scope_id": StringFactValue("full.chaos/chaos-ops"),
				"day": StringFactValue("2026-07-20"), "items_completed": IntegerFactValue(0),
			}},
			{Fields: map[string]FactValue{
				"provider": StringFactValue("github"), "work_scope_id": StringFactValue("full.chaos/dev-health-ops"),
				"day": StringFactValue("2026-08-30"), "items_completed": IntegerFactValue(1),
			}},
		},
	}
	err := dishonest.Validate()
	if err == nil {
		t.Fatal("a two-column-key time_series validated; the CHAOS-4616 defect is representable again")
	}
	// The error must be about the ARITY, not about something else this
	// fixture also happens to trip. A first version of this test asserted
	// only `err != nil` and SURVIVED a mutation that disabled the arity
	// rule outright -- the rows failed the instant-parse check instead, so
	// the test was green against the exact defect it named. Naming the
	// rule is what makes it discriminating.
	if !strings.Contains(err.Error(), "exactly one column") {
		t.Fatalf("the declaration was refused for the wrong reason (%v); this test proves nothing about the one-key-column rule", err)
	}
}

// TestUndeclaredRowsAreNeverCharted is the sharper half. A single-scope,
// single-measure, properly dated table is exactly what a trend SHOULD be,
// and it still selects nothing, because nothing DECLARED it one. That is
// CHAOS-4627's ruled default and it is what stops the geometry inference
// from creeping back: "these rows look like a series" is precisely the
// judgement this slice removed.
//
// What this does NOT prove, so nobody reads more into it than it says: it
// pins UNANNOTATED legacy rows, not universal absence. A future producer
// gated on something new — a declared row-table shape (CHAOS-4627), a new
// fact kind — could legitimately select `dated_fact_trend` with this test
// still green, because its fixture carries no such declaration. That is the
// intended way back, and this test is not the thing standing in its way.
func TestUndeclaredRowsAreNeverCharted(t *testing.T) {
	t.Parallel()
	rows := []ClaimedFactRow{
		{Fields: map[string]ScalarValue{
			"day": renderScalarString("2026-07-20"), "items_completed": renderScalarNumber(0),
		}},
		{Fields: map[string]ScalarValue{
			"day": renderScalarString("2026-08-30"), "items_completed": renderScalarNumber(3),
		}},
	}
	shapes, event := SelectRenderShapes(trendFixture(rows), frameForRenderFixture(trendFixture(rows)))
	if len(shapes) != 0 {
		t.Fatalf("an undeclared table was charted: %+v", shapes)
	}
	if !skipRecorded(event, contractsv1.ContextFabricRenderRuleDatedFactTrend, RenderShapeSkipNoDeclaredTable) {
		t.Errorf("undeclared was not recorded as such; skipped=%+v", event.Skipped)
	}
}

// TestTheTrendRuleDoesNotDisturbTheCohortRules: the two shapes chris asked
// for and that are proven live must be unaffected, both when the trend rule
// was absent and now that it is back. chrisTeamsAnswer carries a
// perfectly-dated readiness table with NO declaration, so the trend rule
// correctly declines it.
func TestTheTrendRuleDoesNotDisturbTheCohortRules(t *testing.T) {
	t.Parallel()
	shapes, _ := SelectRenderShapes(chrisTeamsAnswer(), frameForRenderFixture(chrisTeamsAnswer()))
	rules := map[contractsv1.ContextFabricRenderShapeRule]bool{}
	for _, shape := range shapes {
		rules[shape.SelectedBy] = true
	}
	if !rules[contractsv1.ContextFabricRenderRuleCohortAttentionScore] ||
		!rules[contractsv1.ContextFabricRenderRuleCohortDriverContribution] {
		t.Fatalf("the cohort shapes were disturbed by the trend withdrawal; got %+v", rules)
	}
	if rules[contractsv1.ContextFabricRenderRuleDatedFactTrend] {
		t.Fatal("a trend was selected for an UNDECLARED table")
	}
}

// TestTheScopeColumnCannotBeRecastAsAMeasure is codex round 1 finding 1 (P1,
// EXECUTED by the reviewer and re-run by this lane before being ledgered).
//
// TestTheMisleadingRowsCannotBeDeclaredATimeSeries above closes ONE route
// back to the CHAOS-4616 defect: putting the scope in the KEY, which makes
// the key arity 2 and the table a breakdown by definition. This test closes
// the other: putting the scope in MEASURES.
//
// UPDATED FOR CHAOS-4680, per this test's own prior warning ("if that is
// intended, health.go's `severity` measure must have moved too, and this
// test's reasoning needs rewriting rather than deleting"). At the time this
// test was written, the declaration below validated cleanly -- Measures
// carried no numeric requirement, because doing so would have invalidated
// health.go's own `severity` measure (a legitimate per-day categorical
// observation with nowhere else to go). CHAOS-4680 gives it somewhere else
// to go (FactTable.Observations), which makes a required-numeric Measures
// rule safe. The recast declaration below is therefore now refused AT
// VALIDATION, at the producer -- closing the hole structurally rather than
// merely refusing to draw a line through it at selection.
func TestTheScopeColumnCannotBeRecastAsAMeasure(t *testing.T) {
	t.Parallel()
	recast := FactTable{
		Shape:    FactTableTimeSeries,
		Key:      []string{"day"},
		Measures: []string{"work_scope_id", "items_completed"},
		Rows: []FactValueRow{
			{Fields: map[string]FactValue{
				"day": StringFactValue("2026-07-20"), "work_scope_id": StringFactValue("full.chaos/chaos-ops"), "items_completed": IntegerFactValue(0),
			}},
			{Fields: map[string]FactValue{
				"day": StringFactValue("2026-08-30"), "work_scope_id": StringFactValue("full.chaos/dev-health-ops"), "items_completed": IntegerFactValue(1),
			}},
		},
	}
	// Non-vacuity: every OTHER time_series rule this declaration could trip
	// is satisfied, so the refusal below is attributable to the recasting
	// and to nothing else.
	if len(recast.Key) != 1 {
		t.Fatal("fixture key arity is not 1; it would be refused by the arity rule instead")
	}
	if !parsesAsFactTableInstant(*recast.Rows[0].Fields["day"].String) {
		t.Fatal("fixture key does not parse as an instant; it would be refused by the instant rule instead")
	}

	// CHAOS-4680: the declaration is now refused at the producer, because
	// work_scope_id is a string cell declared as a Measure, and Measures is
	// now required to be numeric.
	err := recast.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want an error: a numeric-identity route recast as a measure must be rejected at the producer")
	}
	if !strings.Contains(err.Error(), "not numeric") {
		t.Fatalf("the declaration was refused for the wrong reason (%v); this test proves nothing about the numeric-measures rule", err)
	}

	// The class is not fully closed by this alone: a table that reached the
	// WIRE without going through domain Validate() (a pre-CHAOS-4680 stored
	// result, or a hand-built wire fixture) still needs a second gate. That
	// gate is RenderShapeSkipUnresolvableMeasureRoles, retained in
	// render_shapes.go as defense-in-depth -- proven here directly against
	// the wire type, bypassing FactTable.Validate() entirely, which is
	// exactly the scenario the domain-level fix cannot reach.
	subject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:fullchaos", Label: "fullchaos"}
	wireTable := &contractsv1.ContextFabricClaimedFactTable{
		Field: "scope_rows", Shape: contractsv1.ContextFabricFactTableShapeTimeSeries,
		Key: []string{"day"}, Measures: []string{"work_scope_id", "items_completed"},
	}
	claim := ClaimedFact{
		ClaimID: "claim_recast_4616", Kind: FactFlow, Subject: subject, Field: "items_completed",
		Table: wireTable,
		Rows: []ClaimedFactRow{
			{Fields: map[string]ScalarValue{"day": renderScalarString("2026-07-20"), "work_scope_id": renderScalarString("full.chaos/chaos-ops"), "items_completed": renderScalarNumber(0)}},
			{Fields: map[string]ScalarValue{"day": renderScalarString("2026-08-30"), "work_scope_id": renderScalarString("full.chaos/dev-health-ops"), "items_completed": renderScalarNumber(1)}},
		},
	}
	// Non-vacuity: the declaration DOES describe the wire rows, and the
	// claim's field IS a declared measure -- so every earlier gate passes
	// and the refusal below is attributable to the unresolvable-roles gate.
	if !claim.Table.HasMeasure(claim.Field) {
		t.Fatal("the claim's field is not a declared measure; claim_field_not_a_measure would refuse it instead")
	}

	result := InvestigationResult{
		Interpretation: InterpretedQuestion{Shape: contractsv1.ContextFabricShapeSingleSubject},
		ClaimedFacts:   []ClaimedFact{claim},
	}
	shapes, event := SelectRenderShapes(result, frameForRenderFixture(result))
	if len(shapes) != 0 {
		t.Fatalf("the recast declaration was charted -- the CHAOS-4616 false line is back through the declaration: %+v", shapes)
	}
	if !skipRecorded(event, contractsv1.ContextFabricRenderRuleDatedFactTrend, RenderShapeSkipUnresolvableMeasureRoles) {
		t.Errorf("the refusal does not name the unresolvable roles; skipped=%+v", event.Skipped)
	}
	if err := event.Accounted(); err != nil {
		t.Errorf("selector accounting: %v", err)
	}
}
