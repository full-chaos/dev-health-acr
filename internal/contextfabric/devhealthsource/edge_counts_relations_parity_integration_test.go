package devhealthsource_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	relParityOrg   = "74000000-0000-4000-8000-000000000001"
	relParityRepoA = "74000000-0000-4000-8000-0000000000a1"
	relParityRepoB = "74000000-0000-4000-8000-0000000000b2"
	relParityGhost = "74000000-0000-4000-8000-0000000000c3"
	relParityZero  = "00000000-0000-0000-0000-000000000000"
)

// relParityDrain drains the source and returns, per relation type, the edges the
// producer leaves asserted: every relationship less the ones a tombstone removes
// (a relationship tombstone, a part_of_edges tombstone of the child, an outranked
// links tombstone of either end), applied in stream order.
func relParityDrain(t *testing.T, ctx context.Context, source contextfabric.ProjectionSource) map[string]contractsv1.ContextFabricRelationshipProjection {
	t.Helper()
	edges := map[string]contractsv1.ContextFabricRelationshipProjection{}
	cursor := ""
	for page := 0; ; page++ {
		if page > 500 {
			t.Fatal("the source did not drain")
		}
		batch, ok, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: relParityOrg, Source: devhealthsource.SourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("read page %d: %v", page, err)
		}
		if !ok {
			break
		}
		for _, r := range batch.Relationships {
			edges[r.RelationshipID] = r
		}
		for _, tomb := range batch.Tombstones {
			for id, edge := range edges {
				switch {
				case tomb.Kind == "relationship" && tomb.CanonicalID == id,
					tomb.Kind == contextfabric.TombstoneKindPartOfEdges && edge.Type == contractsv1.ContextFabricRelationshipPartOf && edge.From.CanonicalID == tomb.CanonicalID,
					tomb.Kind == contextfabric.TombstoneKindOutrankedPullRequestLinks && edge.Type == contractsv1.ContextFabricRelationshipLinksPullRequest && edge.To.CanonicalID == tomb.CanonicalID,
					tomb.Kind == contextfabric.TombstoneKindOutrankedIssueLinks && edge.Type == contractsv1.ContextFabricRelationshipLinksPullRequest && edge.From.CanonicalID == tomb.CanonicalID:
					delete(edges, id)
				}
			}
		}
		cursor = batch.NextCursor
	}
	return edges
}

// TestLiveRepositoryAndLinkEdgeCountsEqualTheEdgesTheProducersAssert seeds, beside
// the rows each producer projects, every class it omits or retracts, drains the
// real producers and requires each measured population to equal the edges left
// asserted: BELONGS_TO_REPOSITORY per source kind, CORRELATED_WITH_INCIDENT,
// PART_OF and LINKS_PULL_REQUEST per tier.
func TestLiveRepositoryAndLinkEdgeCountsEqualTheEdgesTheProducersAssert(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	seed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	created := now.Add(-48 * time.Hour)
	long := strings.Repeat("L", 300)

	for _, repo := range [][2]string{{relParityRepoA, "acme/alpha"}, {relParityRepoB, "acme/beta"}} {
		seed("repo "+repo[1], `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, 'github', ?)`, repo[0], relParityOrg, repo[1], now)
	}

	// Work items: projected with an edge; no repository row (Linear zero UUID and a
	// repository the organization does not hold); an overlong id the producer omits.
	workItem := func(id, repo, itemType, parent string) {
		seed("work item "+id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, parent_id, provider, created_at, updated_at, last_synced) VALUES (?, toUUID(?), ?, ?, ?, 'open', ?, 'github', ?, ?, ?)`,
			id, repo, relParityOrg, id, itemType, parent, created, now, now)
	}
	workItem("gh:acme/alpha#1", relParityRepoA, "issue", "")
	workItem("gh:acme/alpha#2", relParityRepoB, "issue", "")
	workItem("linear:NOREPO-1", relParityZero, "issue", "")
	workItem("gh:ghost#1", relParityGhost, "issue", "")
	workItem(long, relParityRepoA, "issue", "")

	// PART_OF: a parent in the child's repository; a qualified parent held once in
	// another repository; an unresolved parent; a self parent; a non-qualified
	// parent held only by another repository; a qualified parent held twice.
	workItem("gh:acme/alpha#10", relParityRepoA, "epic", "")
	workItem("gh:acme/alpha#11", relParityRepoA, "story", "gh:acme/alpha#10")
	workItem("jira:EPIC-1", relParityRepoB, "epic", "")
	workItem("gh:acme/alpha#12", relParityRepoA, "story", "jira:EPIC-1")
	workItem("gh:acme/alpha#13", relParityRepoA, "story", "gh:acme/alpha#404")
	workItem("gh:acme/alpha#14", relParityRepoA, "story", "gh:acme/alpha#14")
	workItem("gh:acme/beta#99", relParityRepoB, "epic", "")
	workItem("gh:acme/alpha#15", relParityRepoA, "story", "gh:acme/beta#99")
	workItem("jira:DUP-1", relParityRepoA, "epic", "")
	workItem("jira:DUP-1", relParityRepoB, "epic", "")
	workItem("gh:acme/alpha#16", relParityRepoA, "story", "jira:DUP-1")
	// An empty qualified key, a qualified parent nobody holds, a child and a
	// parent whose ids exceed the natural-key bound.
	workItem("jira:", relParityRepoA, "epic", "")
	workItem("gh:acme/alpha#17", relParityRepoA, "story", "jira:")
	workItem("gh:acme/alpha#18", relParityRepoA, "story", "jira:GHOST-9")
	workItem("child-"+long, relParityRepoA, "story", "gh:acme/alpha#10")
	workItem("parent-"+long, relParityRepoA, "epic", "")
	workItem("gh:acme/alpha#19", relParityRepoA, "story", "parent-"+long)

	// Pull requests: one per repository, one in a repository the organization lacks.
	for _, pr := range []struct {
		repo   string
		number int
	}{{relParityRepoA, 6}, {relParityRepoA, 7}, {relParityRepoA, 8}, {relParityRepoB, 9}, {relParityGhost, 5}} {
		seed("pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, last_synced) VALUES (toUUID(?), ?, ?, 'pr', 'open', ?, ?)`, pr.repo, relParityOrg, pr.number, created, now)
	}

	// Deployments and CI runs: projected; overlong id; unknown repository.
	for _, d := range [][2]string{{relParityRepoA, "deploy-1"}, {relParityRepoB, "deploy-2"}, {relParityRepoA, "deploy-" + long}, {relParityGhost, "deploy-3"}} {
		seed("deployment "+d[1], `INSERT INTO deployments (repo_id, org_id, deployment_id, status, environment, deployed_at, started_at, last_synced) VALUES (toUUID(?), ?, ?, 'success', 'prod', ?, ?, ?)`, d[0], relParityOrg, d[1], created, created, now)
	}
	for _, c := range [][2]string{{relParityRepoA, "run-1"}, {relParityRepoB, "run-2"}, {relParityRepoA, "run-" + long}, {relParityGhost, "run-3"}} {
		seed("ci run "+c[1], `INSERT INTO ci_pipeline_runs (repo_id, run_id, org_id, branch, status, started_at, finished_at, last_synced) VALUES (toUUID(?), ?, ?, 'main', 'success', ?, ?, ?)`, c[0], c[1], relParityOrg, created, created, now)
	}

	// Incidents: one service mapped to both repositories (two edges); a deleted
	// incident; a service whose only mapping ended (no edge); a service mapped to
	// one repository and formerly to another (one edge).
	mapping := func(id, service, repo string, active uint8) {
		seed("mapping "+id, `INSERT INTO operational_service_repository_mappings (org_id, source_version_at, id, last_synced, service_id, repo_id, is_active) VALUES (?, ?, ?, ?, ?, toUUID(?), ?)`, relParityOrg, created, id, created, service, repo, active)
	}
	incident := func(id, service string, deleted uint8) {
		seed("incident "+id, `INSERT INTO operational_incidents (id, org_id, source_version_at, service_id, title, normalized_status, raw_status, normalized_severity, raw_severity, started_at, source_event_at, observed_at, is_deleted, last_synced) VALUES (?, ?, ?, ?, 'outage', 'triggered', 'triggered', 'sev2', 'sev2', ?, ?, ?, ?, ?)`,
			id, relParityOrg, created, service, created, created, created, deleted, created)
	}
	mapping("m-shared-a", "svc-shared", relParityRepoA, 1)
	mapping("m-shared-b", "svc-shared", relParityRepoB, 1)
	mapping("m-ended", "svc-ended", relParityRepoA, 0)
	mapping("m-moved-a", "svc-moved", relParityRepoA, 1)
	mapping("m-moved-b", "svc-moved", relParityRepoB, 0)
	incident("inc-shared", "svc-shared", 0)
	incident("inc-deleted", "svc-shared", 1)
	incident("inc-ended", "svc-ended", 0)
	incident("inc-moved", "svc-moved", 0)

	// Deployment incident edges: projected; a second row of the same edge from
	// another source; no incident; no deployment id; an overlong deployment id; an
	// unknown repository.
	devEdge := func(edgeID, deployment, incident, repo, source string) {
		seed("deployment incident edge "+edgeID, `INSERT INTO work_graph_deployment_incident_edges (edge_id, deployment_id, incident_id, repo_id, org_id, confidence, source, evidence, observed_at, computed_at) VALUES (?, ?, ?, toUUID(?), ?, 0.8, ?, '', ?, ?)`,
			edgeID, deployment, incident, repo, relParityOrg, source, now, now)
	}
	devEdge("e1", "deploy-1", "inc-shared", relParityRepoA, "temporal")
	devEdge("e1b", "deploy-1", "inc-shared", relParityRepoA, "heuristic")
	devEdge("e2", "deploy-2", "inc-moved", relParityRepoB, "temporal")
	devEdge("e3", "deploy-1", "none", relParityRepoA, "temporal")
	devEdge("e4", "", "inc-shared", relParityRepoA, "temporal")
	devEdge("e5", "deploy-"+long, "inc-shared", relParityRepoA, "temporal")
	devEdge("e6", "deploy-3", "inc-shared", relParityGhost, "temporal")

	// Links: native; a native link beside which an explicit_text link on the same
	// pull request and a heuristic link on the same issue sit (both outranked); a
	// lone explicit_text and a lone heuristic link; links the producer skips.
	link := func(issue, repo string, number int, tier string) {
		seed("link "+issue, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (toUUID(?), ?, ?, 0.9, ?, '', ?, ?)`, repo, issue, number, tier, now, relParityOrg)
	}
	workItem("gh:acme/alpha#20", relParityRepoA, "issue", "")
	workItem("gh:acme/alpha#21", relParityRepoA, "issue", "")
	workItem("gh:acme/alpha#22", relParityRepoA, "issue", "")
	workItem("gh:acme/alpha#23", relParityRepoA, "issue", "")
	workItem("gh:acme/alpha#24", relParityRepoA, "pr", "")
	workItem("gh:acme/alpha#25", relParityRepoA, "issue", "")
	link("gh:acme/alpha#20", relParityRepoA, 7, "native")
	link("gh:acme/alpha#21", relParityRepoA, 7, "explicit_text")
	link("gh:acme/alpha#20", relParityRepoB, 9, "heuristic")
	link("gh:acme/alpha#22", relParityRepoA, 6, "explicit_text")
	link("gh:acme/alpha#23", relParityRepoB, 9, "heuristic")
	link("gh:acme/alpha#24", relParityRepoA, 8, "native")
	link("gh:acme/alpha#25", relParityRepoB, 9, "\u00a0heuristic\u00a0")
	link("gh:acme/alpha#22", relParityRepoA, 999, "heuristic")
	link(long, relParityRepoB, 9, "heuristic")
	link("gh:ghost-issue", relParityRepoA, 8, "heuristic")
	link("gh:acme/alpha#23", relParityRepoA, 8, "guess")

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := source.ProjectionSourceEdgeCounts(ctx, relParityOrg)
	if err != nil {
		t.Fatalf("ProjectionSourceEdgeCounts: %v", err)
	}
	asserted := relParityDrain(t, ctx, source)
	got := map[contextfabric.EdgeCountKey]int64{}
	for _, edge := range asserted {
		key := contextfabric.EdgeCountKey{Relation: edge.Type}
		switch edge.Type {
		case contractsv1.ContextFabricRelationshipBelongsToRepository:
			key.Arm = string(edge.From.Kind)
		case contractsv1.ContextFabricRelationshipLinksPullRequest:
			if tier := edge.Properties[devhealthsource.IssuePullRequestLinkTierProperty]; tier.String != nil {
				key.Arm = *tier.String
			}
		}
		got[key]++
	}
	for _, key := range source.EdgeCountKeys() {
		if key.Relation == contractsv1.ContextFabricRelationshipBelongsToPullRequest {
			continue
		}
		if got[key] == 0 {
			t.Errorf("%+v: the seed asserts no edge, the comparison would be vacuous", key)
		}
		if counts[key] != got[key] {
			t.Errorf("%+v: the source edge count is %d, the producer asserts %d edges", key, counts[key], got[key])
		}
	}
}
