package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-11912 (plan deviation accepted by team-lead 2026-10-29): the owner
// check (storedResultOwnerRefusal) cannot apply on fact_root_recheck,
// because that surface reads no stored row: it re-decides the turn's own
// committed roots through the gate's decide. This pins the premise. A
// restricted turn that takes the fact_root_recheck decision, admitted or
// denied, makes zero result-store reads. A re-check that read a stored row
// would need the owner check, and this test goes red.
func TestFactRootRecheckReadsNoStoredRow(t *testing.T) {
	restricted := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/allowed"}}
	for _, outcome := range []StoredSubjectOutcome{StoredSubjectAdmitted, StoredSubjectDenied} {
		graph, _ := chaos7080ProjectGraph(outcome, nil)
		var reads []CanonicalFactRequest
		telemetry := &recordingTelemetry{}
		results := &staticResultStore{results: map[string]InvestigationResult{}}
		if _, err := chaos7080EngineWithResults(t, graph, &reads, telemetry, results).Investigate(context.Background(), restricted, validInvestigationRequest()); err != nil {
			t.Fatalf("%s: %v", outcome, err)
		}
		decided := 0
		for _, decision := range telemetry.storedResultAuthorizations {
			if decision.Surface == StoredResultSurfaceFactRootRecheck {
				decided++
			}
		}
		if decided == 0 {
			t.Fatalf("%s: no fact_root_recheck decision line: the turn never reached the re-check, so the read count proves nothing", outcome)
		}
		if len(results.gotIDs) != 0 {
			t.Fatalf("%s: the turn read stored rows %v, want none (fact_root_recheck must read no stored row)", outcome, results.gotIDs)
		}
	}
}
