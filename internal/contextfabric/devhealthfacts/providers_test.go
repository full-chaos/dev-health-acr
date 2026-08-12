package devhealthfacts_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// TestNewProvidersRegistersExactlyTheCoveredKinds proves NewProviders
// returns one provider per canonical-table-backed FactKind, that each
// registers cleanly with the real contextfabric.NewFactCapabilityRegistry
// (which rejects duplicate kinds), and that none of the eight kinds with no
// canonical Ops adapter (doc.go) are present.
func TestNewProvidersRegistersExactlyTheCoveredKinds(t *testing.T) {
	t.Parallel()
	providers := devhealthfacts.NewProviders(&fakeClient{})
	registry, err := contextfabric.NewFactCapabilityRegistry(providers, contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
	capabilities := registry.Capabilities()

	want := map[contextfabric.FactKind]bool{
		contextfabric.FactIdentity:              true,
		contextfabric.FactMembership:            true,
		contextfabric.FactStatus:                true,
		contextfabric.FactActualCompletion:      true,
		contextfabric.FactWork:                  true,
		contextfabric.FactBlockers:              true,
		contextfabric.FactRequiredChildren:      true,
		contextfabric.FactPullRequests:          true,
		contextfabric.FactReviews:               true,
		contextfabric.FactContinuousIntegration: true,
		contextfabric.FactDeployments:           true,
		contextfabric.FactIncidents:             true,
	}
	if len(capabilities) != len(want) {
		t.Fatalf("capabilities = %d, want %d: %+v", len(capabilities), len(want), capabilities)
	}
	gated := []contextfabric.FactKind{
		contextfabric.FactMetrics, contextfabric.FactHealth, contextfabric.FactWorkload,
		contextfabric.FactInvestment, contextfabric.FactReadiness, contextfabric.FactOperationalDeficiencies,
		contextfabric.FactSourceHealth, contextfabric.FactEvidence,
	}
	for _, capability := range capabilities {
		if !want[capability.Kind] {
			t.Fatalf("unexpected capability registered: %+v", capability)
		}
		delete(want, capability.Kind)
		if capability.Version == "" {
			t.Fatalf("capability %q missing version", capability.Kind)
		}
		if len(capability.SupportedSubjectKinds) == 0 {
			t.Fatalf("capability %q missing supported subject kinds", capability.Kind)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing capabilities: %+v", want)
	}
	for _, kind := range gated {
		for _, capability := range capabilities {
			if capability.Kind == kind {
				t.Fatalf("gated fact kind %q must not be registered", kind)
			}
		}
	}
}
