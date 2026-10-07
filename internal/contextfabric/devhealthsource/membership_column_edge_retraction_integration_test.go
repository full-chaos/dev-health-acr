package devhealthsource_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
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

// Many subjects, each with a column project and several transition intervals,
// read over several page cuts: every subject gets exactly one retraction of its
// column edge per batch it appears in (the batch validator rejects a duplicate
// tombstone, so a read that fanned a subject out per transition row would fail
// the whole projection), and every column edge id is retracted.
func TestColumnArmRetractionIsOnePerSubjectAcrossPageCuts(t *testing.T) {
	const subjects = 450
	f := newIngestColumnsFixture(t, "76790000-0000-4000-8000-000000000004", nil, nil)
	for _, id := range []string{"P-a", "P-b", "P-c"} {
		f.project(id, "K-"+id, f.old, f.old)
	}
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced, ingested_at)
SELECT concat('WS-', leftPad(toString(number), 4, '0')), ?, ?, 'issue', 'open', '', '', 'linear', 'P-a', ?, ?, ? FROM numbers(?)`,
		zeroUUID, f.h.orgID, f.old, f.old, f.hourAgo, uint64(subjects))
	for i, project := range []string{"P-a", "P-b", "P-c"} {
		mustExec(t, f.ctx, f.h.direct, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at)
SELECT ?, NULL, ?, 'work_item', concat('WS-', leftPad(toString(number), 4, '0')), 'linear', '', ?, '', '', '', ?, ?, concat('evt-', ?, '-', toString(number)), ? FROM numbers(?)`,
			f.h.orgID, zeroUUID, project, f.old.Add(time.Duration(i)*time.Minute), f.old, project, f.hourAgo, uint64(subjects))
	}

	want := map[string]bool{}
	for n := 0; n < subjects; n++ {
		subject := workItemSubject(t, fmt.Sprintf("WS-%04d", n))
		want[devhealthsource.ProjectMembershipRelationshipIDForTest(t, subject, "linear", "P-a", "")] = false
	}
	d := f.h.drain("")
	for _, b := range d.batches {
		inBatch := map[string]bool{}
		for _, tombstone := range b.Tombstones {
			if tombstone.Reason != "superseded_by_transition_history" {
				continue
			}
			if inBatch[tombstone.CanonicalID] {
				t.Fatalf("one batch tombstones %q twice", tombstone.CanonicalID)
			}
			inBatch[tombstone.CanonicalID] = true
			if _, ok := want[tombstone.CanonicalID]; !ok {
				t.Fatalf("unexpected retraction %q", tombstone.CanonicalID)
			}
			want[tombstone.CanonicalID] = true
		}
	}
	if len(d.batches) < 3 {
		t.Fatalf("%d batches: the read no longer crosses page cuts", len(d.batches))
	}
	for id, seen := range want {
		if !seen {
			t.Fatalf("column edge %q was never retracted", id)
		}
	}
}

func workItemSubject(t *testing.T, workItemID string) string {
	return devhealthsource.WorkItemSubjectCanonicalIDForTest(t, zeroUUID, workItemID)
}
