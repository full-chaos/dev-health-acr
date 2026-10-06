package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func TestInvalidRequestToolErrorSurfacesBodyReasonAndField(t *testing.T) {
	for _, tc := range []struct {
		name    string
		details map[string]any
		want    string
		absent  []string
	}{
		{"field_surfaced", map[string]any{"reason": "schema_violation", "field": "options.max_drivers"}, "reason=schema_violation field=options.max_drivers", nil},
		{"unknown_field_surfaced", map[string]any{"reason": "unknown_field", "field": "surprise_field"}, "reason=unknown_field field=surprise_field", nil},
		{"field_with_free_text_dropped", map[string]any{"reason": "schema_violation", "field": "team Payments has 3 items"}, "reason=schema_violation", []string{"Payments", "field="}},
		{"field_without_reason_dropped", map[string]any{"field": "options.max_drivers"}, "", []string{"field=", "max_drivers"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSONFixture(t, w, http.StatusBadRequest, contractsv1.ErrorEnvelope{
					SchemaVersion: contractsv1.ErrorSchema, RequestID: "req_fixture",
					Error: contractsv1.ErrorDetail{Code: "invalid_request", Message: "server text", HTTPStatus: http.StatusBadRequest, Details: tc.details},
				})
			}))
			t.Cleanup(server.Close)
			cfg := fixtureConfig(t, server)
			client, err := sidecar.NewClient(cfg, fixedCredentialSource(fixtureToken(0xAB)))
			if err != nil {
				t.Fatal(err)
			}
			caps := validCapabilitiesFixture()
			caps.EnabledTools = append(caps.EnabledTools, toolReadFacts)
			before := sidecar.DroppedInvalidRequestReasons()
			result := callReadFacts(t, &Bootstrap{Config: cfg, Client: client, Capabilities: caps}, readFactsArgs)
			if !result.IsError {
				t.Fatal("want a tool error")
			}
			text := toolResultText(result)
			if !strings.Contains(text, "code=invalid_request") || !strings.Contains(text, tc.want) {
				t.Errorf("tool text %q lacks %q", text, tc.want)
			}
			if dropped := sidecar.DroppedInvalidRequestReasons() - before; tc.name != "field_without_reason_dropped" && (tc.want == "") != (dropped == 1) {
				t.Errorf("dropped reasons = %d for want=%q", dropped, tc.want)
			}
			for _, a := range tc.absent {
				if strings.Contains(text, a) {
					t.Errorf("tool text %q must not contain %q", text, a)
				}
			}
		})
	}
}
