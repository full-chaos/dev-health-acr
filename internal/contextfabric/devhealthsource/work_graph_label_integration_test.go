package devhealthsource_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

const workGraphEdgesDDL = `CREATE TABLE work_graph_edges (edge_id String, source_type String, source_id String, target_type String, target_id String, edge_type String, repo_id UUID, provider String, provenance String, confidence Float64, evidence Nullable(String), discovered_at DateTime64(3), last_synced DateTime64(3), org_id String) ENGINE = ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id, edge_id)`

const (
	wgOrg      = "10000000-0000-4000-8000-000000000001"
	wgRepo     = "20000000-0000-4000-8000-000000000002"
	wgOther    = "30000000-0000-4000-8000-000000000003"
	wgDeployID = "40000000-0000-4000-8000-000000000004"
	wgProject  = "60000000-0000-4000-8000-000000000006"
	wgNilRepo  = "00000000-0000-0000-0000-000000000000"
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
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "CHAOS-77", wgRepo, wgOrg, "Gate deploy stages on smoke tests", "open", "", "", "linear", "", now, now)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "CHAOS-78", wgRepo, wgOrg, "Gate deploy stages on smoke tests", "open", "", "", "linear", "", now, now)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "CHAOS-77", wgOther, wgOrg, "Private title of a colliding key", "open", "", "", "linear", "", now, now)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "CHAOS-99", wgOther, wgOrg, "Unlinked hidden title", "open", "", "", "linear", "", now, now)
	edge := func(id, srcType, src, tgtType, tgt, edgeType, evidence string) {
		exec(`INSERT INTO work_graph_edges (edge_id, source_type, source_id, target_type, target_id, edge_type, repo_id, provider, provenance, confidence, evidence, discovered_at, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, srcType, src, tgtType, tgt, edgeType, wgRepo, "linear", "native", 0.9, evidence, now, now, wgOrg)
	}
	edge("e-pr-issue", "pr", wgRepo+"#pr311", "issue", "CHAOS-77", "implements", "linear_attachment")
	edge("e-pr-issue-unique", "pr", wgRepo+"#pr311", "issue", "CHAOS-78", "implements", "")
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "PROJ-5", "00000000-0000-0000-0000-000000000000", wgOrg, "Jira issue without a repository", "open", "", "", "jira", "NOPROJ", now, now)
	edge("e-pr-jira-issue", "pr", wgRepo+"#pr311", "issue", "PROJ-5", "implements", "")
	workItem := func(id, repo, title, provider, project string) {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, repo, wgOrg, title, "open", "", "", provider, project, now, now)
	}
	// A key with two distinct readable titles, one per readable repository bucket.
	workItem("CHAOS-81", wgRepo, "Title in the scoped repository", "linear", "")
	workItem("CHAOS-81", wgNilRepo, "Title of an issue with no repository", "linear", "")
	edge("e-collide", "pr", wgRepo+"#pr311", "issue", "CHAOS-81", "implements", "")
	// A repository-less issue reachable only through a native pull request link.
	workItem("PROJ-6", wgNilRepo, "Jira issue linked by a native pull request", "jira", "")
	exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, wgRepo, "PROJ-6", uint32(311), float32(1), "native", "", now, wgOrg)
	edge("e-pr-linked-jira", "pr", wgRepo+"#pr311", "issue", "PROJ-6", "implements", "")
	// A repository-less issue whose project is owned by a team that owns the scoped repository.
	workItem("PROJ-7", wgNilRepo, "Jira issue in an owned project", "jira", wgProject)
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, 'jira', 'OWN', 'owned', 1, 'started', '', ?)`, wgProject, wgOrg, now)
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?, 'jira', 'team-own', ?, 'OWN', 'native', ?, NULL, ?)`, wgOrg, wgProject, now.Add(-48*time.Hour), now)
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?, 'github', 'team-own', ?, 'acme/deploy-service', 'exact', 'native', 1, 100, 0, ?, NULL, ?)`, wgOrg, wgRepo, now.Add(-48*time.Hour), now)
	edge("e-owned-project-jira", "pr", wgRepo+"#pr311", "issue", "PROJ-7", "implements", "")
	edge("e-foreign-pr", "pr", wgOther+"#pr5", "pr", wgRepo+"#pr311", "relates_to", "")
	edge("e-commit-issue", "commit", "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", "issue", "CHAOS-99", "implements", "")
	edge("e-deploy", "pr", wgRepo+"#pr311", "deployment", wgDeployID, "deployed_as", "")
}

func workGraphLabelsFor(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, grant []string) map[string]string {
	t.Helper()
	plan := contextpacket.ReadPlan{OrgID: wgOrg, RepoID: wgRepo, RepoSlug: "acme/deploy-service", Branch: "main", TimeWindowDays: 30, WorkItemGrant: auth.WorkItemRepositorySelectorSet(grant, true)}
	result, err := contextpacket.ExecuteCatalog(ctx, contextpacket.NewClickHouseSourceExecutor(query), plan)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	for _, unavailable := range result.Unavailable {
		if unavailable.Source == "work_graph.v1" {
			t.Fatalf("work_graph.v1 unavailable: %s", unavailable.Reason)
		}
	}
	labels := map[string]string{}
	for _, ref := range result.Evidence {
		if ref.Source.EntityType != "work_graph_edge" {
			continue
		}
		labels[ref.Source.EntityID] = ref.Source.DisplayLabel
		for _, leak := range []string{"secret/private-repo", "Private roadmap", "Unlinked hidden title", "Private title of a colliding key", wgRepo, wgOther} {
			if strings.Contains(ref.Source.DisplayLabel+" "+ref.Citation, leak) {
				t.Errorf("edge %s serves %q: %q / %q", ref.Source.EntityID, leak, ref.Source.DisplayLabel, ref.Citation)
			}
		}
	}
	return labels
}

func TestWorkGraphLabelsServeIssueTitlesOnlyWhenReadable(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range append(devhealthschema.DDL(), workGraphEdgesDDL) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	seedWorkGraphLabelRows(t, ctx, direct)
	const pr = "acme/deploy-service#311 Rework deploy pipeline stages "
	for name, tc := range map[string]struct {
		grant []string
		want  map[string]string
	}{
		"organization-wide caller": {nil, map[string]string{
			"e-pr-issue":           pr + "implements CHAOS-77 Gate deploy stages on smoke tests",
			"e-pr-issue-unique":    pr + "implements CHAOS-78 Gate deploy stages on smoke tests",
			"e-collide":            pr + "implements CHAOS-81",
			"e-pr-jira-issue":      pr + "implements PROJ-5 Jira issue without a repository",
			"e-pr-linked-jira":     pr + "implements PROJ-6 Jira issue linked by a native pull request",
			"e-owned-project-jira": pr + "implements PROJ-7 Jira issue in an owned project",
			"e-commit-issue":       "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2 implements CHAOS-99",
			"e-foreign-pr":         "pull request #5 relates to " + pr[:len(pr)-1],
			"e-deploy":             pr + "deployed as deployment 40000000",
		}},
		"caller granted the scoped repository only": {[]string{"acme/deploy-service"}, map[string]string{
			"e-pr-issue":           pr + "implements CHAOS-77 Gate deploy stages on smoke tests",
			"e-pr-issue-unique":    pr + "implements CHAOS-78 Gate deploy stages on smoke tests",
			"e-collide":            pr + "implements CHAOS-81 Title in the scoped repository",
			"e-pr-jira-issue":      pr + "implements PROJ-5",
			"e-pr-linked-jira":     pr + "implements PROJ-6 Jira issue linked by a native pull request",
			"e-owned-project-jira": pr + "implements PROJ-7 Jira issue in an owned project",
			"e-commit-issue":       "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2 implements CHAOS-99",
		}},
	} {
		labels := workGraphLabelsFor(t, ctx, query, tc.grant)
		for id, want := range tc.want {
			got, ok := labels[id]
			if !ok {
				t.Fatalf("%s: edge %s not served: %v", name, id, labels)
			}
			if want += " (repository-wide)"; got != want {
				t.Errorf("%s: edge %s label = %q, want %q", name, id, got, want)
			}
		}
	}
}

type workGraphCost struct{ memory, readBytes, freshnessMemory, freshnessReadBytes, gateMemory, gateReadBytes uint64 }

// workGraphStatementCost seeds one isolated database with the label rows plus
// the requested extra rows, runs work_graph.v1 for a restricted caller (the
// widest authorization relation) and returns the statement's peak cost.
func workGraphStatementCost(t *testing.T, fillerPerRepo int, noise bool) workGraphCost {
	t.Helper()
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range append(devhealthschema.DDL(), workGraphEdgesDDL) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	seedWorkGraphLabelRows(t, ctx, direct)
	// Filler work items share the repository of the looked-up issues and
	// interleave with their keys.
	for _, repo := range []string{wgOther, wgRepo} {
		if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) SELECT concat('CHAOS-', toString(1000000 + number)), toUUID('%s'), '%s', 'filler', 'open', '', '', 'linear', '', now64(3), now64(3) FROM numbers(%d)`, repo, wgOrg, fillerPerRepo)); err != nil {
			t.Fatalf("seed filler work items: %v", err)
		}
	}
	if noise {
		if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO work_graph_edges SELECT concat('noise-', toString(number)), 'pr', concat(toString(generateUUIDv4()), '#pr', toString(number)), 'issue', concat('NOISE-', toString(number)), 'implements', toUUID('%s'), 'linear', 'native', 0.9, 'linear_attachment', now64(3), now64(3), '%s' FROM numbers(20000)`, wgOther, wgOrg)); err != nil {
			t.Fatalf("seed noise edges: %v", err)
		}
		if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) SELECT toUUID('%s'), '%s', toUInt32(number + 1000), concat('noise ', toString(number)), 'open', now64(3) FROM numbers(20000)`, wgOther, wgOrg)); err != nil {
			t.Fatalf("seed noise pull requests: %v", err)
		}
	}
	plan := contextpacket.ReadPlan{OrgID: wgOrg, RepoID: wgRepo, RepoSlug: "acme/deploy-service", Branch: "main", TimeWindowDays: 30, WorkItemGrant: auth.WorkItemRepositorySelectorSet([]string{"acme/deploy-service"}, true)}
	executor := contextpacket.NewClickHouseSourceExecutor(query)
	// The first run loads the table indexes into the server, which the first
	// statement to touch a table is charged for; only the three later runs are measured, and each statement is
	// scored by its smallest peak so scheduler jitter on a few MB does not read as a slope.
	var mark int64
	for i := 0; i < 4; i++ {
		if i == 1 {
			mark = time.Now().UnixMicro()
		}
		result, err := contextpacket.ExecuteCatalog(ctx, executor, plan)
		if err != nil {
			t.Fatalf("catalog: %v", err)
		}
		for _, unavailable := range result.Unavailable {
			if unavailable.Source == "work_graph.v1" {
				t.Fatalf("work_graph.v1 unavailable: %s", unavailable.Reason)
			}
		}
	}
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (measurement did not happen): %v", err)
	}
	var database string
	if err := direct.QueryRow(ctx, `SELECT currentDatabase()`).Scan(&database); err != nil {
		t.Fatalf("current database: %v", err)
	}
	var c workGraphCost
	var statements uint64
	row := direct.QueryRow(ctx, `SELECT min(memory_usage), min(read_bytes), count() FROM system.query_log WHERE type = 'QueryFinish' AND query_kind = 'Select' AND current_database = ? AND event_time_microseconds > fromUnixTimestamp64Micro(?) AND position(query, 'work_graph_edges') > 0 AND position(query, 'AS pt ON') > 0 AND position(query, 'SELECT max(observed_at), count() FROM (') = 0`, database, mark)
	if err := row.Scan(&c.memory, &c.readBytes, &statements); err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	if statements < 3 || c.memory == 0 || c.readBytes == 0 {
		t.Fatalf("measurement did not happen: %d statements, %+v", statements, c)
	}
	var freshness uint64
	row = direct.QueryRow(ctx, `SELECT min(memory_usage), min(read_bytes), count() FROM system.query_log WHERE type = 'QueryFinish' AND query_kind = 'Select' AND current_database = ? AND event_time_microseconds > fromUnixTimestamp64Micro(?) AND position(query, 'work_graph_edges') > 0 AND position(query, 'AS pt ON') > 0 AND position(query, 'SELECT max(observed_at), count() FROM (') > 0`, database, mark)
	if err := row.Scan(&c.freshnessMemory, &c.freshnessReadBytes, &freshness); err != nil {
		t.Fatalf("read query_log for the freshness read: %v", err)
	}
	if freshness < 3 || c.freshnessMemory == 0 {
		t.Fatalf("freshness measurement did not happen: %d statements, %+v", freshness, c)
	}
	var gates uint64
	row = direct.QueryRow(ctx, `SELECT min(memory_usage), min(read_bytes), count() FROM system.query_log WHERE type = 'QueryFinish' AND query_kind = 'Select' AND current_database = ? AND event_time_microseconds > fromUnixTimestamp64Micro(?) AND position(query, 'wia_link_auth') > 0 AND position(query, 'GROUP BY w.work_item_id') > 0`, database, mark)
	if err := row.Scan(&c.gateMemory, &c.gateReadBytes, &gates); err != nil {
		t.Fatalf("read query_log for the gate: %v", err)
	}
	if gates < 3 || c.gateMemory == 0 || c.gateReadBytes == 0 {
		t.Fatalf("gate measurement did not happen: %d statements, %+v", gates, c)
	}
	return c
}

// Three stages, each in its own database: the base, the base plus 150k other
// work items only, and the base plus 20k other edges and 20k other pull
// requests only. Neither addition may move the statement's read volume or
// memory past the 1.15x slope.
func TestWorkGraphLabelStatementCostIgnoresOtherWorkItemsAndOtherEdges(t *testing.T) {
	const ceiling = uint64(56 << 20)
	const statementCeiling = uint64(12 << 20)
	base := workGraphStatementCost(t, 50000, false)
	filler := workGraphStatementCost(t, 125000, false)
	noise := workGraphStatementCost(t, 50000, true)
	t.Logf("work_graph.v1 cost: base %+v, +150k work items %+v, +20k edges and pull requests %+v", base, filler, noise)
	t.Errorf("NUMBERS (throwaway): base %+v | +150k work items %+v | +20k edges and pull requests %+v", base, filler, noise)
	for name, stage := range map[string]workGraphCost{"base": base, "+work items": filler, "+edges and pull requests": noise} {
		if stage.memory >= ceiling || stage.gateMemory >= ceiling {
			t.Errorf("%s: work_graph.v1 peaked at %d bytes and its issue gate at %d, want under %d", name, stage.memory, stage.gateMemory, ceiling)
		}
		// The statement alone peaks near 2.5 MB without titles and 3.5 MB with
		// them; a title join per endpoint held 38 MB.
		if stage.memory >= statementCeiling || stage.freshnessMemory >= statementCeiling {
			t.Errorf("%s: work_graph.v1 peaked at %d bytes (freshness read %d), want under %d", name, stage.memory, stage.freshnessMemory, statementCeiling)
		}
	}
	// Slope: a stage is red when it exceeds 1.15x the base plus an absolute
	// floor (2 MiB of memory; 8 KiB of read volume, which only absorbs granule
	// and mark rounding on figures of a few KiB). Each figure is the smallest of
	// three warm runs, so a regression that grows the cost lifts all three.
	const memoryFloor = float64(2 << 20)
	const readSlack = float64(8192)
	for name, stage := range map[string]workGraphCost{"+work items": filler, "+edges and pull requests": noise} {
		for what, pair := range map[string][2]uint64{
			"work_graph.v1 memory":  {stage.memory, base.memory},
			"freshness read memory": {stage.freshnessMemory, base.freshnessMemory},
			"issue gate memory":     {stage.gateMemory, base.gateMemory},
		} {
			if float64(pair[0]) > 1.15*float64(pair[1])+memoryFloor {
				t.Errorf("%s: %s %d bytes against %d at the base, over the 1.15x slope plus the 2 MiB floor", name, what, pair[0], pair[1])
			}
		}
		for what, pair := range map[string][2]uint64{
			"work_graph.v1 read volume": {stage.readBytes, base.readBytes},
			"freshness read volume":     {stage.freshnessReadBytes, base.freshnessReadBytes},
			"issue gate read volume":    {stage.gateReadBytes, base.gateReadBytes},
		} {
			if float64(pair[0]) > 1.15*float64(pair[1])+readSlack {
				t.Errorf("%s: %s %d bytes against %d at the base, over the 1.15x slope", name, what, pair[0], pair[1])
			}
		}
	}
}
