package episode

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var ErrEntitlementRequired = errors.New("agent context runtime entitlement is required")

type ServiceOptions struct {
	Now func() time.Time
}

type Service struct {
	store storage.EpisodeStore
	audit storage.AuditStore
	now   func() time.Time
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
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Service{store: store, audit: audit, now: options.Now}, nil
}

func (s *Service) Create(ctx context.Context, principal storage.Principal, create contractsv1.AgentEpisodeCreate) (contractsv1.AgentEpisode, bool, error) {
	if err := authorizeWrite(principal); err != nil {
		return contractsv1.AgentEpisode{}, false, err
	}
	if err := validateCreate(principal, create); err != nil {
		return contractsv1.AgentEpisode{}, false, err
	}
	if err := auth.AuthorizeRepository(principal, create.Repository.Slug); err != nil {
		return contractsv1.AgentEpisode{}, false, err
	}
	metadata := map[string]any{"outcome": create.Outcome, "retention_class": create.RetentionClass, "packet_linked": create.ContextPacketID != ""}
	if err := s.recordAudit(ctx, principal, "agent_episode_create_requested", create.ClientEpisodeID, metadata); err != nil {
		return contractsv1.AgentEpisode{}, false, fmt.Errorf("audit episode creation request: %w", err)
	}
	expiresAt := expiryFor(create.RetentionClass, s.now().UTC())
	stored, duplicate, err := s.store.CreateIdempotent(ctx, principal, create, expiresAt)
	if err != nil {
		return contractsv1.AgentEpisode{}, false, fmt.Errorf("persist episode: %w", err)
	}
	if create.RetentionClass == "no_persist" {
		action := "agent_episode_tombstoned"
		if duplicate {
			action = "agent_episode_tombstone_replayed"
		}
		s.recordCompletion(ctx, principal, action, create.ClientEpisodeID, metadata)
		return contractsv1.AgentEpisode{}, duplicate, storage.ErrNotFound
	}
	action := "agent_episode_created"
	if duplicate {
		action = "agent_episode_replayed"
	}
	s.recordCompletion(ctx, principal, action, stored.EpisodeID, metadata)
	return stored, duplicate, nil
}

func (s *Service) Get(ctx context.Context, principal storage.Principal, clientEpisodeID string) (contractsv1.AgentEpisode, error) {
	if principal.OrgID == "" || clientEpisodeID == "" {
		return contractsv1.AgentEpisode{}, storage.ErrNotFound
	}
	episode, err := s.store.GetByClientEpisodeID(ctx, principal, clientEpisodeID)
	if err != nil {
		return contractsv1.AgentEpisode{}, fmt.Errorf("get episode: %w", err)
	}
	return episode, nil
}

func (s *Service) Redact(ctx context.Context, principal storage.Principal, episodeID, reason string) (contractsv1.AgentEpisode, error) {
	if err := authorizeWrite(principal); err != nil {
		return contractsv1.AgentEpisode{}, err
	}
	if principal.OrgID == "" || episodeID == "" || reason == "" {
		return contractsv1.AgentEpisode{}, storage.ErrNotFound
	}
	if err := s.recordAudit(ctx, principal, "agent_episode_redact_requested", episodeID, nil); err != nil {
		return contractsv1.AgentEpisode{}, fmt.Errorf("audit episode redaction request: %w", err)
	}
	episode, err := s.store.Redact(ctx, principal, episodeID, reason)
	if err != nil {
		return contractsv1.AgentEpisode{}, fmt.Errorf("redact episode: %w", err)
	}
	s.recordCompletion(ctx, principal, "agent_episode_redacted", episodeID, map[string]any{"reason": reason})
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
	purged, err := purger.PurgeExpiredForPrincipal(ctx, principal, before, limit)
	if err != nil {
		return 0, fmt.Errorf("purge expired episodes: %w", err)
	}
	if purged > 0 {
		s.recordCompletion(ctx, principal, "agent_episode_purged", "retention", map[string]any{"purged_count": purged})
	}
	return purged, nil
}

func (s *Service) recordAudit(ctx context.Context, principal storage.Principal, action, resourceID string, metadata map[string]any) error {
	return s.audit.Record(context.WithoutCancel(ctx), storage.AuditEvent{
		OrgID: principal.OrgID, ActorType: "acr_credential", ActorID: principal.CredentialID, Action: action,
		ResourceType: "agent_episode", ResourceID: resourceID, Status: "success", Metadata: metadata, CreatedAt: s.now().UTC(),
	})
}

func (s *Service) recordCompletion(ctx context.Context, principal storage.Principal, action, resourceID string, metadata map[string]any) {
	_ = s.recordAudit(ctx, principal, action, resourceID, metadata)
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
