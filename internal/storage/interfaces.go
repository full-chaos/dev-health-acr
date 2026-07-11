package storage

import (
	"context"
	"errors"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var (
	ErrNotFound = errors.New("storage record not found")
	ErrConflict = errors.New("storage record conflicts with an existing record")
)

// Principal is derived from validated authentication. Callers must never build
// it from organization identifiers supplied in request bodies.
type Principal struct {
	OrgID               string
	CredentialID        string
	RepositoryScopes    []string
	Permissions         []string
	ProductEntitlements []string
}

type EvidenceBundle struct {
	ResolvedScope contractsv1.ResolvedScope
	Evidence      []contractsv1.EvidenceRef
	Watermarks    []contractsv1.SourceWatermark
	Unavailable   []contractsv1.UnavailableSource
	QueryVersion  string
}

// EvidenceStore is read-only. Implementations may use ClickHouse now and a
// temporal graph later without changing the public v1 contracts.
type EvidenceStore interface {
	ResolveScope(ctx context.Context, principal Principal, request contractsv1.ContextPacketRequest) (contractsv1.ResolvedScope, error)
	ContextForTask(ctx context.Context, principal Principal, request contractsv1.ContextPacketRequest) (EvidenceBundle, error)
	ResolveEvidence(ctx context.Context, principal Principal, evidenceRefID string) (contractsv1.ExpandedEvidence, error)
}

type PacketStore interface {
	SaveSnapshot(ctx context.Context, principal Principal, packet contractsv1.ContextPacket, expiresAt time.Time) error
	GetSnapshot(ctx context.Context, principal Principal, contextPacketID string) (contractsv1.ContextPacket, error)
	PurgeExpired(ctx context.Context, before time.Time, limit int) (int, error)
}

type EpisodeStore interface {
	CreateIdempotent(ctx context.Context, principal Principal, episode contractsv1.AgentEpisodeCreate, expiresAt *time.Time) (contractsv1.AgentEpisode, bool, error)
	GetByClientEpisodeID(ctx context.Context, principal Principal, clientEpisodeID string) (contractsv1.AgentEpisode, error)
	Redact(ctx context.Context, principal Principal, episodeID, reason string) (contractsv1.AgentEpisode, error)
	PurgeExpired(ctx context.Context, before time.Time, limit int) (int, error)
}

// CredentialRecord is the server-side credential representation. TokenHash is
// never included in public DTOs, logs, audit metadata, or API responses.
type CredentialRecord struct {
	Metadata          contractsv1.ClientCredential
	TokenHash         string
	CreatedBy         string
	LastUsedIP        string
	LastUsedUserAgent string
}

// CredentialStore owns both data-plane lookup and the narrow administrative
// lifecycle needed by ACR. Implementations must make Create/Rotate atomic.
type CredentialStore interface {
	Create(ctx context.Context, record CredentialRecord) error
	List(ctx context.Context, orgID string) ([]contractsv1.ClientCredential, error)
	GetByID(ctx context.Context, orgID, credentialID string) (contractsv1.ClientCredential, error)
	FindByTokenHash(ctx context.Context, tokenHash string) (contractsv1.ClientCredential, error)
	Rotate(ctx context.Context, orgID, credentialID string, replacement CredentialRecord, previousValidUntil *time.Time) error
	Revoke(ctx context.Context, orgID, credentialID string, revokedAt time.Time) (contractsv1.ClientCredential, error)
	TouchLastUsed(ctx context.Context, credentialID, ip, userAgent string, usedAt time.Time) error
}

type AuditEvent struct {
	OrgID        string
	RepoID       string
	ActorType    string
	ActorID      string
	Action       string
	ResourceType string
	ResourceID   string
	Status       string
	RequestID    string
	Metadata     map[string]any
	CreatedAt    time.Time
}

type AuditStore interface {
	Record(ctx context.Context, event AuditEvent) error
}
