package modelruntimeresolver

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
)

// TestOrgModelProviderConfigInheritsResynthesisAttempts mirrors
// TestOrgModelProviderConfigInheritsTheLogger for CHAOS-5655's own knob:
// §19.3.2 puts Timeout/MaxAttempts/MaxTransportRetries on the
// deployment-inherited side of the per-organization contract, and this
// extends the same inheritance rather than carving out an exception -- a
// per-organization BYO runtime gets the SAME bounded re-synthesis policy the
// deployment-default runtime does, never its own per-organization value.
func TestOrgModelProviderConfigInheritsResynthesisAttempts(t *testing.T) {
	defaults := modelprovider.Config{MaxSynthesisResynthesisAttempts: 3}
	resolved := contextfabric.ResolvedOrgModelConfig{Provider: "byo", Model: "byo-model"}

	if got := orgModelProviderConfig(defaults, resolved); got.MaxSynthesisResynthesisAttempts != 3 {
		t.Fatalf("orgModelProviderConfig().MaxSynthesisResynthesisAttempts = %d, want 3 (the deployment default's)", got.MaxSynthesisResynthesisAttempts)
	}
}
