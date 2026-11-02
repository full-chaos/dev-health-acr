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
	return NewProvidersWithOperations(client, nil)
}

// NewProvidersWithOperations is NewProviders with the operation holder the
// source_health provider reads the ops sourceHealth root through. A nil
// holder makes that provider answer a limitation row on every read.
func NewProvidersWithOperations(client contextpacket.ClickHouseQueryClient, operations *OperationHolder) []contextfabric.FactProvider {
	return NewProvidersWithOptions(client, ProviderOptions{Operations: operations})
}

// ProviderOptions are the deployment switches the providers read.
type ProviderOptions struct {
	// Operations is the holder the source_health provider reads through; nil
	// answers a limitation row on every read.
	Operations *OperationHolder
	// CompletionDistributionEnabled lets the team workload fact emit its
	// completion distribution (CHAOS-12607; config
	// ACR_CONTEXT_FABRIC_COMPLETION_DISTRIBUTION_ENABLED, default off).
	CompletionDistributionEnabled bool
}

// NewProvidersWithOptions is NewProviders with the deployment switches.
func NewProvidersWithOptions(client contextpacket.ClickHouseQueryClient, options ProviderOptions) []contextfabric.FactProvider {
	operations := options.Operations
	workload := newWorkloadProvider(client)
	workload.completionDistribution = options.CompletionDistributionEnabled
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
		workload,
		newInvestmentProvider(client),
		newReadinessProvider(client),
		newOperationalDeficienciesProvider(client),
		newSourceHealthProvider(operations),
		newFlowProvider(client),
		newLandscapeProvider(client),
		newSecurityAlertsProvider(client),
		newCommitmentProvider(client),
		newFileHotspotsProvider(client),
	}
}
