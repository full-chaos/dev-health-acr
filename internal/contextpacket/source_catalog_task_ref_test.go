package contextpacket_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// CHAOS-4016: a task_ref exact-match filter must not silently degrade to the
// same "no_evidence" reason a source that ignores task_ref entirely gets for
// the same empty result. A caller with a stale/wrong task_ref must see the
// emptiness attributed to the filter it applied.
func TestSourceCatalog_disclosesTaskRefFilteredEmptyResultDistinctly(t *testing.T) {
	// Given
	plan := contextpacket.ReadPlan{OrgID: "org", RepoID: "00000000-0000-0000-0000-000000000001", RepoSlug: "owner/repo", TaskRef: "TASK-9"}
	executor := &catalogRecorder{}

	// When
	result, err := contextpacket.ExecuteCatalog(context.Background(), executor, plan)

	// Then
	if err != nil {
		t.Fatalf("execute catalog: %v", err)
	}
	for _, id := range []string{"work_items.v1", "work_item_dependencies.v1", "work_graph.v1"} {
		if !containsUnavailable(result.Unavailable, id, "no_evidence_task_ref_filtered") {
			t.Fatalf("%s did not disclose its task_ref-filtered empty result distinctly: %#v", id, result.Unavailable)
		}
	}
	// A source with no task_ref predicate at all must keep the generic
	// reason: emptiness there is not attributable to the requested task_ref.
	if !containsUnavailable(result.Unavailable, "repository_freshness.v1", "no_evidence") {
		t.Fatalf("unfiltered source lost its generic no_evidence reason: %#v", result.Unavailable)
	}
}

func TestSourceCatalog_keepsGenericNoEvidenceReasonWithoutTaskRef(t *testing.T) {
	// Given
	plan := contextpacket.ReadPlan{OrgID: "org", RepoID: "00000000-0000-0000-0000-000000000001", RepoSlug: "owner/repo"}
	executor := &catalogRecorder{}

	// When
	result, err := contextpacket.ExecuteCatalog(context.Background(), executor, plan)

	// Then
	if err != nil {
		t.Fatalf("execute catalog: %v", err)
	}
	for _, id := range []string{"work_items.v1", "work_item_dependencies.v1", "work_graph.v1"} {
		if !containsUnavailable(result.Unavailable, id, "no_evidence") {
			t.Fatalf("%s must stay generic no_evidence when no task_ref was requested: %#v", id, result.Unavailable)
		}
		if containsUnavailable(result.Unavailable, id, "no_evidence_task_ref_filtered") {
			t.Fatalf("%s must not claim task_ref filtering when no task_ref was requested: %#v", id, result.Unavailable)
		}
	}
}
