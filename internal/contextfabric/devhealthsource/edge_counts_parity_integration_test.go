package devhealthsource_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	edgeParityOrg  = "73000000-0000-4000-8000-000000000001"
	edgeParityRepo = "73000000-0000-4000-8000-000000000002"
)

func edgeParityProject(i int) string { return fmt.Sprintf("73100000-0000-4000-8000-%012d", i) }

// edgeParityDrain runs the source to the end of its rows and returns the edges the
// producer leaves asserted: every relationship of the type, less the ones a
// relationship tombstone removes and, for the column arm, the ones a column
// tombstone of their work item removes.
func edgeParityDrain(t *testing.T, ctx context.Context, source contextfabric.ProjectionSource, name string, relation contractsv1.ContextFabricRelationshipType) map[string]contractsv1.ContextFabricRelationshipProjection {
	t.Helper()
	edges := map[string]contractsv1.ContextFabricRelationshipProjection{}
	removed := map[string]bool{}
	columnRetracted := map[string]bool{}
	cursor := ""
	for page := 0; ; page++ {
		if page > 500 {
			t.Fatal("the source did not drain")
		}
		batch, ok, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: edgeParityOrg, Source: name, Cursor: cursor})
		if err != nil {
			t.Fatalf("read page %d: %v", page, err)
		}
		if !ok {
			break
		}
		for _, r := range batch.Relationships {
			if r.Type == relation {
				edges[r.RelationshipID] = r
			}
		}
		for _, tomb := range batch.Tombstones {
			switch tomb.Kind {
			case "relationship":
				removed[tomb.CanonicalID] = true
			case contextfabric.TombstoneKindColumnProjectEdges:
				columnRetracted[tomb.CanonicalID] = true
			}
		}
		cursor = batch.NextCursor
	}
	for id, edge := range edges {
		if removed[id] || (edge.ValidFrom == nil && columnRetracted[edge.From.CanonicalID]) {
			delete(edges, id)
		}
	}
	return edges
}

// TestLiveEdgeCountsEqualTheEdgesTheProducersAssert seeds every class a
// producer omits or retracts (an overlong canonical id, a duplicate add, a
// dangling remove, an unresolved and an ambiguous project, a column row history
// supersedes) beside the rows it projects, drains the real producers, and
// requires the source edge count of each measured population to equal the
// edges the producers left asserted.
func TestLiveEdgeCountsEqualTheEdgesTheProducersAssert(t *testing.T) {
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
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	at := func(minutes int) time.Time { return base.Add(time.Duration(minutes) * time.Minute) }
	long := "linear:" + strings.Repeat("X", 300)

	seed("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/parity', 'linear', ?)`, edgeParityRepo, edgeParityOrg, base)
	for i := 1; i <= 2; i++ {
		seed("project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, '', 'linear', 'backlog', '', 1, ?)`, edgeParityProject(i), edgeParityOrg, fmt.Sprintf("P%d", i), base)
	}
	for i := 3; i <= 4; i++ {
		seed("ambiguous project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, 'DUP', 'linear', 'backlog', '', 1, ?)`, edgeParityProject(i), edgeParityOrg, fmt.Sprintf("P%d", i), base)
	}
	longProject := "linear-project-" + strings.Repeat("Y", 300)
	seed("overlong project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, 'long', '', 'linear', 'backlog', '', 1, ?)`, longProject, edgeParityOrg, base)

	transition := func(label, kind, subject, from, to string, minutes int, event string) {
		seed(label, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, toUUID(?), ?, ?, 'linear', ?, ?, '', '', '', ?, ?, ?)`,
			edgeParityOrg, edgeParityRepo, kind, subject, from, to, at(minutes), at(minutes), event)
	}
	workItem := func(id, project string, minutes int) {
		seed("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, toUUID(?), ?, 't', 'open', '', '', 'linear', ?, ?)`,
			id, edgeParityRepo, edgeParityOrg, project, at(minutes))
	}
	// Interval edges: closed and open stretches, a moved item, a duplicate add,
	// a dangling remove, unresolved and ambiguous projects, overlong ids, a PR.
	transition("w1 add", "work_item", "linear:W-1", "", edgeParityProject(1), 1, "ev-w1-a")
	transition("w1 remove", "work_item", "linear:W-1", edgeParityProject(1), "", 2, "ev-w1-b")
	transition("w2 add", "work_item", "linear:W-2", "", edgeParityProject(1), 1, "ev-w2-a")
	transition("w2 move", "work_item", "linear:W-2", edgeParityProject(1), edgeParityProject(2), 3, "ev-w2-b")
	transition("w3 add", "work_item", "linear:W-3", "", edgeParityProject(1), 1, "ev-w3-a")
	transition("w3 add again", "work_item", "linear:W-3", "", edgeParityProject(1), 4, "ev-w3-b")
	transition("w4 unresolved", "work_item", "linear:W-4", "", "ghost-project", 1, "ev-w4-a")
	transition("w5 ambiguous", "work_item", "linear:W-5", "", "DUP", 1, "ev-w5-a")
	transition("w6 dangling remove", "work_item", "linear:W-6", edgeParityProject(1), "", 1, "ev-w6-a")
	transition("w7 overlong work item", "work_item", long, "", edgeParityProject(1), 1, "ev-w7-a")
	transition("w8 overlong project", "work_item", "linear:W-8", "", longProject, 1, "ev-w8-a")
	transition("pr add", "pull_request", "17", "", edgeParityProject(1), 1, "ev-pr-a")
	// Column arm: resolved, unresolved, overlong, and one history supersedes.
	workItem("linear:C-1", edgeParityProject(2), 1)
	workItem("linear:C-2", "ghost-project", 1)
	workItem(long+"-column", edgeParityProject(2), 1)
	workItem("linear:S-1", edgeParityProject(1), 1)
	transition("s1 history", "work_item", "linear:S-1", "", edgeParityProject(2), 2, "ev-s1-a")

	teams, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := teams.ProjectionSourceEdgeCounts(ctx, edgeParityOrg)
	if err != nil {
		t.Fatalf("ProjectionSourceEdgeCounts: %v", err)
	}
	asserted := edgeParityDrain(t, ctx, teams, devhealthsource.TeamsProjectsSourceName, contractsv1.ContextFabricRelationshipBelongsToProject)
	got := map[string]int64{contextfabric.EdgeArmTransition: 0, contextfabric.EdgeArmWorkItemColumn: 0}
	for _, edge := range asserted {
		if edge.ValidFrom != nil {
			got[contextfabric.EdgeArmTransition]++
		} else {
			got[contextfabric.EdgeArmWorkItemColumn]++
		}
	}
	for _, arm := range []string{contextfabric.EdgeArmTransition, contextfabric.EdgeArmWorkItemColumn} {
		key := contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToProject, Arm: arm}
		if got[arm] == 0 {
			t.Fatalf("the seed asserts no %s edge: the comparison would be vacuous", arm)
		}
		if counts[key] != got[arm] {
			t.Errorf("BELONGS_TO_PROJECT %s: the source edge count is %d, the producer asserts %d edges", arm, counts[key], got[arm])
		}
	}

	// Reviews: one projected, one with an overlong review id the producer omits.
	seed("pr", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (toUUID(?), ?, 7, 'pr seven', 'open', ?)`, edgeParityRepo, edgeParityOrg, base)
	seed("review", `INSERT INTO git_pull_request_reviews (review_id, repo_id, org_id, number, state, submitted_at, last_synced) VALUES ('review-1', toUUID(?), ?, 7, 'approved', ?, ?)`, edgeParityRepo, edgeParityOrg, base, base)
	seed("overlong review", `INSERT INTO git_pull_request_reviews (review_id, repo_id, org_id, number, state, submitted_at, last_synced) VALUES (?, toUUID(?), ?, 7, 'approved', ?, ?)`, "review-"+strings.Repeat("Z", 300), edgeParityRepo, edgeParityOrg, base, base)
	main, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	reviewCounts, err := main.ProjectionSourceEdgeCounts(ctx, edgeParityOrg)
	if err != nil {
		t.Fatalf("ProjectionSourceEdgeCounts: %v", err)
	}
	reviewEdges := edgeParityDrain(t, ctx, main, devhealthsource.SourceName, contractsv1.ContextFabricRelationshipBelongsToPullRequest)
	reviewKey := contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToPullRequest}
	if len(reviewEdges) != 1 || reviewCounts[reviewKey] != int64(len(reviewEdges)) {
		t.Errorf("BELONGS_TO_PULL_REQUEST: the source edge count is %d, the producer asserts %d edges (want 1)", reviewCounts[reviewKey], len(reviewEdges))
	}
}
