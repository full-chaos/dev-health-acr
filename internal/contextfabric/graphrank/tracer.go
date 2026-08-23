package graphrank

import (
	"context"
	"log/slog"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/observability"
)

// sanitizeLogString strips ASCII control characters -- notably \n and \r,
// the classic log-forging vector: an unescaped newline inside a logged
// value can make attacker-influenced text masquerade as a separate,
// fabricated log line -- from s before it reaches a logging sink (CodeQL
// go/log-injection, CHAOS-3918, 2026-08-19). Belt-and-suspenders on top of
// log/slog's own TextHandler/JSONHandler value quoting (Go's stdlib
// already escapes control characters inside a structured attribute value
// for both handlers -- so this specific forging vector is not actually
// exploitable through this type's DebugContext calls today), applied
// because a static analyzer has no way to credit that runtime behavior.
// \t is kept (harmless inside one log line, more readable than dropped).
func sanitizeLogString(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || (r < 0x20 && r != '\t') {
			return -1
		}
		return r
	}, s)
}

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
			"term_hash", event.TermHash, "result_count", event.SearchResultCount,
			"truncated", event.Truncated)
	case "search_question":
		// CHAOS-4120: the question-level SearchQuestion pass's own event --
		// before this case existed, this stage fell to the "unknown stage"
		// branch below and silently dropped its result_count/truncated
		// payload, the same defect class evidence_census_commit/
		// evidence_source_native were each found missing this case for.
		// No term_hash: this pass has no per-term identity, only ONE call
		// per resolution.
		t.logger.DebugContext(ctx, "context fabric resolution trace: search question",
			"request_id", event.RequestID, "stage", event.Stage,
			"result_count", event.SearchResultCount, "truncated", event.Truncated)
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
			"search_truncated", event.SearchTruncated,
			// CHAOS-4085/CHAOS-4089: the two fields that make a commit
			// attributable without transcript archaeology. Both are closed
			// vocabularies or booleans -- commit_basis is a CommitBasis enum
			// value, never an identifier -- so they carry no more than the
			// fields beside them already do.
			"commit_basis", event.CommitBasis, "tied_statistical_top", event.TiedStatisticalTop,
			// CHAOS-4117: the nominal MaxSubjectCandidates this resolution
			// ran with -- a plain int, no more sensitive than the counts
			// already on this line -- so a reader can tell a
			// pre-calibration (10) decision apart from a post-calibration
			// (20, or any caller-requested value) one from the trace
			// alone. See ResolutionTraceEvent.SearchCandidateLimit.
			"search_candidate_limit", event.SearchCandidateLimit,
			// CHAOS-4154: which candidate population a statistical commit
			// was decided over -- a closed vocabulary, see
			// ResolutionTraceEvent.PopulationBasis's own doc comment.
			"population_basis", event.PopulationBasis)
	case "kind_coverage_floor":
		// CHAOS-4086: the operator-visible half of CHAOS-4038's floor. The
		// harness reads the same event off an in-process tracer to put
		// these on a trial-report row; this branch is what makes the same
		// facts readable in production, where no harness exists. Counts and
		// booleans only -- no kind name, no term, no candidate identity.
		t.logger.DebugContext(ctx, "context fabric resolution trace: kind coverage floor",
			"request_id", event.RequestID, "stage", event.Stage,
			"fired", event.KindCoverageFloorFired,
			"missing_kinds", event.KindCoverageMissingKinds,
			"truncated", event.KindCoverageFloorTruncated)
	case "confirmed_kind_rescue":
		// CHAOS-4132: the operator-visible half of the confirmed-kind
		// rescue -- this event's own presence in a production log already
		// means the rescue was attempted (see ConfirmedKindRescueFired's
		// own doc comment); "fired"/"result_count" say whether it found
		// anything, and "truncated" says whether that finding is complete
		// enough to trust for a commit (folded into the gate's own
		// searchTruncated input, unlike the coverage floor's own
		// truncation signal -- see ConfirmedKindRescueTruncated's own doc
		// comment for why). Counts and bools only -- no kind name, no
		// term, no candidate identity.
		t.logger.DebugContext(ctx, "context fabric resolution trace: confirmed kind rescue",
			"request_id", event.RequestID, "stage", event.Stage,
			"fired", event.ConfirmedKindRescueFired,
			"result_count", event.ConfirmedKindRescueResultCount,
			"truncated", event.ConfirmedKindRescueTruncated)
	case "kind_offer":
		// CHAOS-4012 v20: the operator-visible half of kindOfferMaterial's
		// own suppression check -- this event fires on EVERY resolution
		// (unlike kind_coverage_floor/confirmed_kind_rescue above, which are
		// gated behind their own preconditions), so "distinct_kind_count"
		// tells an operator apart "genuinely nothing offerable" (0) from
		// "exactly one, still suppressed" (1) -- CHAOS-4012's own open
		// question -- without needing a harness. Counts and a bool only --
		// no kind name, no candidate identity.
		t.logger.DebugContext(ctx, "context fabric resolution trace: kind offer",
			"request_id", event.RequestID, "stage", event.Stage,
			"explicit_hint_count", event.KindOfferExplicitHintCount,
			"distinct_kind_count", event.KindOfferDistinctKindCount,
			"suppressed_by_cardinality", event.KindOfferSuppressedByCardinality)
	case "confirmed_kind_scope":
		// CHAOS-4154: the operator-visible half of the confirmed-kind
		// truncation-scoping mechanism -- this event's own presence in a
		// production log already means the resolution reached this
		// mechanism's own trigger (confirmed kind, resolution-wide
		// searchTruncated, nothing committed yet). "state" is the closed
		// vocabulary ConfirmedKindScopeState carries; "candidate_count" is
		// the isolated snapshot's own size (0 whenever state != "complete",
		// since an incomplete snapshot is never handed to the gate). Counts
		// and closed-vocabulary strings only -- no kind name, no term, no
		// candidate identity.
		t.logger.DebugContext(ctx, "context fabric resolution trace: confirmed kind scope",
			"request_id", event.RequestID, "stage", event.Stage,
			"state", event.ConfirmedKindScopeState,
			"candidate_count", event.ConfirmedKindScopeCandidateCount)
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
			"shadow_anchor_receipt_confirmed", event.ShadowAnchorReceiptConfirmed,
			"shadow_kinds_censused", event.ShadowKindsCensused,
			"shadow_kind_insensitivity_evaluated", event.ShadowKindInsensitivityEvaluated,
			"shadow_kind_insensitivity_outcome", event.ShadowKindInsensitivityOutcome,
			// CHAOS-4079 (codex xhigh review round 2, finding 1): the mode
			// MUST ride along with the outcome. Since CHAOS-4079 the probe
			// also evaluates in a write-free observation mode, so
			// "evaluated=true outcome=commit_sound" alone no longer tells a
			// log consumer whether the verdict held across an actual census
			// narrowing ("narrowed") or merely under a hint that narrowed
			// nothing ("observed_*") -- omitting it here would leave
			// production telemetry strictly less informative than the
			// harness's own tracer, reading every observation as an
			// attestation. Closed enum, no free text.
			"shadow_kind_insensitivity_mode", event.ShadowKindInsensitivityMode)
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
	case "evidence_census_commit":
		// CHAOS-3896 Slice C (codex xhigh review finding, confirmed and
		// fixed: this case was missing entirely, so a LIVE
		// graph_missing_satisfier refusal fell to the "unknown stage"
		// branch below and silently dropped Subject/Outcome/
		// GraphExistenceOK/CensusCommitReason -- unlike evidence_round/
		// evidence_probe above, this stage is NOT shadow-only, so losing
		// it here means losing the one loud signal design brief §1.4
		// requires for this exact refusal class). Content-safe: Subject is
		// kind+canonical_id (the graph's own stable identifier, the same
		// shape every other stage already logs), CensusCommitReason is a
		// closed-vocabulary DegradationReason string, never term/question
		// text.
		t.logger.DebugContext(ctx, "context fabric resolution trace: evidence census commit",
			"request_id", event.RequestID, "stage", event.Stage,
			"subject_kind", string(event.Subject.Kind), "subject_canonical_id", event.Subject.CanonicalID,
			"outcome", event.Outcome, "graph_existence_ok", event.GraphExistenceOK,
			"census_commit_reason", event.CensusCommitReason)
	case "evidence_source_native":
		// CHAOS-3918 (chris-ratified pre-registered shadow measurement,
		// 2026-08-19; codex xhigh review finding, confirmed and fixed:
		// this case was missing entirely, so the widening measurement's
		// whole payload fell to the "unknown stage" branch below and was
		// silently discarded in production -- the same defect class
		// evidence_census_commit above was already fixed for). Content-safe:
		// both non-request-id/stage fields are a count and a bool.
		// request_id/stage pass through sanitizeLogString -- see its own
		// doc comment (CodeQL go/log-injection).
		t.logger.DebugContext(ctx, "context fabric resolution trace: evidence source native (shadow widening)",
			"request_id", sanitizeLogString(event.RequestID), "stage", sanitizeLogString(event.Stage),
			"source_native_match_count", event.ShadowSourceNativeMatchCount,
			"source_native_any_resolved", event.ShadowSourceNativeAnyResolved)
	case "evidence_source_native_probe":
		// CHAOS-3918: ONE per-match receipt, mirrors evidence_probe's own
		// "per-kind, never aggregated" cardinality one level down to "per
		// grammar match". Content-safe: Grammar is the registry entry's own
		// fixed name (never the matched literal -- sourceNativeGrammarRegistry's
		// own doc comment; the ONLY place this field is ever assigned is
		// `Grammar: entry.name`, chaos3899_source_native_grammar.go, always
		// one of 5 fixed constants), Kind is a closed enum. Every
		// string-typed field here (including request_id/stage) still passes
		// through sanitizeLogString -- see its own doc comment (CodeQL
		// go/log-injection): a static analyzer cannot credit "this string
		// is registry-constant by construction" the way a human review can.
		t.logger.DebugContext(ctx, "context fabric resolution trace: evidence source native probe (shadow widening)",
			"request_id", sanitizeLogString(event.RequestID), "stage", sanitizeLogString(event.Stage),
			"source_native_grammar", sanitizeLogString(event.ShadowSourceNativeGrammar),
			"source_native_resolved", event.ShadowSourceNativeResolved,
			"source_native_kind", string(event.ShadowSourceNativeKind))
	case "slice_b_survivor_verdict":
		// CHAOS-4088: SurvivorsFirstOrder's own candidateSurvivorVerdict,
		// traced for the first time -- see ResolutionTraceEvent.SurvivorVerdict's
		// own doc comment for the diagnostic gap this closes and the
		// "silence means never reached, not everything neutral" contract.
		// Content-safe: Subject is the graph's own stable kind+canonical_id,
		// SurvivorVerdict is the closed "neutral"/"eliminated" vocabulary.
		t.logger.DebugContext(ctx, "context fabric resolution trace: slice b survivor verdict",
			"request_id", event.RequestID, "stage", event.Stage,
			"subject_kind", string(event.Subject.Kind), "subject_canonical_id", event.Subject.CanonicalID,
			"survivor_verdict", event.SurvivorVerdict)
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
