package graphrank

import (
	"context"
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
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

// decisionSummaryFieldsUnconstructed reports whether f was NOT built by
// eventspec.NewDecisionSummaryFields -- the generated constructor is the
// ONLY thing that can set the unexported "constructed" marker
// (eventspec.DecisionSummaryFields.IsConstructed()), so no composite
// literal assembled outside the eventspec package can ever read true here,
// whether it is a caller hand-assembling the shared struct's OLD individual
// Decision*/OfferPool* fields and leaving DecisionSummaryFields at its Go
// zero value (this PR's own named failure mode: "callers still assemble
// that event's field list"), a PARTIAL hand-built DecisionSummaryFields
// literal (one exported field set, the rest at their zero value -- round r1's
// P1: an earlier version of this guard checked five open-vocabulary string
// fields directly and a literal setting only one of them slipped past it),
// or even a literal that hand-sets EVERY exported field. tracer.go stopped
// reading the OLD fields for this stage (CHAOS-5516), so emitting an
// unconstructed value anyway would print a decision_summary line with
// misleading zero/nil fields -- indistinguishable from a genuine
// zero-decision resolution.
func decisionSummaryFieldsUnconstructed(f eventspec.DecisionSummaryFields) bool {
	return !f.IsConstructed()
}

func (t SlogResolutionTracer) Trace(event ResolutionTraceEvent) {
	ctx := context.Background()
	switch event.Stage {
	case "search":
		// Rig-visibility fix: measured bounded (1 event per
		// term this resolution's own terms list carries, typically a
		// handful) -- safe to promote straight to Info, same reasoning as
		// kind_offer's own unconditional-and-bounded promotion (CHAOS-5222).
		t.logger.InfoContext(ctx, "context fabric resolution trace: search",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			// CHAOS-5517: the self-carried bounded-many identity -- see
			// ResolutionTraceEvent.Index's own doc comment.
			"index", event.Index, "total", event.Total,
			"term_hash", contextfabric.SanitizeLogAttr(event.TermHash), "result_count", event.SearchResultCount,
			"truncated", event.Truncated)
	case "search_question":
		// CHAOS-4120: the question-level SearchQuestion pass's own event --
		// before this case existed, this stage fell to the "unknown stage"
		// branch below and silently dropped its result_count/truncated
		// payload, the same defect class evidence_census_commit/
		// evidence_source_native were each found missing this case for.
		// No term_hash: this pass has no per-term identity, only ONE call
		// per resolution.
		// Rig-visibility fix: one call per resolution -- safe
		// to promote straight to Info.
		t.logger.InfoContext(ctx, "context fabric resolution trace: search question",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"result_count", event.SearchResultCount, "truncated", event.Truncated)
	case "alias_lookup":
		// Rig-visibility fix: a single emission site
		// (resolve.go), no per-candidate loop -- safe to promote straight
		// to Info.
		t.logger.InfoContext(ctx, "context fabric resolution trace: alias lookup",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"complete", event.AliasLookupComplete, "matched_claimants", event.AliasLookupMatchedClaimants)
	case "kind_hint_search":
		// CHAOS-4348: traceKindHintSearch's own event (chaos4348_reachability.go)
		// -- one per matched node, before this case existed this stage fell
		// to the "unknown stage" branch below (TestSlogResolutionTracer_
		// CoversEveryEmittedStage, chaos3918_tracer_stage_coverage_test.go,
		// is what a codex review caught this against -- see that function's
		// own two-functions-not-one-parameterized doc comment for why the
		// AST walk needed a literal Stage per call site to see it at all).
		// STAYS Debug: an adversarial review round reproduced this as
		// genuinely retrieval-pool-sized (the sibling exact_name_search
		// case, same shape, measured 90 events on a 90-node fixture) --
		// "bounded by matched-node count, small in practice" does not hold
		// in general, and no existing summary event covers this stage's
		// own aggregate, so it stays at Debug rather than being folded.
		t.logger.DebugContext(ctx, "context fabric resolution trace: kind hint search",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"term_hash", contextfabric.SanitizeLogAttr(event.TermHash), "subject_kind", contextfabric.SanitizeLogAttr(string(event.Subject.Kind)),
			"subject_canonical_id", contextfabric.SanitizeLogAttr(event.Subject.CanonicalID))
	case "exact_name_search":
		// CHAOS-4348: traceExactNameSearch's own event, same convention as
		// kind_hint_search immediately above.
		// STAYS Debug: measured retrieval-pool-sized (90 Info lines on a
		// 90-node exact-name-match fixture) -- an adversarial review round
		// found this promotion unsafe; reverted rather than folded, since
		// no operator-facing aggregate need was established for this
		// stage (unlike corroboration/identity_gate/slice_b_survivor_verdict,
		// each of which folds into a genuine summary).
		t.logger.DebugContext(ctx, "context fabric resolution trace: exact name search",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"term_hash", contextfabric.SanitizeLogAttr(event.TermHash), "subject_kind", contextfabric.SanitizeLogAttr(string(event.Subject.Kind)),
			"subject_canonical_id", contextfabric.SanitizeLogAttr(event.Subject.CanonicalID))
	case "corroboration":
		// Measured retrieval-pool-sized (96 events on a
		// 90-candidate crowd, past the per-pass Info ceiling, same class
		// as ranked_cut's own per-candidate line) -- this per-candidate
		// line STAYS Debug. CorroborationSummary (below) is the
		// once-per-pass Info line an operator actually gets; see
		// ResolutionTraceEvent.CorroborationSummary's own doc comment for
		// the full rule and why this is a second event on this SAME token
		// rather than promoting this one.
		if event.CorroborationSummary {
			t.logger.InfoContext(ctx, "context fabric resolution trace: corroboration summary",
				"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
				// CHAOS-5517: this summary is now pass-keyed the same way
				// ranked_cut/anchor_slot_displaced/decision_summary already
				// are -- see ResolutionTraceEvent.Pass's own doc comment.
				"pass", event.Pass,
				"candidate_count", event.CorroborationCandidateCount,
				"top_ids", contextfabric.SanitizeLogStrings(event.CorroborationTopIDs),
				"min_confidence", event.CorroborationMinConfidence,
				"max_confidence", event.CorroborationMaxConfidence)
			return
		}
		t.logger.DebugContext(ctx, "context fabric resolution trace: corroboration",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"pass", event.Pass,
			// CHAOS-5517: the self-carried bounded-many identity -- see
			// ResolutionTraceEvent.Index's own doc comment.
			"index", event.Index, "total", event.Total,
			"subject_kind", contextfabric.SanitizeLogAttr(string(event.Subject.Kind)), "subject_canonical_id", contextfabric.SanitizeLogAttr(event.Subject.CanonicalID),
			"base_confidence", event.BaseConfidence, "final_confidence", event.FinalConfidence,
			"distinct_mechanisms", event.DistinctMechanisms)
	case "decision":
		// The per-subject line below STAYS Debug: it fires once per
		// COMMITTED subject, so a resolution committing a large set is
		// unbounded on one call, the same volume class as identity_gate's
		// own per-candidate line. DecisionSummary (emitted by
		// decisionSummaryBuffer, resolve.go, once per
		// ResolveSubjectsWithCommitBasis call, INCLUDING when it counted
		// nothing) is the folded Info line an operator actually gets --
		// the only Info line that says what the resolver DECIDED rather
		// than what it looked at. See
		// ResolutionTraceEvent.DecisionSummary's own doc comment.
		t.logger.DebugContext(ctx, "context fabric resolution trace: decision",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"subject_kind", contextfabric.SanitizeLogAttr(string(event.Subject.Kind)), "subject_canonical_id", contextfabric.SanitizeLogAttr(event.Subject.CanonicalID),
			"outcome", contextfabric.SanitizeLogAttr(event.Outcome), "winning_mechanism", contextfabric.SanitizeLogAttr(event.WinningMechanism), "commit_gate", contextfabric.SanitizeLogAttr(event.CommitGate),
			"alias_identity_complete", event.AliasLookupComplete, "identity_trust_gate_blocked", event.IdentityTrustGateBlocked,
			"search_truncated", event.SearchTruncated,
			// CHAOS-4085/CHAOS-4089: the two fields that make a commit
			// attributable without transcript archaeology. Both are closed
			// vocabularies or booleans -- commit_basis is a CommitBasis enum
			// value, never an identifier -- so they carry no more than the
			// fields beside them already do.
			"commit_basis", contextfabric.SanitizeLogAttr(event.CommitBasis), "tied_statistical_top", event.TiedStatisticalTop,
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
			"population_basis", contextfabric.SanitizeLogAttr(event.PopulationBasis),
			// CHAOS-4234 (codex round-2 finding #1): offersOnlyDecisionTracer
			// tags every decision event from the offers-only pass with
			// OfferedUnderWindowGate=true before it reaches this sink --
			// without logging it here, a production log line could show
			// "outcome=committed" with no indication the resolution behind
			// it was discarded unconditionally (see
			// offersOnlyDecisionTracer's own doc comment, resolve.go).
			"offered_under_window_gate", event.OfferedUnderWindowGate)
	case "decision_summary":
		// CHAOS-5516 (clauses 1+4): emits via the GENERATED SlogArgs() on
		// the typed value decisionSummaryBuffer.flush() built (resolve.go),
		// not a second, independently hand-typed key list -- the keys this
		// line carries can never drift from spec.go's DecisionSummary
		// declaration, because both this call and the declaration derive
		// from the same generated source
		// (chaos5516_decision_summary_slog_args_test.go pins the exact key
		// set a real emitted line carries against it).
		if decisionSummaryFieldsUnconstructed(event.DecisionSummaryFields) {
			// REFUSED, not a zeroed summary: a caller that still assembles
			// this stage's OLD individual fields reaches here with
			// DecisionSummaryFields at its Go zero value. Emitting it would
			// be silently wrong (all zeros, indistinguishable from a
			// genuine zero-decision resolution); refusing loudly at Error
			// makes the gap itself observable instead.
			t.logger.ErrorContext(ctx, "context fabric resolution trace: decision summary construction refused",
				"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage))
			return
		}
		// r3 fix (round r2 finding 3): event.RequestID and
		// event.DecisionSummaryFields.RequestID are two INDEPENDENT fields
		// on two different types -- IsConstructed() proves the typed value
		// went through the real generated constructor, but proves nothing
		// about whether the caller passed it the SAME request id as the
		// enclosing event's own top-level RequestID. A mismatch here is
		// exactly as silent a defect as an unconstructed value: an operator
		// reading this line's own request_id key (event.RequestID, which
		// the "decision summary construction refused" line above also
		// uses) could be looking at a DIFFERENT resolution's decision than
		// the one this line's every other field actually describes.
		// Compared post-sanitization (contextfabric.SanitizeLogAttr is
		// deterministic and idempotent; was sanitizeLogString before #497
		// routed this package's own sanitization through the shared
		// barrier) because production's own real call site sanitizes
		// b.requestID into the typed constructor but keeps the RAW value on
		// the outer event -- a raw id containing a control character is a
		// legitimate (if rare) match that a naive unsanitized comparison
		// would misreport as a mismatch.
		if contextfabric.SanitizeLogAttr(event.RequestID) != event.DecisionSummaryFields.RequestID {
			t.logger.ErrorContext(ctx, "context fabric resolution trace: decision summary construction refused",
				"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
				// Sanitized again here even though the real construction
				// path already sanitized it before storing (resolve.go) --
				// a bare struct field read is opaque to
				// TestNoUnsanitizedLogAttributeInContextFabric's own static
				// trace (it cannot see the field was sanitized elsewhere),
				// and double-sanitizing is idempotent and harmless.
				"typed_request_id", contextfabric.SanitizeLogAttr(event.DecisionSummaryFields.RequestID))
			return
		}
		t.logger.InfoContext(ctx, "context fabric resolution trace: decision summary",
			event.DecisionSummaryFields.SlogArgs()...)
	case "anchor_pool":
		// Once per resolution, Info: there is no per-candidate counterpart
		// here, so no volume split is needed. Emitted from the same
		// statement that hands the scope to the confirmed-kind filter.
		t.logger.InfoContext(ctx, "context fabric resolution trace: anchor pool kind scope",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"anchor_pool_kind_scope", contextfabric.SanitizeLogAttr(event.DecisionAnchorPoolKindScope),
			"anchor_pool_kind_scope_source", contextfabric.SanitizeLogAttr(event.DecisionAnchorPoolKindScopeSource),
			"member_kind_confirmed", contextfabric.SanitizeLogAttr(event.DecisionMemberKindConfirmed),
			"reserved_kinds", contextfabric.SanitizeLogStrings(event.DecisionReservedKinds),
			"filter_kinds", contextfabric.SanitizeLogStrings(event.DecisionFilterKinds))
	case "offer_pool":
		// Same volume split as corroboration and identity_gate: the
		// per-candidate line is retrieval-pool-sized (186 of 329 offered
		// candidates were vector-only in one measured 36-question arm) and
		// stays Debug; the once-per-call summary is Info. Both carry closed
		// vocabulary and counts only -- the subject's kind and canonical id,
		// never a term, never a confidence.
		if event.OfferPoolSummary {
			t.logger.InfoContext(ctx, "context fabric resolution trace: offer pool summary",
				"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
				// CHAOS-5517: pass-keyed the same way corroboration_summary
				// now is -- see ResolutionTraceEvent.Pass's own doc comment.
				"pass", event.Pass,
				"vector_only_excluded", event.OfferPoolVectorOnlyExcluded,
				"vector_only_demoted", event.OfferPoolVectorOnlyDemoted,
				"emptied_by_exclusion", event.OfferPoolEmptiedByExclusion)
			return
		}
		t.logger.DebugContext(ctx, "context fabric resolution trace: offer pool",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"pass", event.Pass,
			// CHAOS-5517: the self-carried bounded-many identity, spanning
			// BOTH dispositions this stage carries (vector_only_demoted AND
			// vector_only_excluded) -- see ResolutionTraceEvent.Index's own
			// doc comment.
			"index", event.Index, "total", event.Total,
			"subject_kind", contextfabric.SanitizeLogAttr(string(event.Subject.Kind)), "subject_canonical_id", contextfabric.SanitizeLogAttr(event.Subject.CanonicalID),
			"disposition", contextfabric.SanitizeLogAttr(event.OfferPoolDisposition))
	case "kind_coverage_floor":
		// CHAOS-4086: the operator-visible half of CHAOS-4038's floor. The
		// harness reads the same event off an in-process tracer to put
		// these on a trial-report row; this branch is what makes the same
		// facts readable in production, where no harness exists. Counts and
		// booleans only -- no kind name, no term, no candidate identity.
		//
		// missing_kinds_list (CHAOS-4183 phase 2, team-lead ruling
		// 2026-08-23) is the deliberate exception to "no kind name" above --
		// closed-vocabulary kind VALUES only (never a canonical id, never
		// candidate identity), same discipline "boundary_kinds" already
		// established for the kind_offer stage. See
		// KindCoverageMissingKindsList's own doc comment
		// (ResolutionTraceEvent) for the CHAOS-4012 re-smoke ambiguity this
		// resolves.
		t.logger.InfoContext(ctx, "context fabric resolution trace: kind coverage floor",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"fired", event.KindCoverageFloorFired,
			"missing_kinds", event.KindCoverageMissingKinds,
			"truncated", event.KindCoverageFloorTruncated,
			"missing_kinds_list", contextfabric.SanitizeLogStrings(event.KindCoverageMissingKindsList))
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
		// "attempted" is emitted explicitly rather than left implicit in the
		// event's presence. The presence rule is correct and documented
		// above, but "fired=false" reads as "the rescue did not run" to
		// anyone who has not read that doc comment -- it actually means the
		// rescue RAN and found nothing, which is the opposite conclusion
		// about whether "no candidate" is an exhaustive census or a skipped
		// one. That misreading has already happened once. A reader should
		// not need the source to interpret the line.
		//
		// Rig-visibility fix: gated behind the rescue's own
		// trigger, fires at most once per pass -- safe to promote straight
		// to Info.
		t.logger.InfoContext(ctx, "context fabric resolution trace: confirmed kind rescue",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"attempted", true,
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
		// CHAOS-4012 v22: candidate_offer_count/offer_kind ride the SAME
		// unconditional event -- see KindOfferOfferKind's own doc comment
		// (ResolutionTraceEvent) for the closed vocabulary.
		//
		// boundary_kinds (CHAOS-4012 v22, team-lead ruling 2026-08-23) is the
		// deliberate exception to "no kind name" above: closed-vocabulary
		// subject-kind VALUES only (never a canonical id, never candidate
		// identity), naming which kinds survived to this exact call boundary
		// -- see KindOfferBoundaryKinds' own doc comment (ResolutionTraceEvent)
		// for why boundary-scoped presence, distinct from the trace-wide
		// ExpectedInPool, is what this re-smoke follow-up needed.
		//
		// CHAOS-4183 phase 3 (sol design consult, team-lead ratified
		// 2026-08-23): boundary_kinds is now POST-repair (the kind-only
		// projection's own `after` list); the three *_before_repair keys
		// carry the pre-phase-3 readings verbatim, same closed-vocabulary
		// discipline as boundary_kinds itself. See
		// KindOfferBoundaryKindsBeforeRepair's own doc comment
		// (ResolutionTraceEvent) for the full mechanism.
		// Rig-visibility fix: InfoContext, matching kind_offer_withheld's own
		// precedent below -- the production default log level is
		// slog.LevelInfo (internal/sidecar/config.go), so a Debug line does
		// not exist in production at all. This is the operator-visible
		// unconditional per-resolution offer summary; ranked_cut and
		// reserved_kind_admitted just below carry the same reasoning.
		t.logger.InfoContext(ctx, "context fabric resolution trace: kind offer",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"explicit_hint_count", event.KindOfferExplicitHintCount,
			"declared_hint_count", event.KindOfferDeclaredHintCount,
			// CHAOS-5218: the decision basis for withdrawing an unservable
			// frame-declared kind from the offer (a candidate-kind
			// elimination). Always emitted, so 0 is a real reading.
			"declared_withheld_not_in_pool_count", event.KindOfferDeclaredWithheldNotInPoolCount,
			"distinct_kind_count", event.KindOfferDistinctKindCount,
			"suppressed_by_cardinality", event.KindOfferSuppressedByCardinality,
			// CHAOS-5218: the OTHER suppression reason, reported beside it -- an
			// operator must be able to tell "nothing to disambiguate" from "the
			// kind this question is about cannot be served".
			"suppressed_by_unservable_declared_kind", event.KindOfferSuppressedByUnservableDeclaredKind,
			"candidate_offer_count", event.KindOfferCandidateOfferCount,
			"offer_kind", contextfabric.SanitizeLogAttr(event.KindOfferOfferKind),
			// CHAOS-4210: how many of candidate_offer_count's own options
			// needed their Label bounded to the v1 wire contract -- see
			// KindOfferCandidateOfferLabelsNormalizedCount's own doc comment
			// (ResolutionTraceEvent) for why this must be diagnosable from
			// the run's own artifacts, not just applied silently.
			"candidate_offer_labels_normalized_count", event.KindOfferCandidateOfferLabelsNormalizedCount,
			"boundary_kinds", contextfabric.SanitizeLogStrings(event.KindOfferBoundaryKinds),
			"boundary_kinds_before_repair", contextfabric.SanitizeLogStrings(event.KindOfferBoundaryKindsBeforeRepair),
			"distinct_kind_count_before_repair", event.KindOfferDistinctKindCountBeforeRepair,
			"suppressed_by_cardinality_before_repair", event.KindOfferSuppressedByCardinalityBeforeRepair,
			// CHAOS-4119: handleOfferMaterial's own graph-derived-source
			// diagnostics, riding this SAME unconditional event -- see
			// HandleOfferGraphDerivedCount's own doc comment
			// (ResolutionTraceEvent) for what each key measures.
			"handle_offer_count_before_graph_source", event.HandleOfferCountBeforeGraphSource,
			"handle_offer_graph_derived_count", event.HandleOfferGraphDerivedCount,
			"handle_offer_graph_derived_rejected_count", event.HandleOfferGraphDerivedRejectedCount,
			"offered_under_window_gate", event.OfferedUnderWindowGate)
	case "kind_offer_withheld":
		// CHAOS-5218. Emitted ONLY when the offer withheld at least one
		// frame-declared kind because the full merged pool held no candidate
		// of that kind -- an outcome-affecting CANDIDATE-KIND ELIMINATION in
		// AGENTS.md's own sense, not the per-resolution offer bookkeeping the
		// unconditional "kind_offer" stage above carries.
		//
		// InfoContext, deliberately, where every other stage on this sink is
		// DebugContext: the production default log level is slog.LevelInfo
		// (internal/sidecar/config.go), so a Debug line does not exist in
		// production at all -- which is exactly why no ResolutionTraceEvent
		// line appears in any rig log today. An elimination that changes the
		// answer a caller receives must be readable from an ordinary
		// production log, the same posture
		// contextfabric.SlogEngineTelemetry.RecordSubjectlessTerminal and
		// falkorgraph.SlogTelemetry.RecordSubjectCandidatesAuthzDropped
		// already take for their own "nothing is broken, but you must be able
		// to see it" events. The ordinary counts stay on the Debug
		// "kind_offer" line; this is the operator-visible half.
		//
		// request_id rides as an explicit field rather than through ctx join
		// attrs because this sink's Trace method takes no context (it builds
		// context.Background() above) -- an established shape of the
		// ResolutionTracer interface, carried here rather than re-derived.
		// withheld_kinds is closed-vocabulary subject-kind VALUES only, the
		// same ruled exception boundary_kinds carries; never a canonical id,
		// never candidate identity.
		t.logger.InfoContext(ctx, "context fabric resolution trace: kind offer withheld",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"withheld_count", event.KindOfferDeclaredWithheldNotInPoolCount,
			"withheld_kinds", contextfabric.SanitizeLogStrings(event.KindOfferDeclaredWithheldKinds),
			"declared_hint_count", event.KindOfferDeclaredHintCount,
			"distinct_kind_count", event.KindOfferDistinctKindCount,
			"suppressed_by_cardinality", event.KindOfferSuppressedByCardinality,
			"suppressed_by_unservable_declared_kind", event.KindOfferSuppressedByUnservableDeclaredKind)
	case "anchor_offer":
		// CHAOS-4210: unconditional, mirroring kind_offer's own "fires on
		// EVERY resolution" discipline -- see
		// AnchorOfferLabelsNormalizedCount's own doc comment
		// (ResolutionTraceEvent) for why this must be diagnosable from the
		// run's own artifacts, not just applied silently.
		// Rig-visibility fix: this event fires unconditionally,
		// once per resolution -- the SAME shape kind_offer already has at
		// Info (CHAOS-5222) -- so it was already safe to promote and had
		// simply never been. Safe straight promotion.
		t.logger.InfoContext(ctx, "context fabric resolution trace: anchor offer",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"labels_normalized_count", event.AnchorOfferLabelsNormalizedCount)
	case "ranked_cut":
		// Measured before picking a shape (a volume gate on log lines per
		// pass) -- this per-candidate line is emitted once per RETRIEVAL-
		// sized pool candidate (up to 91 in one representative fixture),
		// well past the 25-per-pass ceiling for an unconditional Info
		// line, so it STAYS DebugContext. RankedCutSummary (below) is the
		// once-per-PASS Info line an operator actually gets on the rig --
		// NOT in a fixed count relationship with that pass's own "decision"
		// event(s) (an empty-pool pass decides but has nothing to cut; a
		// multi-subject commit decides once per subject but cuts once), but
		// the LAST summary reaching the tracer for a request_id always
		// describes the pass whose resolution was actually returned, the
		// same guarantee "decision" itself carries; see
		// ResolutionTraceEvent.RankedCutSummary's own doc comment for the
		// full rule and why this is a second event on this SAME token
		// rather than promoting this one.
		if event.RankedCutSummary {
			// CHAOS-5434: the decided scope anchor's slot rides this same
			// once-per-pass line. All four keys are written on EVERY
			// summary, with `none`/0 when nothing was reserved, so an
			// operator can never confuse "this build stopped deciding a
			// slot" with "this pass reserved none" -- and pool_truncated_n
			// is what says whether a slot could have mattered here at all.
			t.logger.InfoContext(ctx, "context fabric resolution trace: ranked cut summary",
				"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
				// CHAOS-5516: which finalization of this resolution produced
				// this line, 1-based.
				"pass", event.Pass,
				"candidate_count", event.RankedCutCandidateCount,
				"survived_count", event.RankedCutSurvivedCount,
				"survived_ids", contextfabric.SanitizeLogStrings(event.RankedCutSurvivedIDs),
				"max", event.RankedCutMax,
				"anchor_slot_reserved", contextfabric.SanitizeLogAttr(event.AnchorSlotReserved),
				"anchor_slot_source", contextfabric.SanitizeLogAttr(event.AnchorSlotSource),
				"anchor_slot_displaced", event.AnchorSlotDisplaced,
				"pool_truncated_n", event.PoolTruncatedN,
				// CHAOS-5388: what the kind-scoped rescue arm did per declared
				// kind. Always present, empty list when nothing was declared.
				"declared_kind_rescue", event.DeclaredKindRescue)
			return
		}
		t.logger.DebugContext(ctx, "context fabric resolution trace: ranked cut",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"subject_kind", contextfabric.SanitizeLogAttr(string(event.Subject.Kind)), "subject_canonical_id", contextfabric.SanitizeLogAttr(event.Subject.CanonicalID),
			"rank", event.Rank, "survived", event.Survived, "coverage_bypass", event.CoverageBypass)
	case "anchor_slot_displaced":
		// CHAOS-5434. The scope anchor's reserved slot is the ONE admission
		// that can take a place from a candidate ranking had earned -- and
		// only when every in-budget tier-2 candidate is of a reserved kind,
		// which on a scope-anchored frame means a saturated member crowd.
		// It is at Info, and it names the evicted subject, because a
		// displacement an operator cannot see is exactly the regression
		// this ticket's acceptance forbids: "the reserved slot never
		// displaces a higher-ranked member SILENTLY". Absence of this line
		// on a pass whose summary reports anchor_slot_displaced=0 is a
		// measured zero, not a missing measurement -- the two are
		// distinguishable because the summary always carries the count.
		t.logger.InfoContext(ctx, "context fabric resolution trace: anchor slot displaced",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			// CHAOS-5516: the same pass this displacement's own RankedCutSummary
			// line carries -- the two lines from one pass agree on it.
			"pass", event.Pass,
			"subject_kind", contextfabric.SanitizeLogAttr(string(event.Subject.Kind)), "subject_canonical_id", contextfabric.SanitizeLogAttr(event.Subject.CanonicalID),
			"anchor_slot_reserved", contextfabric.SanitizeLogAttr(event.AnchorSlotReserved),
			"anchor_slot_source", contextfabric.SanitizeLogAttr(event.AnchorSlotSource),
			"anchor_slot_displaced", event.AnchorSlotDisplaced,
			"pool_truncated_n", event.PoolTruncatedN)
	case "reserved_kind_admitted":
		// One event per candidate that phase 4's kind reserve kept past the
		// flat cut (resolution.go, reservedPrefix). It is the operator-visible
		// half of the reserve: its presence means a kind the FRAME OR RECEIPT
		// declared would otherwise have been truncated out of the offered
		// candidate list entirely, which is the defect the reserve exists to
		// stop. Absence means the reserve was inert on this resolution --
		// either nothing was reserved, or the ranking already kept the kind.
		// Rank is the candidate's PRE-CUT rank, so the distance past `max`
		// says how badly the kind lost the ranking race.
		// Rig-visibility fix: InfoContext -- see kind_offer's own comment above.
		// This event's presence is the operator-visible proof the reserve
		// actually fired for a candidate; absence at Debug (today) is
		// indistinguishable from "the reserve was inert," exactly the
		// ambiguity this ticket exists to remove.
		t.logger.InfoContext(ctx, "context fabric resolution trace: reserved kind admitted",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			// CHAOS-5517: pass-keyed (this admission belongs to the SAME
			// pass its own ranked_cut/anchor_slot_displaced lines do) and
			// self-carrying the bounded-many identity -- see
			// ResolutionTraceEvent.Pass/.Index's own doc comments.
			"pass", event.Pass, "index", event.Index, "total", event.Total,
			"subject_kind", contextfabric.SanitizeLogAttr(string(event.Subject.Kind)), "subject_canonical_id", contextfabric.SanitizeLogAttr(event.Subject.CanonicalID),
			"rank", event.Rank, "survived", event.Survived)
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
		// CHAOS-4155 Phase 1 (codex R1, High, confirmed): the shadow vector
		// census's own outcome MUST reach production logs on this same
		// event, or Phase 2's live-measurement request has nothing to
		// read -- telemetry that never reaches its documented consumer is
		// the same as no telemetry at all. Fields are zero-valued
		// (state=="") whenever the shadow arm was never invoked, matching
		// ConfirmedKindScopeState's own "absent means not attempted"
		// convention.
		//
		// Rig-visibility fix: gated behind this mechanism's own
		// trigger, fires at most once per pass -- safe to promote straight
		// to Info.
		t.logger.InfoContext(ctx, "context fabric resolution trace: confirmed kind scope",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"state", contextfabric.SanitizeLogAttr(event.ConfirmedKindScopeState),
			"candidate_count", event.ConfirmedKindScopeCandidateCount,
			"vector_census_state", contextfabric.SanitizeLogAttr(event.ConfirmedKindVectorScopeState),
			"vector_census_population_count", event.ConfirmedKindVectorScopePopulationCount,
			"vector_census_enumerated_count", event.ConfirmedKindVectorScopeEnumeratedCount,
			"vector_census_malformed_count", event.ConfirmedKindVectorScopeMalformedCount,
			"vector_census_query_count", event.ConfirmedKindVectorScopeQueryCount,
			"vector_census_queries_scored", event.ConfirmedKindVectorScopeQueriesScored,
			"vector_census_comparison_count", event.ConfirmedKindVectorScopeComparisonCount,
			"vector_census_rival_count_above_tau", event.ConfirmedKindVectorScopeRivalCountAboveTau,
			"vector_census_snapshot_stable", event.ConfirmedKindVectorScopeSnapshotStable,
			"vector_census_duration_ms", event.ConfirmedKindVectorScopeDurationMS)
	case "low_population_kind_scope":
		// CHAOS-4417: the operator-visible half of the PRE-CONFIRMATION
		// kind-scoped rescue -- see chaos4417_low_population_kind_scope.go's
		// own doc comment. One event per chaos4417LowPopulationScopedKinds
		// member attempted this resolution; "kind" disambiguates which one
		// (a closed-vocabulary contextfabric.SubjectKind value -- never a
		// term or label), "state" is the SAME closed vocabulary
		// ConfirmedKindScopeState carries (this mechanism reuses
		// buildConfirmedKindScopedSnapshot), "candidate_count" is that
		// attempt's own isolated snapshot size (0 whenever state !=
		// "complete").
		t.logger.DebugContext(ctx, "context fabric resolution trace: low population kind scope",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"kind", contextfabric.SanitizeLogAttr(event.LowPopulationKindScopeKind),
			"state", contextfabric.SanitizeLogAttr(event.LowPopulationKindScopeState),
			"candidate_count", event.LowPopulationKindScopeCandidateCount,
			// outcome (codex R1 P2, CHAOS-4417): populated ONLY on the
			// rescue's own summary event (empty "kind") -- see
			// LowPopulationKindScopeOutcome's own doc comment.
			"outcome", contextfabric.SanitizeLogAttr(event.LowPopulationKindScopeOutcome))
	case "identity_universe":
		// Rig-visibility fix: a single emission site
		// (falkorgraph/reader.go), once per resolution's identity read --
		// safe to promote straight to Info.
		t.logger.InfoContext(ctx, "context fabric resolution trace: identity universe read",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"complete", event.IdentityUniverseComplete)
	case "identity_gate":
		// Measured retrieval-pool-sized (90 events on a
		// 90-Repository-candidate crowd -- identity_gate fires per
		// alias-lookup-scoped candidate NodeCandidate builds, unbounded by
		// this event's own gating and scaling with however many
		// Repository/Project/Team candidates the pool holds) -- this
		// per-candidate line STAYS Debug. IdentityGateSummary (emitted by
		// identityGateSummaryBuffer, resolve.go, once per
		// ResolveSubjectsWithCommitBasis call) is the folded Info line an
		// operator actually gets; see
		// ResolutionTraceEvent.IdentityGateSummary's own doc comment for
		// the full rule.
		if event.IdentityGateSummary {
			t.logger.InfoContext(ctx, "context fabric resolution trace: identity gate summary",
				"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
				"candidate_count", event.IdentityGateCandidateCount,
				"fired_count", event.IdentityGateFiredCount,
				"fired_ids", contextfabric.SanitizeLogStrings(event.IdentityGateFiredIDs))
			return
		}
		t.logger.DebugContext(ctx, "context fabric resolution trace: identity gate",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"subject_kind", contextfabric.SanitizeLogAttr(string(event.Subject.Kind)), "subject_canonical_id", contextfabric.SanitizeLogAttr(event.Subject.CanonicalID),
			"from_keyed_identity_lookup", event.FromKeyedIdentityLookup, "eligible_kind", event.EligibleKind,
			"alias_matched", event.AliasMatched, "provider_matched", event.ProviderMatched,
			"gate_fired", event.GateFired, "final_confidence", event.FinalConfidence)
	case "evidence_round":
		// CHAOS-3899 (design brief v5 §5/§6 Slice A): the shadow evidence
		// round's own per-resolution outcome -- SUPPRESSED from any
		// commit-path decision this slice, logged for measurement only.
		// Content-safe: ShadowDIdentityHash is a SHA-256, never handle/
		// anchor text; every other field is a count/enum/bool.
		t.logger.InfoContext(ctx, "context fabric resolution trace: evidence round (shadow)",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"shadow_outcome", contextfabric.SanitizeLogAttr(event.ShadowOutcome), "shadow_reason", contextfabric.SanitizeLogAttr(event.ShadowReason),
			"shadow_d_identity_hash", contextfabric.SanitizeLogAttr(event.ShadowDIdentityHash),
			"shadow_precondition_unproven", event.ShadowPreconditionUnproven,
			"shadow_unscoped_visibility", event.ShadowUnscopedVisibility,
			"shadow_non_censused_survivor", event.ShadowNonCensusedSurvivor,
			"shadow_handle_grammar_bound", event.ShadowHandleGrammarBound,
			"shadow_anchor_unique_claimant", event.ShadowAnchorUniqueClaimant,
			"shadow_anchor_receipt_confirmed", event.ShadowAnchorReceiptConfirmed,
			"shadow_kinds_censused", event.ShadowKindsCensused,
			"shadow_kind_insensitivity_evaluated", event.ShadowKindInsensitivityEvaluated,
			"shadow_kind_insensitivity_outcome", contextfabric.SanitizeLogAttr(event.ShadowKindInsensitivityOutcome),
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
			"shadow_kind_insensitivity_mode", contextfabric.SanitizeLogAttr(event.ShadowKindInsensitivityMode),
			// CHAOS-4081 (codex R1, Medium, confirmed): the handle member's
			// own probe outcome MUST reach production logs on this same
			// event, exactly like shadow_kind_insensitivity_* immediately
			// above -- omitting it left this test-visible-only, so a
			// production log consumer had no way to observe the gap
			// CHAOS-4081 exists to make OBSERVABLE. Same closed-vocabulary
			// discipline: no free text, count/enum/bool only.
			"shadow_handle_insensitivity_evaluated", event.ShadowHandleInsensitivityEvaluated,
			"shadow_handle_insensitivity_outcome", contextfabric.SanitizeLogAttr(event.ShadowHandleInsensitivityOutcome),
			// CHAOS-4300: which resolution path produced this round -- see
			// ResolutionTraceEvent.ShadowCallerHintShortCircuit's own doc
			// comment. A plain bool, no more sensitive than any other flag
			// on this line.
			"shadow_caller_hint_short_circuit", event.ShadowCallerHintShortCircuit)
	case "evidence_probe":
		// CHAOS-3899: ONE per-kind census receipt (brief §1.3(3), "Per-kind,
		// never aggregated across kinds").
		t.logger.InfoContext(ctx, "context fabric resolution trace: evidence probe (shadow census)",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"census_kind", contextfabric.SanitizeLogAttr(string(event.CensusKind)), "census_complete", event.CensusComplete,
			"census_count", event.CensusCount, "census_read_at_unix", event.CensusReadAtUnix,
			"census_protocol", contextfabric.SanitizeLogAttr(event.CensusProtocol), "census_closure_mismatch", event.CensusClosureMismatch,
			"census_statement_count", event.CensusStatementCount, "census_rows_read", event.CensusRowsRead,
			"census_handle_applied", event.CensusHandleApplied, "census_anchor_applied", event.CensusAnchorApplied,
			// CHAOS-4300: same tag as the sibling evidence_round event.
			"shadow_caller_hint_short_circuit", event.ShadowCallerHintShortCircuit)
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
		t.logger.InfoContext(ctx, "context fabric resolution trace: evidence census commit",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"subject_kind", contextfabric.SanitizeLogAttr(string(event.Subject.Kind)), "subject_canonical_id", contextfabric.SanitizeLogAttr(event.Subject.CanonicalID),
			"outcome", contextfabric.SanitizeLogAttr(event.Outcome), "graph_existence_ok", event.GraphExistenceOK,
			"census_commit_reason", contextfabric.SanitizeLogAttr(event.CensusCommitReason))
	case "evidence_source_native":
		// CHAOS-3918 (chris-ratified pre-registered shadow measurement,
		// 2026-08-19; codex xhigh review finding, confirmed and fixed:
		// this case was missing entirely, so the widening measurement's
		// whole payload fell to the "unknown stage" branch below and was
		// silently discarded in production -- the same defect class
		// evidence_census_commit above was already fixed for). Content-safe:
		// both non-request-id/stage fields are a count and a bool.
		// request_id/stage pass through contextfabric.SanitizeLogAttr --
		// CHAOS-5544, the one recognized barrier (CodeQL go/log-injection).
		t.logger.InfoContext(ctx, "context fabric resolution trace: evidence source native (shadow widening)",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
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
		// through contextfabric.SanitizeLogAttr -- CHAOS-5544, the one
		// recognized barrier (CodeQL go/log-injection): a static analyzer cannot credit "this string
		// is registry-constant by construction" the way a human review can.
		// STAYS Debug: an adversarial review round reproduced this as
		// genuinely retrieval-pool-sized (90 Info lines from 45 grammar
		// matches -- "ONE per-match receipt" is not the small count its own
		// doc comment implies). The sibling "evidence_source_native" event
		// just above ALREADY carries the bounded aggregate an operator
		// needs (source_native_match_count/source_native_any_resolved,
		// exactly once per call) -- reverted rather than folded, since that
		// existing sibling event already IS this stage's own summary in
		// substance, just under a different token.
		t.logger.DebugContext(ctx, "context fabric resolution trace: evidence source native probe (shadow widening)",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"source_native_grammar", contextfabric.SanitizeLogAttr(event.ShadowSourceNativeGrammar),
			"source_native_resolved", event.ShadowSourceNativeResolved,
			"source_native_kind", contextfabric.SanitizeLogAttr(string(event.ShadowSourceNativeKind)))
	case "slice_b_survivor_verdict":
		// CHAOS-4088: SurvivorsFirstOrder's own candidateSurvivorVerdict,
		// traced for the first time -- see ResolutionTraceEvent.SurvivorVerdict's
		// own doc comment for the diagnostic gap this closes and the
		// "silence means never reached, not everything neutral" contract.
		// Content-safe: Subject is the graph's own stable kind+canonical_id,
		// SurvivorVerdict is the closed "neutral"/"eliminated" vocabulary.
		//
		// This per-candidate line is bounded by the FINAL
		// candidate list, which the scale ruling treats as unbounded
		// (MaxSubjectCandidates carries no ceiling of its own) -- STAYS
		// Debug. SurvivorVerdictSummary (emitted by SurvivorsFirstOrder
		// itself, chaos3896_slice_b_presentation.go) is the folded Info
		// line; see ResolutionTraceEvent.SurvivorVerdictSummary's own doc
		// comment for the full rule.
		if event.SurvivorVerdictSummary {
			t.logger.InfoContext(ctx, "context fabric resolution trace: slice b survivor verdict summary",
				"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
				"candidate_count", event.SurvivorVerdictCandidateCount,
				"neutral_count", event.SurvivorVerdictNeutralCount,
				"eliminated_count", event.SurvivorVerdictEliminatedCount,
				"eliminated_ids", contextfabric.SanitizeLogStrings(event.SurvivorVerdictEliminatedIDs))
			return
		}
		t.logger.DebugContext(ctx, "context fabric resolution trace: slice b survivor verdict",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage),
			"subject_kind", contextfabric.SanitizeLogAttr(string(event.Subject.Kind)), "subject_canonical_id", contextfabric.SanitizeLogAttr(event.Subject.CanonicalID),
			"survivor_verdict", contextfabric.SanitizeLogAttr(event.SurvivorVerdict))
	default:
		t.logger.DebugContext(ctx, "context fabric resolution trace: unknown stage",
			"request_id", contextfabric.SanitizeLogAttr(event.RequestID), "stage", contextfabric.SanitizeLogAttr(event.Stage))
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
		"request_id", contextfabric.SanitizeLogAttr(string(requestID)),
		"subject_key", contextfabric.SanitizeLogAttr(subjectKey),
		"mechanism", contextfabric.SanitizeLogAttr(string(node.Mechanism)),
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
