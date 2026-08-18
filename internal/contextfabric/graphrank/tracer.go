package graphrank

import (
	"context"
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/observability"
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
			"distinct_mechanisms", event.DistinctMechanisms)
	case "decision":
		t.logger.DebugContext(ctx, "context fabric resolution trace: decision",
			"request_id", event.RequestID, "stage", event.Stage,
			"subject_kind", string(event.Subject.Kind), "subject_canonical_id", event.Subject.CanonicalID,
			"outcome", event.Outcome, "winning_mechanism", event.WinningMechanism, "commit_gate", event.CommitGate,
			"alias_identity_complete", event.AliasLookupComplete, "identity_trust_gate_blocked", event.IdentityTrustGateBlocked,
			"search_truncated", event.SearchTruncated)
	case "identity_universe":
		t.logger.DebugContext(ctx, "context fabric resolution trace: identity universe read",
			"request_id", event.RequestID, "stage", event.Stage,
			"complete", event.IdentityUniverseComplete)
	case "identity_gate":
		t.logger.DebugContext(ctx, "context fabric resolution trace: identity gate",
			"request_id", event.RequestID, "stage", event.Stage,
			"subject_kind", string(event.Subject.Kind), "subject_canonical_id", event.Subject.CanonicalID,
			"from_keyed_identity_lookup", event.FromKeyedIdentityLookup, "eligible_kind", event.EligibleKind,
			"alias_matched", event.AliasMatched, "provider_matched", event.ProviderMatched,
			"gate_fired", event.GateFired, "final_confidence", event.FinalConfidence)
	case "evidence_round":
		// CHAOS-3899 (design brief v5 §5/§6 Slice A): the shadow evidence
		// round's own per-resolution outcome -- SUPPRESSED from any
		// commit-path decision this slice, logged for measurement only.
		// Content-safe: ShadowDIdentityHash is a SHA-256, never handle/
		// anchor text; every other field is a count/enum/bool.
		t.logger.DebugContext(ctx, "context fabric resolution trace: evidence round (shadow)",
			"request_id", event.RequestID, "stage", event.Stage,
			"shadow_outcome", event.ShadowOutcome, "shadow_reason", event.ShadowReason,
			"shadow_d_identity_hash", event.ShadowDIdentityHash,
			"shadow_precondition_unproven", event.ShadowPreconditionUnproven,
			"shadow_unscoped_visibility", event.ShadowUnscopedVisibility,
			"shadow_non_censused_survivor", event.ShadowNonCensusedSurvivor,
			"shadow_handle_grammar_bound", event.ShadowHandleGrammarBound,
			"shadow_anchor_unique_claimant", event.ShadowAnchorUniqueClaimant,
			"shadow_kinds_censused", event.ShadowKindsCensused)
	case "evidence_probe":
		// CHAOS-3899: ONE per-kind census receipt (brief §1.3(3), "Per-kind,
		// never aggregated across kinds").
		t.logger.DebugContext(ctx, "context fabric resolution trace: evidence probe (shadow census)",
			"request_id", event.RequestID, "stage", event.Stage,
			"census_kind", string(event.CensusKind), "census_complete", event.CensusComplete,
			"census_count", event.CensusCount, "census_read_at_unix", event.CensusReadAtUnix,
			"census_protocol", event.CensusProtocol, "census_closure_mismatch", event.CensusClosureMismatch,
			"census_statement_count", event.CensusStatementCount, "census_rows_read", event.CensusRowsRead,
			"census_handle_applied", event.CensusHandleApplied, "census_anchor_applied", event.CensusAnchorApplied)
	default:
		t.logger.DebugContext(ctx, "context fabric resolution trace: unknown stage",
			"request_id", event.RequestID, "stage", event.Stage)
	}
}

// SlogRawSignalObserver is CHAOS-3890's production RawSignalObserver: the
// CHAOS-3858 capture (ObserveDeps.RawSignalObserver's doc comment) existed
// as a measurement-only port that no production composition root ever set
// -- "what similarity/margin actually decided this" never ran outside a
// harness. This makes it run in prod, gated the SAME way
// SlogResolutionTracer already is: unconditionally wired, silent at any
// level an operator normally runs (Debug), and disclosed the moment they
// raise it, with no separate config knob. Content-safe by construction --
// it only ever reads CandidateNode's numeric raw-signal fields
// (VectorSimilarity, LexicalMatchedTerms, LexicalTermCount) and the
// mechanism enum, matching RawSignalObserver's own doc comment; it never
// reads Name or Attributes, which is where any raw corpus text on a
// CandidateNode would live.
type SlogRawSignalObserver struct {
	logger *slog.Logger
}

// NewSlogRawSignalObserver builds a SlogRawSignalObserver. A nil logger
// falls back to slog.Default(), matching NewSlogResolutionTracer's
// convention.
func NewSlogRawSignalObserver(logger *slog.Logger) SlogRawSignalObserver {
	if logger == nil {
		logger = slog.Default()
	}
	return SlogRawSignalObserver{logger: logger}
}

func (o SlogRawSignalObserver) ObserveCandidate(ctx context.Context, subjectKey string, node CandidateNode) {
	requestID, _ := observability.RequestIDFromContext(ctx)
	attrs := []any{
		"request_id", requestID,
		"subject_key", subjectKey,
		"mechanism", string(node.Mechanism),
	}
	if node.VectorSimilarity != nil {
		attrs = append(attrs, "vector_similarity", *node.VectorSimilarity)
	}
	if node.LexicalMatchedTerms != nil {
		attrs = append(attrs, "lexical_matched_terms", *node.LexicalMatchedTerms)
	}
	if node.LexicalTermCount != nil {
		attrs = append(attrs, "lexical_term_count", *node.LexicalTermCount)
	}
	o.logger.DebugContext(ctx, "context fabric raw retrieval signal", attrs...)
}
