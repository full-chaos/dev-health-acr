package mcp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

type gateClosedModelConfigs struct{}

func (gateClosedModelConfigs) ResolveOrgModelConfig(context.Context, string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
	return contextfabric.ResolvedOrgModelConfig{}, false, entitlements.ErrLLMConfigGateClosed
}

// An organization whose saved model configuration is behind a closed byo_llm
// gate, holding acr_service_model and platform_model_fallback, through the real
// MCP server, sidecar and acr-api routes: investigate_question stays listed so
// the call answers the reason, and the call is refused with
// own_model_gate_closed with the model seam never reached.
func TestOwnModelGateClosedRefusalReachesTheToolCaller(t *testing.T) {
	stack := newDTStackWithModelConfigs(t, api.EntitlementFunc(func(_ context.Context, _ string, entitlement string) (bool, error) {
		return entitlement == "agent_context_runtime" || entitlement == "acr_service_model" || entitlement == "platform_model_fallback", nil
	}), gateClosedModelConfigs{})
	cs, _ := stack.session([]string{auth.ScopeContextRead, auth.ScopeDataRead}, []string{"*"})

	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range listed.Tools {
		found = found || tool.Name == "investigate_question"
	}
	if !found {
		t.Fatal("investigate_question not listed for an organization whose own model is behind a closed gate")
	}
	result, text := dtCallTool(t, cs, "investigate_question", map[string]any{"question": "which teams need attention?"})
	if result != nil {
		t.Fatalf("investigate_question served: %v", result)
	}
	if !strings.Contains(text, "reason=own_model_gate_closed") {
		t.Fatalf("refusal text = %q, want reason=own_model_gate_closed", text)
	}
	if n := stack.seam.used.Load(); n != 0 {
		t.Fatalf("the model seam was used %d times", n)
	}
}
