package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// CHAOS-7167: an invalid_request whose details carry a closed-shape reason
// token must show that reason in the client-visible tool error, and only
// that: never a count, subject, id or name.
func TestCHAOS7167_InvalidRequestToolErrorSurfacesReasonOnly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		details map[string]any
		want    string
		absent  []string
	}{
		{"scope_required", map[string]any{"reason": "scope_required", "count": 0, "subject_id": "work_item_secret"}, "reason=scope_required", []string{"work_item_secret", "count"}},
		{"free_text_reason_dropped", map[string]any{"reason": "team Payments has 3 items"}, "", []string{"reason=", "Payments", "3 items"}},
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
			result := callReadFacts(t, &Bootstrap{Config: cfg, Client: client, Capabilities: caps}, readFactsArgs)
			if !result.IsError {
				t.Fatal("want a tool error")
			}
			text := toolResultText(result)
			if !strings.Contains(text, "code=invalid_request") || !strings.Contains(text, tc.want) {
				t.Errorf("tool text %q lacks %q", text, tc.want)
			}
			for _, a := range tc.absent {
				if strings.Contains(text, a) {
					t.Errorf("tool text %q must not contain %q", text, a)
				}
			}
		})
	}
}
