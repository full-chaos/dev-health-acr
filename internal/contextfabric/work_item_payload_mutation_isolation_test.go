package contextfabric

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// This fixture removes downstream finding and evidence citations so the
// unsupported-claim guard is the only rejection under test. The shared
// acceptance fixture intentionally cites member claims and would otherwise
// mask removal of this guard at the later finding-reference check.
func TestValidateWorkItemTuplePayloadRejectsUnsupportedClaimWithoutDownstreamReferences(t *testing.T) {
	t.Parallel()
	result := workItemTuplePayloadFixture(t)
	result.ClaimedFacts = []ClaimedFact{{
		ClaimID: "claim-unsupported",
		Kind:    FactReadiness,
		Subject: result.Cohort.Members[0].Subject,
	}}
	result.EvidenceRefIDs = nil
	result.RemainingWork = nil
	result.ReadinessGaps = nil
	result.Conflicts = nil
	result.Drivers = nil
	result.EvidenceRefLabels = nil
	if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err == nil {
		t.Fatal("ValidateWorkItemTuplePayload() error = nil, want unsupported claim rejected")
	}
}
