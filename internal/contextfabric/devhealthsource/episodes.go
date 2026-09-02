package devhealthsource

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// EpisodesSourceName is the Source value EpisodesProjectionSource writes.
const EpisodesSourceName = "dev_health_episodes"

const EpisodesSourceVersion = "devhealthsource.episodes.v1"

const (
	episodesIncrementalBatchCap = 200
	episodesSnapshotCap         = 500
)

// EpisodeRows is the narrow read boundary EpisodesProjectionSource needs.
// storage.EpisodeStore (both the Postgres and memory implementations)
// satisfies it directly via its ListSince method -- there is no
// projection-only episode-read interface; the projection worker is simply
// one more EpisodeStore caller, like internal/episode.Service.
type EpisodeRows interface {
	ListSince(ctx context.Context, orgID string, since time.Time, afterEpisodeID string, limit int) ([]storage.EpisodeProjectionRecord, error)
}

// EpisodesProjectionSource is the production contextfabric.ProjectionSource
// for approved agent episodes (acr.agent_episodes). "Approved" here means
// durably created through storage.EpisodeStore.CreateIdempotent -- ACR has
// no separate approval workflow yet. A later redaction
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
	fromScratch := checkpoint.Cursor == ""
	state, err := decodeCursor(checkpoint.Cursor)
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	limit := episodesIncrementalBatchCap
	if fromScratch {
		limit = episodesSnapshotCap
	}
	rows, err := s.rows.ListSince(ctx, orgID, state.Since, state.After, limit+1)
	if err != nil {
		// Same bounded classification the ClickHouse-backed sources use
		// (assemble.go's tableReadError). This boundary escaped CHAOS-3802's
		// first error sweep because that sweep was scoped to the files the
		// branch had changed rather than to the ProjectionSource boundary as
		// a class; today's EpisodeStore happens to sanitize, but the boundary
		// does not enforce it, so an alternate provider would put driver text
		// straight into coordinator logs.
		return contextfabric.ProjectionBatch{}, false, &tableReadError{table: "approved episodes", cause: err}
	}
	// CHAOS-3753 codex round-2 finding K3: an oversized from-scratch read
	// (more than episodesSnapshotCap approved episodes) used to hard-error
	// here instead of paging, mirroring the ClickHouse source's C6 bug --
	// and because a rebuild always resets the checkpoint to the zero
	// cursor, that error was permanent, not transient: every subsequent
	// tick re-attempted the identical oversized from-scratch read and
	// failed the same way forever. It now pages like the ClickHouse
	// source's C6 fix: cap this page at limit and let the caller resume
	// from NextCursor next tick, same as ordinary incremental catch-up.
	// Every episodeCandidate is exactly one candidate per row (never an
	// entity-plus-relationship pair like devhealthsource's ClickHouse
	// tables), so a plain slice truncation here is already row-safe --
	// K2's truncateToCompleteRows has nothing to protect against.
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	if len(rows) == 0 {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	all := make([]candidate, 0, len(rows))
	for _, row := range rows {
		all = append(all, episodeCandidate(row))
	}
	// A batch may only claim FullSnapshot+CompleteEnumeration when it
	// genuinely enumerated everything: fromScratch AND not truncated.
	complete := fromScratch && !truncated
	// cursorSource and items are the same slice here: EpisodesProjectionSource
	// is a separate source with its own assembly path and no quarantine
	// telemetry hook, so CHAOS-4874's per-item quarantine is deliberately NOT
	// applied to it in this change -- adding a drop with nowhere to report it
	// would trade a visible wedge for a silent loss. Its own quarantine is
	// tracked as follow-up work, not left implicit here.
	batch, err := buildBatch(orgID, EpisodesSourceName, EpisodesSourceVersion, checkpoint.Cursor, all, all, complete, complete, s.clock())
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

// episodeCandidate's observedAt (and a tombstone's EffectiveAt) MUST be
// row.UpdatedAt, not row.CreatedAt -- CHAOS-3753 codex finding C4/C5:
// EpisodeStore.ListSince's watermark is UpdatedAt (see its doc comment), so
// the cursor buildBatch encodes from the last candidate's observedAt must
// match that exact column, or a state change that legitimately advanced
// the watermark would encode a NextCursor pointing at the row's stale
// CreatedAt position -- the row would either be skipped forever (if
// CreatedAt < UpdatedAt, the common case) or replayed forever (if a caller
// re-derives since from it), never converging with ListSince's own
// ordering.
func episodeCandidate(row storage.EpisodeProjectionRecord) candidate {
	canonicalID := "episode:" + row.EpisodeID
	if row.RedactionState != "active" {
		tombstone := contractsv1.ContextFabricProjectionTombstone{
			Kind: "episode", CanonicalID: canonicalID, Reason: row.RedactionState, EffectiveAt: row.UpdatedAt, SourceVersion: EpisodesSourceVersion,
		}
		return candidate{observedAt: row.UpdatedAt, sortKey: row.EpisodeID, tombstone: &tombstone}
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
		Authorization: repoAuthorization(row.RepoSlug), EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityEpisode, row.EpisodeID)},
		StartedAt: row.StartedAt, EndedAt: row.EndedAt, SourceVersion: EpisodesSourceVersion,
	}
	return candidate{observedAt: row.UpdatedAt, sortKey: row.EpisodeID, episode: &episode}
}
