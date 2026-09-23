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

// DependencyErrorClass is safe, schema-shaped classification an adapter may
// attach (via error wrapping, alongside ErrUnavailable or ErrConflict) when
// the underlying failure came from the database: the driver's own error
// code, a closed-vocabulary class name derived from it, and -- only when the
// driver's error carries one -- the constraint or table name. It never
// carries row values, raw SQL text, or a DSN; this package's own rule
// ("do not expose raw SQL/driver errors across the storage interface")
// still holds, this is the safe subset that rule always permitted.
//
// A caller extracts it with errors.As to distinguish WHY a dependency
// failed -- a permission denial from a connection failure from a constraint
// violation -- for its own structured logs, without ever seeing the raw
// driver error itself (CHAOS-6278: acr-api's oauth dependency-failure log
// previously carried none of this, which cost real debugging time root-
// causing a prod incident that Postgres's OWN log had to be read to find).
type DependencyErrorClass struct {
	// SQLState is the driver's own error code (e.g. Postgres's SQLSTATE,
	// "23514"), verbatim -- never blank when this type is attached.
	SQLState string
	// Class is a closed-vocabulary name derived from SQLState (e.g.
	// "check_violation", "insufficient_privilege", "connection_failure").
	// "unclassified" when SQLState is not in the adapter's known set --
	// SQLState itself is always present regardless, so nothing is lost.
	Class string
	// Constraint is the violated constraint's name, when the driver error
	// names one; "" otherwise.
	Constraint string
	// Table is the affected table's name, when the driver error names one;
	// "" otherwise.
	Table string
}

func (c *DependencyErrorClass) Error() string {
	return "dependency error class " + c.Class + " (" + c.SQLState + ")"
}

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
	// ListSince is the narrow, org-wide (not repository-scoped) incremental
	// read used by projection/export consumers such as CHAOS-3753's
	// devhealthsource: ordered by (UpdatedAt, EpisodeID), strictly after
	// (since, afterEpisodeID), at most limit rows. It intentionally takes an
	// orgID rather than a Principal -- like CredentialStore.List -- because a
	// projection worker is a service-level caller with no meaningful
	// repository scope of its own, not a repository-scoped client. Redacted
	// and purged-tombstone episodes are still returned (with content already
	// scrubbed by CreateIdempotent/Redact) so a caller can detect and
	// propagate the state change; RedactionState reports which.
	//
	// The watermark is UpdatedAt, not CreatedAt (CHAOS-3753 codex finding
	// C4): CreatedAt never changes, so a Redact/PurgeExpired* state
	// transition happening after a row's CreatedAt position had already been
	// passed by a caller's checkpoint could never be observed again --
	// revocations never reached a projection worker. UpdatedAt equals
	// CreatedAt at creation and is bumped by every state-changing write
	// (Redact, PurgeExpiredForPrincipal), so a post-projection state change
	// always produces a fresh, reachable watermark position. Callers that
	// build a cursor from a returned record MUST use UpdatedAt (not
	// CreatedAt) as the row's position, or their own cursor will never
	// converge with this ordering.
	ListSince(ctx context.Context, orgID string, since time.Time, afterEpisodeID string, limit int) ([]EpisodeProjectionRecord, error)
}

// EpisodeProjectionRecord is EpisodeStore.ListSince's row shape: enough to
// project or export an episode without exposing storage internals (payload
// JSON encoding, repository_id, idempotency keys).
type EpisodeProjectionRecord struct {
	EpisodeID      string
	RepoSlug       string
	TaskRef        string
	Goal           string
	Outcome        string
	Summary        string
	RedactionState string
	StartedAt      time.Time
	EndedAt        time.Time
	CreatedAt      time.Time
	// UpdatedAt is ListSince's watermark column -- see its doc comment.
	UpdatedAt time.Time
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
type CredentialRotationRollbackInput = credentiallifecycle.RotationRollbackInput
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

// WorkloadBindingKey is the exact tuple a validated k8s ServiceAccount
// identity is resolved against (CHAOS-4013, RFC 8693 workload token
// exchange). It is never accepted from a request field; it is built
// entirely from a Kubernetes TokenReview result.
type WorkloadBindingKey struct {
	TrustDomain        string
	Namespace          string
	ServiceAccountName string
	ServiceAccountUID  string
}

// WorkloadBinding is the declarative server-side grant a WorkloadBindingKey
// resolves to. There is deliberately no CRUD HTTP surface for this table
// (design brief's "do NOT build" list) -- rows are provisioned out of band
// (migration seed or a direct operator write), matching the "keep the
// surface minimal" instruction on the parent ticket.
type WorkloadBinding struct {
	BindingID        string
	OrgID            string
	Role             string
	RepositoryScopes []string
	DisabledAt       *time.Time
}

// WorkloadBindingStore is a read-only lookup by the exact validated
// identity tuple. Implementations must never resolve from anything else
// (see WorkloadBindingKey's doc comment).
type WorkloadBindingStore interface {
	Lookup(ctx context.Context, key WorkloadBindingKey) (WorkloadBinding, error)
}

func IsCredentialLifecycleAuditAction(action string) bool {
	switch action {
	case AuditActionCredentialCreated, AuditActionCredentialRotated, AuditActionCredentialRevoked:
		return true
	default:
		return false
	}
}
