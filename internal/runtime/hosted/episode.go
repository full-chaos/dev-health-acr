package hosted

import (
	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/episode"
	"github.com/full-chaos/dev-health-acr/internal/observability"
)

func newEpisode(request episodeServiceRequest) (api.EpisodeCreator, error) {
	return episode.NewService(request.postgres.episodes, request.postgres.audit, episode.ServiceOptions{
		Now: request.options.Now, PacketStore: request.postgres.packets, StoreBackend: episode.StoreBackendPostgres,
		TerminalObserver: observability.NewEpisodeTerminalObserver(request.hooks), StoreObserver: observability.NewEpisodeStoreObserver(request.hooks),
	})
}
