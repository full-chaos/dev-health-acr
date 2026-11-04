package api

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// CHAOS-12109: a web assertion is for consent, device approval and the Agent
// Context routes web still signs; it is refused on every Context Fabric route.
func TestContextFabricRoutesRefuseWebAssertions(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	signer, verifier := newUniformSigner(t, now)
	app, _ := newHostedTestAppWithWebAssertions(t, nil, nil, nil, nil, nil, verifier)
	fill := func(path string) string {
		for _, name := range []string{"{result_id}", "{submission_id}", "{thread_id}"} {
			path = strings.Replace(path, name, "id_0001", 1)
		}
		return path
	}
	routes := []struct{ method, path string }{
		{http.MethodPost, ContextFabricInvestigationsPath},
		{http.MethodGet, ContextFabricInvestigationResultPath},
		{http.MethodGet, ContextFabricSubmissionStatusPath},
		{http.MethodGet, ContextFabricSubmissionEventsPath},
		{http.MethodPost, ContextFabricSubmissionCancelPath},
		{http.MethodGet, ContextFabricThreadsPath},
		{http.MethodGet, ContextFabricThreadPath},
		{http.MethodDelete, ContextFabricThreadPath},
		{http.MethodGet, ContextFabricResultFeedbackPath},
		{http.MethodPost, ContextFabricResultFeedbackPath},
	}
	for _, route := range routes {
		var body []byte
		if route.method == http.MethodPost {
			body = []byte(`{}`)
		}
		response := serve(app, signer.request(route.method, fill(route.path), body, uniformOwnerOrg, []string{hostedTestRepository}))
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with a web assertion = %d, want 401 (body %s)", route.method, route.path, response.Code, response.Body.String())
		}
	}
	// The Agent Context routes web still signs keep accepting an assertion.
	capabilities := serve(app, signer.request(http.MethodGet, "/api/v1/agent-context/capabilities", nil, uniformOwnerOrg, []string{hostedTestRepository}))
	if capabilities.Code != http.StatusOK {
		t.Errorf("capabilities with a web assertion = %d, want 200 (web signs it)", capabilities.Code)
	}
}
