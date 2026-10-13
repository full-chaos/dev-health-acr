package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Two spellings of one relation (a "blocks" row and the inverse "blocked_by"
// row for the same pair) converge on ONE graph edge. When the provider removes
// the link both rows normally go with it; here the inverse row is still
// reported, so the relation is live and its edge must stay: the removed
// "blocks" row must not retract it, in an incremental pass or in a rebuild.
func TestRemovedBlockerLinkWithALiveInverseSpellingKeepsItsEdge(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	const (
		org  = "95140000-0000-4000-8000-0000000000d1"
		repo = "95140000-0000-4000-8000-0000000000d2"
	)
	now := time.Now().UTC().Truncate(time.Second)
	linked := now.Add(-5 * time.Hour)
	resynced := now.Add(-time.Hour)
	mustExec(t, ctx, direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`, repo, "acme/twin", nil, now, nil, linked, org, "jira")
	insertItem := func(id string, at time.Time) {
		mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, repo, org, "issue "+id, "open", "", "", "jira", "", at, at)
	}
	insertRow := func(source, target, relationshipType string) {
		mustExec(t, ctx, direct, `INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, org_id, last_synced) VALUES (?, ?, ?, ?, ?, ?)`, source, target, relationshipType, relationshipType, org, linked)
	}
	const blocker, blocked = "jira:TWIN-1", "jira:TWIN-2"
	insertItem(blocker, linked)
	insertItem(blocked, linked)
	insertRow(blocker, blocked, "blocks")
	insertRow(blocked, blocker, "blocked_by")

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	drain := func(cursor string) (string, map[string]bool, map[string]bool) {
		edges, tombstones := map[string]bool{}, map[string]bool{}
		for i := 0; i < 60; i++ {
			batch, ok, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: org, Source: devhealthsource.SourceName, Cursor: cursor})
			if err != nil {
				t.Fatalf("NextProjectionBatch: %v%s", err, rawCauses(err))
			}
			if !ok {
				return cursor, edges, tombstones
			}
			cursor = batch.NextCursor
			for _, r := range batch.Relationships {
				if r.Type == contractsv1.ContextFabricRelationshipBlocks {
					edges[r.RelationshipID] = true
				}
			}
			for _, tomb := range batch.Tombstones {
				if tomb.Kind == "relationship" {
					tombstones[tomb.CanonicalID] = true
				}
			}
		}
		t.Fatal("did not converge")
		return "", nil, nil
	}
	edgeID := workItemDependencyRelationshipID(t, repo, blocker, repo, blocked, "blocks")

	cursor, edges, _ := drain("")
	if !edges[edgeID] {
		t.Fatalf("the relation was not projected: %v", edges)
	}
	insertItem(blocker, resynced)
	insertItem(blocked, resynced)
	_, _, retracted := drain(cursor)
	if retracted[edgeID] {
		t.Fatal("the removed blocks row retracted the edge its live inverse-spelling row still states")
	}
	_, rebuilt, _ := drain("")
	if !rebuilt[edgeID] {
		t.Fatalf("a rebuild dropped the edge its live inverse-spelling row states: %v", rebuilt)
	}
}
