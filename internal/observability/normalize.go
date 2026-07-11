package observability

import (
	"encoding/hex"
	"strings"
	"time"
)

func parseRequestID(value string) (RequestID, bool) {
	const prefix = "req_"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+32 || strings.ToLower(value) != value {
		return "", false
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(value, prefix)); err != nil {
		return "", false
	}
	return RequestID(value), true
}

func durationMillis(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	return duration.Milliseconds()
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func normalizeOutcome(value Outcome) Outcome {
	switch value {
	case OutcomeSuccess, OutcomeFailure, OutcomeDenied, OutcomeCanceled:
		return value
	default:
		return OutcomeUnknown
	}
}

func normalizeHTTPStatusClass(value HTTPStatusClass) HTTPStatusClass {
	switch value {
	case HTTPStatus2xx, HTTPStatus4xx, HTTPStatus5xx:
		return value
	default:
		return HTTPStatusUnknown
	}
}

func normalizePacketStatus(value PacketStatus) PacketStatus {
	switch value {
	case PacketStatusComplete, PacketStatusPartial, PacketStatusDegraded, PacketStatusEmpty:
		return value
	default:
		return PacketStatusUnknown
	}
}

func normalizeSourceFallback(value SourceFallback) SourceFallback {
	switch value {
	case "", SourceFallbackNone:
		return SourceFallbackNone
	case SourceFallbackCatalog, SourceFallbackUnavailable:
		return value
	default:
		return SourceFallbackUnknown
	}
}

func normalizeQueryVersion(value QueryVersion) QueryVersion {
	if value == QueryVersionV1 {
		return value
	}
	return QueryVersionUnknown
}

func normalizeSchemaVersion(value SchemaVersion) SchemaVersion {
	if value == SchemaVersionContextPacket {
		return value
	}
	return SchemaVersionUnknown
}

func normalizeRankingVersion(value RankingVersion) RankingVersion {
	if value == RankingVersionV1 {
		return value
	}
	return RankingVersionUnknown
}

func normalizeDenial(value DenialClass) DenialClass {
	switch value {
	case "", DenialNone:
		return DenialNone
	case DenialAuthentication, DenialOrganization, DenialPermissionScope, DenialRepositoryScope, DenialLicense, DenialRateLimit:
		return value
	default:
		return DenialUnknown
	}
}

func normalizeOperation(value Operation) Operation {
	switch value {
	case OperationHealth, OperationReadiness, OperationCapabilities, OperationContext, OperationEvidence, OperationSnapshot, OperationEpisode:
		return value
	default:
		return OperationUnknown
	}
}

func normalizeCompatibility(value CompatibilityStatus) CompatibilityStatus {
	switch value {
	case CompatibilityCompatible, CompatibilityPartial, CompatibilityIncompatible:
		return value
	default:
		return CompatibilityUnknown
	}
}

func normalizeSourceCoverage(value SourceCoverage) SourceCoverage {
	switch value {
	case SourceCoverageFull, SourceCoveragePartial, SourceCoverageNone:
		return value
	default:
		return SourceCoverageUnknown
	}
}

func normalizeStoreQueryClass(value StoreQueryClass) StoreQueryClass {
	switch value {
	case StoreQueryPacket, StoreQueryEvidence, StoreQueryEpisode:
		return value
	default:
		return StoreQueryUnknown
	}
}

func normalizeStoreBackend(value StoreBackend) StoreBackend {
	switch value {
	case StoreBackendMemory, StoreBackendPostgres, StoreBackendClickHouse:
		return value
	default:
		return StoreBackendUnknown
	}
}

func normalizeEpisodeOutcome(value EpisodeOutcome) EpisodeOutcome {
	switch value {
	case EpisodeOutcomeSuccess, EpisodeOutcomeFailure, EpisodeOutcomeDuplicate, EpisodeOutcomeRedacted:
		return value
	default:
		return EpisodeOutcomeUnknown
	}
}

func normalizeAuditDelivery(value AuditDelivery) AuditDelivery {
	switch value {
	case AuditDeliveryDelivered, AuditDeliveryFailed, AuditDeliverySkipped:
		return value
	default:
		return AuditDeliveryUnknown
	}
}
