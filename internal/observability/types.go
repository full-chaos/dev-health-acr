// Package observability defines safe, consumer-neutral completion observations.
package observability

import (
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type RequestID string

type Operation string

const (
	OperationUnknown      Operation = "unknown"
	OperationHealth       Operation = "health"
	OperationReadiness    Operation = "readiness"
	OperationCapabilities Operation = "capabilities"
	OperationContext      Operation = "context"
	OperationEvidence     Operation = "evidence"
	OperationSnapshot     Operation = "snapshot"
	OperationEpisode      Operation = "episode"
)

type Kind string

const (
	KindRequest  Kind = "request"
	KindStore    Kind = "store"
	KindRanking  Kind = "ranking"
	KindEvidence Kind = "evidence"
	KindEpisode  Kind = "episode"
)

type Outcome string

const (
	OutcomeUnknown  Outcome = "unknown"
	OutcomeSuccess  Outcome = "success"
	OutcomeFailure  Outcome = "failure"
	OutcomeDenied   Outcome = "denied"
	OutcomeCanceled Outcome = "canceled"
)

type HTTPStatusClass string

const (
	HTTPStatusUnknown HTTPStatusClass = "unknown"
	HTTPStatus2xx     HTTPStatusClass = "2xx"
	HTTPStatus4xx     HTTPStatusClass = "4xx"
	HTTPStatus5xx     HTTPStatusClass = "5xx"
)

type PacketStatus string

const (
	PacketStatusUnknown  PacketStatus = "unknown"
	PacketStatusComplete PacketStatus = "complete"
	PacketStatusPartial  PacketStatus = "partial"
	PacketStatusDegraded PacketStatus = "degraded"
	PacketStatusEmpty    PacketStatus = "empty"
)

type CompatibilityStatus string

const (
	CompatibilityUnknown      CompatibilityStatus = "unknown"
	CompatibilityCompatible   CompatibilityStatus = "compatible"
	CompatibilityPartial      CompatibilityStatus = "partial"
	CompatibilityIncompatible CompatibilityStatus = "incompatible"
)

type SourceCoverage string

const (
	SourceCoverageUnknown SourceCoverage = "unknown"
	SourceCoverageFull    SourceCoverage = "full"
	SourceCoveragePartial SourceCoverage = "partial"
	SourceCoverageNone    SourceCoverage = "none"
)

type StoreQueryClass string

const (
	StoreQueryUnknown  StoreQueryClass = "unknown"
	StoreQueryPacket   StoreQueryClass = "packet"
	StoreQueryEvidence StoreQueryClass = "evidence"
	StoreQueryEpisode  StoreQueryClass = "episode"
)

type StoreBackend string

const (
	StoreBackendUnknown    StoreBackend = "unknown"
	StoreBackendMemory     StoreBackend = "memory"
	StoreBackendPostgres   StoreBackend = "postgres"
	StoreBackendClickHouse StoreBackend = "clickhouse"
)

type EpisodeOutcome string

const (
	EpisodeOutcomeUnknown   EpisodeOutcome = "unknown"
	EpisodeOutcomeSuccess   EpisodeOutcome = "success"
	EpisodeOutcomeFailure   EpisodeOutcome = "failure"
	EpisodeOutcomeDuplicate EpisodeOutcome = "duplicate"
	EpisodeOutcomeRedacted  EpisodeOutcome = "redacted"
)

type AuditDelivery string

const (
	AuditDeliveryUnknown   AuditDelivery = "unknown"
	AuditDeliveryDelivered AuditDelivery = "delivered"
	AuditDeliveryFailed    AuditDelivery = "failed"
	AuditDeliverySkipped   AuditDelivery = "skipped"
)

type SourceFallback string

const (
	SourceFallbackUnknown     SourceFallback = "unknown"
	SourceFallbackNone        SourceFallback = "none"
	SourceFallbackCatalog     SourceFallback = "catalog"
	SourceFallbackUnavailable SourceFallback = "unavailable"
)

type QueryVersion string

const (
	QueryVersionUnknown QueryVersion = "unknown"
	QueryVersionV1      QueryVersion = QueryVersion(contextpacket.QueryVersionV1)
)

type SchemaVersion string

const (
	SchemaVersionUnknown       SchemaVersion = "unknown"
	SchemaVersionContextPacket SchemaVersion = SchemaVersion(contractsv1.ContextPacketSchema)
)

type RankingVersion string

const (
	RankingVersionUnknown RankingVersion = "unknown"
	RankingVersionV1      RankingVersion = RankingVersion(contextpacket.RankingVersionV1)
)

type DenialClass string

const (
	DenialUnknown         DenialClass = "unknown"
	DenialNone            DenialClass = "none"
	DenialAuthentication  DenialClass = "authentication"
	DenialOrganization    DenialClass = "organization_scope"
	DenialPermissionScope DenialClass = "permission_scope"
	DenialRepositoryScope DenialClass = "repository_scope"
	DenialLicense         DenialClass = "license"
	DenialRateLimit       DenialClass = "rate_limit"
)

type PacketObservation struct {
	Status             PacketStatus
	Bytes              int64
	Tokens             int64
	SchemaVersion      SchemaVersion
	BaselineVersion    SchemaVersion
	Compatibility      CompatibilityStatus
	SourceCoverage     SourceCoverage
	Items              int64
	StaleSources       int64
	UnavailableSources int64
	VersionMismatch    bool
}

type RequestObservation struct {
	Operation   Operation
	StatusClass HTTPStatusClass
	Outcome     Outcome
	Duration    time.Duration
	Denial      DenialClass
}

type StoreObservation struct {
	QueryClass StoreQueryClass
	Backend    StoreBackend
	TimedOut   bool
	Outcome    Outcome
	Duration   time.Duration
	Packet     PacketObservation
}

type RankingObservation struct {
	Outcome        Outcome
	Duration       time.Duration
	QueryVersion   QueryVersion
	RankingVersion RankingVersion
}

type EvidenceObservation struct {
	Outcome        Outcome
	Duration       time.Duration
	SourceFallback SourceFallback
	SourceCoverage SourceCoverage
}

type EvidenceQuarantineObservation struct {
	Source   string
	RuleCode string
	Count    int64
}

type EpisodeObservation struct {
	Outcome        Outcome
	EpisodeOutcome EpisodeOutcome
	AuditDelivery  AuditDelivery
	Duration       time.Duration
}

// SupportSnapshot contains only bounded dimensions suitable for support tooling.
type SupportSnapshot struct {
	Kind                  Kind
	RequestID             RequestID
	Operation             Operation
	Outcome               Outcome
	HTTPStatusClass       HTTPStatusClass
	DurationMillis        int64
	PacketStatus          PacketStatus
	PacketBytes           int64
	PacketTokens          int64
	PacketItems           int64
	StaleSources          int64
	UnavailableSources    int64
	VersionMismatch       bool
	PacketSchemaVersion   SchemaVersion
	PacketBaselineVersion SchemaVersion
	Compatibility         CompatibilityStatus
	SourceCoverage        SourceCoverage
	StoreQueryClass       StoreQueryClass
	StoreBackend          StoreBackend
	QueryTimedOut         bool
	SourceFallback        SourceFallback
	QueryVersion          QueryVersion
	RankingVersion        RankingVersion
	Denial                DenialClass
	EpisodeOutcome        EpisodeOutcome
	AuditDelivery         AuditDelivery
	EvidenceSource        string
	EvidenceRuleCode      string
	QuarantinedRows       int64
}
