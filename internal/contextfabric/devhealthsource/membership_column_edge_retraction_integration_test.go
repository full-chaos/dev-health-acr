package devhealthsource_test

import (
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A work item whose project came from the column is projected as a
// column-arm edge. When the same work item later gains transition history the
// presence view drops its column row, so the column edge must be retracted:
// one open membership per subject.
func TestColumnArmEdgeIsRetractedWhenTheSubjectGainsTransitionHistory(t *testing.T) {
	f := newIngestColumnsFixture(t, "76790000-0000-4000-8000-000000000001", nil, nil)
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

	f.transition("WI-gains", "P-late", "LATE", "evt-gains", f.old, f.now.Add(time.Minute))
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
}
