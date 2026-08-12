package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const redactedEpisodeText = "[redacted]"

type EpisodeStore struct {
	DB         *sql.DB
	GenerateID func() (string, error)
}

func NewEpisodeStore(db *sql.DB) (*EpisodeStore, error) {
	if db == nil {
		return nil, errors.New("PostgreSQL database is required")
	}
	return &EpisodeStore{DB: db, GenerateID: generateUUID}, nil
}

func (s *EpisodeStore) CreateIdempotent(ctx context.Context, principal storage.Principal, create contractsv1.AgentEpisodeCreate, expiresAt *time.Time) (contractsv1.AgentEpisode, bool, error) {
	if !episodeRepositoryAllowed(principal.RepositoryScopes, create.Repository.Slug) {
		return contractsv1.AgentEpisode{}, false, storage.ErrNotFound
	}
	repositoryID, err := authorizedRepositoryStorageID(principal, create.Repository.Slug)
	if err != nil {
		return contractsv1.AgentEpisode{}, false, err
	}
	payload, err := json.Marshal(create)
	if err != nil {
		return contractsv1.AgentEpisode{}, false, fmt.Errorf("encode episode payload: %w", err)
	}
	digestText := episodeDigest(payload)
	state := "active"
	storedPayload := episodePayload{Digest: digestText, Episode: &create}
	if create.RetentionClass == "no_persist" {
		storedPayload = episodePayload{Digest: digestText, Tombstone: true}
		state = "purged_tombstone"
	}
	payload, err = json.Marshal(storedPayload)
	if err != nil {
		return contractsv1.AgentEpisode{}, false, fmt.Errorf("encode stored episode payload: %w", err)
	}
	id, err := s.GenerateID()
	if err != nil {
		return contractsv1.AgentEpisode{}, false, fmt.Errorf("generate episode id: %w", err)
	}
	row := s.DB.QueryRowContext(ctx, `
INSERT INTO acr.agent_episodes (
    episode_id, org_id, repo_id, repo_slug, context_packet_id, client_episode_id,
    idempotency_key, schema_version, outcome, retention_class, redaction_state, payload, started_at,
    ended_at, created_at, expires_at
) VALUES ($1, $2::uuid, $3::uuid, $4, NULLIF($5, ''), $6, $7, $8, $9, $10, $11, $12::jsonb, $13, $14, NOW(), $15)
ON CONFLICT DO NOTHING
	RETURNING episode_id, payload, created_at, redaction_state`, id, principal.OrgID, repositoryID,
		create.Repository.Slug, create.ContextPacketID, create.ClientEpisodeID, create.IdempotencyKey, contractsv1.AgentEpisodeSchema,
		create.Outcome, create.RetentionClass, state, string(payload), create.StartedAt, create.EndedAt, expiresAt)
	episode, err := scanEpisode(row)
	if err == nil {
		return episode, false, nil
	}
	if create.RetentionClass == "no_persist" && errors.Is(err, storage.ErrNotFound) {
		return contractsv1.AgentEpisode{}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return contractsv1.AgentEpisode{}, false, fmt.Errorf("insert episode: %w", err)
	}
	existing, err := s.existing(ctx, principal, repositoryID, create, digestText)
	if err != nil {
		return contractsv1.AgentEpisode{}, false, err
	}
	return existing, true, nil
}

func (s *EpisodeStore) GetByClientEpisodeID(ctx context.Context, principal storage.Principal, clientEpisodeID string) (contractsv1.AgentEpisode, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT episode_id, repo_slug, payload, created_at, redaction_state
FROM acr.agent_episodes
WHERE org_id = $1::uuid AND client_episode_id = $2
  AND redaction_state <> 'purged_tombstone'
  AND (expires_at IS NULL OR expires_at > NOW())`, principal.OrgID, clientEpisodeID)
	episode, err := scanAuthorizedEpisode(row, principal)
	if err != nil {
		return contractsv1.AgentEpisode{}, mapNotFound("get episode", err)
	}
	return episode, nil
}

func (s *EpisodeStore) Redact(ctx context.Context, principal storage.Principal, episodeID, _ string) (contractsv1.AgentEpisode, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return contractsv1.AgentEpisode{}, fmt.Errorf("begin episode redaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `
SELECT episode_id, repo_slug, payload, created_at, redaction_state
	FROM acr.agent_episodes
	WHERE org_id = $1::uuid AND episode_id = $2
	  AND (expires_at IS NULL OR expires_at > NOW())
	FOR UPDATE`, principal.OrgID, episodeID)
	episode, err := scanAuthorizedEpisode(row, principal)
	if err != nil {
		return contractsv1.AgentEpisode{}, mapNotFound("load episode for redaction", err)
	}
	if episode.RedactionState == "active" {
		episode.AgentEpisodeCreate = redactedCreate(episode.AgentEpisodeCreate)
		payload, err := json.Marshal(episode.AgentEpisodeCreate)
		if err != nil {
			return contractsv1.AgentEpisode{}, fmt.Errorf("encode redacted episode: %w", err)
		}
		row = tx.QueryRowContext(ctx, `
UPDATE acr.agent_episodes SET payload = jsonb_set(payload, '{episode}', $3::jsonb), redaction_state = 'redacted', redacted_at = NOW()
WHERE org_id = $1::uuid AND episode_id = $2
RETURNING episode_id, payload, created_at, redaction_state`, principal.OrgID, episodeID, string(payload))
		episode, err = scanEpisode(row)
		if err != nil {
			return contractsv1.AgentEpisode{}, fmt.Errorf("update episode redaction: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return contractsv1.AgentEpisode{}, fmt.Errorf("commit episode redaction: %w", err)
	}
	return episode, nil
}

// ListSince is the org-wide incremental read behind storage.EpisodeStore.
// See the interface doc comment for the full contract.
func (s *EpisodeStore) ListSince(ctx context.Context, orgID string, since time.Time, afterEpisodeID string, limit int) ([]storage.EpisodeProjectionRecord, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return nil, errors.New("organization is required")
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT episode_id, repo_slug, redaction_state, outcome, started_at, ended_at, created_at, payload
FROM acr.agent_episodes
WHERE org_id = $1::uuid AND (created_at > $2 OR (created_at = $2 AND episode_id > $3))
ORDER BY created_at ASC, episode_id ASC
LIMIT $4`, orgID, since, afterEpisodeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list episodes since: %w", sanitizeDatabaseError(err))
	}
	defer rows.Close()
	result := make([]storage.EpisodeProjectionRecord, 0, limit)
	for rows.Next() {
		var record storage.EpisodeProjectionRecord
		var payload []byte
		if err := rows.Scan(&record.EpisodeID, &record.RepoSlug, &record.RedactionState, &record.Outcome, &record.StartedAt, &record.EndedAt, &record.CreatedAt, &payload); err != nil {
			return nil, fmt.Errorf("scan episode since: %w", sanitizeDatabaseError(err))
		}
		record.StartedAt, record.EndedAt, record.CreatedAt = record.StartedAt.UTC(), record.EndedAt.UTC(), record.CreatedAt.UTC()
		if record.RedactionState == "active" {
			if create, err := storedEpisodeCreate(payload); err == nil {
				record.Goal, record.Summary, record.TaskRef = create.Goal, create.Summary, create.TaskRef
			}
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate episodes since: %w", sanitizeDatabaseError(err))
	}
	return result, nil
}

func (s *EpisodeStore) PurgeExpired(ctx context.Context, before time.Time, limit int) (int, error) {
	return 0, errors.New("principal-scoped episode purge is required")
}

func (s *EpisodeStore) PurgeExpiredForOrg(ctx context.Context, principal storage.Principal, before time.Time, limit int) (int, error) {
	return 0, errors.New("repository-scoped episode purge is required")
}

func (s *EpisodeStore) PurgeExpiredForPrincipal(ctx context.Context, principal storage.Principal, before time.Time, limit int) (int, error) {
	if strings.TrimSpace(principal.OrgID) == "" || len(principal.RepositoryScopes) == 0 {
		return 0, storage.ErrNotFound
	}
	return s.purgeExpired(ctx, principal.OrgID, principal.RepositoryScopes, before, limit)
}

func (s *EpisodeStore) purgeExpired(ctx context.Context, orgID string, scopes []string, before time.Time, limit int) (int, error) {
	if strings.TrimSpace(orgID) == "" || len(scopes) == 0 {
		return 0, storage.ErrNotFound
	}
	if limit <= 0 {
		return 0, nil
	}
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return 0, fmt.Errorf("encode repository scopes: %w", err)
	}
	result, err := s.DB.ExecContext(ctx, `
WITH candidates AS (
    SELECT episode_id FROM acr.agent_episodes
    WHERE expires_at <= $1 AND redaction_state <> 'purged_tombstone'
	  AND org_id = $3::uuid
	  AND EXISTS (
	          SELECT 1 FROM jsonb_array_elements_text($4::jsonb) AS allowed(scope)
	          WHERE allowed.scope = '*' OR repo_slug = allowed.scope OR repo_slug LIKE replace(allowed.scope, '/*', '/%')
	      )
    ORDER BY expires_at, episode_id LIMIT $2 FOR UPDATE SKIP LOCKED
)
UPDATE acr.agent_episodes AS episode
SET payload = jsonb_build_object('idempotency_digest', episode.payload->>'idempotency_digest', 'tombstone', true),
    redaction_state = 'purged_tombstone'
FROM candidates WHERE episode.episode_id = candidates.episode_id`, before, limit, orgID, string(scopesJSON))
	if err != nil {
		return 0, fmt.Errorf("purge expired episodes: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purged episode rows affected: %w", err)
	}
	return int(count), nil
}

func (s *EpisodeStore) existing(ctx context.Context, principal storage.Principal, repositoryID string, create contractsv1.AgentEpisodeCreate, expectedDigest string) (contractsv1.AgentEpisode, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT episode_id, repo_slug, payload, created_at, redaction_state FROM acr.agent_episodes
WHERE org_id = $1::uuid AND repo_id = $2::uuid AND (idempotency_key = $3 OR client_episode_id = $4)`, principal.OrgID, repositoryID, create.IdempotencyKey, create.ClientEpisodeID)
	if err != nil {
		return contractsv1.AgentEpisode{}, fmt.Errorf("load conflicting episode: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return contractsv1.AgentEpisode{}, storage.ErrConflict
	}
	var episodeID, repoSlug, state string
	var payload []byte
	var createdAt time.Time
	if err := rows.Scan(&episodeID, &repoSlug, &payload, &createdAt, &state); err != nil || !sameEpisodeRepository(repoSlug, create.Repository.Slug) || episodePayloadDigest(payload) != expectedDigest || rows.Next() {
		return contractsv1.AgentEpisode{}, storage.ErrConflict
	}
	if state == "purged_tombstone" {
		return contractsv1.AgentEpisode{}, rows.Err()
	}
	episode, err := decodeEpisode(episodeID, payload, createdAt, state)
	if err != nil {
		return contractsv1.AgentEpisode{}, fmt.Errorf("scan conflicting episode: %w", err)
	}
	return episode, rows.Err()
}

type episodeScanner interface{ Scan(...any) error }

func scanEpisode(row episodeScanner) (contractsv1.AgentEpisode, error) {
	var episodeID, state string
	var payload []byte
	var createdAt time.Time
	if err := row.Scan(&episodeID, &payload, &createdAt, &state); err != nil {
		return contractsv1.AgentEpisode{}, err
	}
	return decodeEpisode(episodeID, payload, createdAt, state)
}

func scanAuthorizedEpisode(row episodeScanner, principal storage.Principal) (contractsv1.AgentEpisode, error) {
	var episodeID, repoSlug, state string
	var payload []byte
	var createdAt time.Time
	if err := row.Scan(&episodeID, &repoSlug, &payload, &createdAt, &state); err != nil {
		return contractsv1.AgentEpisode{}, err
	}
	if !episodeRepositoryAllowed(principal.RepositoryScopes, repoSlug) {
		return contractsv1.AgentEpisode{}, storage.ErrNotFound
	}
	return decodeEpisode(episodeID, payload, createdAt, state)
}

func decodeEpisode(episodeID string, payload []byte, createdAt time.Time, state string) (contractsv1.AgentEpisode, error) {
	if state == "purged_tombstone" {
		return contractsv1.AgentEpisode{}, storage.ErrNotFound
	}
	episode := contractsv1.AgentEpisode{EpisodeID: episodeID, CreatedAt: createdAt, RedactionState: state}
	create, err := storedEpisodeCreate(payload)
	if err != nil {
		return contractsv1.AgentEpisode{}, fmt.Errorf("decode episode payload: %w", err)
	}
	episode.AgentEpisodeCreate = create
	episode.SchemaVersion = contractsv1.AgentEpisodeSchema
	return episode, nil
}
