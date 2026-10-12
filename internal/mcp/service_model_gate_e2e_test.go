package mcp_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// An organization with agent_context_runtime but neither acr_service_model
// nor its own model configuration, through the real MCP server, sidecar and
// acr-api routes: the data tools serve as before, and an investigation that
// would run on the service's model is not offered, and a call to it is refused
// with the model seam never reached.
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

	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "investigate_question" {
			t.Fatal("investigate_question listed for an organization the service model does not serve")
		}
	}
	for _, args := range []map[string]any{
		{"question": "which teams need attention?"},
		{"question": "which teams need attention?", "synthesis": "client"},
	} {
		result, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "investigate_question", Arguments: args})
		if err == nil && (result == nil || !result.IsError) {
			t.Fatalf("investigate_question %v served", args)
		}
	}
	if n := stack.seam.used.Load(); n != 0 {
		t.Fatalf("the model seam was used %d times", n)
	}
}
