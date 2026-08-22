package panelharness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// fakeRealRouteInvestigator is a minimal contextfabric.Investigator that
// ignores its input and returns a fixed, schema-valid result -- enough for
// the real /api/v1/context-fabric/investigations route to produce a 200
// this package's own ValidateStoredResult call accepts. What this test
// cares about is everything BEFORE the investigator runs (requireClientVersion,
// auth, scope, entitlement), not investigation content.
type fakeRealRouteInvestigator struct{}

func (fakeRealRouteInvestigator) Investigate(_ context.Context, _ storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
	return minimalValidResult("result_real_route", request.RequestID), nil
}

type noopContextPacketAssembler struct{}

func (noopContextPacketAssembler) Assemble(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (contractsv1.ContextPacket, error) {
	return contractsv1.ContextPacket{}, errors.New("not used by this fixture")
}

type noopEvidenceStore struct{}

func (noopEvidenceStore) ResolveScope(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (contractsv1.ResolvedScope, error) {
	return contractsv1.ResolvedScope{}, nil
}
func (noopEvidenceStore) ContextForTask(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (storage.EvidenceBundle, error) {
	return storage.EvidenceBundle{}, nil
}
func (noopEvidenceStore) ResolveEvidence(context.Context, storage.Principal, string) (contractsv1.ExpandedEvidence, error) {
	return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
}

// newRealHostedAPITestServer stands up the REAL internal/api.App --
// real route wiring, real requireClientVersion middleware, real
// authentication -- rather than a bare httptest.NewServer handler that
// skips middleware entirely. That skip is exactly the CHAOS-4072 gap: this
// package's other fixtures (client_test.go) never ran requireClientVersion,
// so a missing X-ACR-Client-Version header on every real Investigate call
// went uncaught until a live kind-cluster run 426'd. This constructs the
// smallest real App that registers the context-fabric investigations route
// with requireClientVersion in front of it, and returns a bearer credential
// scoped to actually reach it.
func newRealHostedAPITestServer(t *testing.T) (baseURL, bearerToken string) {
	t.Helper()
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	credentialStore, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: audit, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("NewCredentialStoreWithOptions: %v", err)
	}
	devices, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{Credentials: credentialStore, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("NewDeviceAuthorizationStore: %v", err)
	}
	credentialService, err := auth.NewService(credentialStore, auth.ServiceOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("auth.NewService: %v", err)
	}
	issued, err := credentialService.Create(context.Background(), auth.CreateCredentialRequest{
		OrgID: "org_1", Name: "panelharness real-route test credential",
		RepositoryScopes: []string{"example-org/widget-service"}, Scopes: []string{auth.ScopeContextRead}, CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("credentialService.Create: %v", err)
	}
	entitlements := api.EntitlementFunc(func(context.Context, string, string) (bool, error) { return true, nil })
	manager, err := limits.NewManager(limits.Options{Now: func() time.Time { return now }, PerOrgConcurrency: 4, Policies: limits.PolicySet{
		Auth:    limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 100},
		Context: limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 100, Resources: limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}},
	}})
	if err != nil {
		t.Fatalf("limits.NewManager: %v", err)
	}
	capabilities := api.StaticCapabilitiesProvider{Now: func() time.Time { return now }, Value: contractsv1.Capabilities{
		SchemaVersion: contractsv1.CapabilitiesSchema, Service: "acr", ServiceVersion: "test", MinimumSidecarVersion: "0.1.0",
		SupportedSchemaVersions: []string{contractsv1.ContextFabricInvestigationRequestSchema},
		EnabledTools:            []string{}, Entitlements: contractsv1.CapabilityEntitlements{}, Permissions: contractsv1.CapabilityPermissions{},
		Limits: contractsv1.CapabilityLimits{MaxItems: 50, MaxOutputTokens: 16_000, MaxSerializedBytes: 1 << 20, RequestsPerMinute: 100},
	}}
	hooks := observability.NewHooks(nil, nil)
	app, err := api.NewApp(api.AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: 5 * time.Second}, api.Dependencies{
		Capabilities: capabilities, Observability: &hooks, Limits: manager, Now: func() time.Time { return now },
		Runtime: &api.RuntimeDependencies{
			Credentials: credentialStore, Audit: audit, Entitlements: entitlements,
			Assembler: noopContextPacketAssembler{}, Evidence: noopEvidenceStore{},
			DeviceAuthorizations: devices, DeviceVerificationURL: "https://verify.example.test/device",
			DeviceAuthorizationLimiter: api.NewDeviceAuthorizationLimiter(api.ClockFunc(func() time.Time { return now })),
			ReadinessChecks: []api.ReadinessCheck{
				api.CheckFunc{CheckName: "postgres", Fn: func(context.Context) error { return nil }},
				api.CheckFunc{CheckName: "clickhouse", Fn: func(context.Context) error { return nil }},
				api.CheckFunc{CheckName: "entitlement", Fn: func(context.Context) error { return nil }},
			},
			Investigator: fakeRealRouteInvestigator{},
		},
	}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("api.NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	return server.URL, issued.Token
}

// newInvestigationRequestWithoutClientVersionHeader builds the exact same
// request Investigate would (Content-Type, Authorization, JSON body), MINUS
// the X-ACR-Client-Version header -- reproducing the pre-CHAOS-4072-fix
// Client's actual wire request without needing a second Client
// implementation to do it.
func newInvestigationRequestWithoutClientVersionHeader(baseURL, bearerToken string, request contractsv1.ContextFabricInvestigationRequest) (*http.Request, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	httpRequest, err := http.NewRequest(http.MethodPost, strings.TrimSuffix(baseURL, "/")+investigationsPath, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+bearerToken)
	return httpRequest, nil
}

// TestClient_InvestigateSucceedsAgainstTheRealRequireClientVersionMiddleware
// is CHAOS-4072's own regression proof. The bug was that
// (*Client).Investigate never set X-ACR-Client-Version, so
// internal/api/runtime.go's requireClientVersion middleware 426'd every
// live call -- but every fixture in this package (client_test.go) is a
// bare httptest.HandlerFunc that never runs that middleware at all, which
// is exactly how the omission shipped unnoticed. This test runs the client
// against the REAL internal/api.App (newRealHostedAPITestServer above),
// exercising the actual requireClientVersion middleware end to end, and
// fails if the header is ever dropped again.
func TestClient_InvestigateSucceedsAgainstTheRealRequireClientVersionMiddleware(t *testing.T) {
	baseURL, bearerToken := newRealHostedAPITestServer(t)
	client, err := NewClient(baseURL, bearerToken, 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result, err := client.Investigate(context.Background(), "request_real_route01", validRequest())
	if err != nil {
		t.Fatalf("Investigate against the real requireClientVersion middleware: %v (a missing/incompatible X-ACR-Client-Version header would surface as a 426 here)", err)
	}
	if result.ResultID != "result_real_route" {
		t.Errorf("ResultID = %q, want %q", result.ResultID, "result_real_route")
	}
}

// TestClient_InvestigateFailsClosedWithoutTheClientVersionHeader proves the
// fixture above is actually discriminating -- i.e. it is not silently
// accepting requests regardless of the header -- by sending the SAME
// request through the SAME real middleware with the header stripped, the
// way a caller with the CHAOS-4072 bug would. This must still 426.
func TestClient_InvestigateFailsClosedWithoutTheClientVersionHeader(t *testing.T) {
	baseURL, bearerToken := newRealHostedAPITestServer(t)
	client, err := NewClient(baseURL, bearerToken, 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// Simulate the pre-fix client: same request, header removed after
	// Investigate would have set it, by calling the transport directly
	// with a hand-built request rather than going through Investigate
	// (which -- post-fix -- always sets the header).
	request := validRequest()
	request.SchemaVersion = contractsv1.ContextFabricInvestigationRequestSchema
	request.RequestID = "request_real_route02"
	request.Consumer = contractsv1.ContextFabricConsumerInfo{Name: consumerName, Version: consumerVersion, Surface: contextFabricConsumerSurface}
	httpRequest, err := newInvestigationRequestWithoutClientVersionHeader(baseURL, bearerToken, request)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := client.http.Do(httpRequest)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 426 {
		t.Fatalf("status = %d, want 426 version_mismatch when X-ACR-Client-Version is absent (the real middleware must still gate on it)", response.StatusCode)
	}
}
