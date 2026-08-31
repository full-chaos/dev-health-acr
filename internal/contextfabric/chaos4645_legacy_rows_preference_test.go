package contextfabric

import "testing"

// chaos4645_legacy_rows_preference_test.go pins the ruling that closes
// CHAOS-4645's disclosed-but-unacceptable Rows=nil window: chris's
// no-regression rule means a fact carrying two Rows-shaped fields (the
// pre-existing breakdown plus a new CHAOS-4645 time_series) must not
// silently drop the row table an existing consumer already renders.
//
// The fix is DECLARATION-DRIVEN, not a heuristic: canonicalFieldRows now
// resolves the ambiguity when, and only when, the declared FactTable.Shape
// values pick out exactly one non-time_series (legacy) field among the
// Rows-shaped ones. Two non-time_series tables -- a case this design never
// promises to disambiguate -- still fails closed exactly as before.

func breakdownTableFact(kind FactKind, legacyField, seriesField string) CanonicalFact {
	legacyTable := FactTable{
		Shape: FactTableBreakdown, Key: []string{"team_id"}, Measures: []string{"value"},
		Rows: []FactValueRow{{Fields: map[string]FactValue{"team_id": StringFactValue("team-1"), "value": IntegerFactValue(1)}}},
	}
	seriesTable := FactTable{
		Shape: FactTableTimeSeries, Key: []string{"day"}, Measures: []string{"value"},
		Rows: []FactValueRow{{Fields: map[string]FactValue{"day": StringFactValue("2026-08-30"), "value": IntegerFactValue(2)}}},
	}
	return CanonicalFact{
		Kind: kind, Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:team-1", Label: "Team 1"},
		Fields: map[string]FactValue{
			legacyField: TableFactValue(legacyTable),
			seriesField: TableFactValue(seriesTable),
		},
	}
}

// TestCanonicalFieldRowsPrefersTheDeclaredNonTimeSeriesFieldWhenTwoRowsFieldsExist
// is the RED-FIRST core of the ruling: before the fix, a fact carrying both
// a breakdown and a CHAOS-4645 time_series field returns (nil, true) --
// dropping the legacy breakdown a consumer already renders. After the fix,
// it must serve the unique non-time_series field's rows, unchanged.
func TestCanonicalFieldRowsPrefersTheDeclaredNonTimeSeriesFieldWhenTwoRowsFieldsExist(t *testing.T) {
	fact := breakdownTableFact(FactHealth, "risk_breakdown", "daily_health")
	rows, truncated := canonicalFieldRows(fact)
	if truncated {
		t.Fatalf("truncated = true, want false: the legacy field is unambiguous and must be served, not dropped")
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want the 1 row from the legacy breakdown field", rows)
	}
	if got := rows[0].Fields["team_id"].String; got == nil || *got != "team-1" {
		t.Fatalf("rows[0] = %#v, want the risk_breakdown row (team_id=team-1), not the daily_health row", rows[0])
	}
}

// TestCanonicalFieldRowsSingleFieldUnchanged pins that the count==1 path
// (the overwhelming majority of facts, and every fact before CHAOS-4645)
// is byte-for-byte unchanged by this fix.
func TestCanonicalFieldRowsSingleFieldUnchanged(t *testing.T) {
	fact := CanonicalFact{
		Kind: FactMetrics, Subject: SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:repo-1"},
		Fields: map[string]FactValue{
			"only_table": RowsFactValue([]FactValueRow{{Fields: map[string]FactValue{"x": StringFactValue("y")}}}),
		},
	}
	rows, truncated := canonicalFieldRows(fact)
	if truncated {
		t.Fatalf("truncated = true, want false")
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want 1", rows)
	}
}

// TestCanonicalFieldRowsStillFailsClosedOnTwoNonTimeSeriesTables is the
// negative case the ruling explicitly preserves: TWO declared breakdown
// (non-time_series) tables give no way to prefer one over the other, so
// this must still fail closed, exactly as CHAOS-4355 established.
func TestCanonicalFieldRowsStillFailsClosedOnTwoNonTimeSeriesTables(t *testing.T) {
	breakdownA := FactTable{Shape: FactTableBreakdown, Key: []string{"team_id"}, Measures: []string{"value"},
		Rows: []FactValueRow{{Fields: map[string]FactValue{"team_id": StringFactValue("team-1"), "value": IntegerFactValue(1)}}}}
	breakdownB := FactTable{Shape: FactTableBreakdown, Key: []string{"repo_id"}, Measures: []string{"value"},
		Rows: []FactValueRow{{Fields: map[string]FactValue{"repo_id": StringFactValue("repo-1"), "value": IntegerFactValue(2)}}}}
	fact := CanonicalFact{
		Kind: FactHealth, Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project:linear:proj-1"},
		Fields: map[string]FactValue{
			"table_a": TableFactValue(breakdownA),
			"table_b": TableFactValue(breakdownB),
		},
	}
	rows, truncated := canonicalFieldRows(fact)
	if rows != nil || !truncated {
		t.Fatalf("rows = %#v, truncated = %v, want (nil, true): two non-time_series tables must still fail closed", rows, truncated)
	}
}

// TestCanonicalFieldRowsFailsClosedOnTwoTimeSeriesTables: two time_series
// fields (no legacy field exists at all) must also fail closed -- there is
// nothing here to prefer either.
func TestCanonicalFieldRowsFailsClosedOnTwoTimeSeriesTables(t *testing.T) {
	seriesA := FactTable{Shape: FactTableTimeSeries, Key: []string{"day"}, Measures: []string{"value"},
		Rows: []FactValueRow{{Fields: map[string]FactValue{"day": StringFactValue("2026-08-29"), "value": IntegerFactValue(1)}}}}
	seriesB := FactTable{Shape: FactTableTimeSeries, Key: []string{"day"}, Measures: []string{"value"},
		Rows: []FactValueRow{{Fields: map[string]FactValue{"day": StringFactValue("2026-08-30"), "value": IntegerFactValue(2)}}}}
	fact := CanonicalFact{
		Kind: FactHealth, Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:team-1"},
		Fields: map[string]FactValue{
			"series_a": TableFactValue(seriesA),
			"series_b": TableFactValue(seriesB),
		},
	}
	rows, truncated := canonicalFieldRows(fact)
	if rows != nil || !truncated {
		t.Fatalf("rows = %#v, truncated = %v, want (nil, true)", rows, truncated)
	}
}

// TestCanonicalFieldRowsPrefersLegacyEvenWithoutADeclaredTable: a
// pre-CHAOS-4633 field with NO Table declaration at all (bare
// RowsFactValue) is unambiguously "not time_series" too -- it must count
// as the legacy candidate exactly like a declared breakdown does.
func TestCanonicalFieldRowsPrefersLegacyEvenWithoutADeclaredTable(t *testing.T) {
	seriesTable := FactTable{Shape: FactTableTimeSeries, Key: []string{"day"}, Measures: []string{"value"},
		Rows: []FactValueRow{{Fields: map[string]FactValue{"day": StringFactValue("2026-08-30"), "value": IntegerFactValue(2)}}}}
	fact := CanonicalFact{
		Kind: FactFlow, Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:team-1"},
		Fields: map[string]FactValue{
			"legacy_no_declaration": RowsFactValue([]FactValueRow{{Fields: map[string]FactValue{"x": StringFactValue("y")}}}),
			"daily_flow":            TableFactValue(seriesTable),
		},
	}
	rows, truncated := canonicalFieldRows(fact)
	if truncated {
		t.Fatalf("truncated = true, want false")
	}
	if len(rows) != 1 || rows[0].Fields["x"].String == nil || *rows[0].Fields["x"].String != "y" {
		t.Fatalf("rows = %#v, want the legacy_no_declaration row", rows)
	}
}
