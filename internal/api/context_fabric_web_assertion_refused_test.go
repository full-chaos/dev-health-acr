package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
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
		{http.MethodGet, ContextFabricDataCatalogPath},
		{http.MethodPost, ContextFabricDataSubjectsPath},
		{http.MethodPost, ContextFabricDataFactsPath},
		{http.MethodPost, ContextFabricDataOperationsPath},
		{http.MethodPost, ContextFabricDataGraphQLPath},
		{http.MethodPost, ContextFabricDataRelationshipsPath},
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

// Every route that reaches the query-api client refuses a web assertion before
// the handler runs: the fake query service sees no call.
func TestWebAssertionNeverReachesTheQueryAPIClient(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	signer, verifier := newUniformSigner(t, now)
	upstream := newC7072Upstream(t)
	catalogue, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	client, err := directread.NewHTTPQueryClientWithHTTP(upstream.server.URL, 5*time.Second, upstream.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	runner, err := directread.NewOperationRunner(directread.OperationRunnerConfig{
		Catalogue: catalogue, Gate: directread.NewSubjectGate(nil, nil), Client: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	app, _ := newHostedTestAppWithWebAssertions(t, nil, nil, nil, nil, nil, verifier, func(deps *RuntimeDependencies) {
		deps.DataCatalogue = catalogue
		deps.DataOperations = runner
	})
	for _, path := range []string{ContextFabricInvestigationsPath, ContextFabricDataOperationsPath} {
		body := []byte(`{"operation":"compoundingRisk"}`)
		response := serve(app, signer.request(http.MethodPost, path, body, uniformOwnerOrg, []string{hostedTestRepository}))
		if response.Code != http.StatusUnauthorized {
			t.Errorf("POST %s with a web assertion = %d %s, want 401", path, response.Code, response.Body.String())
		}
	}
	// Control: the same signer is accepted where web signs, so the 401 above is the route's refusal.
	if control := serve(app, signer.request(http.MethodGet, "/api/v1/agent-context/capabilities", nil, uniformOwnerOrg, []string{hostedTestRepository})); control.Code != http.StatusOK {
		t.Fatalf("control: capabilities with the same web assertion = %d, want 200", control.Code)
	}
	if got := upstream.count(); got != 0 {
		t.Fatalf("the query service saw %d calls from web-assertion requests, want 0", got)
	}
}
