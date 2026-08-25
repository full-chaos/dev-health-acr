package contextfabric

import (
	"context"
	"log/slog"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// requestIDLogAttrs (CHAOS-3888) returns the "request_id" slog attribute
// pair for ctx's observability request id, or nil when ctx carries none
// (any caller not reached through the HTTP request-id middleware, e.g. most
// existing unit tests) -- observability.RequestIDFromContext already
// existed; nothing in this package read it before this ticket. Spread into
// a log call via `attrs...` so a request-id-less ctx adds nothing to the
// line rather than logging an empty string.
func requestIDLogAttrs(ctx context.Context) []any {
	if requestID, ok := observability.RequestIDFromContext(ctx); ok {
		return []any{"request_id", string(requestID)}
	}
	return nil
}

// SlogEngineTelemetry is the production EngineTelemetry: every counter
// becomes one structured log line, content-safe by construction (org_id
// plus counts/booleans only -- never question text, subject labels,
// canonical IDs, or result IDs, matching EngineTelemetry's own doc
// comment). It is the first production implementation of this interface;
// composition previously left EngineTelemetry nil.
//
// AC-3782-8 (the reuse rate and the saved model-call count are recorded):
// both are derived entirely from the RecordAnswerReuse log line's
// "outcome" field -- a log aggregation query counts outcome="hit" events
// for the saved-call count, and hits / total for the rate. The non-hit
// outcome values split WHY a call missed (authorization vs. evidence
// containment vs. no candidate at all), so a cratered rate is
// diagnosable from this one stream; see RecordAnswerReuse's doc comment
// on EngineTelemetry.
type SlogEngineTelemetry struct {
	logger *slog.Logger
}

// NewSlogEngineTelemetry builds a SlogEngineTelemetry. A nil logger falls
// back to slog.Default(), matching observability.NewSlogSink's
// convention.
func NewSlogEngineTelemetry(logger *slog.Logger) SlogEngineTelemetry {
	if logger == nil {
		logger = slog.Default()
	}
	return SlogEngineTelemetry{logger: logger}
}

func (t SlogEngineTelemetry) RecordPriorSubjectReceiptsSkipped(ctx context.Context, principal storage.Principal, skipped int) {
	if skipped <= 0 {
		return
	}
	// CHAOS-3888: request_id appended when ctx carries one (see
	// requestIDLogAttrs' own doc comment) -- observability.RequestIDFromContext
	// existed before this ticket but nothing in this package read it, so
	// this line and RecordAnswerReuse's below were not request-correlatable.
	args := append([]any{"org_id", principal.OrgID, "skipped_count", skipped}, requestIDLogAttrs(ctx)...)
	t.logger.WarnContext(ctx, "context fabric prior-subject receipts skipped", args...)
}

// RecordCommitAffirmationRetraction implements CommitAffirmationTelemetry
// (CHAOS-4085) -- the ONE operator-visible record that the commit gate
// removed a subject from an answer.
//
// It exists because it did not, and that was a hole: CommitAffirmationTelemetry
// is an OPTIONAL interface, recordCommitAffirmation type-asserts for it, and
// until this method landed NOTHING in production implemented it. Every
// retraction therefore failed the assertion and vanished silently -- the gate
// could refuse a commit and no operator could observe that it had. Found by
// the codex retroactive pass over the trace commit; it is the same class
// CHAOS-4089 exists to prevent, which is why it is fixed rather than
// ticketed.
//
// Content-safe by construction, exactly like every method beside it: an org
// id, two closed contract enums (the commit basis and the subject KIND --
// never a canonical id or a label), one mechanism enum, and two counts.
// Nothing here is high-cardinality and nothing identifies the subject that
// was retracted; a reader who needs that correlates by request_id with the
// resolution trace, which is where subject identity legitimately lives.
//
// WARN, not INFO: a retraction means the system found a candidate and then
// declined to stand behind it. That is a normal, designed outcome rather
// than an error, but a RATE change in it is exactly the signal that
// retrieval quality or synthesis grounding has moved -- and the DP9 bar
// makes that worth surfacing above the reuse-outcome stream.
func (t SlogEngineTelemetry) RecordCommitAffirmationRetraction(ctx context.Context, principal storage.Principal, outcome CommitAffirmationOutcome) {
	args := append([]any{
		"org_id", principal.OrgID,
		"commit_basis", string(outcome.Basis),
		"subject_kind", string(outcome.SubjectKind),
		"winning_mechanism", outcome.WinningMechanism,
		"provisional_committed", outcome.ProvisionalCommitted,
		"final_committed", outcome.FinalCommitted,
	}, requestIDLogAttrs(ctx)...)
	t.logger.WarnContext(ctx, "context fabric commit affirmation retraction", args...)
}

// RecordSynthesisStatusOverride implements EngineTelemetry (CHAOS-4098) --
// the ONE operator-visible record that the engine served a different
// investigation status than the synthesis step returned.
//
// Content-safe by construction, exactly like every method beside it: an org
// id, three closed vocabularies (two contract status enums and the override
// reason) and one count. Nothing here is high-cardinality, nothing
// identifies a subject, and no model output reaches it; a reader who needs
// the answer itself correlates by request_id.
//
// WARN, not INFO: an override means the model produced a status ACR cannot
// serve, and before CHAOS-4098 that combination FAILED the whole
// investigation with a 500. It is now handled rather than fatal, but a rate
// change in it is a direct signal about synthesis-prompt compliance, and it
// sits at the same level as the commit-gate retraction it runs beside.
func (t SlogEngineTelemetry) RecordSynthesisStatusOverride(ctx context.Context, principal storage.Principal, outcome SynthesisStatusOverrideOutcome) {
	args := append([]any{
		"org_id", principal.OrgID,
		"from_status", string(outcome.From),
		"to_status", string(outcome.To),
		"reason", string(outcome.Reason),
		"committed_count", outcome.CommittedCount,
	}, requestIDLogAttrs(ctx)...)
	t.logger.WarnContext(ctx, "context fabric synthesis status override", args...)
}

func (t SlogEngineTelemetry) RecordAnswerReuse(ctx context.Context, principal storage.Principal, outcome AnswerReuseOutcome) {
	args := append([]any{"org_id", principal.OrgID, "outcome", string(outcome)}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric answer reuse outcome", args...)
}

// RecordSubjectlessTerminal logs at Info: the classification itself
// (empty_pool/authz_filtered_to_empty/ambiguous) is diagnostic detail about
// an already-ordinary outcome (no_match/clarification_required), never a
// sign anything is broken.
func (t SlogEngineTelemetry) RecordSubjectlessTerminal(ctx context.Context, principal storage.Principal, reason string) {
	args := append([]any{"org_id", principal.OrgID, "reason", reason}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric subjectless terminal", args...)
}

// RecordPriorSubjectReceiptSkipReason logs at Info: a per-reason breakdown
// of an already-reported RecordPriorSubjectReceiptsSkipped aggregate, not a
// new failure signal of its own. epochDelta (CHAOS-3898 P2 fix-forward) is
// logged only for reason=="stale_graph_epoch" -- see the interface method's
// own doc comment for why it is 0, and therefore omitted, for every other
// reason.
func (t SlogEngineTelemetry) RecordPriorSubjectReceiptSkipReason(ctx context.Context, principal storage.Principal, reason string, count int, epochDelta int64) {
	if count <= 0 {
		return
	}
	args := []any{"org_id", principal.OrgID, "reason", reason, "count", count}
	if reason == "stale_graph_epoch" {
		args = append(args, "epoch_delta", epochDelta)
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric prior-subject receipt skip reason", args...)
}

// RecordAnswerReuseServedRequestID logs at Info -- a mismatch is the
// ORDINARY case for a reuse hit (the whole point of reuse is serving an
// EARLIER call's answer for a later, differently-request-id'd one), never
// itself a sign anything is wrong, so this is diagnostic correlation
// detail, not a warning condition. request ids are opaque,
// randomly-generated operational identifiers (observability.RequestIDGenerator),
// never subject/question content, so both are safe to log directly, exactly
// like org_id already is throughout this file.
func (t SlogEngineTelemetry) RecordAnswerReuseServedRequestID(ctx context.Context, principal storage.Principal, servedRequestID string, requestIDMismatch bool) {
	args := append([]any{"org_id", principal.OrgID, "served_request_id", servedRequestID, "request_id_mismatch", requestIDMismatch}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric answer reuse served a stored result's own request id", args...)
}

// RecordBindingEpochDelta is CHAOS-3898 §5b's flip_during_investigation/
// cf_binding_epoch_delta pair -- see EngineTelemetry's own doc comment.
// flipped=false (the ordinary case: no build/flip happened mid-investigation)
// logs at Debug; flipped=true logs at Info -- worth an operator's attention
// (grace-window/cache-lease tuning data), never itself an error condition.
func (t SlogEngineTelemetry) RecordBindingEpochDelta(ctx context.Context, principal storage.Principal, flipped bool, delta int64) {
	args := append([]any{"org_id", principal.OrgID, "flip_during_investigation", flipped, "binding_epoch_delta", delta}, requestIDLogAttrs(ctx)...)
	if flipped {
		t.logger.InfoContext(ctx, "context fabric investigation's graph epoch moved between binding resolution and save", args...)
		return
	}
	t.logger.DebugContext(ctx, "context fabric investigation's graph epoch unchanged between binding resolution and save", args...)
}

// RecordWindowBinderOutcome logs at Info: the closed WindowBindReason
// vocabulary is diagnostic (how often does the proposal-only temporal
// binder route a question), never itself a sign anything is wrong.
func (t SlogEngineTelemetry) RecordWindowBinderOutcome(ctx context.Context, principal storage.Principal, reason WindowBindReason) {
	args := append([]any{"org_id", principal.OrgID, "reason", string(reason)}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric window binder outcome", args...)
}

// RecordWindowCanonicalization logs at Info: the closed
// WindowCanonicalizationOutcome vocabulary lets an operator tell how often
// investigations carry a stated/confirmed/inferred/no window apart from how
// often a window confirmation is vetoed -- the latter (veto_unresolved/
// veto_conflict) is worth watching, but is reported through the same
// closed-enum stream as every other outcome, exactly like
// RecordAnswerReuse's own miss-reason split.
func (t SlogEngineTelemetry) RecordWindowCanonicalization(ctx context.Context, principal storage.Principal, outcome WindowCanonicalizationOutcome) {
	args := append([]any{"org_id", principal.OrgID, "outcome", string(outcome)}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric window canonicalization outcome", args...)
}

// RecordStructureNeedsDisclosed (CHAOS-3900 P1.F). member is a closed
// StructureNeedKind enum value -- content-safe by construction, never
// question text or a subject identifier.
func (t SlogEngineTelemetry) RecordStructureNeedsDisclosed(ctx context.Context, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind) {
	args := append([]any{"org_id", principal.OrgID, "member", string(member)}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric structure needs disclosed", args...)
}

// RecordStructureOfferCount (CHAOS-3900 P1.F). member/source are both
// closed enums; count is a plain integer -- the full event is
// counts/enums only, never an offer's own label/value/canonical_id.
func (t SlogEngineTelemetry) RecordGatedOfferResolution(ctx context.Context, principal storage.Principal, outcome GatedOfferResolutionOutcome) {
	args := append([]any{"org_id", principal.OrgID, "outcome", string(outcome)}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric gated offer resolution", args...)
}

func (t SlogEngineTelemetry) RecordStructureOfferCount(ctx context.Context, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind, source contractsv1.ContextFabricStructureOfferSource, count int) {
	args := append([]any{"org_id", principal.OrgID, "member", string(member), "source", string(source), "count", count}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric structure offer count", args...)
}

// RecordStructureReceipt (CHAOS-3900 P1.F). member/outcome are both closed
// enums -- see StructureReceiptOutcome's own doc comment (structure.go)
// for the three-value vocabulary and its atomicity guarantee.
func (t SlogEngineTelemetry) RecordStructureReceipt(ctx context.Context, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind, outcome StructureReceiptOutcome) {
	args := append([]any{"org_id", principal.OrgID, "member", string(member), "outcome", string(outcome)}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric structure receipt", args...)
}

// RecordStructureExplicit (CHAOS-3972 P3) mirrors RecordStructureReceipt's
// own logging shape exactly, for the explicit (non-receipt) structure
// fields.
func (t SlogEngineTelemetry) RecordStructureExplicit(ctx context.Context, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind, outcome StructureExplicitOutcome) {
	args := append([]any{"org_id", principal.OrgID, "member", string(member), "outcome", string(outcome)}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric structure explicit", args...)
}

// RecordPriorConsulted (CHAOS-3977 P5). member/outcome are both closed
// enums -- see PriorConsultedOutcome's own doc comment (priors.go).
func (t SlogEngineTelemetry) RecordPriorConsulted(ctx context.Context, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind, outcome PriorConsultedOutcome) {
	args := append([]any{"org_id", principal.OrgID, "member", string(member), "outcome", string(outcome)}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric prior consulted", args...)
}

// RecordPriorDegradation (CHAOS-3977 P5) logs at Warn for
// PriorDegradationPointerDangling (design brief §3.4: "additionally raises
// an operator signal because it means a retire outran its grace") and at
// Info for every other state -- an ordinary, expected degrade-and-continue
// outcome, never itself a sign anything is broken.
func (t SlogEngineTelemetry) RecordPriorDegradation(ctx context.Context, principal storage.Principal, state PriorDegradationState) {
	args := append([]any{"org_id", principal.OrgID, "state", string(state)}, requestIDLogAttrs(ctx)...)
	if state == PriorDegradationPointerDangling {
		t.logger.WarnContext(ctx, "context fabric prior consultation degraded: active version pointer names a missing snapshot", args...)
		return
	}
	t.logger.InfoContext(ctx, "context fabric prior consultation degraded", args...)
}

// RecordOfferPhrasing implements EngineTelemetry (CHAOS-4171 PR2). outcome
// is the closed OfferPhrasingOutcome enum -- content-safe by construction,
// never the phrasing text itself or a structural Label.
func (t SlogEngineTelemetry) RecordOfferPhrasing(ctx context.Context, principal storage.Principal, outcome OfferPhrasingOutcome) {
	args := append([]any{"org_id", principal.OrgID, "outcome", string(outcome)}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric offer phrasing outcome", args...)
}

// RecordFactScopeExpansion implements EngineTelemetry (CHAOS-4099) -- the
// ONE operator-visible record of whether a fact family could be reached from
// the subjects an investigation resolved.
//
// Content-safe by construction, exactly like every method beside it: an org
// id, six closed vocabularies (two contract subject/fact kinds, the policy
// name, the basis, the outcome and the failure class), seven counts, one
// boolean, and (CHAOS-4101) one closed-vocabulary count map keyed by
// work_item_team_attributions' own source enum -- native_team through
// manual_fallback, never a team/repository identity. Nothing here is
// high-cardinality, nothing identifies a subject, and no model output
// reaches it; a reader who needs to know WHICH project correlates by
// request_id with the resolution trace.
//
// EVERY FIELD IS LOGGED, unconditionally, including the zero-valued counts.
// That is the CHAOS-4085 lesson applied directly: the fields existed on the
// struct and were populated, and none of it reached an operator because the
// sink did not write them. Omitting a count because it happens to be zero
// would also make "the filter dropped nothing" and "nobody ever counted"
// indistinguishable in a log aggregator, which is exactly the ambiguity
// MissingNextHopCount exists to resolve for the zero-UUID sentinel.
//
// LEVEL SPLIT, on whether the answer was actually degraded. An `expanded` or
// `attempted_empty` outcome is the system working -- Info. A
// policy_unavailable, expanded_partial or failed outcome means the caller
// received an answer with a hole in it, and a RATE change in those is the
// signal that a policy is still dark, a cap is being hit, or the traversal
// backend is sick -- Warn, alongside the commit-gate retraction and the
// synthesis-status override.
func (t SlogEngineTelemetry) RecordFactScopeExpansion(ctx context.Context, principal storage.Principal, event FactScopeExpansionEvent) {
	args := append([]any{
		"org_id", principal.OrgID,
		"requirement_kind", string(event.RequirementKind),
		"origin_kind", string(event.OriginKind),
		"target_kind", string(event.TargetKind),
		"policy", string(event.Policy),
		"basis", string(event.Basis),
		// axis (CHAOS-4109) is the decision-basis signal for as-of scope
		// expansion: Axis != current with a non-policy_unavailable Outcome
		// means "as-of resolution applied"; Axis != current WITH
		// policy_unavailable means the observed_time gate (still closed --
		// see resolveRequirement's own comment); temporal_dropped_count > 0
		// on a historical axis names an "interval-miss" (a candidate the
		// current-value column would have matched, excluded because no
		// interval covered the requested window); unbounded_validity_count
		// > 0 names a "fell back to current" admission (no transition
		// history existed for that candidate at all, so it was admitted
		// unconditionally rather than through a genuine as-of resolution).
		"axis", string(event.Axis),
		"outcome", string(event.Outcome),
		"origin_count", event.OriginCount,
		"candidate_count", event.CandidateCount,
		"admitted_count", event.AdmittedCount,
		"authorization_dropped_count", event.AuthorizationDroppedCount,
		"temporal_dropped_count", event.TemporalDroppedCount,
		"unbounded_validity_count", event.UnboundedValidityCount,
		"malformed_touch_count", event.MalformedTouchCount,
		"missing_next_hop_count", event.MissingNextHopCount,
		"target_kind_mismatch_count", event.TargetKindMismatchCount,
		"truncated", event.Truncated,
		"failure_class", string(event.FailureClass),
		// AttributionSourceCounts (CHAOS-4101): closed-vocabulary source
		// breakdown for a team-origin expansion, nil for every project-origin
		// one. Logged unconditionally, nil included, for the SAME reason
		// every zero count above is: "the filter dropped nothing" and
		// "nobody ever counted" must stay distinguishable in a log
		// aggregator.
		"attribution_source_counts", event.AttributionSourceCounts,
	}, requestIDLogAttrs(ctx)...)
	if factScopeGapDegrades(event.Outcome) {
		t.logger.WarnContext(ctx, "context fabric fact scope expansion left a gap", args...)
		return
	}
	t.logger.InfoContext(ctx, "context fabric fact scope expansion outcome", args...)
}
