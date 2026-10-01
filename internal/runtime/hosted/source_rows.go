package hosted

import (
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/sourcerow"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// buildSourceRows composes the CHAOS-6180 source-row resolver over the
// runtime's one ClickHouse client, with the direct data tools' subject gate
// as the ONE ownership authority for team and project rows (CHAOS-7227; nil
// without a graph, and those kinds then stay on the persisted record). It
// returns nil (never a typed nil) when no client is composed, and logs why
// when composition fails, so a deployment whose every acr:v1 ref stays on the
// persisted record says so at startup.
func buildSourceRows(client contextpacket.ClickHouseQueryClient, assembly contextpacket.AssemblyObserver, expansion contextpacket.EvidenceExpansionObserver, gate *directread.SubjectGate, logger *slog.Logger) contextfabric.SourceRowResolver {
	if storage.IsNil(client) {
		if logger != nil {
			logger.Info("context fabric source-row expansion not composed", "reason", "no_clickhouse_client")
		}
		return nil
	}
	var subjects sourcerow.SubjectGate
	if gate != nil {
		subjects = gate
	} else if logger != nil {
		logger.Info("context fabric source-row expansion: no subject gate, team and project refs stay on the persisted record")
	}
	resolver, err := sourcerow.New(contextpacket.NewObservedCatalogClickHouseRows(client, assembly), contextpacket.NewEvidenceResolver(contextpacket.EvidenceResolverOptions{Observer: expansion}), subjects)
	if err != nil {
		if logger != nil {
			logger.Error("context fabric source-row expansion not composed", "reason", "resolver_rejected")
		}
		return nil
	}
	return resolver.WithRepoLessAdmitter(devhealthfacts.NewRepoLessWorkItemAdmitter(client))
}
