package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestEvidenceRouteExpandsPacketReference(t *testing.T) {
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, nil, nil)
	packetRequest := contextPacketRequest(t, app, token, hostedContextRequest())
	packetResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(packetResponse, packetRequest)
	if packetResponse.Code != http.StatusOK {
		t.Fatalf("packet status = %d body=%s", packetResponse.Code, packetResponse.Body.String())
	}
	var packet contractsv1.ContextPacket
	if err := json.Unmarshal(packetResponse.Body.Bytes(), &packet); err != nil {
		t.Fatal(err)
	}
	referenceID := packet.Items[0].EvidenceRefIDs[0]
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/evidence/"+referenceID, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var expanded contractsv1.ExpandedEvidence
	if err := json.Unmarshal(response.Body.Bytes(), &expanded); err != nil {
		t.Fatal(err)
	}
	if expanded.Evidence.EvidenceRefID != referenceID || expanded.SchemaVersion != contractsv1.ExpandedEvidenceSchema {
		t.Fatalf("expanded = %#v", expanded)
	}
}

func TestEvidenceRouteReturnsGenericNotFound(t *testing.T) {
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeEvidenceRead}, nil, nil)
	for _, referenceID := range []string{"malformed", "ev1_unknown-opaque-reference"} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/evidence/"+referenceID, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		assertErrorResponse(t, response, http.StatusNotFound, "not_found")
	}
	audit := app.runtime.Audit.(*memory.AuditStore)
	events := audit.Events()
	denials := 0
	for _, event := range events {
		if event.Action == "evidence_denied" {
			denials++
			if event.ResourceID != "unavailable" || len(event.Metadata) != 0 {
				t.Fatalf("unsafe evidence denial audit = %#v", event)
			}
		}
	}
	if denials != 2 {
		t.Fatalf("evidence denial audits = %d", denials)
	}
}

func TestEvidenceRouteMapsSecurityAndDependencyFailures(t *testing.T) {
	tests := []struct {
		name       string
		scopes     []string
		entitled   bool
		withToken  bool
		storeError error
		wantStatus int
		wantCode   string
	}{
		{name: "missing token", scopes: []string{auth.ScopeEvidenceRead}, entitled: true, wantStatus: http.StatusUnauthorized, wantCode: "invalid_token"},
		{name: "missing scope", scopes: []string{auth.ScopeContextRead}, entitled: true, withToken: true, wantStatus: http.StatusForbidden, wantCode: "insufficient_scope"},
		{name: "missing entitlement", scopes: []string{auth.ScopeEvidenceRead}, withToken: true, wantStatus: http.StatusForbidden, wantCode: "feature_not_enabled"},
		{name: "foreign or unknown", scopes: []string{auth.ScopeEvidenceRead}, entitled: true, withToken: true, storeError: storage.ErrNotFound, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "upstream unavailable", scopes: []string{auth.ScopeEvidenceRead}, entitled: true, withToken: true, storeError: errors.New("database unavailable"), wantStatus: http.StatusServiceUnavailable, wantCode: "upstream_unavailable"},
		{name: "upstream timeout", scopes: []string{auth.ScopeEvidenceRead}, entitled: true, withToken: true, storeError: context.DeadlineExceeded, wantStatus: http.StatusGatewayTimeout, wantCode: "upstream_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entitlements := EntitlementFunc(func(context.Context, string, string) (bool, error) { return test.entitled, nil })
			var store storage.EvidenceStore
			if test.storeError != nil {
				store = failingReadStore{err: test.storeError}
			}
			app, token := newHostedTestApp(t, nil, nil, test.scopes, entitlements, store)
			request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/evidence/opaque-reference", nil)
			if test.withToken {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)
			assertErrorResponse(t, response, test.wantStatus, test.wantCode)
		})
	}
}

type failingReadStore struct{ err error }

func (s failingReadStore) ResolveScope(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (contractsv1.ResolvedScope, error) {
	return contractsv1.ResolvedScope{}, s.err
}

func (s failingReadStore) ContextForTask(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (storage.EvidenceBundle, error) {
	return storage.EvidenceBundle{}, s.err
}

func (s failingReadStore) ResolveEvidence(context.Context, storage.Principal, string) (contractsv1.ExpandedEvidence, error) {
	return contractsv1.ExpandedEvidence{}, s.err
}
