package devhealthsource_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

func TestProbeTitleGateMemory(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range append(devhealthschema.DDL(), workGraphEdgesDDL) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	seedWorkGraphLabelRows(t, ctx, direct)
	scope := readers.WorkItemScopeSQL(readers.AuthorizationScope{RepositorySelectors: &readers.RepositorySelectorScope{}})
	titles := `SELECT w.work_item_id AS id, if(uniqExactIf(w.title, (` + scope.AuthorizationExpr + `)) = 1, anyIf(w.title, (` + scope.AuthorizationExpr + `)), '') AS title FROM work_items AS w FINAL ` + scope.JoinSQL + ` WHERE w.org_id = {org_id:String} AND w.repo_id IN ({repo_id:UUID}, toUUID('00000000-0000-0000-0000-000000000000')) AND w.work_item_id IN (SELECT source_id FROM work_graph_edges WHERE org_id = {org_id:String} AND repo_id = {repo_id:UUID} AND source_type = 'issue' UNION ALL SELECT target_id FROM work_graph_edges WHERE org_id = {org_id:String} AND repo_id = {repo_id:UUID} AND target_type = 'issue') GROUP BY w.work_item_id`
	plan := contextpacket.ReadPlan{OrgID: wgOrg, RepoID: wgRepo, RepoSlug: "acme/deploy-service", WorkItemGrant: auth.WorkItemRepositorySelectorSet([]string{"acme/deploy-service"}, true)}
	_ = query
	grant := plan.WorkItemGrant
	literal := func(statement string) string {
		slugs := "['acme/deploy-service']"
		_ = grant
		return strings.NewReplacer("{org_id:String}", "'"+wgOrg+"'", "{repo_id:UUID}", "toUUID('"+wgRepo+"')", "{authorized_repo_all:UInt8}", "0", "{authorized_repo_slugs:Array(String)}", slugs, "{authorized_repo_owners:Array(String)}", "[]").Replace(statement)
	}
	run := func(label, statement string) {
		for i := 0; i < 2; i++ {
			rows, err := direct.Query(ctx, literal(statement))
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			for rows.Next() {
			}
			rows.Close()
		}
	}
	variants := map[string]string{
		"p_titles_alone":           "SELECT * FROM (" + titles + ") /* p_titles_alone */",
		"p_titles_one_thread":      "SELECT * FROM (" + titles + ") SETTINGS max_threads = 1 /* p_titles_one_thread */",
		"p_titles_no_join_nilrepo": "SELECT w.work_item_id, any(w.title) FROM work_items AS w FINAL WHERE w.org_id = {org_id:String} AND w.repo_id = {repo_id:UUID} AND w.work_item_id IN (SELECT source_id FROM work_graph_edges WHERE org_id = {org_id:String} AND repo_id = {repo_id:UUID}) GROUP BY w.work_item_id /* p_scoped_only */",
	}
	for label, statement := range variants {
		run(label, statement)
	}
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush: %v", err)
	}
	var database string
	if err := direct.QueryRow(ctx, `SELECT currentDatabase()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	var report strings.Builder
	for _, marker := range []string{"p_titles_alone", "p_titles_one_thread", "p_scoped_only"} {
		var mem, rb, rr uint64
		var n uint64
		if err := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_bytes), max(read_rows), count() FROM system.query_log WHERE type='QueryFinish' AND current_database = ? AND position(query, ?) > 0 AND position(query, 'system.query_log') = 0`, database, marker).Scan(&mem, &rb, &rr, &n); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&report, "\n%s: mem %d readBytes %d readRows %d n=%d", marker, mem, rb, rr, n)
	}
	t.Errorf("PROBE (throwaway):%s", report.String())
}
