package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// defaultEpisodeListLimit and maxEpisodeListLimit bound List the same way
// memory.EpisodeStore does: an adapter never trusts a caller-supplied limit
// unclamped.
const (
	defaultEpisodeListLimit = 20
	maxEpisodeListLimit     = 100
)

// GetByEpisodeID looks up a single episode by its server-assigned ID, scoped
// to the caller's org. Cross-tenant access, an unauthorized repository
// scope, a purged/tombstoned episode, and retention expiry all collapse to
// the same ErrNotFound as a missing ID.
func (s *EpisodeStore) GetByEpisodeID(ctx context.Context, principal storage.Principal, episodeID string) (contractsv1.AgentEpisode, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT episode_id, repo_slug, payload, created_at, redaction_state
FROM acr.agent_episodes
WHERE org_id = $1::uuid AND episode_id = $2
  AND redaction_state <> 'purged_tombstone'
  AND (expires_at IS NULL OR expires_at > NOW())`, principal.OrgID, episodeID)
	episode, err := scanAuthorizedEpisode(row, principal)
	if err != nil {
		return contractsv1.AgentEpisode{}, mapNotFound("get episode", err)
	}
	return episode, nil
}

// List returns the caller's episodes, newest first, optionally filtered to
// one repository slug. The repository-scope predicate is pushed into SQL
// (the same jsonb_array_elements_text pattern purgeExpired uses,
// episodes.go) so it applies BEFORE the LIMIT, not just in Go afterward: a
// credential scoped to only some repositories in the org must never receive
// a short or empty page because newer episodes in OTHER repositories filled
// the LIMIT window first (review finding M3). scanAuthorizedEpisode's
// per-row Go re-check stays in place as belt-and-braces defense in depth,
// not as the only thing standing between an out-of-scope row and the
// response.
func (s *EpisodeStore) List(ctx context.Context, principal storage.Principal, repositorySlug string, limit int) ([]contractsv1.AgentEpisode, error) {
	if limit <= 0 {
		limit = defaultEpisodeListLimit
	}
	if limit > maxEpisodeListLimit {
		limit = maxEpisodeListLimit
	}
	scopesJSON, err := json.Marshal(principal.RepositoryScopes)
	if err != nil {
		return nil, fmt.Errorf("encode repository scopes: %w", err)
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT episode_id, repo_slug, payload, created_at, redaction_state
FROM acr.agent_episodes
WHERE org_id = $1::uuid
  AND redaction_state <> 'purged_tombstone'
  AND (expires_at IS NULL OR expires_at > NOW())
  AND ($2 = '' OR repo_slug = $2)
  AND EXISTS (
          SELECT 1 FROM jsonb_array_elements_text($4::jsonb) AS allowed(scope)
          WHERE allowed.scope = '*' OR repo_slug = allowed.scope OR repo_slug LIKE replace(allowed.scope, '/*', '/%')
      )
ORDER BY created_at DESC, episode_id ASC
LIMIT $3`, principal.OrgID, repositorySlug, limit, string(scopesJSON))
	if err != nil {
		return nil, fmt.Errorf("list episodes: %w", err)
	}
	defer rows.Close()
	episodes := make([]contractsv1.AgentEpisode, 0, limit)
	for rows.Next() {
		episode, err := scanAuthorizedEpisode(rows, principal)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				// Out-of-scope repository or a payload decode that hit
				// decodeEpisode's own not-found path: omit the row, never
				// surface it or fail the whole list for it.
				continue
			}
			return nil, fmt.Errorf("scan listed episode: %w", err)
		}
		episodes = append(episodes, episode)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate listed episodes: %w", err)
	}
	return episodes, nil
}
