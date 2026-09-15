package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The100-pair carrier is a validated persisted boundary fixture. This test
// executes real PG Save/Get and the actual API/projection/MCP error surfaces;
// it does not claim a fresh Engine naturally produced100 independent failures.
func TestStoredWorkItemCoverageErrorSurfaces(t *testing.T) {
	db := storedPGBoundaryDatabase(t)
	principal := storage.Principal{OrgID: callerOrgID, RepositoryScopes: []string{hostedTestRepository}}
	stored := storedWorkItemTupleRouteFixture(t, principal)
	stored.Result.ResultID = "result_pg_coverage_surfaces"
	storedPGBoundaryCoverage(&stored.Result, 100, false)
	stored.SemanticState = storedPGBoundaryCanonicalState(stored.SemanticState.WorkItemCensus)
	store := storedPGBoundarySave(t, db, principal, stored)
	before := storedPGBoundaryNative(t, db, stored.Result.ResultID)
	app, token := newParityHostedApp(t, nil, store)
	for _, tc := range []struct {
		name, token string
		want        int
	}{
		{"authorized_capacity_error", token, http.StatusInternalServerError},
		{"revoked_authorization_first", storedServingCredential(t, app, []string{"other/repo"}), http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, view := range []string{"", "view=projection"} {
				req := investigationResultRequest(t, tc.token, stored.Result.ResultID)
				req.URL.RawQuery = view
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, req)
				if rec.Code != tc.want {
					t.Errorf("view=%s HTTP=%d want=%d", view, rec.Code, tc.want)
				}
				assertStoredServingContentAbsent(t, rec.Body.Bytes(), stored.Result)
				if tc.want == http.StatusInternalServerError {
					storedPGBoundarySafeError(t, rec.Code, rec.Body.Bytes(), stored.Result)
				}
			}
			result, body := callStoredServingMCP(t, app, tc.token, stored.Result.ResultID)
			if !result.IsError {
				t.Errorf("MCP returned success for HTTP%d", tc.want)
			}
			assertStoredServingContentAbsent(t, body, stored.Result)
			if bytes.Contains(body, []byte("coverage_bound")) || bytes.Contains(body, []byte("budget_exceeded")) {
				t.Errorf("MCP error disclosed capacity or fabricated budget refusal: %s", body)
			}
		})
	}
	if !bytes.Equal(before, storedPGBoundaryNative(t, db, stored.Result.ResultID)) {
		t.Fatal("error serving changed native PG payload/semantic state")
	}
}
