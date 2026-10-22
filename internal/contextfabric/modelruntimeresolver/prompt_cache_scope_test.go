package modelruntimeresolver

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-go/llm"
)

func TestOrgModelProviderConfigPromptCacheFollowsTheOrgOptIn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		defaults bool
		provider string
		optedIn  bool
		want     bool
	}{
		{"anthropic org that opted in", false, "anthropic", true, true},
		{"anthropic org that opted in, deployment default on", true, "anthropic", true, true},
		{"anthropic org that did not opt in, deployment default on", true, "anthropic", false, false},
		{"anthropic org that did not opt in, deployment default off", false, "anthropic", false, false},
		{"openai org that opted in", false, "openai", true, false},
		{"gateway org that opted in", true, "acme-gateway", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defaults := modelprovider.Config{PromptCache: tc.defaults, MaxAttempts: 2}
			resolved := contextfabric.ResolvedOrgModelConfig{Provider: tc.provider, Model: "m", Credential: llm.NewSecret("sk-org"), PromptCache: tc.optedIn}
			if got := orgModelProviderConfig(defaults, resolved).PromptCache; got != tc.want {
				t.Fatalf("PromptCache = %v, want %v", got, tc.want)
			}
		})
	}
}
