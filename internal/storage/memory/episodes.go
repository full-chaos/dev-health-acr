package memory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const redactedEpisodeText = "[redacted]"

type EpisodeStore struct {
	mu       sync.RWMutex
	now      func() time.Time
	byID     map[string]episodeRecord
	byClient map[string]string
	byKey    map[string]string
}

type episodeRecord struct {
	episode  contractsv1.AgentEpisode
	digest   [sha256.Size]byte
	orgID    string
	repoSlug string
	clientID string
}

// NewEpisodeStore builds an in-memory EpisodeStore. now supplies the clock
// used for every expiry decision (CreatedAt fallback, Get, Redact); pass nil
// to default to time.Now. Callers that inject a fixed clock into the
// episode.Service (ServiceOptions.Now) must inject that same clock here --
// otherwise expiry checks race the real wall clock against a fictional
// service-layer "now" and silently start failing whenever real time crosses
// the fixture's expiry threshold.
func NewEpisodeStore(now func() time.Time) *EpisodeStore {
	if now == nil {
		now = time.Now
	}
	return &EpisodeStore{now: now, byID: map[string]episodeRecord{}, byClient: map[string]string{}, byKey: map[string]string{}}
}

func (s *EpisodeStore) PreflightIdempotency(ctx context.Context, principal storage.Principal, create contractsv1.AgentEpisodeCreate) (storage.EpisodePreflight, error) {
	if err := ctx.Err(); err != nil {
		return storage.EpisodePreflightMiss, err
	}
	if !episodeRepositoryAllowed(principal.RepositoryScopes, create.Repository.Slug) {
		return storage.EpisodePreflightMiss, storage.ErrNotFound
	}
	digest, err := episodeDigest(create)
	if err != nil {
		return storage.EpisodePreflightMiss, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	clientKey, idempotencyKey := episodeKeys(principal.OrgID, create)
	if id, exists := s.byKey[idempotencyKey]; exists {
		return preflightRecord(s.byID[id], digest, create.Repository.Slug), nil
	}
	if id, exists := s.byClient[clientKey]; exists {
		return preflightRecord(s.byID[id], digest, create.Repository.Slug), nil
	}
	return storage.EpisodePreflightMiss, nil
}

func (s *EpisodeStore) CreateIdempotent(ctx context.Context, principal storage.Principal, create contractsv1.AgentEpisodeCreate, expiresAt *time.Time) (contractsv1.AgentEpisode, bool, error) {
	if err := ctx.Err(); err != nil {
		return contractsv1.AgentEpisode{}, false, err
	}
	if !episodeRepositoryAllowed(principal.RepositoryScopes, create.Repository.Slug) {
		return contractsv1.AgentEpisode{}, false, storage.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return contractsv1.AgentEpisode{}, false, err
	}
	digest, err := episodeDigest(create)
	if err != nil {
		return contractsv1.AgentEpisode{}, false, err
	}
	clientKey, idempotencyKey := episodeKeys(principal.OrgID, create)
	if id, exists := s.byKey[idempotencyKey]; exists {
		return s.duplicate(id, digest, create.Repository.Slug)
	}
	if id, exists := s.byClient[clientKey]; exists {
		return s.duplicate(id, digest, create.Repository.Slug)
	}
	id, err := newEpisodeID()
	if err != nil {
		return contractsv1.AgentEpisode{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return contractsv1.AgentEpisode{}, false, err
	}
	stored := contractsv1.AgentEpisode{AgentEpisodeCreate: cloneEpisodeCreate(create), EpisodeID: id, CreatedAt: s.now().UTC(), RedactionState: "active"}
	if expiresAt != nil {
		stored.CreatedAt = expiresAt.Add(-defaultRetention(create.RetentionClass))
	}
	if create.RetentionClass == "no_persist" {
		stored = contractsv1.AgentEpisode{EpisodeID: id, RedactionState: "purged_tombstone"}
	}
	s.byID[id] = episodeRecord{episode: stored, digest: digest, orgID: principal.OrgID, repoSlug: create.Repository.Slug, clientID: create.ClientEpisodeID}
	s.byClient[clientKey], s.byKey[idempotencyKey] = id, id
	return presentation(stored), false, nil
}

func (s *EpisodeStore) Redact(_ context.Context, principal storage.Principal, episodeID, _ string) (contractsv1.AgentEpisode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.byID[episodeID]
	if !exists || record.orgID != principal.OrgID || record.episode.RedactionState == "purged_tombstone" || episodeExpired(record.episode, s.now().UTC()) || record.episode.AgentEpisodeCreate.Repository.Slug == "" || !episodeRepositoryAllowed(principal.RepositoryScopes, record.episode.Repository.Slug) {
		return contractsv1.AgentEpisode{}, storage.ErrNotFound
	}
	if record.episode.RedactionState == "active" {
		record.episode.AgentEpisodeCreate = redactedCreate(record.episode.AgentEpisodeCreate)
		record.episode.RedactionState = "redacted"
		s.byID[episodeID] = record
	}
	return presentation(record.episode), nil
}

func (s *EpisodeStore) PurgeExpired(_ context.Context, before time.Time, limit int) (int, error) {
	return 0, errors.New("principal-scoped episode purge is required")
}

func (s *EpisodeStore) PurgeExpiredForOrg(_ context.Context, principal storage.Principal, before time.Time, limit int) (int, error) {
	return 0, errors.New("repository-scoped episode purge is required")
}

func (s *EpisodeStore) PurgeExpiredForPrincipal(_ context.Context, principal storage.Principal, before time.Time, limit int) (int, error) {
	if strings.TrimSpace(principal.OrgID) == "" || len(principal.RepositoryScopes) == 0 {
		return 0, storage.ErrNotFound
	}
	return s.purgeExpired(before, limit, principal.OrgID, principal.RepositoryScopes)
}

func (s *EpisodeStore) purgeExpired(before time.Time, limit int, orgID string, scopes []string) (int, error) {
	if strings.TrimSpace(orgID) == "" || len(scopes) == 0 {
		return 0, storage.ErrNotFound
	}
	if limit <= 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	purged := 0
	for id, record := range s.byID {
		if purged == limit || record.episode.RedactionState == "purged_tombstone" || record.orgID != orgID || !episodeRepositoryAllowed(scopes, record.episode.Repository.Slug) {
			continue
		}
		expiresAt := episodeExpiry(record.episode)
		if expiresAt != nil && !expiresAt.After(before) {
			record.episode.AgentEpisodeCreate = contractsv1.AgentEpisodeCreate{}
			record.episode.RedactionState = "purged_tombstone"
			s.byID[id] = record
			purged++
		}
	}
	return purged, nil
}

func (s *EpisodeStore) duplicate(id string, digest [sha256.Size]byte, repository string) (contractsv1.AgentEpisode, bool, error) {
	record := s.byID[id]
	if record.digest != digest || !sameEpisodeRepository(record.repoSlug, repository) {
		return contractsv1.AgentEpisode{}, false, storage.ErrConflict
	}
	return presentation(record.episode), true, nil
}

func preflightRecord(record episodeRecord, digest [sha256.Size]byte, repository string) storage.EpisodePreflight {
	if record.digest == digest && sameEpisodeRepository(record.repoSlug, repository) {
		return storage.EpisodePreflightIdentical
	}
	return storage.EpisodePreflightConflict
}

func sameEpisodeRepository(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

func episodeKeys(orgID string, create contractsv1.AgentEpisodeCreate) (string, string) {
	repository := normalizeRepository(create.Repository.Slug)
	return scopedKey(orgID, repository+"\x00"+create.ClientEpisodeID), scopedKey(orgID, repository+"\x00"+create.IdempotencyKey)
}

func normalizeRepository(slug string) string { return strings.ToLower(strings.TrimSpace(slug)) }

func scopedKey(orgID, value string) string { return orgID + "\x00" + value }

func episodeDigest(create contractsv1.AgentEpisodeCreate) ([sha256.Size]byte, error) {
	encoded, err := json.Marshal(create)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func newEpisodeID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "ep_" + hex.EncodeToString(value), nil
}

func presentation(value contractsv1.AgentEpisode) contractsv1.AgentEpisode {
	if value.RedactionState == "purged_tombstone" {
		return contractsv1.AgentEpisode{}
	}
	value.AgentEpisodeCreate = cloneEpisodeCreate(value.AgentEpisodeCreate)
	value.SchemaVersion = contractsv1.AgentEpisodeSchema
	return value
}

func cloneEpisodeCreate(value contractsv1.AgentEpisodeCreate) contractsv1.AgentEpisodeCreate {
	value.Artifacts.FilesTouched = append([]string{}, value.Artifacts.FilesTouched...)
	value.Artifacts.ArtifactURIs = append([]string{}, value.Artifacts.ArtifactURIs...)
	value.Artifacts.TestsRun = append([]string{}, value.Artifacts.TestsRun...)
	return value
}

func redactedCreate(value contractsv1.AgentEpisodeCreate) contractsv1.AgentEpisodeCreate {
	value.Goal, value.Summary, value.TaskRef = redactedEpisodeText, redactedEpisodeText, ""
	value.Artifacts = contractsv1.EpisodeArtifacts{FilesTouched: []string{}, ArtifactURIs: []string{}, TestsRun: []string{}}
	value.Transcript = contractsv1.TranscriptRef{Mode: "none"}
	return value
}

func episodeExpiry(value contractsv1.AgentEpisode) *time.Time {
	if value.RedactionState == "purged_tombstone" {
		return nil
	}
	retention := defaultRetention(value.RetentionClass)
	if retention == 0 {
		return nil
	}
	expiresAt := value.CreatedAt.Add(retention)
	return &expiresAt
}

func episodeExpired(value contractsv1.AgentEpisode, now time.Time) bool {
	expiresAt := episodeExpiry(value)
	return expiresAt != nil && !expiresAt.After(now)
}

func defaultRetention(class string) time.Duration {
	switch class {
	case "default_90d":
		return 90 * 24 * time.Hour
	case "short_30d":
		return 30 * 24 * time.Hour
	default:
		return 0
	}
}

func episodeRepositoryAllowed(scopes []string, slug string) bool {
	for _, scope := range scopes {
		scope = strings.ToLower(strings.TrimSpace(scope))
		if scope == "*" || scope == strings.ToLower(slug) || strings.HasSuffix(scope, "/*") && strings.TrimSuffix(scope, "/*") == strings.Split(strings.ToLower(slug), "/")[0] {
			return true
		}
	}
	return false
}
