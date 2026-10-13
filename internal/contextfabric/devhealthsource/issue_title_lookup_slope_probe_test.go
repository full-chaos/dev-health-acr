package devhealthsource_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

const nilRepoUUID = "00000000-0000-0000-0000-000000000000"

func TestProbeIssueTitleLookupSlope(t *testing.T) {
	edgeIDs := func(repoBound bool) string {
		_ = repoBound
		return fmt.Sprintf(`(SELECT source_id FROM work_graph_edges FINAL WHERE org_id = '%[1]s' AND repo_id = '%[2]s' AND source_type = 'issue' UNION ALL SELECT target_id FROM work_graph_edges FINAL WHERE org_id = '%[1]s' AND repo_id = '%[2]s' AND target_type = 'issue')`, wgOrg, wgRepo)
	}
	shapes := map[string]string{
		"s1_repo_bound":  fmt.Sprintf(`SELECT work_item_id, uniqExact(title), any(title) FROM work_items FINAL WHERE org_id = '%s' AND repo_id IN ('%s', '%s') AND work_item_id IN %s GROUP BY work_item_id /* probe_s1 */`, wgOrg, wgRepo, nilRepoUUID, edgeIDs(true)),
		"s2_id_set_only": fmt.Sprintf(`SELECT work_item_id, uniqExact(title), any(title) FROM work_items FINAL WHERE org_id = '%s' AND work_item_id IN %s GROUP BY work_item_id /* probe_s2 */`, wgOrg, edgeIDs(false)),
	}
	type cost struct{ memory, readBytes, rows uint64 }
	stage := func(name string, fillerPerRepo int, noise bool) map[string]cost {
		ctx := context.Background()
		_, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
		for _, statement := range append(devhealthschema.DDL(), workGraphEdgesDDL) {
			if err := direct.Exec(ctx, statement); err != nil {
				t.Fatalf("create table: %v", err)
			}
		}
		seedWorkGraphLabelRows(t, ctx, direct)
		for _, repo := range []string{wgOther, wgRepo} {
			if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) SELECT concat('CHAOS-', toString(1000000 + number)), toUUID('%s'), '%s', 'filler', 'open', '', '', 'linear', '', now64(3), now64(3) FROM numbers(%d)`, repo, wgOrg, fillerPerRepo)); err != nil {
				t.Fatalf("filler: %v", err)
			}
		}
		if noise {
			if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO work_graph_edges SELECT concat('noise-', toString(number)), 'pr', concat(toString(generateUUIDv4()), '#pr', toString(number)), 'issue', concat('NOISE-', toString(number)), 'implements', toUUID('%s'), 'linear', 'native', 0.9, 'linear_attachment', now64(3), now64(3), '%s' FROM numbers(20000)`, wgOther, wgOrg)); err != nil {
				t.Fatalf("noise edges: %v", err)
			}
			if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) SELECT toUUID('%s'), '%s', toUInt32(number + 1000), concat('noise ', toString(number)), 'open', now64(3) FROM numbers(20000)`, wgOther, wgOrg)); err != nil {
				t.Fatalf("noise prs: %v", err)
			}
		}
		out := map[string]cost{}
		for shape, statement := range shapes {
			for i := 0; i < 2; i++ {
				rows, err := direct.Query(ctx, statement)
				if err != nil {
					t.Fatalf("%s %s: %v", name, shape, err)
				}
				for rows.Next() {
				}
				rows.Close()
			}
		}
		if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
			t.Fatalf("flush logs (measurement did not happen): %v", err)
		}
		var db string
		if err := direct.QueryRow(ctx, `SELECT currentDatabase()`).Scan(&db); err != nil {
			t.Fatalf("current database: %v", err)
		}
		for shape := range shapes {
			marker := "probe_" + strings.SplitN(shape, "_", 2)[0]
			var c cost
			var n uint64
			row := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_bytes), max(read_rows), count() FROM system.query_log WHERE type = 'QueryFinish' AND query_kind = 'Select' AND current_database = ? AND position(query, ?) > 0 AND position(query, 'system.query_log') = 0`, db, marker)
			if err := row.Scan(&c.memory, &c.readBytes, &c.rows, &n); err != nil {
				t.Fatalf("query_log: %v", err)
			}
			if n < 1 || c.memory == 0 {
				t.Fatalf("measurement did not happen: %s %s n=%d %+v", name, shape, n, c)
			}
			out[shape] = c
		}
		return out
	}
	base := stage("base", 125000, false)
	filler := stage("filler", 312500, false)
	noise := stage("noise", 125000, true)
	var report strings.Builder
	for shape := range shapes {
		fmt.Fprintf(&report, "\n%s: base %+v | +filler(250k->625k) %+v x%.2f mem x%.2f | +noise(20k edges+20k PRs) %+v x%.2f mem x%.2f", shape,
			base[shape], filler[shape], float64(filler[shape].readBytes)/float64(base[shape].readBytes), float64(filler[shape].memory)/float64(base[shape].memory),
			noise[shape], float64(noise[shape].readBytes)/float64(base[shape].readBytes), float64(noise[shape].memory)/float64(base[shape].memory))
	}
	t.Errorf("PROBE NUMBERS (throwaway, fails on purpose):%s", report.String())
}
