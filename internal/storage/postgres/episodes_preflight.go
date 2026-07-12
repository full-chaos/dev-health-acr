package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func (s *EpisodeStore) PreflightIdempotency(ctx context.Context, principal storage.Principal, create contractsv1.AgentEpisodeCreate) (storage.EpisodePreflight, error) {
	if err := ctx.Err(); err != nil {
		return storage.EpisodePreflightMiss, err
	}
	if !episodeRepositoryAllowed(principal.RepositoryScopes, create.Repository.Slug) {
		return storage.EpisodePreflightMiss, storage.ErrNotFound
	}
	repositoryID, err := authorizedRepositoryStorageID(principal, create.Repository.Slug)
	if err != nil {
		return storage.EpisodePreflightMiss, err
	}
	payload, err := json.Marshal(create)
	if err != nil {
		return storage.EpisodePreflightMiss, fmt.Errorf("encode episode preflight payload: %w", err)
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT repo_slug, payload FROM acr.agent_episodes
WHERE org_id = $1::uuid AND repo_id = $2::uuid AND (idempotency_key = $3 OR client_episode_id = $4)`, principal.OrgID, repositoryID, create.IdempotencyKey, create.ClientEpisodeID)
	if err != nil {
		return storage.EpisodePreflightMiss, fmt.Errorf("load episode preflight: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return storage.EpisodePreflightMiss, rows.Err()
	}
	var repoSlug string
	var storedPayload []byte
	if err := rows.Scan(&repoSlug, &storedPayload); err != nil {
		return storage.EpisodePreflightMiss, fmt.Errorf("scan episode preflight: %w", err)
	}
	if !sameEpisodeRepository(repoSlug, create.Repository.Slug) {
		return storage.EpisodePreflightConflict, nil
	}
	if rows.Next() || episodePayloadDigest(storedPayload) != episodeDigest(payload) {
		return storage.EpisodePreflightConflict, rows.Err()
	}
	if err := rows.Err(); err != nil {
		return storage.EpisodePreflightMiss, err
	}
	return storage.EpisodePreflightIdentical, nil
}

func sameEpisodeRepository(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}
