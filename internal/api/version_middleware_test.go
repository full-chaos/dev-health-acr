package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

func TestProtectedRoutesRejectIncompatibleClientVersions(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		path    string
		version string
	}{
		{name: "capabilities missing", method: http.MethodGet, path: "/api/v1/agent-context/capabilities"},
		{name: "context old", method: http.MethodPost, path: "/api/v1/agent-context/context-packets", version: "0.0.9"},
		{name: "evidence revoked", method: http.MethodGet, path: "/api/v1/agent-context/evidence/evidence_01", version: "1.2.3+build.7"},
		{name: "episodes old", method: http.MethodPost, path: "/api/v1/agent-context/episodes", version: "0.0.9"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead, auth.ScopeEpisodeWrite}, nil, nil)
			app.config.RevokedClientVersions = []string{"1.2.3+build.7"}
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-ACR-Client-Version", test.version)

			// When
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)

			// Then
			assertErrorResponse(t, response, http.StatusUpgradeRequired, "version_mismatch")
		})
	}
}

func TestProtectedRoutesAuthenticateBeforeVersionCompatibility(t *testing.T) {
	paths := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/v1/agent-context/capabilities"},
		{method: http.MethodPost, path: "/api/v1/agent-context/context-packets"},
		{method: http.MethodGet, path: "/api/v1/agent-context/evidence/evidence_01"},
		{method: http.MethodPost, path: "/api/v1/agent-context/episodes"},
	}
	for _, path := range paths {
		t.Run(path.method+" "+path.path, func(t *testing.T) {
			// Given
			app, _ := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil)
			request := httptest.NewRequest(path.method, path.path, nil)
			request.Header.Set("X-ACR-Client-Version", "0.0.9")

			// When
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)

			// Then
			assertErrorResponse(t, response, http.StatusUnauthorized, "invalid_token")
		})
	}
}
