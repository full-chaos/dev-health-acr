package contextfabric

import (
	"context"
	"log/slog"
	"sort"
	"strconv"

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

// AnswerReuseContainmentEvent is one reuse attempt's containment
// measurement -- see EngineTelemetry.RecordAnswerReuseContainment for why
// this is a measurement rather than another outcome label. Every field is
// a COUNT or a closed label; no reference ids, subject labels or question
// text ever ride here, matching this file's standing rule that telemetry
// is corpus-safe by construction.
type AnswerReuseContainmentEvent struct {
	// DemandedCount is how many distinct references the stored payload
	// would serve, and therefore how many the recheck had to prove.
	DemandedCount int
	// VisibleCount is how many distinct references the fresh discovery
	// proved this caller can see right now.
	VisibleCount int
	// MissingCount is how many demanded references were not proven.
	MissingCount int
	// MissingCitation reports whether any unproven reference was a
	// TOP-LEVEL citation -- the one condition that refuses reuse outright
	// rather than degrading it.
	MissingCitation bool
	// StrippedRefs is how many references the degrade removed from the
	// served copy. Zero on a clean hit and on a refusal.
	StrippedRefs int
	// StrippedLabels is how many display-label entries the degrade's
	// rebuild dropped. Reported separately because a label entry is a
	// second way the same reference reaches a caller -- a strip that
	// cleared every list and left the labels behind removed nothing.
	StrippedLabels int
	// The Dropped* counts are whole entries removed because stripping
	// their references left them invalid under the contract.
	DroppedCandidates int
	DroppedMembers    int
	DroppedDrivers    int
	DroppedFindings   int
	DroppedPaths      int
	// Disclosure names which form the coverage disclosure took
	// ("structured" or "reason_only"); empty when nothing was stripped.
	// A legacy payload that can only carry the composed string is a real
	// difference in what a consumer can key on, so it is reported rather
	// than silently taken.
	Disclosure string
}

// RecordAnswerReuseContainment logs at Info. A degraded serve is an
// ORDINARY outcome under the ruled remedy, not a fault -- but it is never
// silent: an answer narrower than the one stored is exactly the thing an
// operator must be able to see without reading response bodies.
func (t SlogEngineTelemetry) RecordAnswerReuseContainment(ctx context.Context, principal storage.Principal, event AnswerReuseContainmentEvent) {
	args := []any{
		"org_id", principal.OrgID,
		"demanded_refs", event.DemandedCount,
		"visible_refs", event.VisibleCount,
		"missing_refs", event.MissingCount,
		"missing_citation", event.MissingCitation,
		"stripped_refs", event.StrippedRefs,
		"stripped_labels", event.StrippedLabels,
		"dropped_candidates", event.DroppedCandidates,
		"dropped_members", event.DroppedMembers,
		"dropped_drivers", event.DroppedDrivers,
		"dropped_findings", event.DroppedFindings,
		"dropped_paths", event.DroppedPaths,
	}
	if event.Disclosure != "" {
		args = append(args, "disclosure", event.Disclosure)
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric answer reuse containment", args...)
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

// RecordWindowCarry (CHAOS-4360) logs at Info: outcome/chain_depth are both
// closed-vocabulary/plain-integer, content-safe by construction -- never a
// question, subject label, or canonical id. hit vs. every miss reason is
// this stream's own hit-rate denominator (RecordWindowCarry's own doc
// comment, engine.go, for why it fires only on the carry-eligible
// population).
func (t SlogEngineTelemetry) RecordWindowCarry(ctx context.Context, principal storage.Principal, outcome WindowCarryOutcome, chainDepth int) {
	args := append([]any{"org_id", principal.OrgID, "outcome", string(outcome), "chain_depth", chainDepth}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric window carry", args...)
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

// RecordCohortStructureGate (CHAOS-4579/CHAOS-4531). outcome and shape are
// both closed enums -- content-safe by construction, never question text,
// a subject identifier, or an offer label. One event per
// GateSubjectAxisOffers call, which is NOT one per composed StructureNeeds
// -- see the interface method's own doc comment (engine.go) for the exact
// denominator and the two directions it differs in.
func (t SlogEngineTelemetry) RecordCohortStructureGate(ctx context.Context, principal storage.Principal, outcome CohortStructureGateOutcome, shape InvestigationShape) {
	args := append([]any{"org_id", principal.OrgID, "outcome", string(outcome), "shape", string(shape)}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric cohort structure gate", args...)
}

// RecordWindowGateOfferDisclosure (CHAOS-4314) logs at Info: offered is the
// window_gated_offered/window_gated_silent split's own producer signal.
func (t SlogEngineTelemetry) RecordWindowGateOfferDisclosure(ctx context.Context, principal storage.Principal, offered bool) {
	args := append([]any{"org_id", principal.OrgID, "offered", offered}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric window gate offer disclosure", args...)
}

// RecordWindowExpandOfferRedeemed (CHAOS-4314) logs at Info: no
// content-bearing field, a plain occurrence count exactly like
// RecordPriorSubjectReceiptsSkipped's own shape when skipped>0.
func (t SlogEngineTelemetry) RecordWindowExpandOfferRedeemed(ctx context.Context, principal storage.Principal) {
	args := append([]any{"org_id", principal.OrgID}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric window expand offer redeemed", args...)
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

// RecordProjectedRowsCount implements EngineTelemetry (CHAOS-4355) -- see
// that method's doc comment for the count/truncated meaning. Content-safe:
// an org id and two closed, non-identifying numbers.
func (t SlogEngineTelemetry) RecordProjectedRowsCount(ctx context.Context, principal storage.Principal, count int, truncated bool) {
	args := append([]any{"org_id", principal.OrgID, "rows_count", count, "truncated", truncated}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric projected rows count", args...)
}

// RecordProjectedRowsByFactKind implements EngineTelemetry (CHAOS-4418) --
// see that method's doc comment for why a claimed-but-zero kind still gets
// its own line. One record per kind (sorted for deterministic log order),
// not one line with the whole map, so a reader filtering by
// "rows_projected_by_fact_kind" AND fact_kind=metrics finds exactly the
// producer they are diagnosing without parsing a nested value.
// Content-safe: an org id, one closed FactKind vocabulary value, and one
// non-identifying count.
func (t SlogEngineTelemetry) RecordProjectedRowsByFactKind(ctx context.Context, principal storage.Principal, byKind map[FactKind]int) {
	kinds := make([]string, 0, len(byKind))
	for kind := range byKind {
		kinds = append(kinds, string(kind))
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		args := append([]any{"org_id", principal.OrgID, "fact_kind", kind, "rows_projected_by_fact_kind", byKind[FactKind(kind)]}, requestIDLogAttrs(ctx)...)
		t.logger.InfoContext(ctx, "context fabric projected rows count by fact kind", args...)
	}
}

// RecordDualTableFacts implements EngineTelemetry (CHAOS-4682, §5.1 P2) --
// see that method's doc comment for the two counts' meaning. Content-safe:
// an org id and two non-identifying counts.
func (t SlogEngineTelemetry) RecordDualTableFacts(ctx context.Context, principal storage.Principal, dualTableClaims, secondaryRowsBytes int) {
	args := append([]any{"org_id", principal.OrgID, "dual_table_claims", dualTableClaims, "secondary_rows_bytes", secondaryRowsBytes}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric dual table facts", args...)
}

// RecordModelRowsStripped implements EngineTelemetry (CHAOS-4355
// follow-up). Content-safe: an org id and one closed, non-identifying
// count -- never the stripped rows themselves.
func (t SlogEngineTelemetry) RecordModelRowsStripped(ctx context.Context, principal storage.Principal, claims int) {
	args := append([]any{"org_id", principal.OrgID, "cf_model_rows_stripped", claims}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric model-authored claimed fact rows stripped before validation", args...)
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
		"duplicate_add_count", event.DuplicateAddCount,
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

// RecordCohortRanked implements EngineTelemetry (CHAOS-4398). Content-safe:
// counts and a version string only, per CohortRankedEvent's own doc comment
// -- never a team name or a score value. Info level -- a ranked cohort is
// the system working, not a degradation; DegradedMemberCount is the signal
// an operator watches for a real data gap, and it travels as its own field
// rather than as a log level so a fully-degraded org does not get treated
// as an error.
func (t SlogEngineTelemetry) RecordCohortRanked(ctx context.Context, principal storage.Principal, event CohortRankedEvent) {
	t.logger.InfoContext(ctx, "context fabric cohort ranked", append([]any{
		"org_id", principal.OrgID,
		"member_count", event.MemberCount,
		"formula_version", event.FormulaVersion,
		"degraded_member_count", event.DegradedMemberCount,
		"signals_available", event.SignalsAvailable,
		// outcome_counts (CHAOS-4398 PR3, codex R1): a closed-vocabulary
		// count map (qualified/provisional/insufficient_evidence/
		// not_applicable), content-safe by the same reasoning as
		// signals_available above -- counts and enum keys only.
		"outcome_counts", event.OutcomeCounts,
	}, requestIDLogAttrs(ctx)...)...)
}

// RecordCohortDriverNarration implements EngineTelemetry (CHAOS-4398 PR3b,
// team-lead's standing order for this emission). Content-safe: a closed
// outcome enum and counts only, same discipline as RecordCohortRanked
// immediately above -- never a team name or narration prose. Info level
// for every outcome, including budget_exhausted/no_drivers: neither is a
// degradation of the answer (the Rows table/Score/RankingBasis already
// carry the ranking regardless of whether narration ran), it is an
// ordinary, expected shape an operator may still want to see the rate of.
func (t SlogEngineTelemetry) RecordCohortDriverNarration(ctx context.Context, principal storage.Principal, event CohortDriverNarrationEvent) {
	t.logger.InfoContext(ctx, "context fabric cohort driver narration", append([]any{
		"org_id", principal.OrgID,
		"outcome", string(event.Outcome),
		"judgments_emitted", event.JudgmentsEmitted,
		"facts_minted", event.FactsMinted,
		"members_narrated", event.MembersNarrated,
		"members_skipped_no_evidence", event.MembersSkippedNoEvidence,
		// Closed-enum keys and counts only -- the reason a selected driver
		// was eliminated, never the signal's value or the member it
		// belonged to (codex R3, CHAOS-4448).
		"drivers_skipped", event.DriversSkipped,
		// answer_narrative_recomposed (CHAOS-4580): a bool, content-safe by
		// construction -- records whether the engine replaced the
		// pre-narration DirectJudgment/DeterministicAnswer for this
		// investigation, never the prose itself.
		"answer_narrative_recomposed", event.AnswerNarrativeRecomposed,
	}, requestIDLogAttrs(ctx)...)...)
}

// RecordEvidenceLabelFallback implements EngineTelemetry (CHAOS-4690 item
// 4). Content-safe: org id and one closed count -- never the unlabeled
// evidence ref or its entity-type segment (r2 F5; see the interface's own
// doc comment). Called only when count > 0, same gated-telemetry
// discipline as RecordModelRowsStripped above it on the interface.
func (t SlogEngineTelemetry) RecordEvidenceLabelFallback(ctx context.Context, principal storage.Principal, count int) {
	args := append([]any{"org_id", principal.OrgID, "cf_evidence_label_fallback", count}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric evidence ref label fell back to the generic label", args...)
}

// RecordCoverageDisclosurePhrasing implements EngineTelemetry (CHAOS-4690
// Commit F, design §4.2). Content-safe: org id, the closed outcome enum,
// and two counts -- never a detail_id, a phrasing's text, or a Label. Info
// level for every outcome, including rejected_by_guard/
// discarded_undecodable: neither degrades the served answer (every detail
// still ships Label-only), it is an ordinary, expected shape an operator
// may still want the rate of.
func (t SlogEngineTelemetry) RecordCoverageDisclosurePhrasing(ctx context.Context, principal storage.Principal, outcome CoverageDisclosureOutcome, violation CoverageDisclosureViolation, phrased, total int) {
	args := append([]any{
		"org_id", principal.OrgID,
		"outcome", string(outcome),
		"violation", string(violation),
		"phrased", phrased,
		"total", total,
	}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric coverage disclosure phrasing", args...)
}

// RecordCategoryFactComposition implements EngineTelemetry (CHAOS-4347) --
// the operator-visible record of a status-category requirement being
// expanded into a composed fact-kind set. Content-safe: two closed enums
// and a closed-enum slice, nothing else. Info level -- this is the system
// working (a requirement that would otherwise have pruned now reads real
// facts), not a degradation the way a fact-scope gap is.
func (t SlogEngineTelemetry) RecordCategoryFactComposition(ctx context.Context, principal storage.Principal, event CategoryFactCompositionEvent) {
	composedKinds := make([]string, 0, len(event.ComposedKinds))
	for _, kind := range event.ComposedKinds {
		composedKinds = append(composedKinds, string(kind))
	}
	args := append([]any{
		"org_id", principal.OrgID,
		"requirement_kind", string(event.RequirementKind),
		"subject_kind", string(event.SubjectKind),
		"composed_kinds", composedKinds,
	}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric status category fact composition", args...)
}

// RecordRenderShapeSelection implements EngineTelemetry (CHAOS-4415) -- see
// that method's doc comment for what it reports and why it fires even when
// nothing was selected.
//
// One line per selected shape and one per skipped rule, plus a summary line
// carrying the count, rather than one line with a nested value: a reader
// filtering on "render_shape_rule=cohort_driver_contribution" finds the
// decision they are diagnosing without parsing anything. The summary line
// always fires, so render_shapes_selected=0 is a positive statement that
// the rules ran and chose nothing -- never the absence of a log line, which
// is indistinguishable from the selector never having run.
//
// Content-safe: an org id, closed-vocabulary values, and non-identifying
// counts. No shape label, subject label, or plotted number is ever logged.
func (t SlogEngineTelemetry) RecordRenderShapeSelection(ctx context.Context, principal storage.Principal, event RenderShapeSelectionEvent) {
	base := func(extra ...any) []any {
		args := append([]any{"org_id", principal.OrgID, "question_shape", string(event.Shape)}, extra...)
		return append(args, requestIDLogAttrs(ctx)...)
	}
	// render_shape_accounting is CHAOS-4621's structural invariant made
	// DIAGNOSABLE FROM ARTIFACTS (acr/AGENTS.md): a selector that lost a
	// rule's outcome says so in the run's own log line, rather than being
	// discoverable only by re-reading source or re-running with
	// instrumentation added afterwards. "ok" on every healthy selection,
	// so the field is a positive statement and not merely the absence of
	// a complaint.
	accounting := "ok"
	if err := event.Accounted(); err != nil {
		accounting = "violated"
	}
	t.logger.InfoContext(ctx, "context fabric render shape selection",
		base("render_shapes_selected", len(event.Selected),
			"render_shape_rules_skipped", len(event.Skipped),
			"render_shape_members_truncated", event.MembersTruncated,
			"render_shape_trends_omitted", event.TrendsOmitted,
			"render_shape_accounting", accounting)...)
	for _, selection := range event.Selected {
		t.logger.InfoContext(ctx, "context fabric render shape selected", base(
			"render_shape_kind", string(selection.Kind),
			"render_shape_presentation", string(selection.Presentation),
			"render_shape_rule", string(selection.Rule),
			"render_shape_series", selection.SeriesCount,
			"render_shape_points", selection.PointCount,
		)...)
	}
	for _, skip := range event.Skipped {
		t.logger.InfoContext(ctx, "context fabric render shape rule skipped", base(
			"render_shape_rule", string(skip.Rule),
			"render_shape_skip_reason", string(skip.Reason),
		)...)
	}
}

// RecordQuestionFamilyResolution (CHAOS-4632 §4.3) logs at Info, once per
// investigation that reaches the family resolver -- including the
// unclassified and no-majority outcomes, because the denominator has to be
// countable.
//
// EVERY field on the event reaches this line. That is the whole point of
// the CHAOS-4085 sink discipline (see chaos4085_telemetry_sink_test.go's
// header): a field populated on a struct and never logged is not
// telemetry, it is a field. The per-sample rows are flattened into
// indexed keys rather than a nested object because slog's JSON handler has
// no group-per-element form, and an operator greppng
// `cf_family_sample_0_row` needs a key that exists.
func (t SlogEngineTelemetry) RecordQuestionFamilyResolution(ctx context.Context, principal storage.Principal, event QuestionFamilyResolutionEvent) {
	args := []any{
		"org_id", principal.OrgID,
		"family", string(event.Family),
		"source", string(event.Source),
		"ensemble_size", event.EnsembleSize,
		"downgraded_count", event.DowngradedCount,
		"consensus_field_divergence", event.ConsensusFieldDivergence,
		"family_version", event.FamilyVersion,
		"sample_families", sortedFamilyDistribution(event.SampleFamilies),
	}
	for i, sample := range event.Samples {
		prefix := "sample_" + strconv.Itoa(i) + "_"
		args = append(args,
			prefix+"shape", string(sample.Shape),
			prefix+"attempted_family", string(sample.AttemptedFamily),
			prefix+"resolved_family", string(sample.ResolvedFamily),
			prefix+"row", string(sample.Row),
			prefix+"incompatibility_reason", string(sample.Reason),
			prefix+"group_kind_set", sample.GroupKindSet,
			prefix+"scope_anchor_set", sample.ScopeAnchorSet,
		)
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric question family resolution", args...)
}

// RecordFrameValidation (CHAOS-4452 stage 2, §13.6) logs at Info, once per
// frame that reaches validation -- INCLUDING VALID ONES, because the
// denominator has to be countable. An event that fires only on failure
// makes "the validator never rejects anything" indistinguishable from
// "the validator never ran".
//
// EVERY field on the event reaches this line, per the CHAOS-4085 sink
// discipline: a field populated on a struct and never logged is not
// telemetry, it is a field. That bar is why `failed_invariant` and
// `failed_phase` are both here -- the same invariant id failing in a1
// versus a2 is two different investigations.
//
// There are no repair fields, because this slice has no repair path: a
// frame that fails validation is refused. They land with the bounded
// repair itself, so an operator never sees a `repair_attempted` key that
// could only ever read false.
func (t SlogEngineTelemetry) RecordFrameValidation(ctx context.Context, principal storage.Principal, event FrameValidationEvent) {
	args := []any{
		"org_id", principal.OrgID,
		"outcome", string(event.Outcome),
		"failed_invariant", string(event.FailedInvariant),
		"failed_phase", string(event.FailedPhase),
		"failure_detail", string(event.FailureDetail),
		"proposed_kind", string(event.ProposedKind),
		"proposed_goals", goalsLogValue(event.ProposedGoals),
		"derived_obligation_count", event.DerivedObligationCount,
		"widened_obligation_count", event.WidenedObligationCount,
		"shape_diverged", event.ShapeDiverged,
		"emitted_shape", string(event.EmittedShape),
		"derived_shape", string(event.DerivedShape),
		"frame_version", event.FrameVersion,
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric frame validation", args...)
}

// RecordPlanNarrowing (CHAOS-4636) emits one plan-narrowing decision.
//
// Closed enums and counts only -- no question text, no subject identifier,
// no group label. The three stages are separately named because they are
// separately diagnosable: stage 1 is precautionary (nothing measured yet),
// stage 2 bounds what synthesis is given, stage 3 reacts to a measurement
// that already failed. Collapsing them into one field would make "the plan
// was too generous" and "synthesis produced more than the headroom allowed"
// indistinguishable, which is the diagnosis an over-budget answer needs.
func (t SlogEngineTelemetry) RecordPlanNarrowing(ctx context.Context, principal storage.Principal, event PlanNarrowingEvent) {
	args := []any{
		"org_id", principal.OrgID,
		"family", string(event.Family),
		"family_version", event.FamilyVersion,
		"stage", string(event.Stage),
		"basis", string(event.Basis),
		// CHAOS-4809: whether that basis was REPORTED by a selection or
		// DEFAULTED by planStageBasis. Without it a reader must assume the
		// order named actually ran, and the same ticket is the proof that
		// assumption is unsafe.
		"basis_observed", event.BasisObserved,
		"before", event.Before,
		"after", event.After,
		"groups", event.Groups,
		"overrun", string(event.Overrun),
		"measured_items", event.MeasuredItems,
		"measured_bytes", event.MeasuredBytes,
		"max_items", event.MaxItems,
		"max_serialized_bytes", event.MaxSerializedBytes,
		"retry_attempted", event.RetryAttempted,
		"retry_fit", event.RetryFit,
		"retry_failed", event.RetryFailed,
		"refusal_planned", event.RefusalPlanned,
		"deadline_reserved", event.DeadlineReserved,
		"retry_declined", string(event.RetryDeclined),
		// CHAOS-4735 criterion 6: the continuation the refusal offered, as a
		// closed token. Empty unless a refusal was planned. Never free text
		// -- the field it replaces held an English sentence, which could not
		// be a log dimension without becoming unbounded-cardinality prose.
		"narrower_continuation_axis", string(event.NarrowerContinuationAxis),
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric plan narrowing", args...)
}

// RecordGroupedCohortCompleteness (CHAOS-4733) emits one grouped-cohort
// completeness fold: whether the pre-grouping cohort was truncated at
// discovery, how many resulting groups came out marked incomplete, and the
// final cohort-level flags. Closed enums and counts only, same discipline as
// RecordPlanNarrowing.
func (t SlogEngineTelemetry) RecordGroupedCohortCompleteness(ctx context.Context, principal storage.Principal, event GroupedCohortCompletenessEvent) {
	args := []any{
		"org_id", principal.OrgID,
		"family", string(event.Family),
		"pre_grouping_complete", event.PreGroupingComplete,
		"pre_grouping_truncated", event.PreGroupingTruncated,
		"group_count", event.GroupCount,
		"groups_marked_incomplete", event.GroupsMarkedIncomplete,
		"complete", event.Complete,
		"truncated", event.Truncated,
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric grouped cohort completeness", args...)
}

// RecordBudgetAssertion emits the FINAL budget assertion for one fresh result
// exit. Closed enums and counts only.
//
// measured_bytes_post_label is named for what it IS rather than for where it is
// taken: the decisive path also emits measured_bytes on its narrowing event,
// taken BEFORE the plan re-stamp and label composition, and the two fields
// existing side by side is what makes the delta those composers add observable
// in production instead of inferred from a listing.
func (t SlogEngineTelemetry) RecordBudgetAssertion(ctx context.Context, principal storage.Principal, event BudgetAssertionEvent) {
	args := []any{
		"org_id", principal.OrgID,
		"assert_stage", string(event.Stage),
		"fits", event.Fits,
		"overrun", string(event.Overrun),
		"measured_items", event.MeasuredItems,
		"measured_bytes_post_label", event.MeasuredBytesPostLabel,
		"max_items", event.MaxItems,
		"max_serialized_bytes", event.MaxSerializedBytes,
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric budget assertion", args...)
}
