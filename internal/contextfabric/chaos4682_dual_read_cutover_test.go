package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4682 (§5.1 P2 dual-read cutover, orchestrator ruling 2026-09-01):
// serve BOTH tables on a dual-table fact, ADDITIVELY -- Table/Rows keep
// their current meaning and legacy preference unconditionally; the new
// TimeSeriesTable/TimeSeriesRows pair carries the time_series table a
// dual-table fact's own producer already declares but which, before this
// ticket, never reached the wire at all.
//
// Every test here asserts NON-VACUITY FIRST, the same discipline
// chaos4637_declared_render_selection_test.go's own header documents: a
// fixture that does not genuinely reach the guarded path proves nothing
// about the property it claims to pin.

// dualTableProjectWorkloadFact mirrors devhealthfacts/workload.go's REAL
// readProjectWorkload shape exactly: team_breakdown (a legacy breakdown,
// Key on team_id/team_name/work_scope_id/computed_at) alongside
// daily_workload (the CHAOS-4645 time_series, Key=[day]) on the SAME
// project-subject FactWorkload.
func dualTableProjectWorkloadFact(subject SubjectRef) CanonicalFact {
	return CanonicalFact{
		Kind: FactWorkload, Subject: subject,
		Fields: map[string]FactValue{
			"team_breakdown": TableFactValue(FactTable{
				Shape: FactTableBreakdown,
				Key:   []string{"team_id", "team_name", "work_scope_id", "computed_at"},
				Measures: []string{
					"throughput_mean", "throughput_stddev", "backlog_size", "forecast_p50_days",
				},
				Observations: []string{"insufficient_history", "high_variance"},
				Rows: []FactValueRow{
					{Fields: map[string]FactValue{
						"team_id": StringFactValue("team:CHAOS"), "team_name": StringFactValue("CHAOS"),
						"work_scope_id": StringFactValue("scope-1"), "computed_at": StringFactValue("2026-08-30T00:00:00Z"),
						"throughput_mean": NumberFactValue(4.2), "throughput_stddev": NumberFactValue(0.9),
						"backlog_size": IntegerFactValue(12), "forecast_p50_days": IntegerFactValue(5),
						"insufficient_history": BooleanFactValue(false), "high_variance": BooleanFactValue(false),
					}},
				},
			}),
			"daily_workload": TableFactValue(FactTable{
				Shape: FactTableTimeSeries, Key: []string{"day"},
				Measures: []string{"backlog_size", "throughput_mean", "throughput_stddev"},
				Rows: []FactValueRow{
					{Fields: map[string]FactValue{"day": StringFactValue("2026-08-03"), "backlog_size": IntegerFactValue(9), "throughput_mean": NumberFactValue(3.8), "throughput_stddev": NumberFactValue(0.7)}},
					{Fields: map[string]FactValue{"day": StringFactValue("2026-08-18"), "backlog_size": IntegerFactValue(11), "throughput_mean": NumberFactValue(4.0), "throughput_stddev": NumberFactValue(0.8)}},
					{Fields: map[string]FactValue{"day": StringFactValue("2026-08-30"), "backlog_size": IntegerFactValue(12), "throughput_mean": NumberFactValue(4.2), "throughput_stddev": NumberFactValue(0.9)}},
				},
			}),
			// CHAOS-4681's own fix: the scalar sibling matching the declared
			// measure, so a claim naming "backlog_size" is admissible at all
			// (modelFacingFacts drops every Rows-shaped field before
			// synthesis, so the model never sees daily_workload directly).
			"backlog_size": IntegerFactValue(12),
		},
	}
}

// TestDualTableProjectFactRendersATrend is the ruling's first required red
// test: a REAL dual-table project fact (the exact shape CHAOS-4681 fixed the
// scalar-sibling gap for) now gets a rendered trend chart end to end --
// attachCanonicalRows through SelectRenderShapes -- which was structurally
// impossible before this ticket (CHAOS-4645's legacy preference meant
// daily_workload's rows never reached the wire on any field).
func TestDualTableProjectFactRendersATrend(t *testing.T) {
	t.Parallel()
	subject := SubjectRef{Kind: SubjectProject, CanonicalID: "project:ask-dev", Label: "Ask Dev"}
	fact := dualTableProjectWorkloadFact(subject)
	for _, field := range []string{"team_breakdown", "daily_workload"} {
		if err := fact.Fields[field].Validate(); err != nil {
			t.Fatalf("fixture field %q is not a valid declared table: %v", field, err)
		}
	}

	claims := []ClaimedFact{{Kind: FactWorkload, Subject: subject, Field: "backlog_size"}}
	got, _, _, truncated := attachCanonicalRows(claims, []CanonicalFact{fact})
	if truncated {
		t.Fatal("attachCanonicalRows reported truncated=true, want false")
	}
	// Non-vacuity: Table/Rows must still serve the LEGACY field, unchanged
	// -- if this fixture stopped doing that, the rest of the test would not
	// be measuring the additive pair at all.
	if got[0].Table == nil || got[0].Table.Field != "team_breakdown" {
		t.Fatalf("Table = %+v, want the legacy team_breakdown field unchanged", got[0].Table)
	}
	if got[0].TimeSeriesTable == nil || got[0].TimeSeriesTable.Field != "daily_workload" {
		t.Fatalf("TimeSeriesTable = %+v, want daily_workload attached", got[0].TimeSeriesTable)
	}
	if len(got[0].TimeSeriesRows) != 3 {
		t.Fatalf("TimeSeriesRows = %d rows, want the 3 daily_workload rows", len(got[0].TimeSeriesRows))
	}

	claim := got[0]
	claim.ClaimID = "claim_dual_project_workload"
	result := InvestigationResult{
		Interpretation: InterpretedQuestion{Shape: contractsv1.ContextFabricShapeSingleSubject},
		ClaimedFacts:   []ClaimedFact{claim},
	}
	shapes, event := SelectRenderShapes(result)
	trend := shapeByRule(shapes, contractsv1.ContextFabricRenderRuleDatedFactTrend)
	if trend == nil {
		t.Fatalf("the dual-table PROJECT fact's time_series was not charted; shapes=%+v skipped=%+v", shapes, event.Skipped)
	}
	if len(trend.Series) != 1 || trend.Series[0].Key != "backlog_size" || len(trend.Series[0].Points) != 3 {
		t.Fatalf("trend = %+v, want one 3-point backlog_size series", trend)
	}
	result.RenderShapes = shapes
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the dual-table trend does not survive the render-shape validator: %v", err)
	}
}

// TestSingleTableTeamFactStillRendersUnchanged is the ruling's second
// required red test: a single-table time_series fact (team workload, the
// shape that already charted before this ticket) is untouched by P2 --
// TimeSeriesTable/TimeSeriesRows stay nil, and the trend still comes from
// Table/Rows exactly as before.
func TestSingleTableTeamFactStillRendersUnchanged(t *testing.T) {
	t.Parallel()
	subject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CHAOS", Label: "CHAOS"}
	fact := CanonicalFact{
		Kind: FactWorkload, Subject: subject,
		Fields: map[string]FactValue{
			"backlog_size": IntegerFactValue(31),
			"daily_workload": TableFactValue(FactTable{
				Shape: FactTableTimeSeries, Key: []string{"day"},
				Measures: []string{"backlog_size"},
				Rows: []FactValueRow{
					{Fields: map[string]FactValue{"day": StringFactValue("2026-08-03"), "backlog_size": IntegerFactValue(28)}},
					{Fields: map[string]FactValue{"day": StringFactValue("2026-08-30"), "backlog_size": IntegerFactValue(31)}},
				},
			}),
		},
	}
	claims := []ClaimedFact{{Kind: FactWorkload, Subject: subject, Field: "backlog_size"}}
	got, _, _, truncated := attachCanonicalRows(claims, []CanonicalFact{fact})
	if truncated {
		t.Fatal("attachCanonicalRows reported truncated=true, want false")
	}
	if got[0].Table == nil || got[0].Table.Field != "daily_workload" || got[0].Table.Shape != contractsv1.ContextFabricFactTableShapeTimeSeries {
		t.Fatalf("Table = %+v, want the sole daily_workload time_series unchanged", got[0].Table)
	}
	if len(got[0].Rows) != 2 {
		t.Fatalf("Rows = %d, want the 2 daily_workload rows on the EXISTING pair", len(got[0].Rows))
	}
	// The whole point of this test: a single-table fact has nothing left
	// for the additive pair to carry.
	if got[0].TimeSeriesTable != nil || got[0].TimeSeriesRows != nil {
		t.Fatalf("TimeSeriesTable=%+v TimeSeriesRows=%+v, want both nil on a single-table fact", got[0].TimeSeriesTable, got[0].TimeSeriesRows)
	}

	claim := got[0]
	claim.ClaimID = "claim_single_table_team_workload"
	result := InvestigationResult{
		Interpretation: InterpretedQuestion{Shape: contractsv1.ContextFabricShapeSingleSubject},
		ClaimedFacts:   []ClaimedFact{claim},
	}
	shapes, event := SelectRenderShapes(result)
	trend := shapeByRule(shapes, contractsv1.ContextFabricRenderRuleDatedFactTrend)
	if trend == nil {
		t.Fatalf("the single-table fact's trend regressed; shapes=%+v skipped=%+v", shapes, event.Skipped)
	}
	if len(trend.Series) != 1 || len(trend.Series[0].Points) != 2 {
		t.Fatalf("trend = %+v, want one 2-point series from Table/Rows", trend)
	}
}

// TestBreakdownOnlyFactStillSkipsNoTimeSeriesTable is the ruling's third
// required red test: a fact with NO time_series field at all -- one legacy
// breakdown, nothing else -- still correctly skips with no_time_series_table.
// P2 never invents a trend where the producer declared none.
func TestBreakdownOnlyFactStillSkipsNoTimeSeriesTable(t *testing.T) {
	t.Parallel()
	subject := SubjectRef{Kind: SubjectProject, CanonicalID: "project:ask-dev", Label: "Ask Dev"}
	fact := CanonicalFact{
		Kind: FactHealth, Subject: subject,
		Fields: map[string]FactValue{
			"risk_breakdown": TableFactValue(FactTable{
				Shape: FactTableBreakdown, Key: []string{"scope", "scope_id", "computed_at"},
				Measures: []string{"compounding_risk"},
				Rows: []FactValueRow{
					{Fields: map[string]FactValue{"scope": StringFactValue("team"), "scope_id": StringFactValue("t1"), "computed_at": StringFactValue("2026-08-30T00:00:00Z"), "compounding_risk": NumberFactValue(0.4)}},
				},
			}),
			"compounding_risk": NumberFactValue(0.4),
		},
	}
	claims := []ClaimedFact{{Kind: FactHealth, Subject: subject, Field: "compounding_risk"}}
	got, _, _, truncated := attachCanonicalRows(claims, []CanonicalFact{fact})
	if truncated {
		t.Fatal("attachCanonicalRows reported truncated=true, want false")
	}
	if got[0].Table == nil || got[0].Table.Shape != contractsv1.ContextFabricFactTableShapeBreakdown {
		t.Fatalf("Table = %+v, want the sole risk_breakdown unchanged", got[0].Table)
	}
	// Non-vacuity for the additive pair: nothing time_series exists on this
	// fact at all, so the pair has nothing to carry either.
	if got[0].TimeSeriesTable != nil || got[0].TimeSeriesRows != nil {
		t.Fatalf("TimeSeriesTable=%+v TimeSeriesRows=%+v, want both nil -- no time_series field exists on this fact", got[0].TimeSeriesTable, got[0].TimeSeriesRows)
	}

	fact2, _ := datedFactTrendShape(ClaimedFact{
		ClaimID: "claim_breakdown_only", Kind: FactHealth, Subject: subject, Field: "compounding_risk",
		Rows: got[0].Rows, Table: got[0].Table,
	})
	if fact2 != nil {
		t.Fatalf("a breakdown-only fact was charted as a trend: %+v", fact2)
	}
	_, stage := datedFactTrendShape(ClaimedFact{
		ClaimID: "claim_breakdown_only", Kind: FactHealth, Subject: subject, Field: "compounding_risk",
		Rows: got[0].Rows, Table: got[0].Table,
	})
	if stage != trendStageNoTimeSeriesTable {
		t.Fatalf("trend stage = %v, want trendStageNoTimeSeriesTable", stage)
	}
}

// TestCanonicalTimeSeriesFieldIsNilOnAmbiguity locks in
// canonicalTimeSeriesField's own no-fallback rule: two time_series fields,
// or two non-time_series fields, are exactly as ambiguous for the additive
// pair as they already are for canonicalRowsField's legacy field -- neither
// case guesses.
func TestCanonicalTimeSeriesFieldIsNilOnAmbiguity(t *testing.T) {
	t.Parallel()
	subject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CHAOS", Label: "CHAOS"}

	t.Run("two time_series fields", func(t *testing.T) {
		fact := CanonicalFact{
			Kind: FactWorkload, Subject: subject,
			Fields: map[string]FactValue{
				"daily_workload_a": TableFactValue(FactTable{Shape: FactTableTimeSeries, Key: []string{"day"}, Measures: []string{"x"}, Rows: []FactValueRow{{Fields: map[string]FactValue{"day": StringFactValue("2026-08-03"), "x": IntegerFactValue(1)}}}}),
				"daily_workload_b": TableFactValue(FactTable{Shape: FactTableTimeSeries, Key: []string{"day"}, Measures: []string{"y"}, Rows: []FactValueRow{{Fields: map[string]FactValue{"day": StringFactValue("2026-08-03"), "y": IntegerFactValue(2)}}}}),
			},
		}
		if field, ok := canonicalTimeSeriesField(fact); ok {
			t.Fatalf("canonicalTimeSeriesField = (%q, true), want ok=false on two ambiguous time_series fields", field)
		}
	})

	t.Run("two non-time_series fields", func(t *testing.T) {
		fact := CanonicalFact{
			Kind: FactWorkload, Subject: subject,
			Fields: map[string]FactValue{
				"breakdown_a": TableFactValue(FactTable{Shape: FactTableBreakdown, Key: []string{"id"}, Measures: []string{"x"}, Rows: []FactValueRow{{Fields: map[string]FactValue{"id": StringFactValue("1"), "x": IntegerFactValue(1)}}}}),
				"breakdown_b": TableFactValue(FactTable{Shape: FactTableBreakdown, Key: []string{"id"}, Measures: []string{"y"}, Rows: []FactValueRow{{Fields: map[string]FactValue{"id": StringFactValue("2"), "y": IntegerFactValue(2)}}}}),
			},
		}
		if field, ok := canonicalTimeSeriesField(fact); ok {
			t.Fatalf("canonicalTimeSeriesField = (%q, true), want ok=false -- canonicalRowsField itself is already ambiguous here", field)
		}
	})

	t.Run("single field, no second table to add", func(t *testing.T) {
		fact := CanonicalFact{
			Kind: FactWorkload, Subject: subject,
			Fields: map[string]FactValue{
				"daily_workload": TableFactValue(FactTable{Shape: FactTableTimeSeries, Key: []string{"day"}, Measures: []string{"x"}, Rows: []FactValueRow{{Fields: map[string]FactValue{"day": StringFactValue("2026-08-03"), "x": IntegerFactValue(1)}}}}),
			},
		}
		if field, ok := canonicalTimeSeriesField(fact); ok {
			t.Fatalf("canonicalTimeSeriesField = (%q, true), want ok=false -- the sole field is already served by canonicalRowsField/Table", field)
		}
	})
}
