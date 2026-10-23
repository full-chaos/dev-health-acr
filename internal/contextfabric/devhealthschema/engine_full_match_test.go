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

// Ops migration 141 appended row_view to the sorting key of work_item_metrics_daily and it is on prod. The live
// string is the new one; the old one and any other key are refused.
func TestEngineFullMatchesLiveAcceptsOnlyTheNewWorkItemMetricsDailyString(t *testing.T) {
	const table = "work_item_metrics_daily"
	if devhealthschema.EngineFull[table] != workItemMetricsDailyNew {
		t.Fatalf("the canonical string is not the new one: %q", devhealthschema.EngineFull[table])
	}
	if !devhealthschema.EngineFullMatchesLive(table, workItemMetricsDailyNew) {
		t.Errorf("the new string is refused")
	}
	refused := map[string]string{
		"the old string":           workItemMetricsDailyOld,
		"columns swapped":          strings.Replace(workItemMetricsDailyNew, "work_scope_id, team_id, row_view", "team_id, work_scope_id, row_view", 1),
		"view column first":        strings.Replace(workItemMetricsDailyNew, "ORDER BY (org_id,", "ORDER BY (row_view, org_id,", 1),
		"another extra column":     strings.Replace(workItemMetricsDailyNew, "team_id, row_view)", "team_id, row_view, status)", 1),
		"new key without primary":  strings.Replace(workItemMetricsDailyNew, "PRIMARY KEY (org_id, provider, day, work_scope_id, team_id) ", "", 1),
		"new key, other primary":   strings.Replace(workItemMetricsDailyNew, "PRIMARY KEY (org_id, provider, day, work_scope_id, team_id)", "PRIMARY KEY (org_id, provider, day)", 1),
		"new key, other settings":  strings.Replace(workItemMetricsDailyNew, "index_granularity = 8192", "index_granularity = 4096", 1),
		"new key, other engine":    strings.Replace(workItemMetricsDailyNew, "ReplacingMergeTree(computed_at)", "ReplacingMergeTree", 1),
		"new key, other partition": strings.Replace(workItemMetricsDailyNew, "toYYYYMM(day)", "toYear(day)", 1),
		"empty":                    "",
	}
	for name, live := range refused {
		if name != "the old string" && live == workItemMetricsDailyNew {
			t.Fatalf("case %q did not change the string: the case proves nothing", name)
		}
		if devhealthschema.EngineFullMatchesLive(table, live) {
			t.Errorf("a wrong string is accepted (%s): %q", name, live)
		}
	}
}

func TestEngineFullMatchesLiveOfAnotherTableIsExact(t *testing.T) {
	const table = "work_items"
	canonical := devhealthschema.EngineFull[table]
	if !devhealthschema.EngineFullMatchesLive(table, canonical) {
		t.Errorf("the canonical string of %s is refused", table)
	}
	if devhealthschema.EngineFullMatchesLive(table, canonical+" ") {
		t.Errorf("a changed string of %s is accepted", table)
	}
	if devhealthschema.EngineFullMatchesLive("no_such_table", workItemMetricsDailyNew) {
		t.Error("a table that EngineFull does not declare matched")
	}
}

// The declared columns hold row_view as the last column and the fixture DDL carries it in the sorting key.
func TestWorkItemMetricsDailyDeclaresRowViewAsTheLastSortingKeyColumn(t *testing.T) {
	const table = "work_item_metrics_daily"
	columns := devhealthschema.ProductionColumns[table]
	last := columns[len(columns)-1]
	if last.Name != "row_view" || last.Type != "LowCardinality(String)" {
		t.Fatalf("last declared column is %+v, want row_view LowCardinality(String)", last)
	}
	ddl := devhealthschema.DDL(table)[0]
	if !strings.Contains(ddl, "row_view LowCardinality(String)") || !strings.Contains(ddl, "team_id, row_view)") {
		t.Errorf("the fixture DDL does not carry row_view as a column and as the last key column: %s", ddl)
	}
}
