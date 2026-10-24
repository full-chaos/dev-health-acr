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
	depParityOrg   = "75000000-0000-4000-8000-000000000001"
	depParityRepoA = "75000000-0000-4000-8000-0000000000a1"
	depParityRepoB = "75000000-0000-4000-8000-0000000000b2"
)

var depParityRelations = []contractsv1.ContextFabricRelationshipType{
	contractsv1.ContextFabricRelationshipBlocks, contractsv1.ContextFabricRelationshipRelatesTo,
	contractsv1.ContextFabricRelationshipDuplicates, contractsv1.ContextFabricRelationshipRelatedTo,
}

func depParityDrain(t *testing.T, ctx context.Context, source contextfabric.ProjectionSource) map[contractsv1.ContextFabricRelationshipType]int64 {
	t.Helper()
	edges := map[string]contractsv1.ContextFabricRelationshipProjection{}
	cursor := ""
	for page := 0; ; page++ {
		if page > 500 {
			t.Fatal("the source did not drain")
		}
		batch, ok, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: depParityOrg, Source: devhealthsource.SourceName, Cursor: cursor})
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
			if tomb.Kind == "relationship" {
				delete(edges, tomb.CanonicalID)
			}
		}
		cursor = batch.NextCursor
	}
	got := map[contractsv1.ContextFabricRelationshipType]int64{}
	for _, edge := range edges {
		got[edge.Type]++
	}
	return got
}

// TestLiveDependencyEdgeCountsEqualTheEdgesTheProducerAsserts seeds, beside the
// links the dependency producer projects, every class it omits, merges or
// retracts (an inverse spelling and its forward twin, an alias, case and padding
// variants, an ignored type, an unknown type, a removed link, an unresolved
// target, a target with no creation time, overlong ids, a self link, a missing
// source), drains the real producer and requires each relation's source edge
// count to equal the edges left asserted. A second case cuts the removed-link
// set at a bound of one and requires the four keys to be absent.
func TestLiveDependencyEdgeCountsEqualTheEdgesTheProducerAsserts(t *testing.T) {
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
	now := time.Now().UTC().Truncate(time.Second)
	linked := now.Add(-5 * time.Hour)
	resynced := now.Add(-time.Hour)
	long := strings.Repeat("L", 300)

	seed("repo a", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/dep-a', 'github', ?)`, depParityRepoA, depParityOrg, linked)
	seed("repo b", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/dep-b', 'github', ?)`, depParityRepoB, depParityOrg, linked)
	item := func(id, provider string, at time.Time) {
		seed("item "+id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, created_at, updated_at, last_synced) VALUES (?, toUUID(?), ?, ?, 'open', '', '', ?, '', ?, ?, ?)`,
			id, depParityRepoA, depParityOrg, "item "+id, provider, linked, at, at)
	}
	noCreatedItem := func(id string) {
		seed("item without creation time "+id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, toUUID(?), ?, ?, 'open', '', '', 'github', '', ?, ?)`,
			id, depParityRepoA, depParityOrg, "item "+id, linked, linked)
	}
	dep := func(source, target, relType, raw string, at time.Time) {
		seed("dependency "+source+" "+relType, `INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, org_id, last_synced) VALUES (?, ?, ?, ?, ?, ?)`,
			source, target, relType, raw, depParityOrg, at)
	}
	plain := []string{"d:1", "d:2", "d:3", "d:4", "d:5", "d:6", "d:7", "d:8", "d:9", "d:10", "d:11", "d:12", "d:13", "d:14", "d:17", "d:18", "d:19", "d:22", "d:24", "d:25", "d:26", "d:27", "d:29", "d:30", long, "long-target-" + long}
	for _, id := range plain {
		item(id, "github", linked)
	}
	noCreatedItem("d:23")
	item("jira:D-20", "jira", linked)
	item("jira:D-21", "jira", linked)

	dep("d:1", "d:2", "blocks", "blocks", linked)
	dep("d:3", "d:4", "BLOCKED_BY", "blocked by", linked)
	dep("d:4", "d:3", "blocks", "blocks", linked)
	dep("d:5", "d:6", "is_blocked_by", "is blocked by", linked)
	dep("d:7", "d:8", "RELATES", "relates", linked)
	dep("d:7", "d:8", "relates_to", "relates to", linked)
	dep("d:9", "d:10", " Relates_To ", "relates to", linked)
	dep("d:9", "d:10", "relates_to", "relates to", linked)
	dep("d:11", "d:12", "duplicates", "duplicates", linked)
	dep("d:13", "d:14", "related_to", "related to", linked)
	dep("d:17", "EXT-1", "external_issue_key", "external", linked)
	dep("d:18", "d:19", "weird_type", "weird", linked)
	dep("d:22", "ghost-1", "blocks", "blocks", linked)
	dep("d:22", "d:23", "relates_to", "relates to", linked)
	dep(long, "d:24", "blocks", "blocks", linked)
	dep("d:25", "long-target-"+long, "blocks", "blocks", linked)
	dep("d:26", "ghost-"+long, "blocks", "blocks", linked)
	dep("d:27", "d:27", "relates_to", "relates to", linked)
	dep("no-such-source", "d:24", "blocks", "blocks", linked)
	dep("d:29", "d:30", "blocks", "blocks", linked)
	dep("d:29", "d:30", "blocks", "blocks again", resynced)
	dep("jira:D-20", "jira:D-21", "relates_to", "relates to", linked)
	item("jira:D-20", "jira", resynced)
	item("jira:D-21", "jira", resynced)

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := source.ProjectionSourceEdgeCounts(ctx, depParityOrg)
	if err != nil {
		t.Fatalf("ProjectionSourceEdgeCounts: %v", err)
	}
	asserted := depParityDrain(t, ctx, source)
	for _, relation := range depParityRelations {
		key := contextfabric.EdgeCountKey{Relation: relation}
		got, present := counts[key]
		if !present {
			t.Errorf("%s: the source did not measure the relation", relation)
			continue
		}
		if asserted[relation] == 0 {
			t.Errorf("%s: the seed asserts no edge, the comparison would be vacuous", relation)
		}
		if got != asserted[relation] {
			t.Errorf("%s: the source edge count is %d, the producer asserts %d edges", relation, got, asserted[relation])
		}
	}

	cut, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	dep("jira:D-31", "jira:D-32", "relates_to", "relates to", linked)
	item("jira:D-31", "jira", linked)
	item("jira:D-32", "jira", linked)
	item("jira:D-31", "jira", resynced)
	item("jira:D-32", "jira", resynced)
	cut.SetEndedLinksBoundForTest(1)
	cutCounts, err := cut.ProjectionSourceEdgeCounts(ctx, depParityOrg)
	if err != nil {
		t.Fatalf("ProjectionSourceEdgeCounts over a cut set: %v", err)
	}
	for _, relation := range depParityRelations {
		if _, present := cutCounts[contextfabric.EdgeCountKey{Relation: relation}]; present {
			t.Errorf("%s: a removed-link set cut at its bound must leave the relation unmeasured", relation)
		}
	}
	if _, present := cutCounts[contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToPullRequest}]; !present {
		t.Error("a cut removed-link set must not affect the other populations")
	}
}
