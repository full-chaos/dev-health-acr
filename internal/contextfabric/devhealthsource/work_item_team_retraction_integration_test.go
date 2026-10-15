package devhealthsource_test

import (
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func (f *ingestColumnsFixture) attribution(workItem, team, source string, primary uint8, computedAt time.Time) {
	mustExec(f.t, f.ctx, f.h.direct, `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES (?, ?, ?, ?, ?, ?, 'high', ?)`,
		f.h.orgID, zeroUUID, workItem, team, source, primary, computedAt)
}

func workItemTeamEdge(t *testing.T, d drained, workItem, team string) (contractsv1.ContextFabricRelationshipProjection, bool) {
	t.Helper()
	for _, r := range d.relationships[contractsv1.ContextFabricRelationshipOwnedByTeam] {
		if r.From.Kind == contractsv1.ContextFabricSubjectWorkItem && r.From.Label == workItem && r.To.Label == team {
			return r, true
		}
	}
	return contractsv1.ContextFabricRelationshipProjection{}, false
}

func relationshipTombstone(d drained, relationshipID string) (contractsv1.ContextFabricProjectionTombstone, bool) {
	for _, b := range d.batches {
		for _, ts := range b.Tombstones {
			if ts.Kind == "relationship" && ts.CanonicalID == relationshipID {
				return ts, true
			}
		}
	}
	return contractsv1.ContextFabricProjectionTombstone{}, false
}

// A work item -> team edge is projected from the rows that qualify
// (is_primary in 1, 2; active team). When the row stops qualifying, the edge
// projected earlier must be retracted by a tombstone on its own id.
func TestWorkItemTeamEdgeIsRetractedWhenTheAttributionNoLongerQualifies(t *testing.T) {
	seed := func(t *testing.T, orgID string) (*ingestColumnsFixture, drained, contractsv1.ContextFabricRelationshipProjection) {
		t.Helper()
		f := newIngestColumnsFixture(t, orgID, nil, nil)
		first := f.anchor()
		f.team("T-A", f.old, f.old)
		f.team("T-B", f.old, f.old)
		f.workItem("linear:RET-1", "", f.old, f.old)
		f.attribution("linear:RET-1", "T-A", "native_team", 1, f.now.Add(-10*time.Minute))
		f.attribution("linear:RET-1", "T-B", "project_ownership", 2, f.now.Add(-10*time.Minute))
		d := f.h.drain(first.cursor)
		edge, ok := workItemTeamEdge(t, d, "linear:RET-1", "T-A")
		if !ok {
			t.Fatal("the qualifying attribution did not project its edge")
		}
		if _, ok := workItemTeamEdge(t, d, "linear:RET-1", "T-B"); !ok {
			t.Fatal("the co-owner attribution did not project its edge")
		}
		return f, d, edge
	}

	t.Run("primary row demoted to a candidate, incremental walk", func(t *testing.T) {
		f, d, edge := seed(t, "97940000-0000-4000-8000-000000000001")
		f.attribution("linear:RET-1", "T-A", "native_team", 0, f.now)
		next := f.h.drain(d.cursor)
		ts, ok := relationshipTombstone(next, edge.RelationshipID)
		if !ok {
			t.Fatalf("no relationship tombstone for the demoted edge %s: the edge stays open in the graph", edge.RelationshipID)
		}
		if ts.EffectiveAt.Before(edge.ObservedAt) {
			t.Fatalf("tombstone effective %v is older than the edge %v, so the sink would keep the edge", ts.EffectiveAt, edge.ObservedAt)
		}
		if _, asserted := workItemTeamEdge(t, next, "linear:RET-1", "T-A"); asserted {
			t.Fatal("the demoted pair is still asserted")
		}
	})

	t.Run("co-owner removed keeps the primary edge", func(t *testing.T) {
		f, d, _ := seed(t, "97940000-0000-4000-8000-000000000002")
		coOwner, _ := workItemTeamEdge(t, d, "linear:RET-1", "T-B")
		f.attribution("linear:RET-1", "T-B", "project_ownership", 0, f.now)
		full := f.h.drain("")
		if _, ok := relationshipTombstone(full, coOwner.RelationshipID); !ok {
			t.Fatal("the removed co-owner's edge was not retracted")
		}
		primary, ok := workItemTeamEdge(t, full, "linear:RET-1", "T-A")
		if !ok {
			t.Fatal("the primary edge was lost")
		}
		if _, retracted := relationshipTombstone(full, primary.RelationshipID); retracted {
			t.Fatal("the primary edge was retracted with the co-owner")
		}
	})

	t.Run("re-qualified pair is asserted, never retracted", func(t *testing.T) {
		f, d, edge := seed(t, "97940000-0000-4000-8000-000000000004")
		f.attribution("linear:RET-1", "T-A", "native_team", 0, f.now.Add(-5*time.Minute))
		f.attribution("linear:RET-1", "T-A", "native_team", 1, f.now)
		next := f.h.drain(d.cursor)
		if _, retracted := relationshipTombstone(next, edge.RelationshipID); retracted {
			t.Fatal("a pair that qualifies again was tombstoned beside its own assertion")
		}
		if _, ok := workItemTeamEdge(t, next, "linear:RET-1", "T-A"); !ok {
			t.Fatal("the re-qualified pair was not asserted")
		}
	})
}

// A deactivated team leaves its work item -> team edges as they were (the
// behaviour before the retraction existed): the pair is read for neither an
// edge nor a tombstone, so a team that comes back finds its edges in place.
func TestWorkItemTeamEdgeSurvivesTheTeamsDeactivationAndReactivation(t *testing.T) {
	f := newIngestColumnsFixture(t, "97940000-0000-4000-8000-000000000011", nil, nil)
	f.anchor()
	f.team("T-A", f.old, f.old)
	f.workItem("linear:LC-1", "", f.old, f.old)
	f.attribution("linear:LC-1", "T-A", "native_team", 1, f.old)
	d := f.h.drain("")
	edge, ok := workItemTeamEdge(t, d, "linear:LC-1", "T-A")
	if !ok {
		t.Fatal("the qualifying attribution did not project its edge")
	}
	insertTeam := func(active uint8, at time.Time) {
		mustExec(t, f.ctx, f.h.direct, `INSERT INTO teams (id, name, description, updated_at, last_synced, org_id, provider, native_team_key, project_keys, is_active) VALUES ('T-A', 'team T-A', '', ?, ?, ?, 'linear', 'T-A', [], ?)`,
			at, at, f.h.orgID, active)
	}
	insertTeam(0, time.Now().UTC())
	gone := f.h.drain("")
	if _, retracted := relationshipTombstone(gone, edge.RelationshipID); retracted {
		t.Fatal("a deactivated team's edge was tombstoned")
	}
	if _, asserted := workItemTeamEdge(t, gone, "linear:LC-1", "T-A"); asserted {
		t.Fatal("a deactivated team's edge was asserted")
	}
	insertTeam(1, time.Now().UTC().Add(time.Second))
	back := f.h.drain("")
	if _, retracted := relationshipTombstone(back, edge.RelationshipID); retracted {
		t.Fatal("a reactivated team's edge was tombstoned")
	}
	if _, ok := workItemTeamEdge(t, back, "linear:LC-1", "T-A"); !ok {
		t.Fatal("the reactivated team's pair is not read again")
	}
}
