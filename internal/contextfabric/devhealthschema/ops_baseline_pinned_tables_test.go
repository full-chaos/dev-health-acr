package devhealthschema_test

import (
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

// The tables acr reads in production that ops creates in its baseline. The
// declaration for each must equal the CREATE text cmd/pinopsbaseline copied
// from ops baseline/head.json, so a hand edit or a stale regen both fail.
var opsBaselinePinned = []string{
	"ai_workflow_artifact_edges",
	"ai_workflow_runs",
	"git_commit_stats",
	"git_commits",
	"work_graph_edges",
	"work_graph_pr_review_outcome_edges",
}

func TestOpsBaselinePinnedTablesMatchTheDeclaration(t *testing.T) {
	raw, err := os.ReadFile("testdata/ops_baseline_pinned_tables.sql")
	if err != nil {
		t.Fatal(err)
	}
	parsed := map[string]devhealthschema.OpsCreate{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		create, err := devhealthschema.ParseOpsCreate(line)
		if err != nil {
			t.Fatal(err)
		}
		parsed[create.Table] = create
	}
	if len(parsed) != len(opsBaselinePinned) {
		t.Fatalf("testdata holds %d tables, want %d", len(parsed), len(opsBaselinePinned))
	}
	for _, table := range opsBaselinePinned {
		want, ok := parsed[table]
		if !ok {
			t.Errorf("testdata lacks %s", table)
			continue
		}
		got := devhealthschema.ProductionColumns[table]
		if len(got) != len(want.Columns) {
			t.Errorf("%s: declaration has %d columns, ops baseline has %d", table, len(got), len(want.Columns))
			continue
		}
		for i, column := range want.Columns {
			if got[i].Name != column.Name || got[i].Type != column.Type {
				t.Errorf("%s column %d: declaration %s %q, ops baseline %s %q", table, i, got[i].Name, got[i].Type, column.Name, column.Type)
			}
		}
		if engine := devhealthschema.EngineFull[table]; engine != want.Engine {
			t.Errorf("%s: EngineFull %q, ops baseline %q", table, engine, want.Engine)
		}
	}
}

// The view's output columns, in order, are the aliases of the first SELECT
// arm after the CTEs. ViewColumnNames is what the live freshness test
// compares with system.columns.
func TestProjectMembershipPresenceViewReadsOnlyDeclaredBaseColumns(t *testing.T) {
	names := devhealthschema.ProjectMembershipPresenceViewColumnNames()
	want := []string{"org_id", "subject_kind", "repo_id", "subject_id", "provider", "project_id", "project_key", "observed_at", "last_synced", "source"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("view columns = %v, want %v", names, want)
	}
	for _, table := range []string{"project_membership_transitions", "work_items"} {
		if _, ok := devhealthschema.ProductionColumns[table]; !ok {
			t.Fatalf("%s is not declared", table)
		}
	}
	for table, columns := range map[string][]string{
		"project_membership_transitions": {"org_id", "subject_kind", "repo_id", "subject_id", "provider", "occurred_at", "ingested_at", "event_id", "to_project_id", "to_project_key", "from_project_id", "from_project_key"},
		"work_items":                     {"org_id", "repo_id", "work_item_id", "provider", "project_id", "project_key", "updated_at", "ingested_at"},
	} {
		declared := map[string]bool{}
		for _, c := range devhealthschema.ProductionColumns[table] {
			declared[c.Name] = true
		}
		for _, column := range columns {
			if !declared[column] {
				t.Errorf("the view reads %s.%s but ProductionColumns does not declare it", table, column)
			}
		}
	}
}
