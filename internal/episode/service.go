package episode

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const auditTimeout = 100 * time.Millisecond

var (
	ErrEntitlementRequired = errors.New("agent context runtime entitlement is required")
	ErrNoPersistAccepted   = errors.New("episode tombstone accepted")
)

type ServiceOptions struct {
	Now              func() time.Time
	TerminalObserver TerminalObserver
	StoreObserver    StoreObserver
	StoreBackend     StoreBackend
	PacketStore      storage.PacketStore
}

type Service struct {
	store         storage.EpisodeStore
	audit         storage.AuditStore
	now           func() time.Time
	observer      TerminalObserver
	storeObserver StoreObserver
	storeBackend  StoreBackend
	packetStore   storage.PacketStore
}

type scopedEpisodePurger interface {
	PurgeExpiredForPrincipal(context.Context, storage.Principal, time.Time, int) (int, error)
}

func NewService(store storage.EpisodeStore, audit storage.AuditStore, options ServiceOptions) (*Service, error) {
	if store == nil {
		return nil, errors.New("episode store is required")
	}
	if audit == nil {
		return nil, errors.New("episode audit store is required")
	}
	if options.PacketStore == nil {
		return nil, errors.New("episode packet store is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Service{
		store: store, audit: audit, now: options.Now, observer: options.TerminalObserver,
		storeObserver: options.StoreObserver, storeBackend: options.StoreBackend, packetStore: options.PacketStore,
	}, nil
}

func (s *Service) Create(ctx context.Context, principal storage.Principal, create contractsv1.AgentEpisodeCreate) (contractsv1.AgentEpisode, bool, error) {
	started := s.now()
	observation := TerminalObservation{Outcome: TerminalOutcomeFailure, AuditDelivery: AuditDeliverySkipped}
	defer func() { observation.Duration = max(s.now().Sub(started), 0); s.observeTerminal(ctx, observation) }()

	if err := authorizeWrite(principal); err != nil {
		return contractsv1.AgentEpisode{}, false, err
	}
	if err := validateCreate(principal, create); err != nil {
		return contractsv1.AgentEpisode{}, false, err
	}
	if err := auth.AuthorizeRepository(principal, create.Repository.Slug); err != nil {
		return contractsv1.AgentEpisode{}, false, err
	}
	storeStarted := s.now()
	preflight, err := s.store.PreflightIdempotency(ctx, principal, create)
	s.observeStoreCall(ctx, storeStarted, err)
	if err != nil {
		return contractsv1.AgentEpisode{}, false, fmt.Errorf("preflight episode idempotency: %w", err)
	}
	if preflight == storage.EpisodePreflightMiss {
		if err := s.verifyPacketLink(ctx, principal, create); err != nil {
			return contractsv1.AgentEpisode{}, false, err
		}
	}
	metadata := map[string]any{"outcome": create.Outcome, "retention_class": create.RetentionClass, "packet_linked": create.ContextPacketID != ""}
	if err := s.recordAudit(ctx, principal, "agent_episode_create_requested", create.ClientEpisodeID, metadata); err != nil {
		observation.AuditDelivery = AuditDeliveryFailed
		return contractsv1.AgentEpisode{}, false, fmt.Errorf("audit episode creation request: %w", err)
	}
	observation.AuditDelivery = AuditDeliveryDelivered
	expiresAt := expiryFor(create.RetentionClass, s.now().UTC())
	storeStarted = s.now()
	stored, duplicate, err := s.store.CreateIdempotent(ctx, principal, create, expiresAt)
	s.observeStoreCall(ctx, storeStarted, err)
	if err != nil {
		return contractsv1.AgentEpisode{}, false, fmt.Errorf("persist episode: %w", err)
	}
	if create.RetentionClass == "no_persist" {
		action := "agent_episode_tombstoned"
		if duplicate {
			action = "agent_episode_tombstone_replayed"
			observation.Outcome = TerminalOutcomeDuplicate
		} else {
			observation.Outcome = TerminalOutcomeSuccess
		}
		observation.AuditDelivery = s.recordCompletion(ctx, principal, action, create.ClientEpisodeID, metadata)
		return contractsv1.AgentEpisode{}, duplicate, ErrNoPersistAccepted
	}
	action := "agent_episode_created"
	if duplicate {
		action = "agent_episode_replayed"
		observation.Outcome = TerminalOutcomeDuplicate
	} else {
		observation.Outcome = TerminalOutcomeSuccess
	}
	observation.AuditDelivery = s.recordCompletion(ctx, principal, action, stored.EpisodeID, metadata)
	return stored, duplicate, nil
}

func (s *Service) verifyPacketLink(ctx context.Context, principal storage.Principal, create contractsv1.AgentEpisodeCreate) error {
	if create.ContextPacketID == "" {
		return nil
	}
	packet, err := s.packetStore.GetSnapshot(ctx, principal, create.ContextPacketID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return storage.ErrNotFound
		}
		return fmt.Errorf("get context packet snapshot: %w", err)
	}
	if normalizeRepository(create.Repository.Slug) != normalizeRepository(packet.Repository.Slug) {
		return storage.ErrNotFound
	}
	return nil
}

func normalizeRepository(slug string) string {
	return strings.ToLower(strings.TrimSpace(slug))
}

func (s *Service) Get(ctx context.Context, principal storage.Principal, clientEpisodeID string) (contractsv1.AgentEpisode, error) {
	if principal.OrgID == "" || clientEpisodeID == "" {
		return contractsv1.AgentEpisode{}, storage.ErrNotFound
	}
	storeStarted := s.now()
	episode, err := s.store.GetByClientEpisodeID(ctx, principal, clientEpisodeID)
	s.observeStoreCall(ctx, storeStarted, err)
	if err != nil {
		return contractsv1.AgentEpisode{}, fmt.Errorf("get episode: %w", err)
	}
	return episode, nil
}

// GetByID reads a single episode by its server-assigned episode_id.
// authorizeRead is independent of authorizeWrite: a credential scoped only
// to episode:write (a recorder-only agent) must not be able to read
// episodes back, and vice versa.
func (s *Service) GetByID(ctx context.Context, principal storage.Principal, episodeID string) (contractsv1.AgentEpisode, error) {
	if err := authorizeRead(principal); err != nil {
		return contractsv1.AgentEpisode{}, err
	}
	if strings.TrimSpace(episodeID) == "" {
		return contractsv1.AgentEpisode{}, storage.ErrNotFound
	}
	storeStarted := s.now()
	episode, err := s.store.GetByEpisodeID(ctx, principal, episodeID)
	s.observeStoreCall(ctx, storeStarted, err)
	if err != nil {
		return contractsv1.AgentEpisode{}, fmt.Errorf("get episode: %w", err)
	}
	return episode, nil
}

// List returns the caller's episodes, newest first, optionally filtered to
// one repository. The storage adapter applies retention/redaction/scope to
// every row (see storage.EpisodeStore.List), not just to a single lookup.
func (s *Service) List(ctx context.Context, principal storage.Principal, repositorySlug string, limit int) ([]contractsv1.AgentEpisode, error) {
	if err := authorizeRead(principal); err != nil {
		return nil, err
	}
	storeStarted := s.now()
	episodes, err := s.store.List(ctx, principal, repositorySlug, limit)
	s.observeStoreCall(ctx, storeStarted, err)
	if err != nil {
		return nil, fmt.Errorf("list episodes: %w", err)
	}
	return episodes, nil
}

func (s *Service) Redact(ctx context.Context, principal storage.Principal, episodeID, reason string) (contractsv1.AgentEpisode, error) {
	started := s.now()
	observation := TerminalObservation{Outcome: TerminalOutcomeFailure, AuditDelivery: AuditDeliverySkipped}
	defer func() { observation.Duration = max(s.now().Sub(started), 0); s.observeTerminal(ctx, observation) }()

	if err := authorizeWrite(principal); err != nil {
		return contractsv1.AgentEpisode{}, err
	}
	if principal.OrgID == "" || episodeID == "" || reason == "" {
		return contractsv1.AgentEpisode{}, storage.ErrNotFound
	}
	if err := s.recordAudit(ctx, principal, "agent_episode_redact_requested", episodeID, nil); err != nil {
		observation.AuditDelivery = AuditDeliveryFailed
		return contractsv1.AgentEpisode{}, fmt.Errorf("audit episode redaction request: %w", err)
	}
	observation.AuditDelivery = AuditDeliveryDelivered
	storeStarted := s.now()
	episode, err := s.store.Redact(ctx, principal, episodeID, reason)
	s.observeStoreCall(ctx, storeStarted, err)
	if err != nil {
		return contractsv1.AgentEpisode{}, fmt.Errorf("redact episode: %w", err)
	}
	observation.Outcome = TerminalOutcomeRedacted
	observation.AuditDelivery = s.recordCompletion(ctx, principal, "agent_episode_redacted", episodeID, map[string]any{"reason": reason})
	return episode, nil
}

func (s *Service) PurgeExpired(ctx context.Context, principal storage.Principal, before time.Time, limit int) (int, error) {
	if err := authorizeWrite(principal); err != nil {
		return 0, err
	}
	purger, ok := s.store.(scopedEpisodePurger)
	if !ok || principal.OrgID == "" {
		return 0, errors.New("scoped episode purge is required")
	}
	if err := s.recordAudit(ctx, principal, "agent_episode_purge_requested", "retention", nil); err != nil {
		return 0, fmt.Errorf("audit episode purge request: %w", err)
	}
	storeStarted := s.now()
	purged, err := purger.PurgeExpiredForPrincipal(ctx, principal, before, limit)
	s.observeStoreCall(ctx, storeStarted, err)
	if err != nil {
		return 0, fmt.Errorf("purge expired episodes: %w", err)
	}
	if purged > 0 {
		s.recordCompletion(ctx, principal, "agent_episode_purged", "retention", map[string]any{"purged_count": purged})
	}
	return purged, nil
}

func (s *Service) recordAudit(ctx context.Context, principal storage.Principal, action, resourceID string, metadata map[string]any) error {
	auditContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditTimeout)
	defer cancel()
	return s.audit.Record(auditContext, storage.AuditEvent{
		OrgID: principal.OrgID, ActorType: "acr_credential", ActorID: principal.CredentialID, Action: action,
		ResourceType: "agent_episode", ResourceID: resourceID, Status: "success", Metadata: metadata, CreatedAt: s.now().UTC(),
	})
}

func (s *Service) recordCompletion(ctx context.Context, principal storage.Principal, action, resourceID string, metadata map[string]any) AuditDelivery {
	if err := s.recordAudit(ctx, principal, action, resourceID, metadata); err != nil {
		return AuditDeliveryFailed
	}
	return AuditDeliveryDelivered
}

func authorizeWrite(principal storage.Principal) error {
	if len(principal.RepositoryScopes) == 0 {
		return auth.ErrRepositoryForbidden
	}
	if !auth.HasScope(principal.Permissions, auth.ScopeEpisodeWrite) {
		return auth.ErrInsufficientScope
	}
	if slices.Contains(principal.ProductEntitlements, "agent_context_runtime") {
		return nil
	}
	return ErrEntitlementRequired
}

// authorizeRead is deliberately separate from authorizeWrite: episode:write
// and episode:read are independent grants, so a recorder-only credential
// must not be able to read episodes back, and a read-only credential must
// not be able to record them.
func authorizeRead(principal storage.Principal) error {
	if len(principal.RepositoryScopes) == 0 {
		return auth.ErrRepositoryForbidden
	}
	if !auth.HasScope(principal.Permissions, auth.ScopeEpisodeRead) {
		return auth.ErrInsufficientScope
	}
	if slices.Contains(principal.ProductEntitlements, "agent_context_runtime") {
		return nil
	}
	return ErrEntitlementRequired
}

func expiryFor(class string, now time.Time) *time.Time {
	var duration time.Duration
	switch class {
	case "default_90d":
		duration = 90 * 24 * time.Hour
	case "short_30d":
		duration = 30 * 24 * time.Hour
	default:
		return nil
	}
	expiresAt := now.Add(duration)
	return &expiresAt
}
