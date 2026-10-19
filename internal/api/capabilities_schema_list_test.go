package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestServedCapabilitiesSchemaListOmitsRetiredOrgModelConfigSchemas(t *testing.T) {
	provider := StaticCapabilitiesProvider{Value: realHostedCapabilities()}
	app, token := newHostedTestApp(t, provider, nil, []string{auth.ScopeContextRead}, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	var served contractsv1.Capabilities
	if err := json.Unmarshal(response.Body.Bytes(), &served); err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{
		"context_fabric_org_model_config.v1",
		"context_fabric_org_model_config_write_request.v1",
	} {
		if slices.Contains(served.SupportedSchemaVersions, retired) {
			t.Fatalf("served supported_schema_versions still lists retired schema %q", retired)
		}
	}
	if !slices.Contains(served.SupportedSchemaVersions, contractsv1.ContextFabricProjectionBatchSchema) {
		t.Fatal("served supported_schema_versions lost a live schema: the list is empty or wrong")
	}
}
