package observability

import (
	"log/slog"
	"sync"
)

type MemorySink struct {
	mu       sync.RWMutex
	capacity int
	values   []SupportSnapshot
}

func NewMemorySink(capacity int) *MemorySink {
	if capacity < 1 {
		capacity = 1
	}
	return &MemorySink{capacity: capacity, values: make([]SupportSnapshot, 0, capacity)}
}

func (s *MemorySink) Record(snapshot SupportSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.values) == s.capacity {
		copy(s.values, s.values[1:])
		s.values = s.values[:s.capacity-1]
	}
	s.values = append(s.values, snapshot)
}

func (s *MemorySink) Snapshots() []SupportSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]SupportSnapshot(nil), s.values...)
}

type SlogSink struct {
	logger *slog.Logger
}

func NewSlogSink(logger *slog.Logger) SlogSink {
	if logger == nil {
		logger = slog.Default()
	}
	return SlogSink{logger: logger}
}

func (s SlogSink) Record(snapshot SupportSnapshot) {
	s.logger.Info("observability snapshot",
		"kind", snapshot.Kind,
		"request_id", snapshot.RequestID,
		"operation", snapshot.Operation,
		"outcome", snapshot.Outcome,
		"http_status_class", snapshot.HTTPStatusClass,
		"duration_ms", snapshot.DurationMillis,
		"packet_status", snapshot.PacketStatus,
		"packet_bytes", snapshot.PacketBytes,
		"packet_tokens", snapshot.PacketTokens,
		"packet_items", snapshot.PacketItems,
		"stale_sources", snapshot.StaleSources,
		"unavailable_sources", snapshot.UnavailableSources,
		"version_mismatch", snapshot.VersionMismatch,
		"packet_schema_version", snapshot.PacketSchemaVersion,
		"packet_baseline_version", snapshot.PacketBaselineVersion,
		"compatibility", snapshot.Compatibility,
		"store_query_class", snapshot.StoreQueryClass,
		"store_backend", snapshot.StoreBackend,
		"query_timed_out", snapshot.QueryTimedOut,
		"source_coverage", snapshot.SourceCoverage,
		"source_fallback", snapshot.SourceFallback,
		"query_version", snapshot.QueryVersion,
		"ranking_version", snapshot.RankingVersion,
		"denial_class", snapshot.Denial,
		"episode_outcome", snapshot.EpisodeOutcome,
		"audit_delivery", snapshot.AuditDelivery,
	)
}
