package devhealthsource

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// EpisodesSourceName is the Source value EpisodesProjectionSource writes.
const EpisodesSourceName = "dev_health_episodes"

const episodesSourceVersion = "devhealthsource.episodes.v1"

const (
	episodesIncrementalBatchCap = 200
	episodesSnapshotCap         = 500
)

// EpisodeRows is the narrow read boundary EpisodesProjectionSource needs;
// pgprojection.EpisodeStore satisfies it against Postgres.
type EpisodeRows interface {
	EpisodesSince(ctx context.Context, orgID string, since time.Time, afterID string, limit int) ([]pgprojection.EpisodeRow, error)
}

// EpisodesProjectionSource is the production contextfabric.ProjectionSource
// for approved agent episodes (acr.agent_episodes). "Approved" here means
// durably created through internal/storage.EpisodeStore.CreateIdempotent --
// ACR has no separate approval workflow yet. A later redaction
// (EpisodeStore.Redact) is projected as a tombstone on the next batch that
// observes it, propagating the revocation into the graph.
type EpisodesProjectionSource struct {
	rows EpisodeRows
	now  func() time.Time
}

func NewEpisodesProjectionSource(rows EpisodeRows) (*EpisodesProjectionSource, error) {
	if rows == nil {
		return nil, fmt.Errorf("devhealthsource: episode rows dependency is required")
	}
	return &EpisodesProjectionSource{rows: rows, now: time.Now}, nil
}

func (s *EpisodesProjectionSource) NextProjectionBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	if s == nil || s.rows == nil {
		return contextfabric.ProjectionBatch{}, false, fmt.Errorf("devhealthsource: episode source is not configured")
	}
	orgID := strings.TrimSpace(checkpoint.OrgID)
	if orgID == "" {
		return contextfabric.ProjectionBatch{}, false, fmt.Errorf("devhealthsource: organization is required")
	}
	fullSnapshot := checkpoint.Cursor == ""
	state, err := decodeCursor(checkpoint.Cursor)
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	limit := episodesIncrementalBatchCap
	if fullSnapshot {
		limit = episodesSnapshotCap
	}
	rows, err := s.rows.EpisodesSince(ctx, orgID, state.Since, state.After, limit+1)
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, fmt.Errorf("%w: read approved episodes: %v", contextfabric.ErrUnavailable, err)
	}
	truncated := len(rows) > limit
	if truncated {
		if fullSnapshot {
			return contextfabric.ProjectionBatch{}, false, fmt.Errorf("devhealthsource: organization %s exceeds full-snapshot episode capacity; incremental catch-up is required instead of a rebuild at this scale", redactOrg(orgID))
		}
		rows = rows[:limit]
	}
	if len(rows) == 0 {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	all := make([]candidate, 0, len(rows))
	for _, row := range rows {
		all = append(all, episodeCandidate(row))
	}
	batch, err := buildBatch(orgID, EpisodesSourceName, episodesSourceVersion, checkpoint.Cursor, all, fullSnapshot, fullSnapshot, s.clock())
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	return batch, true, nil
}

func (s *EpisodesProjectionSource) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func episodeCandidate(row pgprojection.EpisodeRow) candidate {
	canonicalID := "episode:" + row.EpisodeID
	if row.RedactionState != "active" {
		tombstone := contractsv1.ContextFabricProjectionTombstone{
			Kind: "episode", CanonicalID: canonicalID, Reason: row.RedactionState, EffectiveAt: row.CreatedAt, SourceVersion: episodesSourceVersion,
		}
		return candidate{observedAt: row.CreatedAt, sortKey: row.EpisodeID, tombstone: &tombstone}
	}
	goal := strings.TrimSpace(row.Goal)
	if goal == "" {
		goal = "(episode goal unavailable)"
	}
	summary := strings.TrimSpace(row.Summary)
	if summary == "" {
		summary = "(episode summary unavailable)"
	}
	label := goal
	if len(label) > 500 {
		label = label[:500]
	}
	episode := contractsv1.ContextFabricEpisodeProjection{
		EpisodeID: canonicalID,
		Subject:   contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectEpisode, CanonicalID: canonicalID, Label: label},
		Goal:      goal, Outcome: row.Outcome, Summary: summary,
		Authorization: repoAuthorization(row.RepoSlug), EvidenceRefIDs: []string{"acr:v1:episode:" + row.EpisodeID},
		StartedAt: row.StartedAt, EndedAt: row.EndedAt, SourceVersion: episodesSourceVersion,
	}
	return candidate{observedAt: row.CreatedAt, sortKey: row.EpisodeID, episode: &episode}
}
