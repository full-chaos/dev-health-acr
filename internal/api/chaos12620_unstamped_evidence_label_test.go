package api

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
)

// CHAOS-12620 (codex r2): the projection's last tier is built from the answer as
// the engine or the store gave it. A stored label from before the reader form
// ("<noun>: <ref id>") is restated there too.
func TestTheUnstampedProjectionRestatesAStoredEvidenceLabel(t *testing.T) {
	answer := threeRollupProjectStatusResult("result_unstamped_label")
	const ref = "acr:v1:pull-request:920f9442-07df-4217-4dc4-c5833c0b8268:869"
	answer.Drivers[0].EvidenceRefIDs = []string{ref}
	answer.EvidenceRefIDs = []string{ref, "evidence_2"}
	answer.EvidenceRefLabels = map[string]string{
		"evidence_2": "Evidence: evidence_2",
		ref:          "Pull request: 920f9442-07df-4217-4dc4-c5833c0b8268:869",
	}
	projection, _, _, ok := unstampedProjection(answer, answerprojection.Budget{}, true)
	if !ok {
		t.Fatalf("the unstamped projection did not serve: %+v", projection.EvidenceRefLabels)
	}
	if got := projection.EvidenceRefLabels[ref]; got != "Pull request: #869" {
		t.Fatalf("unstamped projection label %q, want the reader form", got)
	}
}
