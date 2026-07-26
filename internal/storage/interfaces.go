package storage

import (
	"context"
	"errors"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage/internal/credentiallifecycle"
)

var (
	ErrNotFound                   = errors.New("storage record not found")
	ErrConflict                   = errors.New("storage record conflicts with an existing record")
	ErrUnavailable                = errors.New("storage operation unavailable")
	ErrInvalidAuditMetadata       = errors.New("storage audit metadata is invalid")
	ErrInvalidCredentialLifecycle = credentiallifecycle.ErrInvalidLifecycle
	ErrInvalidCredentialInput     = credentiallifecycle.ErrInvalidInput
)

const MaximumCredentialOverlap = credentiallifecycle.MaximumOverlap

const (
	AuditActionCredentialCreated = "credential_created"
	AuditActionCredentialRotated = "credential_rotated"
	AuditActionCredentialRevoked = "credential_revoked"
)

// Principal is derived from validated authentication. Callers must never build
// it from organization identifiers supplied in request bodies.
type Principal struct {
	AuthenticationMethod AuthenticationMethod
	Subject              string
	OrgID                string
	CredentialID         string
	RepositoryScopes     []string
	Permissions          []string
	ProductEntitlements  []string
}

// AuthenticationMethod identifies the validated authentication boundary that
// derived a Principal. It is never supplied by a request payload.
type AuthenticationMethod string

const (
	AuthenticationMethodCredential   AuthenticationMethod = "credential"
	AuthenticationMethodWebAssertion AuthenticationMethod = "web_assertion"
)

// AuditActor returns the identity safe to use for audit and rate correlation.
// Web assertions deliberately leave CredentialID empty.
func (p Principal) AuditActor() (string, string) {
	if p.AuthenticationMethod == AuthenticationMethodWebAssertion {
		return string(AuthenticationMethodWebAssertion), p.Subject
	}
	return string(AuthenticationMethodCredential), p.Subject
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
	// ResolveEvidence owns independent organization and repository authorization
	// because the opaque public handle intentionally exposes no repository slug.
	// Unknown, malformed, foreign, deleted, and unauthorized handles must all
	// return ErrNotFound.
	ResolveEvidence(ctx context.Context, principal Principal, evidenceRefID string) (contractsv1.ExpandedEvidence, error)
}

type PacketStore interface {
	SaveSnapshot(ctx context.Context, principal Principal, packet contractsv1.ContextPacket, expiresAt time.Time) error
	GetSnapshot(ctx context.Context, principal Principal, contextPacketID string) (contractsv1.ContextPacket, error)
	PurgeExpired(ctx context.Context, before time.Time, limit int) (int, error)
}

type EpisodeStore interface {
	PreflightIdempotency(ctx context.Context, principal Principal, episode contractsv1.AgentEpisodeCreate) (EpisodePreflight, error)
	CreateIdempotent(ctx context.Context, principal Principal, episode contractsv1.AgentEpisodeCreate, expiresAt *time.Time) (contractsv1.AgentEpisode, bool, error)
	GetByClientEpisodeID(ctx context.Context, principal Principal, clientEpisodeID string) (contractsv1.AgentEpisode, error)
	Redact(ctx context.Context, principal Principal, episodeID, reason string) (contractsv1.AgentEpisode, error)
	PurgeExpired(ctx context.Context, before time.Time, limit int) (int, error)
}

// EpisodePreflight is the opaque idempotency state used before creating an
// episode. It deliberately carries no episode or tombstone data.
type EpisodePreflight uint8

const (
	EpisodePreflightMiss EpisodePreflight = iota
	EpisodePreflightIdentical
	EpisodePreflightConflict
)

// CredentialRecord is the server-side credential representation. TokenHash is
// never included in public DTOs, logs, audit metadata, or API responses.
type CredentialRecord struct {
	Metadata           contractsv1.ClientCredential
	TokenHash          string
	CreatedBy          string
	RotatedAt          *time.Time
	LastUsedIP         string
	LastUsedUserAgent  string
	IssuanceProvenance CredentialIssuanceProvenance
}

// CredentialStore is the read and authentication data plane. It deliberately
// exposes no credential lifecycle mutation.
type CredentialStore interface {
	List(ctx context.Context, orgID string) ([]contractsv1.ClientCredential, error)
	GetByID(ctx context.Context, orgID, credentialID string) (contractsv1.ClientCredential, error)
	FindByTokenHash(ctx context.Context, tokenHash string) (contractsv1.ClientCredential, error)
	TouchLastUsed(ctx context.Context, credentialID, ip, userAgent string, usedAt time.Time) error
}

type CredentialLifecycle = credentiallifecycle.Lifecycle
type CredentialCreateInput = credentiallifecycle.CreateInput
type CredentialRotationInput = credentiallifecycle.RotationInput
type CredentialRotationReplacement = credentiallifecycle.RotationReplacement
type CredentialRevocationInput = credentiallifecycle.RevocationInput

func ValidateCredentialCreateInput(input CredentialCreateInput) error {
	return credentiallifecycle.ValidateCreateInput(input)
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

func IsCredentialLifecycleAuditAction(action string) bool {
	switch action {
	case AuditActionCredentialCreated, AuditActionCredentialRotated, AuditActionCredentialRevoked:
		return true
	default:
		return false
	}
}
