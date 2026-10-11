package mcp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// An organization with agent_context_runtime but neither acr_service_model
// nor its own model configuration, through the real MCP server, sidecar and
// acr-api routes: the data tools serve as before, and an investigation that
// would run on the service's model is the entitlement refusal that names the
// caller's own-model path, with the model seam never reached.
func TestServiceModelRefusalAtTheToolBoundaryLeavesTheDataToolsOpen(t *testing.T) {
	stack := newDTStackWith(t, api.EntitlementFunc(func(_ context.Context, _ string, entitlement string) (bool, error) {
		return entitlement == "agent_context_runtime", nil
	}))
	cs, _ := stack.session([]string{auth.ScopeContextRead, auth.ScopeDataRead}, []string{"*"})

	found, text := dtCallTool(t, cs, "find_subjects", map[string]any{"kind": "repository"})
	if found == nil || len(found["subjects"].([]any)) != 2 {
		t.Fatalf("find_subjects: %v %q", found, text)
	}
	risk, text := dtCallTool(t, cs, "run_operation", map[string]any{"operation": "compoundingRisk", "variables": map[string]any{"filter": map[string]any{"breakout": "REPO", "repoIds": []string{"repository:" + dtRepoA}}}})
	if risk == nil || risk["call"] != "served" {
		t.Fatalf("run_operation: %v %q", risk, text)
	}
	catalog, text := dtCallTool(t, cs, "data_catalog", map[string]any{"sections": []string{"operations"}})
	if catalog == nil || len(catalog["operations"].(map[string]any)["operations"].([]any)) != 20 {
		t.Fatalf("data_catalog: %v %q", catalog, text)
	}

	for _, args := range []map[string]any{
		{"question": "which teams need attention?"},
		{"question": "which teams need attention?", "synthesis": "client"},
	} {
		refused, text := dtCallTool(t, cs, "investigate_question", args)
		if refused != nil {
			t.Fatalf("investigate_question %v served: %v", args, refused)
		}
		if !strings.HasPrefix(text, "entitlement: reason=service_model_not_entitled: ") || !strings.Contains(text, "investigate_with_interpretation with synthesis \"client\"") || !strings.Contains(text, "own model provider") {
			t.Fatalf("investigate_question %v error text = %q, want the service model refusal naming both options", args, text)
		}
	}
	if n := stack.seam.used.Load(); n != 0 {
		t.Fatalf("the model seam was used %d times", n)
	}
}
