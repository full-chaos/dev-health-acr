package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestContextPacketRouteDrivesRealAssembler(t *testing.T) {
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil)
	requestID := "req_0123456789abcdef0123456789abcdef"
	request := contextPacketRequest(t, app, token, hostedContextRequest())
	request.Header.Set("X-Request-ID", requestID)
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var packet contractsv1.ContextPacket
	if err := json.Unmarshal(response.Body.Bytes(), &packet); err != nil {
		t.Fatal(err)
	}
	if packet.RequestID != requestID || packet.Repository.Slug != hostedTestRepository || packet.Repository.RepoID == "caller-controlled" || len(packet.Items) == 0 {
		t.Fatalf("packet = %#v", packet)
	}
}

func TestContextPacketRouteReturnsDegradedPacketForEvidenceTimeout(t *testing.T) {
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, nil, failingReadStore{err: context.DeadlineExceeded})
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, contextPacketRequest(t, app, token, hostedContextRequest()))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var packet contractsv1.ContextPacket
	if err := json.Unmarshal(response.Body.Bytes(), &packet); err != nil {
		t.Fatal(err)
	}
	if packet.Status != contractsv1.PacketDegraded || len(packet.Coverage.DegradedReasons) == 0 {
		t.Fatalf("packet = %#v", packet)
	}
}

func TestContextPacketRouteEnforcesQuota(t *testing.T) {
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil)
	manager, err := limits.NewManager(limits.Options{Policies: limits.PolicySet{Context: limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 1, Resources: limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}}}})
	if err != nil {
		t.Fatal(err)
	}
	app.limits = manager

	first := httptest.NewRecorder()
	app.Handler().ServeHTTP(first, contextPacketRequest(t, app, token, hostedContextRequest()))
	second := httptest.NewRecorder()
	app.Handler().ServeHTTP(second, contextPacketRequest(t, app, token, hostedContextRequest()))

	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d", first.Code)
	}
	assertErrorResponse(t, second, http.StatusTooManyRequests, "rate_limited")
}

type failingAssembler struct{ err error }

func (a failingAssembler) Assemble(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (contractsv1.ContextPacket, error) {
	return contractsv1.ContextPacket{}, a.err
}

type staticAssembler struct{ packet contractsv1.ContextPacket }

func (a staticAssembler) Assemble(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (contractsv1.ContextPacket, error) {
	return a.packet, nil
}

func TestContextPacketRouteMapsAssemblerDeadline(t *testing.T) {
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil)
	app.runtime.Assembler = failingAssembler{err: context.DeadlineExceeded}
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, contextPacketRequest(t, app, token, hostedContextRequest()))

	assertErrorResponse(t, response, http.StatusGatewayTimeout, "upstream_unavailable")
}

func TestContextPacketRouteRejectsAssemblerOutputBeyondRequestBudget(t *testing.T) {
	tests := []struct {
		name   string
		packet contractsv1.ContextPacket
	}{
		{name: "items", packet: contractsv1.ContextPacket{Items: make([]contractsv1.ContextPacketItem, 11)}},
		{name: "tokens", packet: contractsv1.ContextPacket{Items: []contractsv1.ContextPacketItem{}, Budget: contractsv1.PacketBudget{EstimatedTokens: 501}}},
		{name: "bytes", packet: contractsv1.ContextPacket{Items: []contractsv1.ContextPacketItem{}, Budget: contractsv1.PacketBudget{SerializedBytes: 8193}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil)
			app.runtime.Assembler = staticAssembler{packet: test.packet}
			response := httptest.NewRecorder()

			app.Handler().ServeHTTP(response, contextPacketRequest(t, app, token, hostedContextRequest()))

			assertErrorResponse(t, response, http.StatusInternalServerError, "internal_error")
		})
	}
}

func TestContextPacketRouteDoesNotWriteAfterCancellation(t *testing.T) {
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil)
	app.runtime.Assembler = failingAssembler{err: context.Canceled}
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, contextPacketRequest(t, app, token, hostedContextRequest()))

	if response.Body.Len() != 0 || response.Header().Get("Content-Type") != "" {
		t.Fatalf("wrote response after cancellation: headers=%#v body=%s", response.Header(), response.Body.String())
	}
}

func TestContextPacketRouteRejectsUnsafeRequests(t *testing.T) {
	tests := []struct {
		name           string
		scopes         []string
		entitled       bool
		entitlementErr error
		request        contractsv1.ContextPacketRequest
		body           string
		withToken      bool
		bodyMaximum    int64
		wantStatus     int
		wantCode       string
	}{
		{name: "missing token", scopes: []string{auth.ScopeContextRead}, entitled: true, request: hostedContextRequest(), wantStatus: http.StatusUnauthorized, wantCode: "invalid_token"},
		{name: "missing scope", scopes: []string{auth.ScopeEvidenceRead}, entitled: true, request: hostedContextRequest(), withToken: true, wantStatus: http.StatusForbidden, wantCode: "insufficient_scope"},
		{name: "missing entitlement", scopes: []string{auth.ScopeContextRead}, request: hostedContextRequest(), withToken: true, wantStatus: http.StatusForbidden, wantCode: "feature_not_enabled"},
		{name: "entitlement unavailable", scopes: []string{auth.ScopeContextRead}, entitlementErr: errors.New("entitlement unavailable"), request: hostedContextRequest(), withToken: true, wantStatus: http.StatusServiceUnavailable, wantCode: "upstream_unavailable"},
		{name: "foreign repository", scopes: []string{auth.ScopeContextRead}, entitled: true, request: contextRequestFor("other-org/other-repo"), withToken: true, wantStatus: http.StatusForbidden, wantCode: "repo_forbidden"},
		{name: "unknown field", scopes: []string{auth.ScopeContextRead}, entitled: true, body: `{"schema_version":"context_packet_request.v1","unknown":true}`, withToken: true, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "oversized body", scopes: []string{auth.ScopeContextRead}, entitled: true, body: strings.Repeat(" ", 128) + `{}`, withToken: true, bodyMaximum: 32, wantStatus: http.StatusRequestEntityTooLarge, wantCode: "invalid_request"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entitlements := EntitlementFunc(func(_ context.Context, _, _ string) (bool, error) { return test.entitled, test.entitlementErr })
			app, token := newHostedTestApp(t, nil, nil, test.scopes, entitlements, nil)
			if test.bodyMaximum > 0 {
				app.config.MaxRequestBodyBytes = test.bodyMaximum
			}
			var request *http.Request
			if test.body != "" {
				request = httptest.NewRequest(http.MethodPost, "/api/v1/agent-context/context-packets", strings.NewReader(test.body))
			} else {
				request = contextPacketRequest(t, app, token, test.request)
			}
			if test.withToken {
				request.Header.Set("Authorization", "Bearer "+token)
			} else {
				request.Header.Del("Authorization")
			}
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)
			assertErrorResponse(t, response, test.wantStatus, test.wantCode)
		})
	}
}

func hostedContextRequest() contractsv1.ContextPacketRequest {
	request := contextRequestFor(hostedTestRepository)
	request.Repository.RepoID = "caller-controlled"
	request.Repository.RemoteURL = "https://caller.invalid/repository"
	return request
}

func contextRequestFor(repository string) contractsv1.ContextPacketRequest {
	return contractsv1.ContextPacketRequest{
		SchemaVersion: contractsv1.ContextPacketRequestSchema, RequestID: "caller-request-id", Goal: "Investigate fixture evidence",
		Repository: contractsv1.RepositoryRef{Slug: repository}, Scope: contractsv1.RequestedScope{Branch: "main"},
		Options: contractsv1.PacketOptions{MaxItems: 10, MaxOutputTokens: 500, MaxSerializedBytes: 8192},
		Client:  contractsv1.ClientInfo{Name: "test", Version: "1.0.0", SidecarVersion: "0.1.0"},
	}
}

func contextPacketRequest(t *testing.T, _ *App, token string, value contractsv1.ContextPacketRequest) *http.Request {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent-context/context-packets", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	return request
}
