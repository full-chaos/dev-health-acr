package devhealthschema_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

const (
	workItemMetricsDailyOld = "ReplacingMergeTree(computed_at) PARTITION BY toYYYYMM(day) ORDER BY (org_id, provider, day, work_scope_id, team_id) SETTINGS index_granularity = 8192"
	workItemMetricsDailyNew = "ReplacingMergeTree(computed_at) PARTITION BY toYYYYMM(day) PRIMARY KEY (org_id, provider, day, work_scope_id, team_id) ORDER BY (org_id, provider, day, work_scope_id, team_id, row_view) SETTINGS index_granularity = 8192"
)

// CHAOS-10668: the live-schema-parity job reads ops main, which gains row_view
// at the end of the work_item_metrics_daily sorting key. The old and the new
// string must both pass and nothing else may: a set that accepts anything is
// not a guard.
func TestEngineFullMatchesLiveAcceptsExactlyTheOldAndTheNewWorkItemMetricsDailyString(t *testing.T) {
	const table = "work_item_metrics_daily"
	if devhealthschema.EngineFull[table] != workItemMetricsDailyOld {
		t.Fatalf("the canonical string moved: %q", devhealthschema.EngineFull[table])
	}
	for name, live := range map[string]string{"old": workItemMetricsDailyOld, "new": workItemMetricsDailyNew} {
		if !devhealthschema.EngineFullMatchesLive(table, live) {
			t.Errorf("the %s string is refused: %q", name, live)
		}
	}
	refused := map[string]string{
		"columns swapped":          strings.Replace(workItemMetricsDailyNew, "work_scope_id, team_id, row_view", "team_id, work_scope_id, row_view", 1),
		"view column first":        strings.Replace(workItemMetricsDailyNew, "ORDER BY (org_id,", "ORDER BY (row_view, org_id,", 1),
		"another extra column":     strings.Replace(workItemMetricsDailyNew, "team_id, row_view)", "team_id, row_view, status)", 1),
		"new key without primary":  strings.Replace(workItemMetricsDailyNew, "PRIMARY KEY (org_id, provider, day, work_scope_id, team_id) ", "", 1),
		"new key, other primary":   strings.Replace(workItemMetricsDailyNew, "PRIMARY KEY (org_id, provider, day, work_scope_id, team_id)", "PRIMARY KEY (org_id, provider, day)", 1),
		"old key, other settings":  strings.Replace(workItemMetricsDailyOld, "index_granularity = 8192", "index_granularity = 4096", 1),
		"new key, other engine":    strings.Replace(workItemMetricsDailyNew, "ReplacingMergeTree(computed_at)", "ReplacingMergeTree", 1),
		"new key, other partition": strings.Replace(workItemMetricsDailyNew, "toYYYYMM(day)", "toYear(day)", 1),
		"empty":                    "",
	}
	for name, live := range refused {
		if live == workItemMetricsDailyNew || live == workItemMetricsDailyOld {
			t.Fatalf("case %q did not change the string: the case proves nothing", name)
		}
		if devhealthschema.EngineFullMatchesLive(table, live) {
			t.Errorf("a wrong string is accepted (%s): %q", name, live)
		}
	}
}

func TestEngineFullMatchesLiveOfATableWithNoAlternateIsExact(t *testing.T) {
	const table = "work_items"
	if len(devhealthschema.EngineFullAlternates(table)) != 0 {
		t.Fatalf("the test table has alternates now: pick another")
	}
	canonical := devhealthschema.EngineFull[table]
	if !devhealthschema.EngineFullMatchesLive(table, canonical) {
		t.Errorf("the canonical string of %s is refused", table)
	}
	if devhealthschema.EngineFullMatchesLive(table, canonical+" ") {
		t.Errorf("a changed string of %s is accepted", table)
	}
	if devhealthschema.EngineFullMatchesLive("no_such_table", workItemMetricsDailyOld) {
		t.Error("a table that EngineFull does not declare matched")
	}
}

func TestEngineFullAlternatesExistOnlyForDeclaredTablesAndDifferFromTheCanonicalString(t *testing.T) {
	for table := range devhealthschema.ProductionColumns {
		canonical, declared := devhealthschema.EngineFull[table]
		alternates := devhealthschema.EngineFullAlternates(table)
		if !declared && len(alternates) > 0 {
			t.Errorf("%s has alternates and no canonical string", table)
		}
		seen := map[string]bool{canonical: true}
		for _, alternate := range alternates {
			if seen[alternate] {
				t.Errorf("%s: an alternate equals the canonical string or another alternate: %q", table, alternate)
			}
			seen[alternate] = true
		}
	}
	if len(devhealthschema.EngineFullAlternates("work_item_metrics_daily")) != 1 {
		t.Error("work_item_metrics_daily must have exactly the one new-key alternate")
	}
	if devhealthschema.EngineFullAlternates("no_such_table") != nil {
		t.Error("an unknown table has alternates")
	}
}
