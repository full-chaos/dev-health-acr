package devhealthsource_test

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// The producer reads an ended blocks link as a row flagged ended (the shared
// relation-current rule runs in the statement). It must retract the edge under
// the ids the asserting branch derives, in both spellings of the target, and
// assert nothing for it.
func TestEndedBlockerLinkRowIsRetractedNotAsserted(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	endedAt := at.Add(2 * time.Hour)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tables := baseTables(at)
	for index, table := range tables {
		if table.match == "FROM work_item_dependencies AS d" {
			tables[index].rows = [][]any{{"WIDGET-101", "WIDGET-099", "blocks", "repo-1", "example-org/widget-service", at,
				created, uint8(0), zeroTime, uint8(1), created, uint8(0), zeroTime, "repo-1", uint8(1), endedAt, endedAt}}
			continue
		}
		tables[index].rows = nil
	}
	batch := projectOneBatch(t, tables)

	resolved := workItemDependencyRelationshipID(t, "repo-1", "WIDGET-101", "repo-1", "WIDGET-099", "blocks")
	for _, relationship := range batch.Relationships {
		if relationship.RelationshipID == resolved {
			t.Fatalf("an ended link was asserted as an edge: %+v", relationship)
		}
	}
	source, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", "WIDGET-101"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	refID, refOmitted := identity.DeriveWorkItemRef("WIDGET-099", nil)
	if refOmitted {
		t.Fatal("ref id omitted")
	}
	refEdge := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency, source, refID, "blocks")

	seen := map[string]string{}
	for _, tomb := range batch.Tombstones {
		seen[tomb.CanonicalID] = tomb.Kind
		if !tomb.EffectiveAt.Equal(endedAt) {
			t.Fatalf("tombstone %q effective at %v, want the time the link ended %v", tomb.CanonicalID, tomb.EffectiveAt, endedAt)
		}
	}
	for id, kind := range map[string]string{resolved: "relationship", refEdge: "relationship", refID: "work_item_ref"} {
		if seen[id] != kind {
			t.Fatalf("no %s tombstone for %q; tombstones: %v", kind, id, seen)
		}
	}
}
