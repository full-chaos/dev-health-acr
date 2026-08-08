package memory

import (
	"context"
	"sort"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// defaultEpisodeListLimit and maxEpisodeListLimit bound List the same way
// sourceEvidenceRowLimit bounds catalog evidence: an adapter never trusts a
// caller-supplied limit unclamped.
const (
	defaultEpisodeListLimit = 20
	maxEpisodeListLimit     = 100
)

// GetByEpisodeID looks up a single episode by its server-assigned ID. Cross-
// tenant access, an unauthorized repository scope, a purged/tombstoned
// episode, and retention expiry all collapse to the same ErrNotFound as a
// missing ID -- see the EpisodeStore interface doc for why that matters.
func (s *EpisodeStore) GetByEpisodeID(_ context.Context, principal storage.Principal, episodeID string) (contractsv1.AgentEpisode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, exists := s.byID[episodeID]
	if !exists || record.orgID != principal.OrgID || record.episode.RedactionState == "purged_tombstone" ||
		episodeExpired(record.episode, time.Now().UTC()) || !episodeRepositoryAllowed(principal.RepositoryScopes, record.repoSlug) {
		return contractsv1.AgentEpisode{}, storage.ErrNotFound
	}
	return presentation(record.episode), nil
}

// List returns the caller's episodes, newest first, applying the same
// org/repository-scope/redaction/retention rules as GetByEpisodeID to every
// row rather than only to a single lookup.
func (s *EpisodeStore) List(_ context.Context, principal storage.Principal, repositorySlug string, limit int) ([]contractsv1.AgentEpisode, error) {
	if limit <= 0 {
		limit = defaultEpisodeListLimit
	}
	if limit > maxEpisodeListLimit {
		limit = maxEpisodeListLimit
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()
	results := make([]contractsv1.AgentEpisode, 0, limit)
	for _, record := range s.byID {
		if record.orgID != principal.OrgID || record.episode.RedactionState == "purged_tombstone" ||
			episodeExpired(record.episode, now) || !episodeRepositoryAllowed(principal.RepositoryScopes, record.repoSlug) {
			continue
		}
		if repositorySlug != "" && !sameEpisodeRepository(record.repoSlug, repositorySlug) {
			continue
		}
		results = append(results, presentation(record.episode))
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].CreatedAt.Equal(results[j].CreatedAt) {
			return results[i].EpisodeID < results[j].EpisodeID
		}
		return results[i].CreatedAt.After(results[j].CreatedAt)
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}
