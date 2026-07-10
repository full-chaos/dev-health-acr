package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type PacketStore struct {
	DB  *sql.DB
	now func() time.Time
}

var repositoryPartPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,98}[a-z0-9])?$`)

func NewPacketStore(db *sql.DB, now func() time.Time) (*PacketStore, error) {
	if db == nil {
		return nil, errors.New("PostgreSQL database is required")
	}
	if now == nil {
		now = time.Now
	}
	return &PacketStore{DB: db, now: now}, nil
}

func (s *PacketStore) SaveSnapshot(ctx context.Context, principal storage.Principal, packet contractsv1.ContextPacket, expiresAt time.Time) error {
	if err := validatePacket(principal, packet, expiresAt); err != nil {
		return err
	}
	if !expiresAt.After(s.now().UTC()) {
		return errors.New("packet snapshot is expired")
	}
	payload, err := json.Marshal(packet)
	if err != nil {
		return fmt.Errorf("encode packet snapshot: %w", err)
	}
	result, err := s.DB.ExecContext(ctx, `
INSERT INTO acr.context_packet_snapshots (
    context_packet_id, org_id, repo_id, repo_slug, request_id, schema_version,
    query_version, ranking_version, scope_resolution, status, payload,
    generated_at, expires_at, created_at
) VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12, $13, $14)
ON CONFLICT (context_packet_id) DO NOTHING`,
		packet.ContextPacketID, principal.OrgID, packet.Repository.RepoID, packet.Repository.Slug, packet.RequestID, packet.SchemaVersion,
		packet.QueryVersion, packet.RankingVersion, packet.ResolvedScope.Resolution, packet.Status, string(payload), packet.GeneratedAt, expiresAt.UTC(), s.now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("insert packet snapshot: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("packet snapshot rows affected: %w", err)
	}
	if rows == 1 {
		return nil
	}
	var existing []byte
	err = s.DB.QueryRowContext(ctx, `SELECT payload FROM acr.context_packet_snapshots WHERE context_packet_id = $1 AND org_id = $2::uuid`, packet.ContextPacketID, principal.OrgID).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("load existing packet snapshot: %w", err)
	}
	if !sameJSON(existing, payload) {
		return storage.ErrConflict
	}
	return nil
}

func (s *PacketStore) GetSnapshot(ctx context.Context, principal storage.Principal, contextPacketID string) (contractsv1.ContextPacket, error) {
	var payload []byte
	var repoSlug string
	err := s.DB.QueryRowContext(ctx, `
SELECT payload, repo_slug
FROM acr.context_packet_snapshots
WHERE context_packet_id = $1 AND org_id = $2::uuid AND expires_at > $3`, contextPacketID, principal.OrgID, s.now().UTC()).Scan(&payload, &repoSlug)
	if errors.Is(err, sql.ErrNoRows) {
		return contractsv1.ContextPacket{}, storage.ErrNotFound
	}
	if err != nil {
		return contractsv1.ContextPacket{}, fmt.Errorf("get packet snapshot: %w", err)
	}
	if !repositoryAllowed(principal.RepositoryScopes, repoSlug) {
		return contractsv1.ContextPacket{}, storage.ErrNotFound
	}
	var packet contractsv1.ContextPacket
	if err := json.Unmarshal(payload, &packet); err != nil {
		return contractsv1.ContextPacket{}, fmt.Errorf("decode packet snapshot: %w", err)
	}
	return packet, nil
}

func (s *PacketStore) PurgeExpired(ctx context.Context, before time.Time, limit int) (int, error) {
	return s.PurgeExpiredWithAudit(ctx, before, limit)
}

func validatePacket(principal storage.Principal, packet contractsv1.ContextPacket, expiresAt time.Time) error {
	if principal.OrgID == "" || packet.SchemaVersion != contractsv1.ContextPacketSchema || packet.ContextPacketID == "" || packet.RequestID == "" || packet.GeneratedAt.IsZero() || packet.Goal == "" || packet.QueryVersion == "" || packet.RankingVersion == "" || packet.Summary == "" || packet.Repository.RepoID == "" || packet.Repository.Slug == "" || packet.ResolvedScope.RepoID != packet.Repository.RepoID || packet.ResolvedScope.RepoSlug != packet.Repository.Slug || packet.ResolvedScope.FallbackReasons == nil || packet.Items == nil || packet.RequiredChecks == nil || packet.RecommendedNextSteps == nil || packet.Freshness.AsOf.IsZero() || packet.Freshness.Watermarks == nil || packet.Coverage.SourcesConsidered == nil || packet.Coverage.SourcesAvailable == nil || packet.Coverage.SourcesUnavailable == nil || packet.Coverage.DegradedReasons == nil || packet.Warnings == nil || packet.Compatibility.ServiceVersion == "" || packet.Compatibility.MinimumSidecarVersion == "" || packet.Compatibility.SupportedSchemaVersions == nil || packet.Budget.MaxItems < 1 || packet.Budget.MaxOutputTokens < 1 || packet.Budget.MaxSerializedBytes < 1 || expiresAt.IsZero() {
		return errors.New("invalid packet snapshot")
	}
	if !validStatus(packet.Status) || !validResolution(packet.ResolvedScope.Resolution) {
		return errors.New("invalid packet snapshot")
	}
	for _, item := range packet.Items {
		if err := item.Validate(); err != nil {
			return errors.New("invalid packet snapshot")
		}
	}
	if !repositoryAllowed(principal.RepositoryScopes, packet.Repository.Slug) {
		return storage.ErrNotFound
	}
	return nil
}

func validStatus(value contractsv1.PacketStatus) bool {
	return value == contractsv1.PacketComplete || value == contractsv1.PacketPartial || value == contractsv1.PacketDegraded || value == contractsv1.PacketEmpty
}

func validResolution(value contractsv1.ScopeResolution) bool {
	return value == contractsv1.ScopeExactCommit || value == contractsv1.ScopeBranchFiltered || value == contractsv1.ScopeRepoFallback || value == contractsv1.ScopeUnresolved
}

func repositoryAllowed(scopes []string, slug string) bool {
	normalized := strings.ToLower(strings.TrimSpace(slug))
	parts := strings.Split(normalized, "/")
	if len(parts) != 2 || !repositoryPartPattern.MatchString(parts[0]) || !repositoryPartPattern.MatchString(parts[1]) {
		return false
	}
	owner := parts[0]
	for _, raw := range scopes {
		scope := strings.ToLower(strings.TrimSpace(raw))
		if scope == "*" || scope == normalized || scope == owner+"/*" {
			return true
		}
	}
	return false
}

func sameJSON(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, leftErr := json.Marshal(leftValue)
	rightCanonical, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && string(leftCanonical) == string(rightCanonical)
}
