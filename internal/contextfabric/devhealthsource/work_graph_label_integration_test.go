package devhealthsource_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const workGraphEdgesDDL = `CREATE TABLE work_graph_edges (edge_id String, source_type String, source_id String, target_type String, target_id String, edge_type String, repo_id UUID, provider String, provenance String, confidence Float64, evidence Nullable(String), discovered_at DateTime64(3), last_synced DateTime64(3), org_id String) ENGINE = ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id, edge_id)`

const (
	wgOrg      = "10000000-0000-4000-8000-000000000001"
	wgRepo     = "20000000-0000-4000-8000-000000000002"
	wgOther    = "30000000-0000-4000-8000-000000000003"
	wgDeployID = "40000000-0000-4000-8000-000000000004"
)

func seedWorkGraphLabelRows(t *testing.T, ctx context.Context, direct interface {
	Exec(context.Context, string, ...any) error
}) {
	t.Helper()
	now := time.Now().UTC()
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, wgRepo, wgOrg, "acme/deploy-service", "github", now)
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, wgOther, wgOrg, "secret/private-repo", "github", now)
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, ?, ?, ?)`, wgRepo, wgOrg, uint32(311), "Rework deploy pipeline stages", "open", now)
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, ?, ?, ?)`, wgOther, wgOrg, uint32(5), "Private roadmap", "open", now)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "CHAOS-77", wgRepo, wgOrg, "Gate deploy stages on smoke tests", "Jira issue without a repository", "open", "", "", "linear", "", now, now)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "CHAOS-78", wgRepo, wgOrg, "Gate deploy stages on smoke tests", "open", "", "", "linear", "", now, now)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "CHAOS-77", wgOther, wgOrg, "Private title of a colliding key", "open", "", "", "linear", "", now, now)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "CHAOS-99", wgOther, wgOrg, "Unlinked hidden title", "open", "", "", "linear", "", now, now)
	edge := func(id, srcType, src, tgtType, tgt, edgeType, evidence string) {
		exec(`INSERT INTO work_graph_edges (edge_id, source_type, source_id, target_type, target_id, edge_type, repo_id, provider, provenance, confidence, evidence, discovered_at, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, srcType, src, tgtType, tgt, edgeType, wgRepo, "linear", "native", 0.9, evidence, now, now, wgOrg)
	}
	edge("e-pr-issue", "pr", wgRepo+"#pr311", "issue", "CHAOS-77", "implements", "linear_attachment")
	edge("e-pr-issue-unique", "pr", wgRepo+"#pr311", "issue", "CHAOS-78", "implements", "")
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "PROJ-5", "00000000-0000-0000-0000-000000000000", wgOrg, "Jira issue without a repository", "open", "", "", "jira", "", now, now)
	edge("e-pr-jira-issue", "pr", wgRepo+"#pr311", "issue", "PROJ-5", "implements", "")
	edge("e-foreign-pr", "pr", wgOther+"#pr5", "pr", wgRepo+"#pr311", "relates_to", "")
	edge("e-commit-issue", "commit", "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", "issue", "CHAOS-99", "implements", "")
	edge("e-deploy", "pr", wgRepo+"#pr311", "deployment", wgDeployID, "deployed_as", "")
}

func workGraphCatalogQuery(t *testing.T) contextpacket.SourceQuery {
	t.Helper()
	for _, source := range contextpacket.SourceQueryCatalogV1 {
		if source.ID == "work_graph.v1" {
			return source
		}
	}
	t.Fatal("work_graph.v1 is not in the catalog")
	return contextpacket.SourceQuery{}
}

func TestWorkGraphLabelsUseDisplayNamesAndWithholdUnreadableNames(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range append(devhealthschema.DDL(), workGraphEdgesDDL) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	seedWorkGraphLabelRows(t, ctx, direct)
	plan := contextpacket.ReadPlan{OrgID: wgOrg, RepoID: wgRepo, RepoSlug: "acme/deploy-service", Branch: "main", TimeWindowDays: 30}
	evidence, err := contextpacket.NewClickHouseSourceExecutor(query).QueryEvidence(ctx, workGraphCatalogQuery(t), plan.Bindings())
	if err != nil {
		t.Fatalf("work_graph.v1: %v", err)
	}
	byID := map[string]contractsv1.EvidenceRef{}
	for _, ref := range evidence {
		byID[ref.Source.EntityID] = ref
	}
	for id, want := range map[string]string{
		"e-pr-issue":        "acme/deploy-service#311 Rework deploy pipeline stages implements CHAOS-77",
		"e-pr-jira-issue":   "acme/deploy-service#311 Rework deploy pipeline stages implements PROJ-5",
		"e-pr-issue-unique": "acme/deploy-service#311 Rework deploy pipeline stages implements CHAOS-78",
		"e-foreign-pr":      "pull request #5 relates to acme/deploy-service#311 Rework deploy pipeline stages",
		"e-commit-issue":    "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2 implements CHAOS-99",
		"e-deploy":          "acme/deploy-service#311 Rework deploy pipeline stages deployed as deployment 40000000",
	} {
		ref, ok := byID[id]
		if !ok {
			t.Fatalf("edge %s not served: %v", id, byID)
		}
		if ref.Source.DisplayLabel != want {
			t.Errorf("edge %s label = %q, want %q", id, ref.Source.DisplayLabel, want)
		}
	}
	for id, ref := range byID {
		for _, leak := range []string{"secret/private-repo", "Private roadmap", "Unlinked hidden title", "Private title of a colliding key", "Gate deploy stages on smoke tests", wgRepo, wgOther} {
			if strings.Contains(ref.Source.DisplayLabel+" "+ref.Citation, leak) {
				t.Errorf("edge %s serves %q: %q / %q", id, leak, ref.Source.DisplayLabel, ref.Citation)
			}
		}
	}
}

func TestWorkGraphLabelStatementMemoryFollowsItsOwnEdges(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range append(devhealthschema.DDL(), workGraphEdgesDDL) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	seedWorkGraphLabelRows(t, ctx, direct)
	plan := contextpacket.ReadPlan{OrgID: wgOrg, RepoID: wgRepo, RepoSlug: "acme/deploy-service", Branch: "main", TimeWindowDays: 30}
	executor := contextpacket.NewClickHouseSourceExecutor(query)
	source := workGraphCatalogQuery(t)
	run := func() {
		if _, err := executor.QueryEvidence(ctx, source, plan.Bindings()); err != nil {
			t.Fatalf("work_graph.v1: %v", err)
		}
	}
	type cost struct{ memory, readBytes uint64 }
	measure := func(mark int64) cost {
		if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
			t.Fatalf("flush logs (measurement did not happen): %v", err)
		}
		var c cost
		var statements uint64
		row := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_bytes), uniqExact(normalized_query_hash) FROM system.query_log WHERE event_time_microseconds > fromUnixTimestamp64Micro(?) AND type = 'QueryFinish' AND query_kind = 'Select' AND position(query, 'work_graph_edges') > 0 AND position(query, 'AS ist ON') > 0`, mark)
		if err := row.Scan(&c.memory, &c.readBytes, &statements); err != nil {
			t.Fatalf("read query_log: %v", err)
		}
		if statements < 1 || c.memory == 0 || c.readBytes == 0 {
			t.Fatalf("measurement did not happen: %d statements, %+v", statements, c)
		}
		return c
	}
	// Filler work items share the repository of the looked-up issues and
	// interleave with their keys.
	filler := func(from, count int) {
		for _, repo := range []string{wgOther, wgRepo} {
			if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) SELECT concat('CHAOS-', toString(%d + number)), toUUID('%s'), '%s', 'filler', 'open', '', '', 'linear', '', now64(3), now64(3) FROM numbers(%d)`, from, repo, wgOrg, count)); err != nil {
				t.Fatalf("seed filler work items: %v", err)
			}
		}
	}
	measureAt := func() cost {
		run()
		mark := time.Now().UnixMicro()
		run()
		return measure(mark)
	}
	filler(1000000, 125000)
	small := measureAt()
	filler(2000000, 187500)
	if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO work_graph_edges SELECT concat('noise-', toString(number)), 'pr', concat(toString(generateUUIDv4()), '#pr', toString(number)), 'issue', concat('NOISE-', toString(number)), 'implements', toUUID('%s'), 'linear', 'native', 0.9, 'linear_attachment', now64(3), now64(3), '%s' FROM numbers(20000)`, wgOther, wgOrg)); err != nil {
		t.Fatalf("seed noise edges: %v", err)
	}
	if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) SELECT toUUID('%s'), '%s', toUInt32(number + 1000), concat('noise ', toString(number)), 'open', now64(3) FROM numbers(20000)`, wgOther, wgOrg)); err != nil {
		t.Fatalf("seed noise pull requests: %v", err)
	}
	large := measureAt()
	const ceiling = uint64(56 << 20)
	if large.memory >= ceiling || small.memory >= ceiling {
		t.Errorf("work_graph.v1 peaked at %d / %d bytes, want under %d", small.memory, large.memory, ceiling)
	}
	if float64(large.memory) > 1.15*float64(small.memory) {
		t.Errorf("work_graph.v1 memory grew with other work items: %d bytes at 625k, %d at 250k, over the 1.15x slope", large.memory, small.memory)
	}
	if float64(large.readBytes) > 1.15*float64(small.readBytes) {
		t.Errorf("work_graph.v1 read volume grew with other work items: %d bytes read at 625k, %d at 250k, over the 1.15x slope", large.readBytes, small.readBytes)
	}
}
