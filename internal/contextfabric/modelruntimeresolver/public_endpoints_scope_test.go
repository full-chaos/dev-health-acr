package modelruntimeresolver

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-go/llm"
)

// A per-organization BYO config dials public addresses only unless the
// deployment's own allowance says otherwise; the allowance itself is never
// copied into the organization's config, and the organization's choices
// (provider, base_url, an empty base_url) do not move it.
func TestOrgModelProviderConfigPublicEndpointsOnlyFollowsTheDeploymentAllowance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		allow    bool
		provider string
		baseURL  string
		want     bool
	}{
		{"default, own base_url", false, "openai", "https://org.example.invalid/v1", true},
		{"default, no base_url (the provider's own endpoint)", false, "anthropic", "", true},
		{"default, a loopback base_url", false, "openai", "https://127.0.0.1:8443/v1", true},
		{"allowance on, own base_url", true, "openai", "https://org.example.invalid/v1", false},
		{"allowance on, no base_url", true, "anthropic", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defaults := modelprovider.Config{AllowNonPublicBYOEndpoint: tc.allow, MaxAttempts: 2}
			resolved := contextfabric.ResolvedOrgModelConfig{Provider: tc.provider, BaseURL: tc.baseURL, Model: "m", Credential: llm.NewSecret("sk-org")}
			got := orgModelProviderConfig(defaults, resolved)
			if got.PublicEndpointsOnly != tc.want {
				t.Fatalf("PublicEndpointsOnly = %v, want %v", got.PublicEndpointsOnly, tc.want)
			}
			if got.AllowNonPublicBYOEndpoint {
				t.Fatal("the deployment allowance was copied into the organization's config")
			}
		})
	}
}
