package modelruntimeresolver

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-go/llm"
)

func TestOrgModelProviderConfigNeverInheritsThePromptCache(t *testing.T) {
	defaults := modelprovider.Config{PromptCache: true, MaxAttempts: 2}
	resolved := contextfabric.ResolvedOrgModelConfig{Provider: "anthropic", Model: "claude-sonnet-5-5", Credential: llm.NewSecret("sk-org")}
	if got := orgModelProviderConfig(defaults, resolved); got.PromptCache {
		t.Fatal("an organization's own key got the prompt cache marker: a low-traffic BYO org would pay the write surcharge with no read")
	}
}
