package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func newLifecycleServer(t *testing.T, token string, polls []string) *httptest.Server {
	t.Helper()
	createdAt := time.Now().UTC().Truncate(time.Second)
	poll := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/oauth/device_authorization":
			if r.Header.Get("Authorization") != "" {
				t.Fatal("device authorization request unexpectedly had bearer authorization")
			}
			writeLifecycleJSON(t, w, contractsv1.DeviceAuthorizationResponse{SchemaVersion: contractsv1.DeviceAuthorizationResponseSchema, DeviceCode: strings.Repeat("d", 32), UserCode: "ABCDEFGH", VerificationURI: "http://" + r.Host + "/acr/device", ExpiresIn: 600, Interval: 5})
		case "/api/v1/oauth/token":
			if r.Header.Get("Authorization") != "" {
				t.Fatal("device token request unexpectedly had bearer authorization")
			}
			result := polls[poll]
			poll++
			if result != "success" {
				w.WriteHeader(http.StatusBadRequest)
				writeLifecycleJSON(t, w, contractsv1.OAuthDeviceErrorResponse{SchemaVersion: contractsv1.OAuthDeviceErrorSchema, Error: contractsv1.OAuthDeviceErrorCode(result)})
				return
			}
			expiresAt := createdAt.Add(30 * 24 * time.Hour)
			writeLifecycleJSON(t, w, contractsv1.DeviceTokenResponse{SchemaVersion: contractsv1.DeviceTokenResponseSchema, AccessToken: token, TokenType: "Bearer", ExpiresIn: 30 * 24 * 60 * 60, Credential: contractsv1.ClientCredential{SchemaVersion: contractsv1.ClientCredentialSchema, CredentialID: "credential-1", Name: "device credential", TokenPrefix: "fcacr_abcd1234", OrgID: "org-1", RepositoryScopes: []string{"owner/repo"}, Scopes: []string{"context:read", "evidence:read"}, CreatedAt: createdAt, ExpiresAt: &expiresAt}})
		case "/api/v1/agent-context/capabilities":
			if r.Header.Get("Authorization") != "Bearer "+token {
				t.Fatal("live doctor did not use the persisted credential")
			}
			writeLifecycleCapabilities(t, w)
		case "/acr/device":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
	}))
}

func newCredentialLifecycleServer(t *testing.T, original, successor string, revocations *int, revokeFails bool) *httptest.Server {
	t.Helper()
	createdAt := time.Now().UTC().Truncate(time.Second)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/credentials/self/rotate":
			if r.Header.Get("Authorization") != "Bearer "+original {
				t.Fatal("refresh did not use the original credential")
			}
			rollbackUntil := createdAt.Add(15 * time.Minute)
			writeLifecycleJSON(t, w, contractsv1.CredentialRotateResponse{SchemaVersion: contractsv1.CredentialRotateResponseSchema, AccessToken: successor, Credential: lifecycleCredential(createdAt, nil), Receipt: contractsv1.CredentialRotationReceipt{SourceCredentialID: "credential-1", ReplacementCredentialID: "credential-2", RollbackUntil: rollbackUntil}})
		case "/api/v1/auth/credentials/self/revoke":
			(*revocations)++
			if revokeFails {
				w.WriteHeader(http.StatusUnauthorized)
				writeLifecycleError(t, w, http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+successor && r.Header.Get("Authorization") != "Bearer "+original {
				t.Fatal("revoke did not use an expected credential")
			}
			revokedAt := createdAt.Add(time.Minute)
			writeLifecycleJSON(t, w, contractsv1.CredentialRevokeResponse{SchemaVersion: contractsv1.CredentialRevokeResponseSchema, Credential: lifecycleCredential(createdAt, &revokedAt)})
		default:
			t.Fatalf("unexpected credential lifecycle request path %q", r.URL.Path)
		}
	}))
}

func lifecycleCredential(createdAt time.Time, revokedAt *time.Time) contractsv1.ClientCredential {
	return contractsv1.ClientCredential{SchemaVersion: contractsv1.ClientCredentialSchema, CredentialID: "credential-1", Name: "MCP sidecar", TokenPrefix: "fcacr_abcd1234", OrgID: "org-1", RepositoryScopes: []string{"owner/repo"}, Scopes: []string{"context:read", "evidence:read"}, CreatedAt: createdAt, RevokedAt: revokedAt}
}

func writeLifecycleError(t *testing.T, w http.ResponseWriter, status int) {
	t.Helper()
	writeLifecycleJSON(t, w, contractsv1.ErrorEnvelope{SchemaVersion: contractsv1.ErrorSchema, RequestID: "request-1", Error: contractsv1.ErrorDetail{Code: "invalid_token", Message: "credential rejected", HTTPStatus: status, Retryable: false}})
}

func writeLifecycleCapabilities(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	_, err := w.Write([]byte(`{"schema_version":"capabilities.v1","service":"dev-health-acr","service_version":"dev","minimum_sidecar_version":"1.0.0","supported_schema_versions":` + schemaVersionsJSON() + `,"enabled_tools":["context_for_task","source_evidence"],"entitlements":{"agent_context_runtime":true},"permissions":{"context_read":true,"evidence_read":true,"episode_write":false},"limits":{"max_items":30,"max_output_tokens":4000,"max_serialized_bytes":262144,"requests_per_minute":60},"generated_at":"2026-07-25T00:00:00Z"}`))
	if err != nil {
		t.Fatalf("write capabilities: %v", err)
	}
}

func writeLifecycleJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write lifecycle JSON: %v", err)
	}
}
