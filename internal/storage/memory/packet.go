package memory

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type PacketStore struct {
	mu    sync.RWMutex
	now   func() time.Time
	audit storage.AuditStore
	data  map[string]packetSnapshot
}

type packetSnapshot struct {
	orgID     string
	payload   []byte
	repoSlug  string
	expiresAt time.Time
}

var repositoryPartPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,98}[a-z0-9])?$`)

func NewPacketStore(now func() time.Time) *PacketStore {
	return newPacketStore(now, nil)
}

func NewPacketStoreWithAudit(now func() time.Time, audit storage.AuditStore) *PacketStore {
	return newPacketStore(now, audit)
}

func newPacketStore(now func() time.Time, audit storage.AuditStore) *PacketStore {
	if now == nil {
		now = time.Now
	}
	return &PacketStore{now: now, audit: audit, data: make(map[string]packetSnapshot)}
}

func (s *PacketStore) SaveSnapshot(ctx context.Context, principal storage.Principal, packet contractsv1.ContextPacket, expiresAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validatePacket(principal, packet, expiresAt); err != nil {
		return err
	}
	if !expiresAt.After(s.now().UTC()) {
		return errors.New("packet snapshot is expired")
	}
	payload, err := json.Marshal(packet)
	if err != nil {
		return errors.New("encode packet snapshot")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.data[packet.ContextPacketID]; ok {
		if existing.orgID == principal.OrgID && sameJSON(existing.payload, payload) {
			return nil
		}
		return storage.ErrConflict
	}
	s.data[packet.ContextPacketID] = packetSnapshot{orgID: principal.OrgID, payload: append([]byte(nil), payload...), repoSlug: packet.Repository.Slug, expiresAt: expiresAt.UTC()}
	return nil
}

func (s *PacketStore) GetSnapshot(ctx context.Context, principal storage.Principal, contextPacketID string) (contractsv1.ContextPacket, error) {
	if err := ctx.Err(); err != nil {
		return contractsv1.ContextPacket{}, err
	}
	s.mu.RLock()
	snapshot, ok := s.data[contextPacketID]
	s.mu.RUnlock()
	if !ok || snapshot.orgID != principal.OrgID || !snapshot.expiresAt.After(s.now().UTC()) || !repositoryAllowed(principal.RepositoryScopes, snapshot.repoSlug) {
		return contractsv1.ContextPacket{}, storage.ErrNotFound
	}
	var packet contractsv1.ContextPacket
	if err := json.Unmarshal(snapshot.payload, &packet); err != nil {
		return contractsv1.ContextPacket{}, errors.New("decode packet snapshot")
	}
	return packet, nil
}

func (s *PacketStore) PurgeExpired(ctx context.Context, before time.Time, limit int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if limit <= 0 {
		return 0, nil
	}
	audit, ok := s.audit.(*AuditStore)
	if !ok || audit == nil {
		return 0, errors.New("packet snapshot purge requires memory audit store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := expiredSnapshotIDs(s.data, before, limit)
	events := make([]storage.AuditEvent, 0, len(ids))
	for _, id := range ids {
		event, err := snapshotPurgeEvent(id, s.data[id], before, s.now())
		if err != nil {
			return 0, err
		}
		events = append(events, event)
	}
	audit.mu.Lock()
	for _, event := range events {
		event.Metadata = cloneMetadata(event.Metadata)
		audit.events = append(audit.events, event)
	}
	audit.mu.Unlock()
	for _, id := range ids {
		delete(s.data, id)
	}
	return len(ids), nil
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

func sameJSON(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, leftErr := json.Marshal(leftValue)
	rightCanonical, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && string(leftCanonical) == string(rightCanonical)
}

func expiredSnapshotIDs(values map[string]packetSnapshot, before time.Time, limit int) []string {
	ids := make([]string, 0, limit)
	for id, snapshot := range values {
		if !snapshot.expiresAt.After(before.UTC()) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := values[ids[i]].expiresAt, values[ids[j]].expiresAt
		return left.Before(right) || left.Equal(right) && ids[i] < ids[j]
	})
	if len(ids) > limit {
		return ids[:limit]
	}
	return ids
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
