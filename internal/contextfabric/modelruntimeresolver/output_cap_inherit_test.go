package modelruntimeresolver

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
)

func TestOrgModelProviderConfigInheritsTheOutputCap(t *testing.T) {
	got := orgModelProviderConfig(modelprovider.Config{MaxOutputTokens: 8192}, contextfabric.ResolvedOrgModelConfig{Provider: "anthropic", Model: "claude-haiku-5-5"})
	if got.MaxOutputTokens != 8192 {
		t.Fatalf("org MaxOutputTokens = %d, want the deployment default 8192", got.MaxOutputTokens)
	}
}
