package devhealthsource_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// One removed link more than the set bound (lowered for the test): the set keeps the newest removals, so
// the newest link is retracted and the oldest keeps its edge (disclosed by the
// cut line, and recoverable by raising the bound and rebuilding).
func TestRemovedLinkSetOverTheBoundKeepsTheNewestRemovals(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	const (
		org  = "95140000-0000-4000-8000-0000000000b1"
		repo = "95140000-0000-4000-8000-0000000000b2"
	)
	const bound = 40
	links := bound + 1
	at := time.Now().UTC().Truncate(time.Second).Add(-8 * time.Hour)
	mustExec(t, ctx, direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`, repo, "acme/bound", nil, at, nil, at, org, "jira")
	mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced)
SELECT concat('jira:B-', toString(number)), ?, ?, 'issue', 'open', '', '', 'jira', '', ?, ? FROM numbers(?)`, repo, org, at, at, uint64(2*links))
	mustExec(t, ctx, direct, `INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, org_id, last_synced)
SELECT concat('jira:B-', toString(2 * number)), concat('jira:B-', toString(2 * number + 1)), 'blocks', 'blocks', ?, ? + toIntervalSecond(1) FROM numbers(?)`, org, at, uint64(links))

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	source.SetEndedLinksBoundForTest(bound)
	drain := func(cursor string) (string, map[string]bool, map[string]bool) {
		edges, tombstones := map[string]bool{}, map[string]bool{}
		for i := 0; i < 1500; i++ {
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
	cursor, _, _ := drain("")
	// Every link is removed: each blocker syncs again, link n one second after link n-1.
	mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced)
SELECT concat('jira:B-', toString(number)), ?, ?, 'issue', 'open', '', '', 'jira', '', ?, ? + toIntervalHour(2) + toIntervalSecond(intDiv(number, 2)) FROM numbers(?)`, repo, org, at, at, uint64(2*links))
	_, _, retracted := drain(cursor)
	id := func(n int) string {
		return workItemDependencyRelationshipID(t, repo, fmt.Sprintf("jira:B-%d", 2*n), repo, fmt.Sprintf("jira:B-%d", 2*n+1), "blocks")
	}
	if !retracted[id(links-1)] {
		t.Fatal("the newest removed link kept its edge")
	}
	if retracted[id(0)] {
		t.Fatal("the oldest removed link, beyond the bound, was retracted: the cut did not keep the newest removals")
	}
	_, rebuilt, _ := drain("")
	if !rebuilt[id(0)] || rebuilt[id(links-1)] {
		t.Fatalf("after a rebuild the oldest link must keep its edge (%v) and the newest must not (%v)", rebuilt[id(0)], rebuilt[id(links-1)])
	}
}
