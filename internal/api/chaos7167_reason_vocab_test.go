package api

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-7167: every invalid_request reason the routes emit must be in the
// closed vocabulary the sidecar surfaces, or a client silently loses it.
func TestCHAOS7167_EmittedInvalidRequestReasonsAreInTheClosedVocabulary(t *testing.T) {
	for _, reason := range []string{
		"scope_required", "invalid_find_request", "malformed_request", "unknown_section",
		directread.RelationshipsRefusalInvalidRequest, directread.RelationshipsRefusalInvalidCursor,
		directread.RelationshipsRefusalExpiredCursor, directread.RelationshipsRefusalDeniedOrNotFound,
		directread.FactsRefusalInvalidRequest, directread.FactsRefusalDeniedOrNotFound,
	} {
		if !contractsv1.IsInvalidRequestReason(reason) {
			t.Errorf("route reason %q missing from contractsv1 invalid_request vocabulary", reason)
		}
	}
}
