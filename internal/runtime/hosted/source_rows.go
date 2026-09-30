package hosted

import (
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/sourcerow"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// buildSourceRows composes the CHAOS-6180 source-row resolver over the
// runtime's one ClickHouse client. It returns nil (never a typed nil) when no
// client is composed, and logs why when composition fails, so a deployment
// whose every acr:v1 ref stays on the persisted record says so at startup.
func buildSourceRows(client contextpacket.ClickHouseQueryClient, assembly contextpacket.AssemblyObserver, expansion contextpacket.EvidenceExpansionObserver, logger *slog.Logger) contextfabric.SourceRowResolver {
	if storage.IsNil(client) {
		if logger != nil {
			logger.Info("context fabric source-row expansion not composed", "reason", "no_clickhouse_client")
		}
		return nil
	}
	resolver, err := sourcerow.New(contextpacket.NewObservedCatalogClickHouseRows(client, assembly), contextpacket.NewEvidenceResolver(contextpacket.EvidenceResolverOptions{Observer: expansion}))
	if err != nil {
		if logger != nil {
			logger.Error("context fabric source-row expansion not composed", "reason", "resolver_rejected")
		}
		return nil
	}
	return resolver
}
