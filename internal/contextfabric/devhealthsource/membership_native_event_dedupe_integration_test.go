package devhealthsource_test

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-7829: one provider-native event id stored at two occurred_at values
// (the provider moved the event's timestamp between two syncs) is ONE event.
// The reader keeps the copy with the latest last_synced (then ingested_at, then
// occurred_at). Each test asserts the STATE the reader projects.

// nativeTouch inserts one transition row with its own last_synced.
func (f *ingestColumnsFixture) nativeTouch(subject, from, to, event string, occurred, lastSynced time.Time) {
	mustExec(f.t, f.ctx, f.h.direct, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at) VALUES (?, NULL, ?, 'work_item', ?, 'linear', ?, ?, '', '', '', ?, ?, ?, ?)`,
		f.h.orgID, zeroUUID, subject, from, to, occurred, lastSynced, event, lastSynced)
}

func belongsTo(d drained) []contractsv1.ContextFabricRelationshipProjection {
	return d.relationships[contractsv1.ContextFabricRelationshipBelongsToProject]
}

func TestMembershipReaderKeepsOneCopyOfANativeEventStoredAtTwoOccurredAt(t *testing.T) {
	t.Run("ADD: the copy with the later last_synced wins, not the later occurred_at", func(t *testing.T) {
		f := newIngestColumnsFixture(t, "78290000-0000-4000-8000-000000000001", nil, nil)
		f.project("P-a", "A", f.old, f.old)
		f.team("T-a", f.old, f.old)
		first := f.old.Add(time.Hour)
		moved := first.Add(145 * time.Second)
		// The earlier occurred_at was written by the LATER sync: it wins.
		f.nativeTouch("WI-dup", "", "P-a", "linear:H1", moved, f.old)
		f.nativeTouch("WI-dup", "", "P-a", "linear:H1", first, f.hourAgo)
		edges := belongsTo(f.h.drain(""))
		if len(edges) != 1 {
			t.Fatalf("%d BELONGS_TO_PROJECT edges, want 1", len(edges))
		}
		if edges[0].ValidFrom == nil || !edges[0].ValidFrom.Equal(first) {
			t.Fatalf("interval opens at %v, want %v (the copy with the latest last_synced)", edges[0].ValidFrom, first)
		}
	})

	t.Run("ADD: the re-synced later copy replaces the earlier one (the shape measured on prod)", func(t *testing.T) {
		f := newIngestColumnsFixture(t, "78290000-0000-4000-8000-000000000004", nil, nil)
		f.project("P-a", "A", f.old, f.old)
		f.team("T-a", f.old, f.old)
		first := f.old.Add(time.Hour)
		moved := first.Add(145 * time.Second)
		f.nativeTouch("WI-moved", "", "P-a", "linear:H4", first, f.old)
		f.nativeTouch("WI-moved", "", "P-a", "linear:H4", moved, f.hourAgo)
		edges := belongsTo(f.h.drain(""))
		if len(edges) != 1 {
			t.Fatalf("%d BELONGS_TO_PROJECT edges, want 1", len(edges))
		}
		if edges[0].ValidFrom == nil || !edges[0].ValidFrom.Equal(moved) {
			t.Fatalf("interval opens at %v, want %v (the re-synced copy; the stale copy must not open it)", edges[0].ValidFrom, moved)
		}
	})

	t.Run("exactly one statement row is read for the replayed event, the latest last_synced", func(t *testing.T) {
		var logs bytes.Buffer
		f := newIngestColumnsFixture(t, "78290000-0000-4000-8000-000000000005", nil, slog.New(slog.NewJSONHandler(&logs, nil)))
		f.project("P-a", "A", f.old, f.old)
		f.team("T-a", f.old, f.old)
		first := f.old.Add(time.Hour)
		moved := first.Add(1993 * time.Second)
		f.nativeTouch("WI-row", "", "P-a", "linear:H5", first, f.old)
		f.nativeTouch("WI-row", "", "P-a", "linear:H5", moved, f.hourAgo)
		got := drainMemberships(t, f, nil)
		if len(got.transition) != 1 {
			t.Fatalf("%d transition edges, want 1", len(got.transition))
		}
		consumed := 0
		for _, line := range pageLines(t, &logs)["transition"] {
			consumed += line.consumed
		}
		if consumed != 1 {
			t.Fatalf("the pages consumed %d transition statement rows, want 1 (the interval row of the latest last_synced copy; no duplicate-ADD row)", consumed)
		}
	})

	t.Run("REMOVE: a native REMOVE stored twice closes the interval once, at the winning copy", func(t *testing.T) {
		f := newIngestColumnsFixture(t, "78290000-0000-4000-8000-000000000002", nil, nil)
		f.project("P-a", "A", f.old, f.old)
		f.team("T-a", f.old, f.old)
		add := f.old.Add(time.Hour)
		remove := add.Add(time.Hour)
		moved := remove.Add(157 * time.Second)
		f.nativeTouch("WI-gone", "", "P-a", "linear:H2", add, f.old)
		f.nativeTouch("WI-gone", "P-a", "", "linear:H3", remove, f.old)
		f.nativeTouch("WI-gone", "P-a", "", "linear:H3", moved, f.hourAgo)
		edges := belongsTo(f.h.drain(""))
		if len(edges) != 1 {
			t.Fatalf("%d BELONGS_TO_PROJECT edges, want 1", len(edges))
		}
		if edges[0].ValidTo == nil || !edges[0].ValidTo.Equal(moved) {
			t.Fatalf("interval closes at %v, want %v (the REMOVE copy with the latest last_synced)", edges[0].ValidTo, moved)
		}
	})

	t.Run("two distinct native events of one subject stay two", func(t *testing.T) {
		f := newIngestColumnsFixture(t, "78290000-0000-4000-8000-000000000003", nil, nil)
		f.project("P-a", "A", f.old, f.old)
		f.project("P-b", "B", f.old, f.old)
		f.team("T-a", f.old, f.old)
		at := f.old.Add(time.Hour)
		f.nativeTouch("WI-two", "", "P-a", "linear:E1", at, f.old)
		f.nativeTouch("WI-two", "", "P-b", "linear:E2", at, f.old)
		if edges := belongsTo(f.h.drain("")); len(edges) != 2 {
			t.Fatalf("%d BELONGS_TO_PROJECT edges, want 2 (distinct event ids are distinct events)", len(edges))
		}
	})
}
