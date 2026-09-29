package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// The three S1a data routes read ClickHouse-backed stores, so each must sit
// behind requireDataStoresReady (readiness gated per request, 503
// store_unavailable), inside the protection chain.
func TestChaos7072DataRoutesAnswerStoreUnavailableWhenClickHouseIsDown(t *testing.T) {
	down := func(deps *RuntimeDependencies) {
		deps.DataStoreChecks = []ReadinessCheck{CheckFunc{CheckName: "clickhouse", Fn: func(context.Context) error { return errors.New("down") }}}
	}
	h := newChaos7071Harness(t, 100, down)
	issued := h.issue(t, []string{auth.ScopeContextRead, auth.ScopeDataRead}, nil)
	cases := []struct{ method, path string }{
		{http.MethodGet, ContextFabricDataCatalogPath},
		{http.MethodPost, ContextFabricDataSubjectsPath},
		{http.MethodPost, ContextFabricDataOperationsPath},
	}
	for _, c := range cases {
		response := h.call(c.method, c.path, issued.Token)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "store_unavailable") {
			t.Fatalf("%s %s = %d %s, want 503 store_unavailable", c.method, c.path, response.Code, response.Body.String())
		}
	}
}
