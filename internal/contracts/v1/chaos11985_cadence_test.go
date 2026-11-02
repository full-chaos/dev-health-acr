package v1

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// chaos11985_cadence_test.go pins the wire declaration of a time_series
// table's cadence (CHAOS-11985): only on a time_series table, from a closed
// vocabulary, and only over rows that keep it. Every case is red on the base
// (Cadence does not exist there).

func cadenceTable() ContextFabricClaimedFactTable {
	return ContextFabricClaimedFactTable{
		Field: "daily_metrics", Shape: ContextFabricFactTableShapeTimeSeries,
		Key: []string{"day"}, Measures: []string{"commits_count"},
		Cadence: ContextFabricFactTableCadenceDay,
	}
}

func dayRows(days ...string) []ContextFabricClaimedFactRow {
	rows := make([]ContextFabricClaimedFactRow, 0, len(days))
	for _, day := range days {
		day := day
		rows = append(rows, ContextFabricClaimedFactRow{Fields: map[string]ContextFabricScalarValue{"day": {String: &day}}})
	}
	return rows
}

// Newest first and with a gap, as a daily series is served.
func TestCHAOS11985ADailyTableKeepsItsCadenceAcrossAGap(t *testing.T) {
	table := cadenceTable()
	rows := dayRows("2026-11-01", "2026-10-31", "2026-10-28")
	if err := validateTimeSeriesTable(&table, rows); err != nil {
		t.Fatalf("validateTimeSeriesTable() error = %v, want nil: a missing day is a gap, not a break of the cadence", err)
	}
	if err := validateClaimedFactTable(&table, rows); err != nil {
		t.Fatalf("validateClaimedFactTable() error = %v, want nil", err)
	}
}

func TestCHAOS11985ACadenceIsOnlyForATimeSeries(t *testing.T) {
	for _, shape := range []ContextFabricFactTableShape{ContextFabricFactTableShapeBreakdown, ContextFabricFactTableShapeRanking} {
		table := cadenceTable()
		table.Shape = shape
		if shape == ContextFabricFactTableShapeRanking {
			table.OrderBy = "commits_count"
		}
		err := table.Validate()
		if err == nil || !strings.Contains(err.Error(), "only for a time_series table") {
			t.Errorf("shape %q: Validate() error = %v, want a time_series-only refusal", shape, err)
		}
	}
}

func TestCHAOS11985AnUnknownCadenceIsRefused(t *testing.T) {
	for _, cadence := range []ContextFabricFactTableCadence{"week", "daily", "Day", " day"} {
		table := cadenceTable()
		table.Cadence = cadence
		err := table.Validate()
		if err == nil || !strings.Contains(err.Error(), "closed vocabulary") {
			t.Errorf("cadence %q: Validate() error = %v, want a closed-vocabulary refusal", cadence, err)
		}
		if err := table.CadenceAligned(dayRows("2026-11-01")); err == nil {
			t.Errorf("cadence %q: CadenceAligned() = nil, want no alignment rule for an unknown cadence", cadence)
		}
	}
}

// A day cadence needs a key column to read the day from.
func TestCHAOS11985ADayCadenceNeedsAKey(t *testing.T) {
	table := cadenceTable()
	table.Key = nil
	if err := table.CadenceAligned(dayRows("2026-11-01")); err == nil || !strings.Contains(err.Error(), "no alignment rule") {
		t.Fatalf("CadenceAligned() error = %v, want no alignment rule for a table with no key", err)
	}
}

// Each refusal is matched on its own message, so a case fails only at the
// rule it names.
func TestCHAOS11985RowsThatDoNotKeepTheDayCadenceAreRefused(t *testing.T) {
	seven := int64(7)
	for _, tc := range []struct {
		name string
		rows []ContextFabricClaimedFactRow
		want string
	}{
		{"an instant", dayRows("2026-11-01T00:00:00Z"), "written YYYY-MM-DD"},
		{"no zero padding", dayRows("2026-11-1"), "written YYYY-MM-DD"},
		{"no such day", dayRows("2026-02-30"), "written YYYY-MM-DD"},
		{"a repeated day", dayRows("2026-11-01", "2026-10-31", "2026-11-01"), "repeats day 2026-11-01"},
		{"not a string", []ContextFabricClaimedFactRow{{Fields: map[string]ContextFabricScalarValue{"day": {Integer: &seven}}}}, "is not a calendar date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table := cadenceTable()
			err := table.CadenceAligned(tc.rows)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CadenceAligned() error = %v, want one containing %q", err, tc.want)
			}
			// The served-table validator holds the same rule; with no cadence
			// the same rows are a valid table (an instant key, say).
			if err := validateClaimedFactTable(&table, tc.rows); err == nil {
				t.Fatal("validateClaimedFactTable() error = nil, want the cadence refusal")
			}
			table.Cadence = ""
			if err := table.CadenceAligned(tc.rows); err != nil {
				t.Fatalf("with no cadence: CadenceAligned() error = %v, want nil", err)
			}
		})
	}
}

// The Go vocabulary and every published enum are one list, in one order.
func TestCHAOS11985TheCadenceVocabularyIsTheSchemaEnum(t *testing.T) {
	vocabulary := ContextFabricFactTableCadenceVocabulary()
	want := make([]string, 0, len(vocabulary))
	for _, cadence := range vocabulary {
		want = append(want, string(cadence))
	}
	documents := schemaDocuments(t)
	for _, reference := range []string{
		"common#$defs.ClaimedFactTable.properties.cadence",
		"answer#$defs.ProjectedFactTable.properties.cadence",
	} {
		raw, err := json.Marshal(schemaNodeAt(t, documents, reference)["enum"])
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%s: enum is not a list of strings: %v", reference, err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s enum = %v, want the Go vocabulary %v", reference, got, want)
		}
	}
}

func TestCHAOS11985TheCadenceEncodesOnlyWhenStated(t *testing.T) {
	table := cadenceTable()
	encoded, err := json.Marshal(table)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"cadence":"day"`) {
		t.Fatalf("encoded table = %s, want cadence day", encoded)
	}
	table.Cadence = ""
	if encoded, err = json.Marshal(table); err != nil || strings.Contains(string(encoded), "cadence") {
		t.Fatalf("encoded table = %s (%v), want no cadence key when none is stated", encoded, err)
	}
}
