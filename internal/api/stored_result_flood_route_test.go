package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
)

// A stored result that is not a work-item tuple, with a limitation list at the
// cap that holds no model-authored entry, is withheld on the two stored-result
// routes like the work-item tuple is; a list one short of the cap, or at the
// cap with one model-authored entry, is served as stored.
func TestStoredResultFloodedLimitationsAnyKindByIDAndEvidence(t *testing.T) {
	cited := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityOrganization, "team-cited-0001")
	max := contractsv1.ContextFabricLimitationsMaxCount
	for _, tc := range []struct {
		name        string
		limitations []string
		status      int
	}{
		{name: "service disclosures fill the cap", limitations: storedRouteGapLookalikes(max), status: http.StatusNotFound},
		{name: "one short of the cap", limitations: storedRouteGapLookalikes(max - 1), status: http.StatusOK},
		{name: "full list with a model caveat", limitations: append(storedRouteGapLookalikes(max-1), "The provider returned a stale status."), status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memoryinvestigation.NewStore()
			row := citingStoredResult("result_flood_any_kind", cited)
			row.Limitations = tc.limitations
			seedResult3355(t, store, "org_1", row)
			app, token := newParityHostedAppWithLogs(t, nil, store, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, &bytes.Buffer{})

			byID := httptest.NewRecorder()
			app.Handler().ServeHTTP(byID, investigationResultRequest(t, token, row.ResultID))
			if byID.Code != tc.status {
				t.Fatalf("result by id status %d, want %d: %.300s", byID.Code, tc.status, byID.Body.String())
			}
			expand := httptest.NewRecorder()
			app.Handler().ServeHTTP(expand, evidenceRequest(t, token, cited))
			if expand.Code != tc.status {
				t.Fatalf("evidence expansion status %d, want %d: %.300s", expand.Code, tc.status, expand.Body.String())
			}
			t.Logf("by_id=%d evidence=%d limitations=%d", byID.Code, expand.Code, len(tc.limitations))
		})
	}
}
