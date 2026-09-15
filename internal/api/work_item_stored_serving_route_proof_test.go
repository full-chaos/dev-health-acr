package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestProofRouteEmitsTransformedMatchingCensusResult(t *testing.T) {
	principal := storage.Principal{OrgID: callerOrgID, RepositoryScopes: []string{hostedTestRepository}}
	stored := storedWorkItemTupleRouteFixture(t, principal)
	stored.Result.Coverage.Details = nil
	stored.Result.Coverage.DegradedReasons = nil
	store := &storedWorkItemTupleRouteStore{stored: stored}
	app, token := newContextFabricTestAppWithResults(t, nil, store)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, stored.Result.ResultID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var got contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	detail := findWorkItemTupleRouteCensusDetail(got.Coverage.Details)
	if detail == nil || detail.Declared == nil || *detail.Declared != contextfabric.WorkItemMembershipCensusLimit || detail.Served == nil || *detail.Served != 1 {
		t.Fatalf("served detail = %#v, want reconstructed floor census", detail)
	}
}
