package mcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

// turnedOffModelConfigs reads the org model configuration through the real
// entitlement client from an ops stand-in that answers "turned off by an org
// admin" for every org.
type turnedOffModelConfigs struct{ client *entitlements.Client }

func (c turnedOffModelConfigs) ResolveOrgModelConfig(ctx context.Context, orgID string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
	_, found, err := c.client.OrgLLMConfig(ctx, orgID)
	return contextfabric.ResolvedOrgModelConfig{}, found, err
}

func newTurnedOffModelConfigs(t *testing.T) turnedOffModelConfigs {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Org-Llm-Config", "none")
		w.Header().Set("X-Org-Llm-Config-Reason", "byo_llm_turned_off")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Not found"}`))
	}))
	t.Cleanup(server.Close)
	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("svc_acr_standin_token_0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := entitlements.New(entitlements.Config{
		BaseURL: baseURL, TokenFile: tokenFile, Timeout: time.Second, MaxResponseBytes: 4096,
		PositiveCacheTTL: time.Second, NegativeCacheTTL: time.Second, CacheCapacity: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return turnedOffModelConfigs{client: client}
}

// An organization whose admin turned its saved model configuration off,
// holding acr_service_model but not platform_model_fallback, through the real
// MCP server, sidecar and acr-api routes: investigate_question stays listed so
// the call answers the reason, and the call is refused with
// own_model_turned_off with the model seam never reached.
func TestOwnModelTurnedOffRefusalReachesTheToolCaller(t *testing.T) {
	stack := newDTStackWithModelConfigs(t, api.EntitlementFunc(func(_ context.Context, _ string, entitlement string) (bool, error) {
		return entitlement == "agent_context_runtime" || entitlement == "acr_service_model", nil
	}), newTurnedOffModelConfigs(t))
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
		t.Fatal("investigate_question not listed for an organization whose own model is turned off")
	}
	result, text := dtCallTool(t, cs, "investigate_question", map[string]any{"question": "which teams need attention?"})
	if result != nil {
		t.Fatalf("investigate_question served: %v", result)
	}
	if !strings.Contains(text, "reason=own_model_turned_off") {
		t.Fatalf("refusal text = %q, want reason=own_model_turned_off", text)
	}
	if n := stack.seam.used.Load(); n != 0 {
		t.Fatalf("the model seam was used %d times", n)
	}
}

// investigate_with_interpretation is recommended to a model that can run
// prompts/get, and its description says the client path needs no plan feature
// and no model configuration. The gate refuses that path for an organization
// whose saved model configuration it cannot use, so what the server tells a
// caller up front must name those refusals, in the tool description and in the
// initialize instructions alike.
func TestCallerModelPathGuidanceNamesTheRefusalsOfAnUnusableOwnModel(t *testing.T) {
	stack := newDTStackWithModelConfigs(t, api.EntitlementFunc(func(_ context.Context, _ string, entitlement string) (bool, error) {
		return entitlement == "agent_context_runtime" || entitlement == "acr_service_model", nil
	}), newTurnedOffModelConfigs(t))
	cs, _ := stack.session([]string{auth.ScopeContextRead, auth.ScopeDataRead}, []string{"*"})

	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	reasons := []string{"own_model_gate_closed", "ai_features_turned_off", "byo_no_investigate_config", "own_model_turned_off"}
	description := ""
	for _, tool := range listed.Tools {
		if tool.Name == "investigate_with_interpretation" {
			description = tool.Description
		}
	}
	if description == "" {
		t.Fatal("investigate_with_interpretation not listed")
	}
	instructions := cs.InitializeResult().Instructions
	for _, reason := range reasons {
		if !strings.Contains(description, reason) {
			t.Errorf("investigate_with_interpretation description does not name %s: %s", reason, description)
		}
		if !strings.Contains(instructions, reason) {
			t.Errorf("initialize instructions do not name %s: %s", reason, instructions)
		}
	}
	if !strings.Contains(instructions, "investigate_with_interpretation is refused with the organization's reason") {
		t.Errorf("initialize instructions do not say the caller-model tool is refused with the organization's reason: %s", instructions)
	}
}
