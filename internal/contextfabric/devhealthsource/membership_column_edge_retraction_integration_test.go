package devhealthsource_test

import (
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A work item whose project came from the column is projected as a
// column-arm edge. When the same work item later gains transition history the
// presence view drops its column row, so the column edge must be retracted:
// one owner per subject at any instant. The retraction is keyed on the
// subject, whatever projects its transitions name.
func TestColumnArmEdgeIsRetractedWhenTheSubjectGainsTransitionHistory(t *testing.T) {
	cases := []struct {
		name, orgID, transitionProject, transitionKey string
	}{
		{"history names the column project", "76790000-0000-4000-8000-000000000001", "P-late", "LATE"},
		{"history names only another project", "76790000-0000-4000-8000-000000000002", "P-other", "OTHER"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newIngestColumnsFixture(t, c.orgID, nil, nil)
			f.project("P-other", "OTHER", f.hourAgo, f.hourAgo)
			first := f.anchor()
			f.workItem("WI-gains", "P-late", f.old, f.now)
			second := f.h.drain(first.cursor)
			var columnID string
			for _, r := range second.relationships[contractsv1.ContextFabricRelationshipBelongsToProject] {
				if r.ValidFrom == nil {
					columnID = r.RelationshipID
				}
			}
			if columnID == "" {
				t.Fatal("the column-arm edge was never projected; the retraction below would pass vacuously")
			}

			f.transition("WI-gains", c.transitionProject, c.transitionKey, "evt-gains", f.old, f.now.Add(time.Minute))
			third := f.h.drain(second.cursor)
			interval := 0
			for _, r := range third.relationships[contractsv1.ContextFabricRelationshipBelongsToProject] {
				if r.ValidFrom != nil {
					interval++
				}
			}
			if interval != 1 {
				t.Fatalf("want the new transition interval projected once, got %d", interval)
			}
			retracted := false
			for _, b := range third.batches {
				if hasTombstone(b, columnID) {
					retracted = true
				}
			}
			if !retracted {
				t.Fatalf("column-arm edge %q stays open beside the transition interval: two open memberships for one subject", columnID)
			}
		})
	}
}

// A work item with a column project and NO transition history keeps its
// column edge: nothing is retracted.
func TestColumnArmEdgeIsKeptWithoutTransitionHistory(t *testing.T) {
	f := newIngestColumnsFixture(t, "76790000-0000-4000-8000-000000000003", nil, nil)
	first := f.anchor()
	f.workItem("WI-plain", "P-late", f.old, f.now)
	second := f.h.drain(first.cursor)
	for _, b := range second.batches {
		for _, tombstone := range b.Tombstones {
			if tombstone.Reason == "superseded_by_transition_history" {
				t.Fatalf("a work item without history had its column edge retracted: %+v", tombstone)
			}
		}
	}
	if n := relationshipsOfType(second, contractsv1.ContextFabricRelationshipBelongsToProject); n != 1 {
		t.Fatalf("want the column edge, got %d BELONGS_TO_PROJECT edges", n)
	}
}
