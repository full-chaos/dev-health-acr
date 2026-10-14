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

// A blocks link the provider removed leaves its work_item_dependencies row
// behind with its old last_synced; only a later sync of an item that writes the
// link shows it gone. The producer must not project such a row as an edge, and
// an edge projected before the link went must be retracted. Each case runs on
// real ClickHouse through the production statement, per provider shape of the
// writer: jira and linear write a native link from both items, github reads
// the link from the text of one item, gitlab from the description of one item
// or, for a raw value it cannot attribute, from either.
type removedLinkCase struct {
	name       string
	provider   string
	source     string
	target     string
	raw        string
	removed    bool
	syncSource bool
	syncTarget bool
}

func TestRemovedBlockerLinkIsRetractedAndNotProjected(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	linked := now.Add(-5 * time.Hour)
	resynced := now.Add(-time.Hour)

	cases := []removedLinkCase{
		{name: "jira native link removed, either item synced", provider: "jira", source: "jira:PRJ-1", target: "jira:PRJ-2", raw: "blocks", removed: true, syncTarget: true},
		{name: "jira native link kept, rewritten by the later sync", provider: "jira", source: "jira:PRJ-3", target: "jira:PRJ-4", raw: "blocks", removed: false},
		{name: "linear native link removed", provider: "linear", source: "linear:LIN-1", target: "linear:LIN-2", raw: "blocks", removed: true, syncSource: true},
		{name: "linear link whose items were not synced since stays", provider: "linear", source: "linear:LIN-3", target: "linear:LIN-4", raw: "blocks", removed: false},
		{name: "github text 'blocks' removed, blocker synced", provider: "github", source: "gh:o/r#1", target: "gh:o/r#2", raw: "blocks #2", removed: true, syncSource: true},
		{name: "github text 'blocks' stays when only the blocked item is synced", provider: "github", source: "gh:o/r#3", target: "gh:o/r#4", raw: "blocks #4", removed: false, syncTarget: true},
		{name: "github text 'blocked by' removed, blocked item synced", provider: "github", source: "gh:o/r#6", target: "gh:o/r#5", raw: "blocked by #6", removed: true, syncTarget: true},
		{name: "gitlab 'blocking' removed, blocker synced", provider: "gitlab", source: "gitlab:g/p#1", target: "gitlab:g/p#2", raw: "blocking", removed: true, syncSource: true},
		{name: "gitlab 'blocks' with an undecided writer stays when one item is synced", provider: "gitlab", source: "gitlab:g/p#3", target: "gitlab:g/p#4", raw: "blocks", removed: false, syncTarget: true},
		{name: "gitlab 'blocks' with an undecided writer ends when both items are synced", provider: "gitlab", source: "gitlab:g/p#5", target: "gitlab:g/p#6", raw: "blocks", removed: true, syncSource: true, syncTarget: true},
	}

	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			orgID := fmt.Sprintf("95140000-0000-4000-8000-%012d", index+1)
			repoID := fmt.Sprintf("95140000-0000-4000-8000-%012d", 1000+index)
			mustExec(t, ctx, direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`, repoID, fmt.Sprintf("acme/removed-link-%d", index), nil, now, nil, linked, orgID, tc.provider)
			insertItem := func(id string, at time.Time) {
				mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					id, repoID, orgID, "issue "+id, "open", "", "", tc.provider, "", at, at)
			}
			insertItem(tc.source, linked)
			insertItem(tc.target, linked)
			mustExec(t, ctx, direct, `INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, org_id, last_synced) VALUES (?, ?, 'blocks', ?, ?, ?)`, tc.source, tc.target, tc.raw, orgID, linked)

			source, err := devhealthsource.NewClickHouseProjectionSource(query)
			if err != nil {
				t.Fatal(err)
			}
			drain := func(cursor string) (string, map[string]bool, map[string]bool) {
				edges, tombstones := map[string]bool{}, map[string]bool{}
				for i := 0; i < 60; i++ {
					batch, ok, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: devhealthsource.SourceName, Cursor: cursor})
					if err != nil {
						t.Fatalf("NextProjectionBatch: %v", err)
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
			edgeID := workItemDependencyRelationshipID(t, repoID, tc.source, repoID, tc.target, "blocks")

			cursor, edges, _ := drain("")
			if !edges[edgeID] {
				t.Fatalf("the link was not projected before the provider removed it: %v", edges)
			}

			if tc.syncSource {
				insertItem(tc.source, resynced)
			}
			if tc.syncTarget {
				insertItem(tc.target, resynced)
			}
			if tc.name == "jira native link kept, rewritten by the later sync" {
				insertItem(tc.source, resynced)
				mustExec(t, ctx, direct, `INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, org_id, last_synced) VALUES (?, ?, 'blocks', ?, ?, ?)`, tc.source, tc.target, tc.raw, orgID, resynced)
			}

			_, _, retracted := drain(cursor)
			if got := retracted[edgeID]; got != tc.removed {
				t.Fatalf("incremental pass retracted=%v, want %v (removed link must be retracted, a current one never)", got, tc.removed)
			}

			_, rebuilt, _ := drain("")
			if got := rebuilt[edgeID]; got == tc.removed {
				t.Fatalf("rebuild projected the edge=%v, want %v", got, !tc.removed)
			}
		})
	}
}
