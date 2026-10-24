package devhealthsource_test

import (
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestLiveOwnedByTeamEdgeCountsEqualTheEdgesTheProducersAssert seeds, beside the
// groups each ownership producer projects, every class it omits, collapses or
// retracts, drains the real producers and requires the source edge count of each
// arm to equal the OWNED_BY_TEAM edges left asserted. The counts only measure
// what the producers already emit.
//
// repository: an id row, a name-resolved row, a row that resolves to nothing, a
// repository with no repos row, a closed window, two assertions of one group, an
// inactive team. project: versions that close and reopen, a key-only row, a row
// with no key, a conflicting identity and an ambiguous key (both suppressed). work
// item: a primary and a co-owner team, a candidate-only pair, two sources of one
// pair, an inactive team, an overlong id, an attribution with no work item.
func TestLiveOwnedByTeamEdgeCountsEqualTheEdgesTheProducersAssert(t *testing.T) {
	f := newIngestColumnsFixture(t, "76000000-0000-4000-8000-000000000001", nil, nil)
	seedOwnershipEdgeCases(t, f, true)
	of := &ownershipFixture{orgID: f.h.orgID, direct: f.h.direct}
	chaos7119Seed(t, f.ctx, of, f.old)
	chaos7119Team(t, f.ctx, of, "team-closed", f.old)
	mustExec(t, f.ctx, f.h.direct,
		`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.h.orgID, "github", "team-closed", chaos7119RepoK, "acme/repo-k", "exact", "native", uint8(1), uint16(100), int32(0), f.old, f.old.Add(time.Hour), f.old)
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, project_keys, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"team-off", "team-off name", "", f.old, f.h.orgID, "github", "team-off", []string{}, uint8(0))
	chaos7119Own(t, f.ctx, of, "team-off", "github", "acme/repo-k", chaos7119RepoK, f.old)

	at := f.hourAgo
	long := strings.Repeat("W", 300)
	item := func(id string) {
		mustExec(t, f.ctx, f.h.direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced, ingested_at) VALUES (?, ?, ?, 'issue', 'open', '', '', 'linear', '', ?, ?, ?)`,
			id, zeroUUID, f.h.orgID, f.old, f.old, f.old)
	}
	attribute := func(workItem, team, source string, primary uint8) {
		mustExec(t, f.ctx, f.h.direct, `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES (?, ?, ?, ?, ?, ?, 'high', ?)`,
			f.h.orgID, zeroUUID, workItem, team, source, primary, at)
	}
	for _, id := range []string{"linear:OWN-1", "linear:OWN-2", "linear:OWN-3", "linear:OWN-5", long} {
		item(id)
	}
	attribute("linear:OWN-1", "T1", "native_team", 1)
	attribute("linear:OWN-1", "T2", "project_ownership", 2)
	attribute("linear:OWN-2", "T1", "assignee_membership", 0)
	attribute("linear:OWN-3", "T1", "native_team", 1)
	attribute("linear:OWN-3", "T1", "linked_issue", 1)
	attribute("linear:OWN-5", "team-off", "native_team", 1)
	attribute(long, "T1", "native_team", 1)
	attribute("linear:GHOST-1", "T1", "native_team", 1)

	counter, ok := f.h.src.(contextfabric.ProjectionSourceEdgeCounts)
	if !ok {
		t.Fatal("the teams and projects source reports edge counts")
	}
	counts, err := counter.ProjectionSourceEdgeCounts(f.ctx, f.h.orgID)
	if err != nil {
		t.Fatalf("ProjectionSourceEdgeCounts: %v", err)
	}
	d := f.h.drain("")
	edges := map[string]contractsv1.ContextFabricRelationshipProjection{}
	for _, batch := range d.batches {
		for _, r := range batch.Relationships {
			if r.Type == contractsv1.ContextFabricRelationshipOwnedByTeam {
				edges[r.RelationshipID] = r
			}
		}
		for _, tomb := range batch.Tombstones {
			if tomb.Kind == "relationship" {
				delete(edges, tomb.CanonicalID)
			}
		}
	}
	got := map[string]int64{}
	for _, edge := range edges {
		got[string(edge.From.Kind)]++
	}
	for _, arm := range []string{string(contractsv1.ContextFabricSubjectWorkItem), string(contractsv1.ContextFabricSubjectProject), string(contractsv1.ContextFabricSubjectRepository)} {
		key := contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipOwnedByTeam, Arm: arm}
		if got[arm] == 0 {
			t.Errorf("%s: the seed asserts no edge, the comparison would be vacuous", arm)
		}
		if counts[key] != got[arm] {
			t.Errorf("OWNED_BY_TEAM %s: the source edge count is %d, the producer asserts %d edges", arm, counts[key], got[arm])
		}
	}
	if len(got) != 3 {
		t.Errorf("OWNED_BY_TEAM edges from other kinds: %v", got)
	}
}
