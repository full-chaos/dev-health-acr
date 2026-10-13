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

func TestProbeScalarVariants(t *testing.T) {
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
	a := strings.Index(s, "arrayElement(")
	b := strings.Index(s, ", e.source_id)")
	if a < 0 || b < 0 {
		t.Fatalf("statement shape changed")
	}
	m := s[a+len("arrayElement(") : b]
	elem := func(id string) string { return "arrayElement(" + m + ", " + id + ")" }
	u1 := strings.ReplaceAll(strings.ReplaceAll(s, elem("e.source_id"), "issue_titles[e.source_id]"), elem("e.target_id"), "issue_titles[e.target_id]")
	u1 = strings.Replace(u1, " SELECT concat('acr:v1:", " WITH "+m+" AS issue_titles SELECT concat('acr:v1:", 1)
	u3 := s
	for _, id := range []string{"e.source_id", "e.target_id"} {
		kind := strings.Replace(id, "_id", "_type", 1)
		old := kind + " = 'issue' AND " + elem(id) + " != '', concat(" + id + ", ' ', " + elem(id) + "), " + id + ")"
		if !strings.Contains(u3, old) {
			t.Fatalf("branch text not found for %s", id)
		}
		u3 = strings.Replace(u3, old, kind+" = 'issue', trimRight(concat("+id+", ' ', "+elem(id)+")), "+id+")", 1)
	}
	variants := map[string]string{"u1_with_alias": u1, "u2_inline4": s, "u3_inline2": u3}
	plan := contextpacket.ReadPlan{OrgID: wgOrg, RepoID: wgRepo, RepoSlug: "acme/deploy-service", Branch: "main", TimeWindowDays: 30, WorkItemGrant: auth.WorkItemRepositorySelectorSet([]string{"acme/deploy-service"}, true), ReadableRepositorylessIssueIDs: []string{"PROJ-6", "PROJ-7"}}
	executor := contextpacket.NewClickHouseSourceExecutor(query)
	names := []string{}
	for n := range variants {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		stmt := strings.TrimSuffix(strings.TrimSpace(variants[n]), ")") + " AND 'tag_" + n + "' != '' )"
		for r := 0; r < 2; r++ {
			if _, err := executor.QueryEvidence(ctx, contextpacket.SourceQuery{ID: n, Statement: stmt}, plan.Bindings()); err != nil {
				t.Fatalf("%s: %v", n, err)
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
	for _, n := range names {
		var mem, rr, rb, c uint64
		if err := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_rows), max(read_bytes), count() FROM system.query_log WHERE type='QueryFinish' AND current_database = ? AND position(query, ?) > 0`, database, "tag_"+n).Scan(&mem, &rr, &rb, &c); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&report, "\n%s: mem %d readRows %d readBytes %d n=%d", n, mem, rr, rb, c)
	}
	t.Errorf("PROBE (throwaway):%s", report.String())
}
