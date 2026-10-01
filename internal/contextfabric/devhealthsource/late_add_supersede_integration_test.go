package devhealthsource_test

import (
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func (f *ingestColumnsFixture) removal(subject, fromProject, fromKey, event string, occurred, ingested time.Time) {
	mustExec(f.t, f.ctx, f.h.direct, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at) VALUES (?, NULL, ?, 'work_item', ?, 'linear', ?, '', ?, '', '', ?, ?, ?, ?)`,
		f.h.orgID, zeroUUID, subject, fromProject, fromKey, occurred, occurred, event, ingested)
}

// graphEdges replays drained batches the way the graph applies them: each
// batch writes its relationships by id, then deletes its tombstoned ids.
func graphEdges(ds ...drained) map[string]contractsv1.ContextFabricRelationshipProjection {
	edges := map[string]contractsv1.ContextFabricRelationshipProjection{}
	for _, d := range ds {
		for _, b := range d.batches {
			for _, r := range b.Relationships {
				if r.Type == contractsv1.ContextFabricRelationshipBelongsToProject {
					edges[r.RelationshipID] = r
				}
			}
			for _, tomb := range b.Tombstones {
				// The adapter deletes a relationship tombstone only when the
				// stored observed_at is at or before EffectiveAt.
				if tomb.Kind != "relationship" || tomb.Reason != "superseded_by_earlier_add" {
					panic("unexpected tombstone " + tomb.Kind + "/" + tomb.Reason)
				}
				if e, ok := edges[tomb.CanonicalID]; ok && !e.ObservedAt.After(tomb.EffectiveAt) {
					delete(edges, tomb.CanonicalID)
				}
			}
		}
	}
	return edges
}

func openEdges(edges map[string]contractsv1.ContextFabricRelationshipProjection) []contractsv1.ContextFabricRelationshipProjection {
	var open []contractsv1.ContextFabricRelationshipProjection
	for _, e := range edges {
		if e.ValidTo == nil {
			open = append(open, e)
		}
	}
	return open
}

func lateAddFixture(t *testing.T, org string) (*ingestColumnsFixture, time.Time, time.Time) {
	f := newIngestColumnsFixture(t, org, nil, nil)
	recent := f.now.Add(-10 * time.Minute)
	f.project("P-late", "LATE", recent, recent)
	f.team("T-anchor", recent, recent)
	return f, f.old, f.old.Add(time.Hour)
}

func TestLateEarlierAddLeavesOneOpenInterval(t *testing.T) {
	f, earlier, later := lateAddFixture(t, "73950000-0000-4000-8000-000000000001")
	f.transition("WI-a", "P-late", "LATE", "evt-add-2", later, f.now.Add(-10*time.Minute))
	first := f.h.drain("")
	if n := len(openEdges(graphEdges(first))); n != 1 {
		t.Fatalf("before the late ADD: %d open intervals, want 1", n)
	}
	f.transition("WI-a", "P-late", "LATE", "evt-add-1", earlier, f.now)
	second := f.h.drain(first.cursor)
	open := openEdges(graphEdges(first, second))
	if len(open) != 1 {
		t.Fatalf("after the late earlier ADD: %d open intervals, want 1", len(open))
	}
	if got := open[0].ValidFrom; got == nil || !got.Equal(earlier) {
		t.Fatalf("open interval starts %v, want the earlier ADD %v", got, earlier)
	}
}

func TestOrderedAddsLeaveOneOpenInterval(t *testing.T) {
	f, earlier, later := lateAddFixture(t, "73950000-0000-4000-8000-000000000002")
	recent := f.now.Add(-10 * time.Minute)
	f.transition("WI-a", "P-late", "LATE", "evt-add-1", earlier, recent)
	f.transition("WI-a", "P-late", "LATE", "evt-add-2", later, recent)
	open := openEdges(graphEdges(f.h.drain("")))
	if len(open) != 1 || open[0].ValidFrom == nil || !open[0].ValidFrom.Equal(earlier) {
		t.Fatalf("ordered ADDs: %d open intervals %v, want 1 starting %v", len(open), open, earlier)
	}
}

func TestLateRemoveAfterAddStillClosesTheInterval(t *testing.T) {
	f, earlier, later := lateAddFixture(t, "73950000-0000-4000-8000-000000000003")
	recent := f.now.Add(-10 * time.Minute)
	f.transition("WI-a", "P-late", "LATE", "evt-add-1", earlier, recent)
	first := f.h.drain("")
	f.removal("WI-a", "P-late", "LATE", "evt-rm-1", later, f.now)
	second := f.h.drain(first.cursor)
	edges := graphEdges(first, second)
	if len(edges) != 1 || len(openEdges(edges)) != 0 {
		t.Fatalf("late REMOVE: %d edges, %d open, want 1 closed", len(edges), len(openEdges(edges)))
	}
	for _, e := range edges {
		if e.ValidTo == nil || !e.ValidTo.Equal(later) {
			t.Fatalf("interval closes %v, want %v", e.ValidTo, later)
		}
	}
}
