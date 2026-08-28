package modelruntimeresolver

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
)

// TestOrgModelProviderConfigInheritsTelemetryFromDefaults is the CHAOS-4355
// follow-up wiring proof for the per-organization runtime path: a BYO LLM
// organization's runtime is built from the ORGANIZATION's own
// Provider/BaseURL/Model/Credential, but Telemetry has no per-organization
// analogue -- it must always come from defaults (the SAME sink
// wrapWithOrgModelRuntimeResolver's caller stamped onto defaults), never
// left unset just because the rest of the struct is org-specific.
func TestOrgModelProviderConfigInheritsTelemetryFromDefaults(t *testing.T) {
	telemetry := contextfabric.NewSlogEngineTelemetry(nil)
	defaults := modelprovider.Config{Timeout: 0, MaxAttempts: 2, Telemetry: telemetry}
	resolved := contextfabric.ResolvedOrgModelConfig{Provider: "acme-gateway", Model: "org-model", Credential: "sk-org"}

	got := orgModelProviderConfig(defaults, resolved)

	if got.Telemetry != telemetry {
		t.Fatalf("orgModelProviderConfig().Telemetry = %#v, want %#v", got.Telemetry, telemetry)
	}
	if got.Provider != resolved.Provider || got.Model != resolved.Model || got.APIKey != resolved.Credential {
		t.Fatalf("orgModelProviderConfig() = %#v, want the organization's own Provider/Model/Credential", got)
	}
}
