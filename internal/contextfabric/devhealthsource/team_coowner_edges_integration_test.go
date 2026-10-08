package devhealthsource_test

import (
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Every work item below carries a primary row for T-A and a co-owner row
// (is_primary = 2) for T-B on ONE computed_at, plus one lone T-A row that sorts
// first, so a page cut falls between the primary and the co-owner row of a
// pair. Each pair projects an edge to each team and no page skips one.
func TestWorkItemTeamEdgesIncludeCoOwnersAcrossPageCuts(t *testing.T) {
	const pairs = 450
	f := newIngestColumnsFixture(t, "89300000-0000-4000-8000-000000000001", nil, nil)
	f.team("T-A", f.old, f.old)
	f.team("T-B", f.old, f.old)
	at := f.hourAgo
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced, ingested_at)
SELECT if(number = 0, 'linear:A-lone', concat('linear:B-', leftPad(toString(number), 4, '0'))), ?, ?, 'issue', 'open', '', '', 'linear', '', ?, ?, ? FROM numbers(?)`,
		zeroUUID, f.h.orgID, f.old, f.old, f.old, uint64(pairs+1))
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at)
SELECT ?, ?, if(number = 0, 'linear:A-lone', concat('linear:B-', leftPad(toString(number), 4, '0'))), 'T-A', 'native_team', 1, 'high', ? FROM numbers(?)`,
		f.h.orgID, zeroUUID, at, uint64(pairs+1))
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at)
SELECT ?, ?, concat('linear:B-', leftPad(toString(number), 4, '0')), 'T-B', 'project_ownership', 2, 'high', ? FROM numbers(1, ?)`,
		f.h.orgID, zeroUUID, at, uint64(pairs))

	edges := map[string]bool{}
	cursor := ""
	for page := 0; ; page++ {
		if page > 400 {
			t.Fatal("the drain did not converge")
		}
		b, ok, err := f.h.src.NextProjectionBatch(f.ctx, contextfabric.ProjectionCheckpoint{OrgID: f.h.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if !ok {
			break
		}
		cursor = b.NextCursor
		for _, r := range b.Relationships {
			if r.Type == contractsv1.ContextFabricRelationshipOwnedByTeam && r.From.Kind == contractsv1.ContextFabricSubjectWorkItem {
				edges[r.From.Label+"->"+r.To.Label] = true
			}
		}
	}
	if want := 1 + 2*pairs; len(edges) != want {
		t.Fatalf("work item -> team edges = %d, want %d (one per attribution row, none skipped by a page cut)", len(edges), want)
	}
	for n := 1; n <= pairs; n++ {
		id := fmt.Sprintf("linear:B-%04d", n)
		if !edges[id+"->T-A"] || !edges[id+"->T-B"] {
			t.Fatalf("%s: edges to T-A=%v, T-B=%v, want both", id, edges[id+"->T-A"], edges[id+"->T-B"])
		}
	}
}
