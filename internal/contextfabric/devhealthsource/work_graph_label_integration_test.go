package devhealthsource_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

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
	// A second readable title that is too long to display still collides.
	workItem("CHAOS-82", wgRepo, "Short title in the scoped repository", "linear", "")
	workItem("CHAOS-82", wgNilRepo, strings.Repeat("long ", 300), "linear", "")
	edge("e-collide-long", "pr", wgRepo+"#pr311", "issue", "CHAOS-82", "implements", "")
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
	// Not served: every writer gives a pull-request-sourced edge the repo_id of the
	// repository in its source id (ops issuepredges.go:116-147, prcommit/fastpath.go:49-61),
	// so this edge is outside the contract and the key-prefix scope does not read it.
	edge("e-foreign-pr", "pr", wgOther+"#pr5", "pr", wgRepo+"#pr311", "relates_to", "")
	// Not served: a commit-sourced edge carries the id "<repo_id>@<sha>" in every writer
	// (ops issuecommitedges.go:249-270, ids.go GenerateCommitID), so a bare-sha source id is
	// outside the contract and the key-prefix scope does not read it.
	edge("e-commit-issue", "commit", "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", "issue", "CHAOS-99", "implements", "")
	edge("e-commit-issue-writer", "commit", wgRepo+"@a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", "issue", "CHAOS-99", "implements", "")
	edge("e-deploy", "pr", wgRepo+"#pr311", "deployment", wgDeployID, "deployed_as", "")
}

func workGraphLabelsFor(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, grant []string) map[string]string {
	t.Helper()
	return workGraphLabelsForCommit(t, ctx, query, grant, "")
}

// workGraphLabelsForCommit is workGraphLabelsFor for a request that names one commit.
func workGraphLabelsForCommit(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, grant []string, commitSHA string) map[string]string {
	t.Helper()
	plan := contextpacket.ReadPlan{OrgID: wgOrg, RepoID: wgRepo, RepoSlug: "acme/deploy-service", Branch: "main", CommitSHA: commitSHA, TimeWindowDays: 30, WorkItemGrant: auth.WorkItemRepositorySelectorSet(grant, true)}
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
	for _, statement := range devhealthschema.DDL() {
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
			"e-pr-issue":            pr + "implements CHAOS-77 Gate deploy stages on smoke tests",
			"e-pr-issue-unique":     pr + "implements CHAOS-78 Gate deploy stages on smoke tests",
			"e-collide":             pr + "implements CHAOS-81",
			"e-collide-long":        pr + "implements CHAOS-82",
			"e-pr-jira-issue":       pr + "implements PROJ-5 Jira issue without a repository",
			"e-pr-linked-jira":      pr + "implements PROJ-6 Jira issue linked by a native pull request",
			"e-owned-project-jira":  pr + "implements PROJ-7 Jira issue in an owned project",
			"e-commit-issue-writer": "commit a1b2c3d4 implements CHAOS-99",
			"e-deploy":              pr + "deployed as deployment 40000000",
		}},
		"caller granted the scoped repository only": {[]string{"acme/deploy-service"}, map[string]string{
			"e-pr-issue":            pr + "implements CHAOS-77 Gate deploy stages on smoke tests",
			"e-pr-issue-unique":     pr + "implements CHAOS-78 Gate deploy stages on smoke tests",
			"e-collide":             pr + "implements CHAOS-81 Title in the scoped repository",
			"e-collide-long":        pr + "implements CHAOS-82 Short title in the scoped repository",
			"e-pr-jira-issue":       pr + "implements PROJ-5",
			"e-pr-linked-jira":      pr + "implements PROJ-6 Jira issue linked by a native pull request",
			"e-owned-project-jira":  pr + "implements PROJ-7 Jira issue in an owned project",
			"e-commit-issue-writer": "commit a1b2c3d4 implements CHAOS-99",
		}},
	} {
		labels := workGraphLabelsFor(t, ctx, query, tc.grant)
		if _, served := labels["e-commit-issue"]; served {
			t.Errorf("%s: e-commit-issue is served; a commit-sourced edge whose source id lacks the repository prefix is not in the writers' contract", name)
		}
		if _, served := labels["e-foreign-pr"]; served {
			t.Errorf("%s: e-foreign-pr is served; a pull-request-sourced edge outside its source repository's prefix is not in the writers' contract", name)
		}
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

// workGraphCost holds each statement's lowest and highest peak memory over the
// measured warm runs, and its lowest read volume.
type workGraphCost struct {
	memory, memoryHigh, readBytes                            uint64
	freshnessMemory, freshnessMemoryHigh, freshnessReadBytes uint64
	gateMemory, gateMemoryHigh, gateReadBytes                uint64
	// memoryRuns, freshnessRuns and gateRuns are the peak memory of every
	// measured run, in time order; the slope is read from these.
	memoryRuns, freshnessRuns, gateRuns []uint64
}

// The query_log predicates that tell the three statements of the catalog apart.
// Each excludes reads of system.query_log itself: those carry the markers in
// their own text (the predicates), and would otherwise match once logged.
const (
	workGraphStatementPredicate = `position(query, 'system.query_log') = 0 AND position(query, 'work_graph_edges') > 0 AND position(query, 'AS pt ON') > 0 AND position(query, 'SELECT max(observed_at), count() FROM (') = 0`
	workGraphFreshnessPredicate = `position(query, 'system.query_log') = 0 AND position(query, 'work_graph_edges') > 0 AND position(query, 'AS pt ON') > 0 AND position(query, 'SELECT max(observed_at), count() FROM (') > 0`
	workGraphGatePredicate      = `position(query, 'system.query_log') = 0 AND position(query, 'wia_link_auth') > 0 AND position(query, 'GROUP BY w.work_item_id') > 0`
)

// workGraphCommitNoiseSQL seeds rows commit-sourced edges of another
// repository of the organization, with the writers' source id
// "<repo_id>@<sha>" (ops issuecommitedges.go, ids.go GenerateCommitID).
func workGraphCommitNoiseSQL(rows int) string {
	return fmt.Sprintf(`INSERT INTO work_graph_edges (edge_id, source_type, source_id, target_type, target_id, edge_type, repo_id, provider, provenance, confidence, evidence, discovered_at, last_synced, org_id) SELECT concat('cnoise-', toString(number)), 'commit', concat('%s@', lower(hex(SHA1(toString(number))))), 'issue', concat('CNOISE-', toString(number)), 'implements', toUUID('%s'), 'github', 'explicit_text', 0.9, 'Fixes #1', now64(3), now64(3), '%s' FROM numbers(%d)`, wgOther, wgOther, wgOrg, rows)
}

// workGraphStage is one isolated database seeded with the label rows plus the
// requested extra rows. Its statements run for a restricted caller (the widest
// authorization relation); the stages are measured interleaved, round by
// round, so whatever the server's state does over time reaches all of them.
type workGraphStage struct {
	name     string
	ctx      context.Context
	direct   clickhousedriver.Conn
	executor *contextpacket.ClickHouseSourceExecutor
	plan     contextpacket.ReadPlan
	database string
}

func newWorkGraphStage(t *testing.T, name string, fillerPerRepo, noiseRows, commitNoiseRows int) *workGraphStage {
	t.Helper()
	ctx := context.Background()
	rawQuery, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	query := statementthreads.API(rawQuery)
	for _, statement := range devhealthschema.DDL() {
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
	// The noise source ids start with "f" so every one sorts after the scoped
	// repository's "2..." rows: the scoped pull-request rows then always sit at the
	// start of the pull-request group, in the granule that also holds the commit to
	// pull-request boundary, whatever the noise size. With random ids their position
	// moved with the noise count and the read cost moved by whole granules.
	if noiseRows > 0 {
		if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO work_graph_edges (edge_id, source_type, source_id, target_type, target_id, edge_type, repo_id, provider, provenance, confidence, evidence, discovered_at, last_synced, org_id) SELECT concat('noise-', toString(number)), 'pr', concat('f', substring(toString(generateUUIDv4()), 2), '#pr', toString(number)), 'issue', concat('NOISE-', toString(number)), 'implements', toUUID('%s'), 'linear', 'native', 0.9, 'linear_attachment', now64(3), now64(3), '%s' FROM numbers(%d)`, wgOther, wgOrg, noiseRows)); err != nil {
			t.Fatalf("seed noise edges: %v", err)
		}
		if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO work_graph_edges (edge_id, source_type, source_id, target_type, target_id, edge_type, repo_id, provider, provenance, confidence, evidence, discovered_at, last_synced, org_id) SELECT concat('issue-noise-', toString(number)), 'issue', concat('NZ-', toString(number)), 'issue', concat('NZ-T-', toString(number)), 'relates_to', NULL, 'linear', 'native', 0.9, '', now64(3), now64(3), '%s' FROM numbers(%d)`, wgOrg, noiseRows)); err != nil {
			t.Fatalf("seed issue-sourced noise edges: %v", err)
		}
		if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) SELECT toUUID('%s'), '%s', toUInt32(number + 1000), concat('noise ', toString(number)), 'open', now64(3) FROM numbers(%d)`, wgOther, wgOrg, noiseRows)); err != nil {
			t.Fatalf("seed noise pull requests: %v", err)
		}
	}
	if commitNoiseRows > 0 {
		if err := direct.Exec(ctx, workGraphCommitNoiseSQL(commitNoiseRows)); err != nil {
			t.Fatalf("seed commit noise edges: %v", err)
		}
	}
	// Background merges would otherwise change the part layout, and with it the
	// bytes a statement reads, between the stages being compared.
	tables, err := direct.Query(ctx, `SELECT name FROM system.tables WHERE database = currentDatabase() AND engine LIKE '%MergeTree'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		names = append(names, name)
	}
	tables.Close()
	for _, name := range names {
		if err := direct.Exec(ctx, "OPTIMIZE TABLE `"+name+"` FINAL"); err != nil {
			t.Fatalf("optimize %s: %v", name, err)
		}
	}
	plan := contextpacket.ReadPlan{OrgID: wgOrg, RepoID: wgRepo, RepoSlug: "acme/deploy-service", Branch: "main", TimeWindowDays: 30, WorkItemGrant: auth.WorkItemRepositorySelectorSet([]string{"acme/deploy-service"}, true)}
	executor := contextpacket.NewClickHouseSourceExecutor(query)
	var database string
	if err := direct.QueryRow(ctx, `SELECT currentDatabase()`).Scan(&database); err != nil {
		t.Fatalf("current database: %v", err)
	}
	return &workGraphStage{name: name, ctx: ctx, direct: direct, executor: executor, plan: plan, database: database}
}

// run executes the catalog once.
func (s *workGraphStage) run(t *testing.T) {
	t.Helper()
	result, err := contextpacket.ExecuteCatalog(s.ctx, s.executor, s.plan)
	if err != nil {
		t.Fatalf("%s: catalog: %v", s.name, err)
	}
	for _, unavailable := range result.Unavailable {
		if unavailable.Source == "work_graph.v1" {
			t.Fatalf("%s: work_graph.v1 unavailable: %s", s.name, unavailable.Reason)
		}
	}
}

// cost reads, for the runs after mark, each statement's lowest and highest
// peak memory, its lowest read volume and every run's peak memory.
func (s *workGraphStage) cost(t *testing.T, mark int64, rounds int) workGraphCost {
	t.Helper()
	ctx, direct, database := s.ctx, s.direct, s.database
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (measurement did not happen): %v", err)
	}
	var c workGraphCost
	var statements, threadless uint64
	row := direct.QueryRow(ctx, `SELECT min(memory_usage), max(memory_usage), min(read_bytes), count(), `+statementthreads.UnboundThreadsCount+` FROM system.query_log WHERE type = 'QueryFinish' AND query_kind = 'Select' AND current_database = ? AND event_time_microseconds > fromUnixTimestamp64Micro(?) AND `+workGraphStatementPredicate, database, mark)
	if err := row.Scan(&c.memory, &c.memoryHigh, &c.readBytes, &statements, &threadless); err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	statementthreads.RequireThreadBound(t, ctx, direct, "work_graph.v1", threadless)
	if statements < 3 || c.memory == 0 || c.readBytes == 0 {
		t.Fatalf("measurement did not happen: %d statements, %+v", statements, c)
	}
	var freshness uint64
	row = direct.QueryRow(ctx, `SELECT min(memory_usage), max(memory_usage), min(read_bytes), count(), `+statementthreads.UnboundThreadsCount+` FROM system.query_log WHERE type = 'QueryFinish' AND query_kind = 'Select' AND current_database = ? AND event_time_microseconds > fromUnixTimestamp64Micro(?) AND `+workGraphFreshnessPredicate, database, mark)
	if err := row.Scan(&c.freshnessMemory, &c.freshnessMemoryHigh, &c.freshnessReadBytes, &freshness, &threadless); err != nil {
		t.Fatalf("read query_log for the freshness read: %v", err)
	}
	statementthreads.RequireThreadBound(t, ctx, direct, "work_graph.v1 freshness read", threadless)
	if freshness < 3 || c.freshnessMemory == 0 {
		t.Fatalf("freshness measurement did not happen: %d statements, %+v", freshness, c)
	}
	var gates uint64
	row = direct.QueryRow(ctx, `SELECT min(memory_usage), max(memory_usage), min(read_bytes), count(), `+statementthreads.UnboundThreadsCount+` FROM system.query_log WHERE type = 'QueryFinish' AND query_kind = 'Select' AND current_database = ? AND event_time_microseconds > fromUnixTimestamp64Micro(?) AND `+workGraphGatePredicate, database, mark)
	if err := row.Scan(&c.gateMemory, &c.gateMemoryHigh, &c.gateReadBytes, &gates, &threadless); err != nil {
		t.Fatalf("read query_log for the gate: %v", err)
	}
	statementthreads.RequireThreadBound(t, ctx, direct, "work_graph.v1 issue gate", threadless)
	if gates < 3 || c.gateMemory == 0 || c.gateReadBytes == 0 {
		t.Fatalf("gate measurement did not happen: %d statements, %+v", gates, c)
	}
	c.memoryRuns = s.memoryRuns(t, mark, workGraphStatementPredicate, rounds)
	c.freshnessRuns = s.memoryRuns(t, mark, workGraphFreshnessPredicate, rounds)
	c.gateRuns = s.memoryRuns(t, mark, workGraphGatePredicate, rounds)
	s.logRunTable(t, mark)
	return c
}

// memoryRuns is the peak memory of every run of one statement after mark, in
// time order.
func (s *workGraphStage) memoryRuns(t *testing.T, mark int64, predicate string, rounds int) []uint64 {
	t.Helper()
	rows, err := s.direct.Query(s.ctx, `SELECT memory_usage FROM system.query_log WHERE type = 'QueryFinish' AND query_kind = 'Select' AND current_database = ? AND event_time_microseconds > fromUnixTimestamp64Micro(?) AND `+predicate+` ORDER BY event_time_microseconds`, s.database, mark)
	if err != nil {
		t.Fatalf("%s: read the per-run memory (measurement did not happen): %v", s.name, err)
	}
	defer rows.Close()
	var out []uint64
	for rows.Next() {
		var memory uint64
		if err := rows.Scan(&memory); err != nil {
			t.Fatalf("%s: scan a per-run memory: %v", s.name, err)
		}
		out = append(out, memory)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: per-run memory rows: %v", s.name, err)
	}
	if len(out) < rounds {
		t.Fatalf("%s: %d runs measured, want at least %d (measurement did not happen)", s.name, len(out), rounds)
	}
	return out
}

// logRunTable prints every measured run with what could decide its memory:
// read volume, rows, the threads it used, the max_threads setting and the
// stage's part and granule counts. It is a diagnostic: a failing query here
// only logs, it never fails the test.
func (s *workGraphStage) logRunTable(t *testing.T, mark int64) {
	t.Helper()
	var maxThreads string
	if err := s.direct.QueryRow(s.ctx, `SELECT toString(getSetting('max_threads'))`).Scan(&maxThreads); err != nil {
		maxThreads = "unreadable: " + err.Error()
	}
	parts, err := s.direct.Query(s.ctx, `SELECT table, count(), sum(rows), sum(marks) FROM system.parts WHERE database = currentDatabase() AND active AND table IN ('work_graph_edges', 'work_items', 'git_pull_requests') GROUP BY table ORDER BY table`)
	if err != nil {
		t.Logf("%s: part layout unavailable: %v", s.name, err)
	} else {
		for parts.Next() {
			var table string
			var count, rows, marks uint64
			if err := parts.Scan(&table, &count, &rows, &marks); err != nil {
				t.Logf("%s: part layout scan: %v", s.name, err)
				break
			}
			t.Logf("%s: layout %s parts=%d rows=%d granules=%d max_threads=%s", s.name, table, count, rows, marks, maxThreads)
		}
		parts.Close()
	}
	runs, err := s.direct.Query(s.ctx, `SELECT multiIf(position(query, 'wia_link_auth') > 0, 'gate', position(query, 'SELECT max(observed_at), count() FROM (') > 0, 'freshness', 'work_graph.v1') AS statement, memory_usage, read_bytes, read_rows, length(thread_ids), Settings['max_threads'] FROM system.query_log WHERE type = 'QueryFinish' AND query_kind = 'Select' AND current_database = ? AND event_time_microseconds > fromUnixTimestamp64Micro(?) AND ((`+workGraphStatementPredicate+`) OR (`+workGraphFreshnessPredicate+`) OR (`+workGraphGatePredicate+`)) ORDER BY event_time_microseconds`, s.database, mark)
	if err != nil {
		t.Logf("%s: per-run table unavailable: %v", s.name, err)
		return
	}
	defer runs.Close()
	for n := 0; runs.Next(); n++ {
		var statement, setting string
		var memory, readBytes, readRows, threads uint64
		if err := runs.Scan(&statement, &memory, &readBytes, &readRows, &threads, &setting); err != nil {
			t.Logf("%s: per-run table scan: %v", s.name, err)
			return
		}
		t.Logf("%s: run %d %s memory=%d read_bytes=%d read_rows=%d threads=%d max_threads_setting=%q", s.name, n, statement, memory, readBytes, readRows, threads, setting)
	}
}

// Four stages, each in its own database: the base, the base plus 150k other
// work items only, the base plus 20k other edges and 20k other pull
// requests only, and the base plus 40k commit-sourced edges of another
// repository only. No addition may move the statement's read volume or
// memory past the 1.15x slope.
func TestWorkGraphLabelStatementCostIgnoresOtherWorkItemsAndOtherEdges(t *testing.T) {
	const ceiling = uint64(56 << 20)
	const statementCeiling = uint64(12 << 20)
	stages := []*workGraphStage{
		newWorkGraphStage(t, "base", 50000, 20000, 20000),
		newWorkGraphStage(t, "+work items", 125000, 20000, 20000),
		newWorkGraphStage(t, "+edges and pull requests", 50000, 60000, 20000),
		newWorkGraphStage(t, "+commit edges", 50000, 20000, 60000),
	}
	// The first rounds load the table indexes and caches into the server; a
	// statement's peak memory settles on its plateau only by the third run, and
	// a lower peak before it shrinks the slope bound. Three rounds are
	// unmeasured. The measured rounds run every stage once each, so a change of
	// the server's state over time reaches all four stages alike instead of
	// whichever stage happened to be running; the peak memory of every run is
	// kept and the slope reads the lower quartile of them (the second-smallest of eight), a level the statement reached at least twice.
	const warmupRounds, measuredRounds = 3, 8
	for round := 0; round < warmupRounds; round++ {
		for _, stage := range stages {
			stage.run(t)
		}
	}
	mark := time.Now().UnixMicro()
	for round := 0; round < measuredRounds; round++ {
		for _, stage := range stages {
			stage.run(t)
		}
	}
	costs := map[string]workGraphCost{}
	for _, stage := range stages {
		costs[stage.name] = stage.cost(t, mark, measuredRounds)
	}
	base, filler, noise, commits := costs["base"], costs["+work items"], costs["+edges and pull requests"], costs["+commit edges"]
	t.Logf("work_graph.v1 cost: base %+v, +150k work items %+v, +40k edges and pull requests %+v, +40k commit edges %+v", base, filler, noise, commits)
	// Ceilings are absolute bounds, so each asserts the largest of the measured
	// warm peaks: a cap is hit by the worst run. Warm, the statement peaks near
	// 7 MB (11.6 MB on its higher plateau), its freshness read near 4 to 6 MB
	// and the issue gate near 8 MB.
	for _, name := range []string{"base", "+work items", "+edges and pull requests", "+commit edges"} {
		stage := costs[name]
		for _, c := range []struct {
			what         string
			runs         []uint64
			ceilingBytes uint64
		}{
			{"work_graph.v1", stage.memoryRuns, statementCeiling},
			{"work_graph.v1", stage.memoryRuns, ceiling},
			{"freshness read", stage.freshnessRuns, statementCeiling},
			{"issue gate", stage.gateRuns, ceiling},
		} {
			if peak, over := highestRunOver(c.runs, c.ceilingBytes); over {
				t.Errorf("%s: %s peaked at %d bytes on its highest warm run (runs %v), want under %d", name, c.what, peak, c.runs, c.ceilingBytes)
			} else {
				t.Logf("%s: %s memory highest %d bytes over %d runs, ceiling under %d on the highest", name, c.what, peak, len(c.runs), c.ceilingBytes)
			}
		}
	}
	// Slope: a stage is red when its plateau exceeds 1.15x the base's plus an
	// absolute floor (2 MiB of memory; 8 KiB of read volume, which only absorbs
	// granule and mark rounding on figures of a few KiB). The plateau is the
	// lower quartile of the measured runs (slopeOver), so a regression that
	// grows the cost lifts every run while a single high run lifts nothing.
	const readSlack = float64(8192)
	for name, stage := range map[string]workGraphCost{"+work items": filler, "+edges and pull requests": noise, "+commit edges": commits} {
		for what, runs := range map[string][2][]uint64{
			"work_graph.v1 memory":  {stage.memoryRuns, base.memoryRuns},
			"freshness read memory": {stage.freshnessRuns, base.freshnessRuns},
			"issue gate memory":     {stage.gateRuns, base.gateRuns},
		} {
			if over, level, bound := slopeOver(runs[0], runs[1]); over {
				t.Errorf("%s: %s plateau %d bytes (runs %v) against the base's plateau %d (runs %v), over the %.0f bytes of the 1.15x slope plus the 2 MiB floor", name, what, level, runs[0], lowerQuartile(runs[1]), runs[1], bound)
			} else {
				t.Logf("%s: %s plateau %d bytes (runs %v), base plateau %d, slope bound under %.0f on the plateau", name, what, level, runs[0], lowerQuartile(runs[1]), bound)
			}
		}
		for what, pair := range map[string][2]uint64{
			"work_graph.v1 read volume": {stage.readBytes, base.readBytes},
			"freshness read volume":     {stage.freshnessReadBytes, base.freshnessReadBytes},
			"issue gate read volume":    {stage.gateReadBytes, base.gateReadBytes},
		} {
			over := float64(pair[0]) > 1.15*float64(pair[1])+readSlack
			if over {
				t.Errorf("%s: %s %d bytes against %d at the base, over the 1.15x slope", name, what, pair[0], pair[1])
			}
		}
	}
}

// The statement's edge scope must reach the primary key: with noise edges of
// another repository in the same organization, the scoped read selects fewer
// granules than the table holds. A read that prunes nothing would pass on a
// small table, so the noise is sized to many granules.
func TestWorkGraphEdgeScopePrunesThePrimaryKey(t *testing.T) {
	ctx := context.Background()
	_, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	seedWorkGraphLabelRows(t, ctx, direct)
	if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO work_graph_edges (edge_id, source_type, source_id, target_type, target_id, edge_type, repo_id, provider, provenance, confidence, evidence, discovered_at, last_synced, org_id) SELECT concat('noise-', toString(number)), 'pr', concat(toString(generateUUIDv4()), '#pr', toString(number)), 'issue', concat('NOISE-', toString(number)), 'implements', toUUID('%s'), 'linear', 'native', 0.9, 'linear_attachment', now64(3), now64(3), '%s' FROM numbers(200000)`, wgOther, wgOrg)); err != nil {
		t.Fatalf("seed noise edges: %v", err)
	}
	if err := direct.Exec(ctx, workGraphCommitNoiseSQL(200000)); err != nil {
		t.Fatalf("seed commit noise edges: %v", err)
	}
	if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO work_graph_edges (edge_id, source_type, source_id, target_type, target_id, edge_type, repo_id, provider, provenance, confidence, evidence, discovered_at, last_synced, org_id) SELECT concat('issue-noise-', toString(number)), 'issue', concat('NZ-', toString(number)), 'issue', concat('NZ-T-', toString(number)), 'relates_to', NULL, 'linear', 'native', 0.9, '', now64(3), now64(3), '%s' FROM numbers(300000)`, wgOrg)); err != nil {
		t.Fatalf("seed issue-sourced noise edges: %v", err)
	}
	if err := direct.Exec(ctx, "OPTIMIZE TABLE work_graph_edges FINAL"); err != nil {
		t.Fatalf("optimize: %v", err)
	}
	scope := strings.ReplaceAll(contextpacket.WorkGraphEdgeScopeSQL("e."), "{repo_id:UUID}", "toUUID('"+wgRepo+"')")
	granules := func(where string) (selected, total int) {
		rows, err := direct.Query(ctx, "EXPLAIN indexes = 1 SELECT e.edge_id FROM work_graph_edges AS e FINAL WHERE e.org_id = '"+wgOrg+"' AND "+where)
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		defer rows.Close()
		var lines []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatalf("scan explain: %v", err)
			}
			lines = append(lines, line)
		}
		inKey := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "PrimaryKey" {
				inKey = true
			}
			if inKey && strings.HasPrefix(trimmed, "Granules:") {
				if _, err := fmt.Sscanf(trimmed, "Granules: %d/%d", &selected, &total); err != nil {
					t.Fatalf("parse %q: %v", trimmed, err)
				}
				return selected, total
			}
		}
		t.Fatalf("measurement did not happen: no PrimaryKey granules in the explain output:\n%s", strings.Join(lines, "\n"))
		return 0, 0
	}
	selected, total := granules(scope)
	t.Logf("scoped edge read selects %d of %d granules", selected, total)
	if total < 8 || selected*2 > total {
		t.Errorf("the scope selects %d of %d granules: the primary key does not prune it", selected, total)
	}
	bare, bareTotal := granules("e.repo_id = toUUID('" + wgRepo + "')")
	if bare != bareTotal {
		t.Errorf("control: a bare repo_id equality selects %d of %d granules, want all (the control no longer shows the defect)", bare, bareTotal)
	}
	// The commit arm alone: with 200k commit edges of another repository, the
	// prefix read selects a sliver of the commit granules, where the whole
	// source_type read (the arm before the prefix) selects all of them.
	commitArm, commitTotal := granules("e.repo_id = toUUID('" + wgRepo + "') AND e.source_type = 'commit' AND startsWith(e.source_id, concat(toString(toUUID('" + wgRepo + "')), '@'))")
	commitWhole, _ := granules("e.repo_id = toUUID('" + wgRepo + "') AND e.source_type = 'commit'")
	t.Logf("commit arm selects %d of %d granules; the whole source_type='commit' read selects %d", commitArm, commitTotal, commitWhole)
	if commitWhole < 8 || commitArm*4 > commitWhole {
		t.Errorf("the commit arm selects %d granules against %d for the whole commit source type: the key prefix does not prune it", commitArm, commitWhole)
	}
	assertIssueGateEdgeReadsPrune(t, ctx, direct)
}

// The statement the issue-title gate read before its edge arms were scoped by
// the table key: one arm per edge end, each filtered by repo_id alone.
const priorIssueEndpointIDsSQL = `(SELECT source_id FROM work_graph_edges WHERE org_id = {org_id:String} AND repo_id = {repo_id:UUID} AND source_type = 'issue' ` +
	`UNION ALL SELECT target_id FROM work_graph_edges WHERE org_id = {org_id:String} AND repo_id = {repo_id:UUID} AND target_type = 'issue')`

// assertIssueGateEdgeReadsPrune runs on a table holding 200k pull-request-sourced,
// 200k commit-sourced and 300k issue-sourced edges of other work (the
// issue-sourced ones with no repo_id, as in production). The gate read must read
// a small share of the rows the prior statement read, and the two statements
// must name the same issue keys, also once issue-sourced edges of the scoped
// repository's own work items exist.
func assertIssueGateEdgeReadsPrune(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) {
	t.Helper()
	bind := strings.NewReplacer("{org_id:String}", "'"+wgOrg+"'", "{repo_id:UUID}", "toUUID('"+wgRepo+"')").Replace
	rowsRead := func(label, statement string) uint64 {
		rows, err := direct.Query(ctx, "SELECT DISTINCT * FROM "+bind(statement)+" SETTINGS log_comment = '"+label+"'")
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		returned := 0
		for rows.Next() {
			returned++
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		t.Logf("%s returned %d rows", label, returned)
		if err := rows.Close(); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
			t.Fatalf("flush logs (measurement did not happen): %v", err)
		}
		var read uint64
		if err := direct.QueryRow(ctx, `SELECT read_rows FROM system.query_log WHERE type = 'QueryFinish' AND current_database = currentDatabase() AND log_comment = ? ORDER BY event_time_microseconds DESC LIMIT 1`, label).Scan(&read); err != nil {
			t.Fatalf("%s: measurement did not happen: %v", label, err)
		}
		return read
	}
	before, after := rowsRead("gate-prior", priorIssueEndpointIDsSQL), rowsRead("gate-current", contextpacket.WorkGraphIssueEndpointIDsSQL)
	t.Logf("gate read reads %d rows, the prior statement read %d", after, before)
	if before < 100000 {
		t.Errorf("control: the prior gate read read %d rows, want the noise read (the control no longer shows the defect)", before)
	}
	if after*4 > before {
		t.Errorf("the gate read reads %d rows against %d for the prior statement: the primary key does not prune it", after, before)
	}
	keys := func(statement string) []string {
		rows, err := direct.Query(ctx, "SELECT DISTINCT * FROM "+bind(statement)+" ORDER BY 1")
		if err != nil {
			t.Fatalf("issue keys: %v", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan issue key: %v", err)
			}
			out = append(out, id)
		}
		return out
	}
	same := func(when string) {
		t.Helper()
		priorKeys, currentKeys := keys(priorIssueEndpointIDsSQL), keys(contextpacket.WorkGraphIssueEndpointIDsSQL)
		if len(priorKeys) < 8 || strings.Join(priorKeys, ",") != strings.Join(currentKeys, ",") {
			t.Errorf("%s: issue keys differ: prior %v, current %v (want the same, at least 8)", when, priorKeys, currentKeys)
		}
	}
	same("seeded rows")
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	now := time.Now().UTC()
	for _, item := range []struct{ id, repo string }{{"CHAOS-91", wgRepo}, {"CHAOS-92", wgOther}} {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.id, item.repo, wgOrg, "Issue with an issue-sourced edge", "open", "", "", "linear", "", now, now)
		exec(fmt.Sprintf(`INSERT INTO work_graph_edges (edge_id, source_type, source_id, target_type, target_id, edge_type, repo_id, provider, provenance, confidence, evidence, discovered_at, last_synced, org_id) SELECT 'e-issue-%s', 'issue', '%s', 'issue', 'CHAOS-77', 'relates_to', NULL, 'linear', 'native', 0.9, '', now64(3), now64(3), '%s'`, item.id, item.id, wgOrg))
	}
	same("with issue-sourced edges of work items")
}

// An evidence-reference or source-row re-resolution names a repository-less
// issue exactly as the packet did for the same caller: by title where the
// caller's grant admits it, by key alone without a grant on the context.
func TestWorkGraphReresolutionServesTheSameRepositorylessTitlesAsThePacket(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	seedWorkGraphLabelRows(t, ctx, direct)
	rows := contextpacket.NewCatalogClickHouseRows(query)
	scope := contractsv1.ResolvedScope{RepoID: wgRepo, RepoSlug: "acme/deploy-service"}
	restricted := contextpacket.WithWorkItemGrant(ctx, auth.WorkItemRepositorySelectorSet([]string{"acme/deploy-service"}, true))
	for name, tc := range map[string]struct {
		ctx       context.Context
		wantTitle bool
	}{"restricted caller with a grant": {restricted, true}, "no grant on the context": {ctx, false}} {
		references, err := rows.ResolveEvidenceReference(tc.ctx, wgOrg, scope, "work_graph.v1", "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		served := false
		for _, reference := range references {
			if strings.Contains(reference.Evidence.Source.DisplayLabel, "implements PROJ-6") {
				served = true
				if got := strings.HasSuffix(reference.Evidence.Source.DisplayLabel, "Jira issue linked by a native pull request"); got != tc.wantTitle {
					t.Errorf("%s: PROJ-6 label %q, title served = %v, want %v", name, reference.Evidence.Source.DisplayLabel, got, tc.wantTitle)
				}
				if tc.wantTitle {
					if _, err := rows.ResolveSourceRow(tc.ctx, wgOrg, scope, contextpacket.SourceRowRead{QueryID: "work_graph.v1", Locator: reference.Evidence.EvidenceRefID}); err != nil {
						t.Fatalf("source row: %v", err)
					}
				}
			}
		}
		if !served {
			t.Errorf("%s: no reference for PROJ-6 among %d", name, len(references))
		}
	}
}

// A request that names a commit reaches the edges of the writers' commit id
// "<repo_id>@<sha>" on either endpoint, and a commit is labelled by the first
// eight characters of its sha, never by the repository id inside the id. An
// edge whose source id is the bare sha is outside the commit arm's key prefix
// and is not served.
func TestWorkGraphCommitShaFilterServesTheWriterFormEdges(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	seedWorkGraphLabelRows(t, ctx, direct)
	const sha = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	const other = "f9e8d7c6e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	for _, row := range []struct{ id, srcType, source, tgtType, target, edgeType string }{
		// A different edge type from the seed twin e-commit-issue-writer: the table key has no
		// edge id, so the same source, edge type and target would collapse the two rows.
		{"w-commit-writer", "commit", wgRepo + "@" + sha, "issue", "CHAOS-99", "relates_to"},
		{"w-pr-contains-commit", "pr", wgRepo + "#pr311", "commit", wgRepo + "@" + sha, "contains"},
		{"w-commit-other", "commit", wgRepo + "@" + other, "issue", "CHAOS-77", "implements"},
	} {
		if err := direct.Exec(ctx, `INSERT INTO work_graph_edges (edge_id, source_type, source_id, target_type, target_id, edge_type, repo_id, provider, provenance, confidence, evidence, discovered_at, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, 'github', 'native', 0.9, '', now64(3), now64(3), ?)`, row.id, row.srcType, row.source, row.tgtType, row.target, row.edgeType, wgRepo, wgOrg); err != nil {
			t.Fatalf("seed %s: %v", row.id, err)
		}
	}
	labels := workGraphLabelsForCommit(t, ctx, query, nil, sha)
	want := map[string]string{
		"e-commit-issue-writer": "commit a1b2c3d4 implements CHAOS-99 (repository-wide)",
		"w-commit-writer":       "commit a1b2c3d4 relates to CHAOS-99 (repository-wide)",
		"w-pr-contains-commit":  "acme/deploy-service#311 Rework deploy pipeline stages contains commit a1b2c3d4 (repository-wide)",
	}
	if len(labels) != len(want) {
		t.Errorf("a request for commit %s serves %v, want exactly %v", sha[:8], labels, want)
	}
	for id, label := range want {
		if got := labels[id]; got != label {
			t.Errorf("edge %s label = %q, want %q", id, got, label)
		}
	}
	if _, served := labels["w-commit-other"]; served {
		t.Errorf("w-commit-other (another commit) is served for a request naming commit %s", sha[:8])
	}
	if _, served := labels["e-commit-issue"]; served {
		t.Errorf("e-commit-issue (a bare sha source id no writer produces) is served for a request naming commit %s", sha[:8])
	}
}

// The commit arm's prefix prune serves the same edges as the arm it replaced
// for every writer-conforming edge. The old scope read every commit-sourced
// edge of the organization and filtered repo_id after the read; the new one
// reads the "<repo_id>@" prefix only. On a seed that mixes both writers'
// edges, other repositories' edges and two edges no writer produces, the new
// scope is the old scope minus exactly those two edges.
func TestWorkGraphCommitArmPrefixServesTheSameWriterEdgesAsTheWholeCommitRead(t *testing.T) {
	ctx := context.Background()
	_, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	seedWorkGraphLabelRows(t, ctx, direct)
	const sha = "b1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	for _, row := range []struct{ id, source, repo string }{
		{"w-commit-a", wgRepo + "@" + sha, wgRepo},
		{"w-commit-b", wgRepo + "@c1c2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", wgRepo},
		// Distinct source ids: the table key has no edge id, so one source, edge type and target keeps one row.
		{"o-commit-a", wgOther + "@c1c2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", wgOther},
		{"x-commit-foreign", wgOther + "@" + sha, wgRepo},
	} {
		if err := direct.Exec(ctx, `INSERT INTO work_graph_edges (edge_id, source_type, source_id, target_type, target_id, edge_type, repo_id, provider, provenance, confidence, evidence, discovered_at, last_synced, org_id) VALUES (?, 'commit', ?, 'issue', 'CHAOS-77', 'implements', ?, 'github', 'explicit_text', 0.9, '', now64(3), now64(3), ?)`, row.id, row.source, row.repo, wgOrg); err != nil {
			t.Fatalf("seed %s: %v", row.id, err)
		}
	}
	if err := direct.Exec(ctx, workGraphCommitNoiseSQL(50)); err != nil {
		t.Fatalf("seed commit noise: %v", err)
	}
	edgeIDs := func(scope string) map[string]bool {
		t.Helper()
		scope = strings.ReplaceAll(scope, "{repo_id:UUID}", "toUUID('"+wgRepo+"')")
		rows, err := direct.Query(ctx, "SELECT e.edge_id FROM work_graph_edges AS e FINAL WHERE e.org_id = '"+wgOrg+"' AND "+scope)
		if err != nil {
			t.Fatalf("read edges: %v", err)
		}
		defer rows.Close()
		ids := map[string]bool{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			ids[id] = true
		}
		return ids
	}
	const before = "e.repo_id = {repo_id:UUID} AND ((e.source_type = 'pr' AND startsWith(e.source_id, concat(toString({repo_id:UUID}), '#pr'))) OR e.source_type NOT IN ('pr', 'issue'))"
	old, now := edgeIDs(before), edgeIDs(contextpacket.WorkGraphEdgeScopeSQL("e."))
	for _, id := range []string{"w-commit-a", "w-commit-b", "e-commit-issue-writer", "e-pr-issue", "e-deploy"} {
		if !old[id] || !now[id] {
			t.Errorf("writer-conforming edge %s: served before %v, after %v, want both", id, old[id], now[id])
		}
	}
	for _, id := range []string{"o-commit-a", "x-commit-foreign", "e-foreign-pr"} {
		if now[id] {
			t.Errorf("edge %s of another repository is served after the change", id)
		}
	}
	for id := range now {
		if !old[id] {
			t.Errorf("edge %s is served after the change and was not before: the new scope may only narrow", id)
		}
	}
	dropped := []string{}
	for id := range old {
		if !now[id] {
			dropped = append(dropped, id)
		}
	}
	sort.Strings(dropped)
	if len(dropped) != 2 || dropped[0] != "e-commit-issue" || dropped[1] != "x-commit-foreign" {
		t.Errorf("the change drops %v, want exactly [e-commit-issue x-commit-foreign] (the two seed edges no writer produces: a bare-sha source id and a source id of another repository)", dropped)
	}
	t.Logf("served before %d edges, after %d; dropped %v", len(old), len(now), dropped)
}
