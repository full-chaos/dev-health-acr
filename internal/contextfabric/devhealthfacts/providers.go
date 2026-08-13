package devhealthfacts

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// NewProviders returns every contextfabric.FactProvider this package
// implements, ready to hand to contextfabric.NewFactCapabilityRegistry. It
// registers exactly one provider per FactKind, as the registry requires --
// see doc.go for the full list of FactKinds this package covers and the
// eight it deliberately leaves unregistered.
func NewProviders(client contextpacket.ClickHouseQueryClient) []contextfabric.FactProvider {
	return []contextfabric.FactProvider{
		newIdentityProvider(client),
		newMembershipProvider(client),
		newStatusProvider(client),
		newActualCompletionProvider(client),
		newWorkProvider(client),
		newBlockersProvider(client),
		newRequiredChildrenProvider(client),
		newPullRequestsProvider(client),
		newReviewsProvider(client),
		newContinuousIntegrationProvider(client),
		newDeploymentsProvider(client),
		newIncidentsProvider(client),
		newMetricsProvider(client),
		newHealthProvider(client),
		newWorkloadProvider(client),
		newInvestmentProvider(client),
		newReadinessProvider(client),
		newOperationalDeficienciesProvider(client),
		newSourceHealthProvider(client),
	}
}
