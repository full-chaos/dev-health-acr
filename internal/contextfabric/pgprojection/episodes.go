package pgprojection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// EpisodeRow is one acr.agent_episodes row, narrowed to what projection
// needs. RedactionState mirrors the column directly ("active", "redacted",
// or "purged_tombstone" -- migrations/postgres/0001_acr_core.sql): only
// "active" episodes are approved for projection as content; the other two
// states mean the episode must become a ProjectionTombstone instead.
type EpisodeRow struct {
	EpisodeID       string
	RepoSlug        string
	ClientEpisodeID string
	TaskRef         string
	Goal            string
	Outcome         string
	Summary         string
	StartedAt       time.Time
	EndedAt         time.Time
	CreatedAt       time.Time
	RedactionState  string
}

// episodePayload mirrors internal/storage/postgres/episode_helpers.go's
// unexported episodePayload -- the stored wire shape of acr.agent_episodes.payload.
// Duplicated rather than imported: pgprojection stays self-contained against
// Postgres (see doc.go) instead of depending on internal/storage/postgres.
type episodePayload struct {
	Episode *contractsv1.AgentEpisodeCreate `json:"episode,omitempty"`
}

// EpisodeStore is the production episode-projection read boundary. It reads
// the same acr.agent_episodes table internal/storage.EpisodeStore writes,
// through its own SQL rather than that package's interfaces -- see doc.go.
type EpisodeStore struct {
	db *sql.DB
}

func NewEpisodeStore(db *sql.DB) (*EpisodeStore, error) {
	if db == nil {
		return nil, errors.New("pgprojection: episode store requires a database")
	}
	return &EpisodeStore{db: db}, nil
}

// EpisodesSince returns up to limit episodes for orgID ordered by
// (created_at, episode_id), strictly after (since, afterID).
func (s *EpisodeStore) EpisodesSince(ctx context.Context, orgID string, since time.Time, afterID string, limit int) ([]EpisodeRow, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("pgprojection: episode store is not configured")
	}
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return nil, errors.New("pgprojection: organization is required")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT episode_id, repo_slug, client_episode_id, redaction_state, outcome, started_at, ended_at, created_at, payload
FROM acr.agent_episodes
WHERE org_id = $1 AND (created_at > $2 OR (created_at = $2 AND episode_id > $3))
ORDER BY created_at ASC, episode_id ASC
LIMIT $4`, orgID, since, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("query approved episodes: %w", sanitizeError(err))
	}
	defer rows.Close()
	result := make([]EpisodeRow, 0, limit)
	for rows.Next() {
		var row EpisodeRow
		var payload []byte
		if err := rows.Scan(&row.EpisodeID, &row.RepoSlug, &row.ClientEpisodeID, &row.RedactionState, &row.Outcome, &row.StartedAt, &row.EndedAt, &row.CreatedAt, &payload); err != nil {
			return nil, fmt.Errorf("scan approved episode: %w", sanitizeError(err))
		}
		row.StartedAt, row.EndedAt, row.CreatedAt = row.StartedAt.UTC(), row.EndedAt.UTC(), row.CreatedAt.UTC()
		if row.RedactionState == "active" {
			var stored episodePayload
			if err := json.Unmarshal(payload, &stored); err == nil && stored.Episode != nil {
				row.Goal, row.Summary, row.TaskRef = stored.Episode.Goal, stored.Episode.Summary, stored.Episode.TaskRef
			}
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate approved episodes: %w", sanitizeError(err))
	}
	return result, nil
}
