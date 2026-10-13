package devhealthsource_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

func TestProbeTitleMemoryVariants(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range append(devhealthschema.DDL(), workGraphEdgesDDL) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	seedWorkGraphLabelRows(t, ctx, direct)
	for _, repo := range []string{wgOther, wgRepo} {
		if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) SELECT concat('CHAOS-', toString(1000000 + number)), toUUID('%s'), '%s', 'filler', 'open', '', '', 'linear', '', now64(3), now64(3) FROM numbers(50000)`, repo, wgOrg)); err != nil {
			t.Fatalf("filler: %v", err)
		}
	}
	var s string
	for _, q := range contextpacket.SourceQueryCatalogV1 {
		if q.ID == "work_graph.v1" {
			s = q.Statement
		}
	}
	i := strings.Index(s, "LEFT JOIN (SELECT w.work_item_id AS id")
	j := strings.Index(s, ") AS wis ON")
	k := strings.Index(s, " WHERE e.org_id")
	if i < 0 || j < 0 || k < 0 {
		t.Fatalf("statement shape changed: %d %d %d", i, j, k)
	}
	ts := s[i+len("LEFT JOIN (") : j]
	ea := strings.Index(ts, "w.work_item_id IN ") + len("w.work_item_id IN ")
	eb := strings.Index(ts, " AND (w.repo_id = {repo_id:UUID}")
	endpoints := ts[ea:eb]
	ts6 := "SELECT work_item_id AS id, if(uniqExact(t) = 1, any(t), '') AS title FROM (SELECT repo_id, work_item_id, argMax(title, last_synced) AS t FROM work_items WHERE org_id = {org_id:String} AND repo_id IN ({repo_id:UUID}, toUUID('00000000-0000-0000-0000-000000000000')) AND work_item_id IN " + endpoints + " AND (repo_id = {repo_id:UUID} OR work_item_id IN {readable_repositoryless_issue_ids:Array(String)}) GROUP BY repo_id, work_item_id HAVING lengthUTF8(t) BETWEEN 1 AND 1000) GROUP BY work_item_id"
	noJoin := func(x string) string {
		return strings.ReplaceAll(strings.ReplaceAll(x, "wis.title", "issue_titles[e.source_id]"), "wit.title", "issue_titles[e.target_id]")
	}
	withMap := func(sub string) string {
		body := noJoin(s[:i] + s[k:])
		return strings.Replace(body, " SELECT concat('acr:v1:", " WITH (SELECT mapFromArrays(groupArray(id), groupArray(title)) FROM ("+sub+")) AS issue_titles SELECT concat('acr:v1:", 1)
	}
	asJoin := func(sub string) string {
		return s[:i] + "LEFT JOIN (" + sub + ") AS wis ON wis.id = e.source_id LEFT JOIN (" + sub + ") AS wit ON wit.id = e.target_id" + s[k:]
	}
	variants := map[string]string{
		"t0_current":      s,
		"t1_no_titles":    strings.ReplaceAll(strings.ReplaceAll(s[:i]+s[k:], "wis.title", "''"), "wit.title", "''"),
		"t4_scalar_map":   withMap(ts),
		"t6_argmax_joins": asJoin(ts6),
		"t7_argmax_map":   withMap(ts6),
	}
	plan := contextpacket.ReadPlan{OrgID: wgOrg, RepoID: wgRepo, RepoSlug: "acme/deploy-service", Branch: "main", TimeWindowDays: 30, WorkItemGrant: auth.WorkItemRepositorySelectorSet([]string{"acme/deploy-service"}, true), ReadableRepositorylessIssueIDs: []string{"PROJ-6", "PROJ-7"}}
	executor := contextpacket.NewClickHouseSourceExecutor(query)
	names := make([]string, 0, len(variants))
	for name := range variants {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		stmt := strings.TrimSuffix(strings.TrimSpace(variants[name]), ")") + " AND 'tag_" + name + "' != '' )"
		for r := 0; r < 2; r++ {
			if _, err := executor.QueryEvidence(ctx, contextpacket.SourceQuery{ID: name, Statement: stmt}, plan.Bindings()); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
	}
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatal(err)
	}
	var database string
	if err := direct.QueryRow(ctx, `SELECT currentDatabase()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	var report strings.Builder
	for _, name := range names {
		var mem, rr, rb, n uint64
		if err := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_rows), max(read_bytes), count() FROM system.query_log WHERE type='QueryFinish' AND current_database = ? AND position(query, ?) > 0`, database, "tag_"+name).Scan(&mem, &rr, &rb, &n); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&report, "\n%s: mem %d readRows %d readBytes %d n=%d", name, mem, rr, rb, n)
	}
	t.Errorf("PROBE (throwaway):%s", report.String())
}
