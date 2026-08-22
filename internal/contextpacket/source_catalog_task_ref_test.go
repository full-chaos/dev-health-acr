package contextpacket_test

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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

// A query failure on a task_ref-filtered source is a real fault, not a
// task_ref-narrowed empty result, and must keep disclosing as such even when
// the request supplied a task_ref.
func TestSourceCatalog_taskRefFilteredSourceKeepsFailureReasonOnQueryError(t *testing.T) {
	// Given
	plan := contextpacket.ReadPlan{OrgID: "org", RepoID: "00000000-0000-0000-0000-000000000001", RepoSlug: "owner/repo", TaskRef: "TASK-9"}
	executor := &failingRecorder{failFor: "work_items.v1"}

	// When
	result, err := contextpacket.ExecuteCatalog(context.Background(), executor, plan)

	// Then
	if err != nil {
		t.Fatalf("execute catalog: %v", err)
	}
	if !containsUnavailable(result.Unavailable, "work_items.v1", "source_unavailable") {
		t.Fatalf("query failure must stay source_unavailable even with task_ref set: %#v", result.Unavailable)
	}
	if containsUnavailable(result.Unavailable, "work_items.v1", "no_evidence_task_ref_filtered") {
		t.Fatalf("a real query failure must not be relabeled as task_ref filtering: %#v", result.Unavailable)
	}
}

type failingRecorder struct{ failFor string }

func (r *failingRecorder) QueryEvidence(_ context.Context, query contextpacket.SourceQuery, _ []contextpacket.ClickHouseBinding) ([]contractsv1.EvidenceRef, error) {
	if query.ID == r.failFor {
		return nil, errors.New("boom")
	}
	return nil, nil
}
