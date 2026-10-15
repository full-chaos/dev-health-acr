package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

func TestCapabilitiesEntitlementFailureClasses(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		status    int
		code      string
		retryable bool
		class     string
	}{
		{"unknown organization", entitlements.ErrOrganizationUnknown, http.StatusUnauthorized, "invalid_token", false, "entitlement_organization_unknown"},
		{"wrapped unknown organization", fmt.Errorf("lookup: %w", entitlements.ErrOrganizationUnknown), http.StatusUnauthorized, "invalid_token", false, "entitlement_organization_unknown"},
		{"timeout or 5xx", errors.New("Dev Health entitlement service unavailable"), http.StatusServiceUnavailable, "upstream_unavailable", true, "entitlement_unavailable"},
		{"context deadline", context.DeadlineExceeded, http.StatusServiceUnavailable, "upstream_unavailable", true, "entitlement_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buffer := &bytes.Buffer{}
			provider := EntitlementFunc(func(context.Context, string, string) (bool, error) { return false, tc.err })
			app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, provider, nil)
			app.logger = testLogger(buffer)
			request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-Request-ID", testRequestID)
			request.Header.Set("X-ACR-Client-Version", "1.0.0")
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d", response.Code, tc.status)
			}
			var envelope contractsv1.ErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != tc.code || envelope.Error.Retryable != tc.retryable {
				t.Fatalf("envelope = %#v, want code %s retryable %v", envelope.Error, tc.code, tc.retryable)
			}
			logs := buffer.String()
			if !strings.Contains(logs, `"failure_class":"`+tc.class+`"`) {
				t.Fatalf("log lacks failure_class %s: %s", tc.class, logs)
			}
			if strings.Contains(logs, "org_1") {
				t.Fatalf("log carries the organization id: %s", logs)
			}
		})
	}
}

func TestEntitlementGatesAnswerUnknownOrganizationAs401AndOutageAs503(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"unknown organization", entitlements.ErrOrganizationUnknown, http.StatusUnauthorized},
		{"outage", errors.New("Dev Health entitlement service unavailable"), http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := EntitlementFunc(func(context.Context, string, string) (bool, error) { return false, tc.err })
			app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, provider, nil)
			request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/evidence/acr:v1:x:1", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-ACR-Client-Version", "1.0.0")
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("runtime gate status = %d, want %d: %s", response.Code, tc.status, response.Body.String())
			}
			post := contextPacketRequest(t, app, token, hostedContextRequest())
			postResponse := httptest.NewRecorder()
			app.Handler().ServeHTTP(postResponse, post)
			if postResponse.Code != tc.status {
				t.Fatalf("read gate status = %d, want %d: %s", postResponse.Code, tc.status, postResponse.Body.String())
			}
		})
	}
}
