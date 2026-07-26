package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHostedReadRoutesMatchOpenAPI(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "contracts", "openapi", "acr-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]map[string]struct {
			OperationID string                     `json:"operationId"`
			Responses   map[string]json.RawMessage `json:"responses"`
			Security    []map[string][]string      `json:"security"`
		} `json:"paths"`
		Components struct {
			Parameters map[string]struct {
				Name        string `json:"name"`
				Required    bool   `json:"required"`
				Description string `json:"description"`
			} `json:"parameters"`
			SecuritySchemes map[string]struct {
				Type string `json:"type"`
				In   string `json:"in"`
				Name string `json:"name"`
			} `json:"securitySchemes"`
		} `json:"components"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path, method, operation string
		statuses                []string
	}{
		{path: "/api/v1/agent-context/capabilities", method: "get", operation: "getCapabilities", statuses: []string{"200", "401", "403", "426", "429", "500", "503"}},
		{path: "/api/v1/agent-context/context-packets", method: "post", operation: "createContextPacket", statuses: []string{"200", "400", "401", "403", "413", "426", "429", "500", "503", "504"}},
		{path: "/api/v1/agent-context/evidence/{evidence_ref_id}", method: "get", operation: "getEvidence", statuses: []string{"200", "401", "403", "404", "413", "426", "429", "500", "503", "504"}},
		{path: "/api/v1/agent-context/episodes", method: "post", operation: "createEpisode", statuses: []string{"200", "201", "204", "400", "401", "403", "404", "409", "413", "426", "429", "500", "503", "504"}},
		{path: "/api/v1/oauth/device_authorization", method: "post", operation: "createDeviceAuthorization", statuses: []string{"200", "400", "429", "503"}},
		{path: "/api/v1/oauth/token", method: "post", operation: "exchangeDeviceToken", statuses: []string{"200", "400", "429", "503"}},
		{path: "/api/v1/oauth/device_approval", method: "post", operation: "approveDeviceAuthorization", statuses: []string{"200", "400", "401", "403", "409", "429", "503"}},
		{path: "/api/v1/auth/credentials/self/rotate", method: "post", operation: "rotateOwnCredential", statuses: []string{"200", "400", "401", "429", "503"}},
		{path: "/api/v1/auth/credentials/self/revoke", method: "post", operation: "revokeOwnCredential", statuses: []string{"200", "400", "401", "429", "503"}},
	}
	for _, test := range tests {
		operation, ok := document.Paths[test.path][test.method]
		if !ok || operation.OperationID != test.operation {
			t.Fatalf("%s %s operation = %#v", test.method, test.path, operation)
		}
		for _, status := range test.statuses {
			if _, ok := operation.Responses[status]; !ok {
				t.Fatalf("%s %s is missing response %s", test.method, test.path, status)
			}
		}
	}
	assertOpenAPIParameter(t, document.Components.Parameters["RequestID"], "X-Request-ID", false, "Optional correlation ID for request tracing and audit. The service generates and returns a request ID when absent or invalid. It does not authenticate or authorize the caller.")
	assertOpenAPIParameter(t, document.Components.Parameters["ClientVersion"], "X-ACR-Client-Version", true, "Required SemVer compatibility signal for protected routes. It is advisory only and does not authenticate or authorize the caller; unsupported versions return 426 version_mismatch.")
	for _, route := range []struct{ path, method string }{
		{path: "/api/v1/agent-context/capabilities", method: "get"},
		{path: "/api/v1/agent-context/context-packets", method: "post"},
		{path: "/api/v1/agent-context/context-packets/{context_packet_id}", method: "get"},
		{path: "/api/v1/agent-context/evidence/{evidence_ref_id}", method: "get"},
	} {
		if !hasSecurityAlternative(document.Paths[route.path][route.method].Security, "webAssertionAuth") {
			t.Fatalf("%s %s must allow web assertions", route.method, route.path)
		}
	}
	if hasSecurityAlternative(document.Paths["/api/v1/agent-context/episodes"]["post"].Security, "webAssertionAuth") {
		t.Fatal("episode writes must remain bearer-only")
	}
	web := document.Components.SecuritySchemes["webAssertionAuth"]
	if web.Type != "apiKey" || web.In != "header" || web.Name != "X-ACR-Web-Assertion" {
		t.Fatalf("web assertion security scheme = %#v", web)
	}
}

func hasSecurityAlternative(security []map[string][]string, scheme string) bool {
	for _, alternative := range security {
		if _, found := alternative[scheme]; found {
			return true
		}
	}
	return false
}

func assertOpenAPIParameter(t *testing.T, parameter struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}, name string, required bool, description string) {
	t.Helper()
	if parameter.Name != name || parameter.Required != required || parameter.Description != description {
		t.Fatalf("OpenAPI parameter = %#v", parameter)
	}
}
