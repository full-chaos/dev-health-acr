package devhealthfacts

import (
	"strings"
	"testing"
	"time"
)

// CHAOS-6594: the mix statement names work_unit_investments exactly once, for
// one and for two windows. A second textual reference is a second scan.
func TestRepoMixStatementReadsWorkUnitInvestmentsOnce(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	current := factTimeBound{active: true, hasStart: true, start: start, end: end}
	prior := factTimeBound{active: true, hasStart: true, start: start.Add(-end.Sub(start)), end: start}
	for name, bounds := range map[string][]factTimeBound{
		"current axis":        {{}},
		"range":               {current},
		"range with prior":    {current, prior},
		"point in time":       {{active: true, end: end}},
		"point in time+prior": {{active: true, end: end}, prior},
	} {
		statement := repoMixStatement(bounds)
		if got := strings.Count(statement, "work_unit_investments"); got != 1 {
			t.Errorf("%s: statement references work_unit_investments %d times, want 1", name, got)
		}
		if got := strings.Count(statement, "FROM repos"); got != 1 {
			t.Errorf("%s: statement references repos %d times, want 1", name, got)
		}
		if strings.Contains(statement, "WITH ") {
			t.Errorf("%s: statement uses a CTE; ClickHouse inlines it at every reference", name)
		}
	}
}

// CHAOS-7257: the project roll-up and project-native statements name
// work_unit_investments exactly once, for every time bound. A second textual
// reference (a CTE used twice inlines twice) is a second scan of the table: it
// is what read 65.83 MiB against the 64 MiB max_bytes_to_read on prod. This is
// the cheap static guard; the byte measurement on a real ClickHouse is
// chaos7257_project_theme_mix_read_bytes_integration_test.go.
func TestProjectMixStatementsReadWorkUnitInvestmentsOnce(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for name, bound := range map[string]factTimeBound{
		"current axis":  {},
		"range":         {active: true, hasStart: true, start: start, end: end},
		"point in time": {active: true, end: end},
	} {
		for statementName, statement := range map[string]string{
			"roll-up": projectRollupMixStatement(bound),
			"native":  projectNativeMixStatement(bound, maxFactRowsProbe),
		} {
			if got := strings.Count(statement, "FROM work_unit_investments"); got != 1 {
				t.Errorf("%s / %s: statement reads work_unit_investments %d times, want 1", statementName, name, got)
			}
			// Any other mention (a table alias, a join) would be a second reader.
			if got := strings.Count(statement, "work_unit_investments"); got != 1 {
				t.Errorf("%s / %s: statement names work_unit_investments %d times, want 1", statementName, name, got)
			}
			for _, cte := range []string{"latest AS", "windowed AS", "repo_linked AS", "evidence_resolved AS", "unit_issue AS", "resolved AS"} {
				if strings.Contains(statement, cte) {
					t.Errorf("%s / %s: statement still defines the %q CTE of the multi-reference form", statementName, name, cte)
				}
			}
		}
	}
}

// CHAOS-7271: each phased project-mix statement names work_unit_investments
// once and reads ONE wide column group: structural_evidence_json, the theme map
// or the subcategory map, never two of them. A phase that reads two brings back
// the whole-table statement this change split (the byte guard is
// chaos7257_project_theme_mix_read_bytes_integration_test.go).
func TestPhasedProjectMixStatementsReadOneWideColumnGroupEach(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	wide := []string{"structural_evidence_json", "theme_distribution_json", "subcategory_distribution_json"}
	for boundName, bound := range map[string]factTimeBound{
		"current axis": {},
		"range":        {active: true, hasStart: true, start: start, end: end},
	} {
		for name, c := range map[string]struct {
			statement string
			wide      string // the one wide column the phase reads; "" = none
		}{
			"scope":            {projectMixScopeStatement(bound), ""},
			"roll-up themes":   {projectRollupRepoThemesStatement(), "theme_distribution_json"},
			"roll-up bugfix":   {projectRollupRepoBugfixStatement(), "subcategory_distribution_json"},
			"roll-up evidence": {projectRollupEvidenceArmStatement(), "structural_evidence_json"},
			"native placement": {projectNativePlacementStatement(), "structural_evidence_json"},
			"native themes":    {projectNativeThemeValuesStatement(), "theme_distribution_json"},
			"native bugfix":    {projectNativeBugfixValuesStatement(), "subcategory_distribution_json"},
		} {
			if got := strings.Count(c.statement, "FROM work_unit_investments"); got != 1 {
				t.Errorf("%s / %s: reads work_unit_investments %d times, want 1", name, boundName, got)
			}
			for _, column := range wide {
				named := strings.Contains(c.statement, "argMax("+column) || strings.Contains(c.statement, "("+column+",")
				if named != (column == c.wide) {
					t.Errorf("%s / %s: reads %s = %v, want %v (one wide column group per phase)", name, boundName, column, named, column == c.wide)
				}
			}
			if name != "scope" && !strings.Contains(c.statement, "(work_unit_id, toUnixTimestamp64Milli(computed_at)) IN (") {
				t.Errorf("%s / %s: phase is not pinned to the exact (unit, version) pairs of the scope", name, boundName)
			}
			if strings.HasPrefix(name, "roll-up") && (strings.Contains(c.statement, "team_project_ownership") || !strings.Contains(c.statement, "{link_json:String}")) {
				t.Errorf("%s / %s: phase re-derives ownership instead of reading the link table the scope captured", name, boundName)
			}
		}
	}
}

// The mix statement reads each repository's earliest unit through a sentinel
// window; the unit listing, which pages only the requested window, must not.
func TestSpanSentinelIsInTheMixStatementAndNotTheUnitListing(t *testing.T) {
	t.Parallel()
	sentinel := "arrayConcat([" + spanSentinelWindow + "], "
	if !strings.Contains(repoMixStatement([]factTimeBound{{}}), sentinel) {
		t.Error("the mix statement lacks the span sentinel window")
	}
	if strings.Contains(investmentUnitsStatement(factTimeBound{}, false), sentinel) {
		t.Error("the unit listing carries the span sentinel window")
	}
	if got := strings.Count(repoMixStatement([]factTimeBound{{}, {}}), "work_unit_investments"); got != 1 {
		t.Errorf("mix statement names work_unit_investments %d times with the sentinel, want 1", got)
	}
}

// The project scope statement reports the organization's earliest unit over
// ALL latest units, and filters to the window only inside the aggregates, so a
// window with no overlapping unit still reports the span.
func TestProjectScopeStatementReportsTheSpanBeyondTheWindowFilter(t *testing.T) {
	t.Parallel()
	bound := factTimeBound{active: true, hasStart: true, start: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), end: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	statement := projectMixScopeStatement(bound)
	for _, want := range []string{"groupArrayIf(work_unit_id, in_window)", "min(span_from) AS span_from", "min(from_ts) OVER () AS span_from"} {
		if !strings.Contains(statement, want) {
			t.Errorf("project scope statement lacks %q", want)
		}
	}
	if strings.Contains(statement, "\nWHERE 1") {
		t.Error("project scope statement filters rows to the window before the span aggregate")
	}
	if got := strings.Count(statement, "work_unit_investments"); got != 1 {
		t.Errorf("project scope statement names work_unit_investments %d times, want 1", got)
	}
}
