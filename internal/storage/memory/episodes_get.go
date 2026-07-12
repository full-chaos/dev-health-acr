package memory

import (
	"context"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func (s *EpisodeStore) GetByClientEpisodeID(_ context.Context, principal storage.Principal, clientEpisodeID string) (contractsv1.AgentEpisode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result contractsv1.AgentEpisode
	for _, record := range s.byID {
		if record.orgID != principal.OrgID || record.clientID != clientEpisodeID || record.episode.RedactionState == "purged_tombstone" || episodeExpired(record.episode, time.Now().UTC()) || !episodeRepositoryAllowed(principal.RepositoryScopes, record.repoSlug) {
			continue
		}
		if result.EpisodeID != "" {
			return contractsv1.AgentEpisode{}, storage.ErrNotFound
		}
		result = presentation(record.episode)
	}
	if result.EpisodeID == "" {
		return contractsv1.AgentEpisode{}, storage.ErrNotFound
	}
	return result, nil
}
