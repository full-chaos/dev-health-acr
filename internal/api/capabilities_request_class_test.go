package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var capabilitiesProbeSubject = limits.Subject{OrgID: callerOrgID, CredentialID: "usage_probe"}

func newCapabilitiesClassTestApp(t *testing.T, capabilitiesLimit int) (*App, string) {
	t.Helper()
	app, token, _ := newContextFabricTestAppWithResultsAndLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, nil
	}), nil)
	manager, err := limits.NewManager(limits.Options{
		Now: app.now, PerOrgConcurrency: 4,
		Policies: limits.PolicySet{
			Auth:         limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 100, PerCredentialLimit: 100},
			Context:      limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 100, PerCredentialLimit: 100},
			Capabilities: limits.CapabilitiesPolicy{Window: time.Minute, PerOrgLimit: capabilitiesLimit, PerCredentialLimit: capabilitiesLimit},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.limits = manager
	return app, token
}

func capabilitiesRequest(token string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	return request
}

func admittedByClass(t *testing.T, app *App) (capabilities, auth, contextClass int64) {
	t.Helper()
	read := func(class limits.RequestClass) int64 {
		usage, err := app.limits.Usage(capabilitiesProbeSubject, class)
		if err != nil {
			t.Fatal(err)
		}
		return usage.Org.Admitted
	}
	return read(limits.RequestClassCapabilities), read(limits.RequestClassAuth), read(limits.RequestClassContext)
}

func TestCapabilitiesCallsSpendOnlyTheCapabilitiesClass(t *testing.T) {
	app, token := newCapabilitiesClassTestApp(t, 100)
	const calls = 7
	for i := 0; i < calls; i++ {
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, capabilitiesRequest(token))
		if response.Code != http.StatusOK {
			t.Fatalf("call %d status = %d body=%s", i, response.Code, response.Body.String())
		}
	}
	capabilities, auth, contextClass := admittedByClass(t, app)
	if capabilities != calls || auth != 0 || contextClass != 0 {
		t.Fatalf("admitted after %d capabilities calls: capabilities=%d auth=%d context=%d, want %d/0/0", calls, capabilities, auth, contextClass, calls)
	}
}

func TestCapabilitiesClassExhaustionLeavesAuthAndContextUntouched(t *testing.T) {
	app, token := newCapabilitiesClassTestApp(t, 2)
	var last *httptest.ResponseRecorder
	for i := 0; i < 3; i++ {
		last = httptest.NewRecorder()
		app.Handler().ServeHTTP(last, capabilitiesRequest(token))
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("third call status = %d, want 429 body=%s", last.Code, last.Body.String())
	}
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(last.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if got := envelope.Error.Details[contractsv1.RateLimitDetailsLimitKey]; got != contractsv1.RateLimitCapabilitiesBudget {
		t.Fatalf("details.limit = %v, want %q", got, contractsv1.RateLimitCapabilitiesBudget)
	}
	capabilities, auth, contextClass := admittedByClass(t, app)
	if capabilities != 2 || auth != 0 || contextClass != 0 {
		t.Fatalf("admitted: capabilities=%d auth=%d context=%d, want 2/0/0", capabilities, auth, contextClass)
	}
}

func TestCapabilitiesStaysAuthenticatedOnEveryCall(t *testing.T) {
	app, token := newCapabilitiesClassTestApp(t, 100)
	good := httptest.NewRecorder()
	app.Handler().ServeHTTP(good, capabilitiesRequest(token))
	if good.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d", good.Code)
	}
	anonymous := httptest.NewRecorder()
	app.Handler().ServeHTTP(anonymous, capabilitiesRequest(""))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", anonymous.Code)
	}
	if capabilities, _, _ := admittedByClass(t, app); capabilities != 1 {
		t.Fatalf("capabilities admitted = %d, want 1 (a refused credential must not spend the class)", capabilities)
	}
}
