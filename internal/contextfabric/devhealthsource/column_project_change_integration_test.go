package devhealthsource_test

import (
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// columnGraph replays drained batches the way the graph applies them, for any
// tombstone reason, and returns the BELONGS_TO_PROJECT edges that stay open.
func columnGraph(ds ...drained) map[string]contractsv1.ContextFabricRelationshipProjection {
	edges := map[string]contractsv1.ContextFabricRelationshipProjection{}
	for _, d := range ds {
		for _, b := range d.batches {
			for _, r := range b.Relationships {
				if r.Type == contractsv1.ContextFabricRelationshipBelongsToProject {
					edges[r.RelationshipID] = r
				}
			}
			for _, tomb := range b.Tombstones {
				if e, ok := edges[tomb.CanonicalID]; ok && !e.ObservedAt.After(tomb.EffectiveAt) {
					delete(edges, tomb.CanonicalID)
				}
			}
		}
	}
	return edges
}

func openColumnEdgesOf(edges map[string]contractsv1.ContextFabricRelationshipProjection, label string) []contractsv1.ContextFabricRelationshipProjection {
	var out []contractsv1.ContextFabricRelationshipProjection
	for _, e := range edges {
		if e.From.Label == label && e.ValidFrom == nil && e.ValidTo == nil {
			out = append(out, e)
		}
	}
	return out
}

func TestColumnProjectChangeLeavesOneOwnerPerWorkItem(t *testing.T) {
	t.Run("column project moves from A to B", func(t *testing.T) {
		f := newIngestColumnsFixture(t, "88610000-0000-4000-8000-000000000001", nil, nil)
		f.project("P-a", "A", f.old, f.old)
		f.project("P-b", "B", f.old, f.old)
		f.workItem("WI-move", "P-a", f.old, f.old)
		first := f.h.drain("")
		if n := len(openColumnEdgesOf(columnGraph(first), "WI-move")); n != 1 {
			t.Fatalf("before the change: %d open column edges, want 1", n)
		}
		later := f.now.Add(-time.Minute)
		f.workItem("WI-move", "P-b", later, later)
		second := f.h.drain(first.cursor)
		open := openColumnEdgesOf(columnGraph(first, second), "WI-move")
		if len(open) != 1 {
			t.Fatalf("after the column project changed A to B: %d open column edges for the work item, want 1 (the edge to B)", len(open))
		}
		if open[0].To.Label != "P-b" {
			t.Fatalf("the open edge points at %q, want P-b", open[0].To.Label)
		}
	})

	t.Run("column project moves from A to a key that is ambiguous", func(t *testing.T) {
		f := newIngestColumnsFixture(t, "88610000-0000-4000-8000-000000000002", nil, nil)
		f.project("P-a", "A", f.old, f.old)
		f.project("P-x1", "SHARED", f.old, f.old)
		f.project("P-x2", "SHARED", f.old, f.old)
		f.workItem("WI-move", "P-a", f.old, f.old)
		first := f.h.drain("")
		if n := len(openColumnEdgesOf(columnGraph(first), "WI-move")); n != 1 {
			t.Fatalf("before the change: %d open column edges, want 1", n)
		}
		later := f.now.Add(-time.Minute)
		f.workItem("WI-move", "SHARED", later, later)
		second := f.h.drain(first.cursor)
		if open := openColumnEdgesOf(columnGraph(first, second), "WI-move"); len(open) != 0 {
			t.Fatalf("the column project is now ambiguous: %d open column edges remain for the work item, want 0 (no edge to the old project)", len(open))
		}
	})
}
