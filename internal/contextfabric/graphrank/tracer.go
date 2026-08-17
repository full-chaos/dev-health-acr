package graphrank

import (
	"context"
	"log/slog"
)

// SlogResolutionTracer is the production ResolutionTracer (team-lead
// ruling, 2026-08-17): every stage event becomes one structured log line,
// content-safe BY CONSTRUCTION -- ResolutionTraceEvent's own fields are
// already counts/enums/subject-ids/confidence-numbers/bools only (see its
// doc comment), so this sink never needs its own filtering logic to keep
// term or question text out of the log stream. Mirrors
// contextfabric.SlogEngineTelemetry's exact pattern: one method, one log
// line per call, a nil logger falls back to slog.Default().
type SlogResolutionTracer struct {
	logger *slog.Logger
}

// NewSlogResolutionTracer builds a SlogResolutionTracer. A nil logger
// falls back to slog.Default(), matching SlogEngineTelemetry/
// observability.NewSlogSink's convention.
func NewSlogResolutionTracer(logger *slog.Logger) SlogResolutionTracer {
	if logger == nil {
		logger = slog.Default()
	}
	return SlogResolutionTracer{logger: logger}
}

func (t SlogResolutionTracer) Trace(event ResolutionTraceEvent) {
	ctx := context.Background()
	switch event.Stage {
	case "search":
		t.logger.DebugContext(ctx, "context fabric resolution trace: search",
			"request_id", event.RequestID, "stage", event.Stage,
			"term_hash", event.TermHash, "result_count", event.SearchResultCount)
	case "alias_lookup":
		t.logger.DebugContext(ctx, "context fabric resolution trace: alias lookup",
			"request_id", event.RequestID, "stage", event.Stage,
			"complete", event.AliasLookupComplete, "matched_claimants", event.AliasLookupMatchedClaimants)
	case "corroboration":
		t.logger.DebugContext(ctx, "context fabric resolution trace: corroboration",
			"request_id", event.RequestID, "stage", event.Stage,
			"subject_kind", string(event.Subject.Kind), "subject_canonical_id", event.Subject.CanonicalID,
			"base_confidence", event.BaseConfidence, "final_confidence", event.FinalConfidence,
			"distinct_mechanisms", event.DistinctMechanisms, "identity_trusted", event.IdentityTrusted)
	case "decision":
		t.logger.DebugContext(ctx, "context fabric resolution trace: decision",
			"request_id", event.RequestID, "stage", event.Stage,
			"subject_kind", string(event.Subject.Kind), "subject_canonical_id", event.Subject.CanonicalID,
			"outcome", event.Outcome, "winning_mechanism", event.WinningMechanism, "identity_trusted", event.IdentityTrusted)
	default:
		t.logger.DebugContext(ctx, "context fabric resolution trace: unknown stage",
			"request_id", event.RequestID, "stage", event.Stage)
	}
}
