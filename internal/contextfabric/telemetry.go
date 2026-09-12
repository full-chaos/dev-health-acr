package contextfabric

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"strings"

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
		// CHAOS-5544: sanitized before it becomes a log attribute -- see
		// SanitizeLogAttr's own doc comment (go/log-injection, CWE-117).
		return []any{"request_id", SanitizeLogAttr(string(requestID))}
	}
	return nil
}

// SlogEngineTelemetry is the production EngineTelemetry: every counter
// becomes one structured log line, content-safe by construction -- never
// question text or a subject label. The few fields that ARE
// request/stored-derived correlation handles (org_id, request ids, result
// and context ids) route through the one shared SanitizeLogAttr barrier
// rather than being excluded from this file, matching EngineTelemetry's
// own doc comment. It is the first production implementation of this
// interface; composition previously left EngineTelemetry nil.
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
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "skipped_count", skipped}, requestIDLogAttrs(ctx)...)
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
		"org_id", SanitizeLogAttr(principal.OrgID),
		"commit_basis", SanitizeLogAttr(string(outcome.Basis)),
		"subject_kind", SanitizeLogAttr(string(outcome.SubjectKind)),
		"winning_mechanism", SanitizeLogAttr(outcome.WinningMechanism),
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
		"org_id", SanitizeLogAttr(principal.OrgID),
		"from_status", SanitizeLogAttr(string(outcome.From)),
		"to_status", SanitizeLogAttr(string(outcome.To)),
		"reason", SanitizeLogAttr(string(outcome.Reason)),
		"committed_count", outcome.CommittedCount,
	}, requestIDLogAttrs(ctx)...)
	t.logger.WarnContext(ctx, "context fabric synthesis status override", args...)
}

func (t SlogEngineTelemetry) RecordAnswerReuse(ctx context.Context, principal storage.Principal, outcome AnswerReuseOutcome) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "outcome", SanitizeLogAttr(string(outcome))}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric answer reuse outcome", args...)
}

// RecordAnswerReuseBypass (CHAOS-4998) logs at Info under its OWN message,
// distinct from the reuse-outcome line above so the bypassed population can
// be counted apart from the attempted one rather than polluting that
// stream's hit-rate denominator. reason is a closed AnswerReuseBypassReason
// -- content-safe by construction, never question text, a subject label or
// a receipt id.
func (t SlogEngineTelemetry) RecordAnswerReuseBypass(ctx context.Context, principal storage.Principal, reason AnswerReuseBypassReason) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "reason", SanitizeLogAttr(string(reason))}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric answer reuse bypass", args...)
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
		"org_id", SanitizeLogAttr(principal.OrgID),
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
		args = append(args, "disclosure", SanitizeLogAttr(event.Disclosure))
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric answer reuse containment", args...)
}

// RecordSubjectlessTerminal logs at Info: the classification itself
// (empty_pool/authz_filtered_to_empty/ambiguous) is diagnostic detail about
// an already-ordinary outcome (no_match/clarification_required), never a
// sign anything is broken.
func (t SlogEngineTelemetry) RecordSubjectlessTerminal(ctx context.Context, principal storage.Principal, reason string, refusalBasis string) {
	// refusal_basis is emitted on EVERY subjectless terminal, carrying the
	// explicit token "none" when the turn was not refused -- never omitted
	// on the ordinary path. A key that appeared only on refusals would be
	// indistinguishable, on the ordinary line, from a build that stopped
	// emitting it, and the regression this key guards against is exactly a
	// build that stopped disclosing.
	//
	// The token is composed by the CALLER (FrameGate.ObservableRefusalBasis)
	// and written here verbatim. Substituting a default in this sink would
	// make it the second authority on what an unrefused turn reports, and
	// would keep this one line looking correct while every other recorder
	// implementation emitted an empty value.
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "reason", SanitizeLogAttr(reason), "refusal_basis", SanitizeLogAttr(refusalBasis)}, requestIDLogAttrs(ctx)...)
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
	args := []any{"org_id", SanitizeLogAttr(principal.OrgID), "reason", SanitizeLogAttr(reason), "count", count}
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
// detail, not a warning condition. `servedRequestID` is read back from a
// stored result rather than generated fresh by this process, so -- unlike
// the LIVE request id `requestIDLogAttrs` below carries -- it never passed
// through `observability.WithRequestID`'s own format check; it is
// sanitized here through the one shared barrier (SanitizeLogAttr) like
// every other request/stored-derived value in this file.
func (t SlogEngineTelemetry) RecordAnswerReuseServedRequestID(ctx context.Context, principal storage.Principal, servedRequestID string, requestIDMismatch bool) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "served_request_id", SanitizeLogAttr(servedRequestID), "request_id_mismatch", requestIDMismatch}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric answer reuse served a stored result's own request id", args...)
}

// RecordBindingEpochDelta is CHAOS-3898 §5b's flip_during_investigation/
// cf_binding_epoch_delta pair -- see EngineTelemetry's own doc comment.
// flipped=false (the ordinary case: no build/flip happened mid-investigation)
// logs at Debug; flipped=true logs at Info -- worth an operator's attention
// (grace-window/cache-lease tuning data), never itself an error condition.
func (t SlogEngineTelemetry) RecordBindingEpochDelta(ctx context.Context, principal storage.Principal, flipped bool, delta int64) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "flip_during_investigation", flipped, "binding_epoch_delta", delta}, requestIDLogAttrs(ctx)...)
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
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "reason", SanitizeLogAttr(string(reason))}, requestIDLogAttrs(ctx)...)
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
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "outcome", SanitizeLogAttr(string(outcome))}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric window canonicalization outcome", args...)
}

// RecordWindowCarry (CHAOS-4360) logs at Info: outcome/chain_depth are both
// closed-vocabulary/plain-integer, content-safe by construction -- never a
// question, subject label, or canonical id. hit vs. every miss reason is
// this stream's own hit-rate denominator (RecordWindowCarry's own doc
// comment, engine.go, for why it fires only on the carry-eligible
// population).
func (t SlogEngineTelemetry) RecordWindowCarry(ctx context.Context, principal storage.Principal, outcome WindowCarryOutcome, chainDepth int, seedSource CarrySeedSource, viaStoredAncestry bool) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "outcome", SanitizeLogAttr(string(outcome)), "chain_depth", chainDepth, "seed_source", SanitizeLogAttr(string(seedSource)), "via_stored_ancestry", viaStoredAncestry}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric window carry", args...)
}

// RecordKindCarry logs at Info, mirroring RecordWindowCarry exactly:
// outcome/chain_depth are closed-vocabulary/plain-integer, content-safe by
// construction -- never a question, subject label, or canonical id. A
// distinct message from the window carry's so the two axes' hit rates can
// be counted apart.
func (t SlogEngineTelemetry) RecordKindCarry(ctx context.Context, principal storage.Principal, outcome KindCarryOutcome, chainDepth int, carriedKind, redeemedKind contractsv1.ContextFabricSubjectKind, seedSource CarrySeedSource, viaStoredAncestry bool) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "outcome", SanitizeLogAttr(string(outcome)), "chain_depth", chainDepth,
		"carried_kind", SanitizeLogAttr(string(carriedKind)), "redeemed_kind", SanitizeLogAttr(string(redeemedKind)), "seed_source", SanitizeLogAttr(string(seedSource)), "via_stored_ancestry", viaStoredAncestry}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric kind carry", args...)
}

// RecordStructureNeedsDisclosed (CHAOS-3900 P1.F). member is a closed
// StructureNeedKind enum value -- content-safe by construction, never
// question text or a subject identifier.
func (t SlogEngineTelemetry) RecordStructureNeedsDisclosed(ctx context.Context, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "member", SanitizeLogAttr(string(member))}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric structure needs disclosed", args...)
}

// RecordStructureOfferCount (CHAOS-3900 P1.F). member/source are both
// closed enums; count is a plain integer -- the full event is
// counts/enums only, never an offer's own label/value/canonical_id.
func (t SlogEngineTelemetry) RecordGatedOfferResolution(ctx context.Context, principal storage.Principal, outcome GatedOfferResolutionOutcome) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "outcome", SanitizeLogAttr(string(outcome))}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric gated offer resolution", args...)
}

// RecordCohortStructureGate (CHAOS-4579/CHAOS-4531). outcome and shape are
// both closed enums -- content-safe by construction, never question text,
// a subject identifier, or an offer label. One event per
// GateSubjectAxisOffers call, which is NOT one per composed StructureNeeds
// -- see the interface method's own doc comment (engine.go) for the exact
// denominator and the two directions it differs in.
func (t SlogEngineTelemetry) RecordCohortStructureGate(ctx context.Context, principal storage.Principal, outcome CohortStructureGateOutcome, shape InvestigationShape) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "outcome", SanitizeLogAttr(string(outcome)), "shape", SanitizeLogAttr(string(shape))}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric cohort structure gate", args...)
}

// RecordWindowGateOfferDisclosure (CHAOS-4314) logs at Info: offered is the
// window_gated_offered/window_gated_silent split's own producer signal.
func (t SlogEngineTelemetry) RecordWindowGateOfferDisclosure(ctx context.Context, principal storage.Principal, offered bool) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "offered", offered}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric window gate offer disclosure", args...)
}

// RecordWindowExpandOfferRedeemed (CHAOS-4314) logs at Info: no
// content-bearing field, a plain occurrence count exactly like
// RecordPriorSubjectReceiptsSkipped's own shape when skipped>0.
func (t SlogEngineTelemetry) RecordWindowExpandOfferRedeemed(ctx context.Context, principal storage.Principal) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID)}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric window expand offer redeemed", args...)
}

// RecordInterpretedTimeBound (CHAOS-5421) logs at Info -- the PRODUCTION
// level, which is the whole requirement: the arms of the interpreted-time
// verdict are distinguished only by wrapped error text that the failure
// classifier deliberately never logs at any level, so before this line an
// operator could read `failure_classification="invalid_time_bound"` and
// still not know which of six rules refused the turn, or whether the
// caller or this engine's own interpreter produced the bound.
//
// Every field is emitted on every call, including the zeros: axis and
// outcome always, clamp_applied as an explicit true/false, and range_days
// as an explicit 0 off the range axis. A reader must never have to
// distinguish "we measured zero" from "we did not measure".
//
// Closed enums and counts only -- no instant, no question text, no
// interpreter output -- so the stream stays corpus-safe and readable as a
// dashboard, the same discipline every sibling event here holds.
func (t SlogEngineTelemetry) RecordInterpretedTimeBound(ctx context.Context, principal storage.Principal, decision InterpretedTimeBoundDecision) {
	args := append([]any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"axis", SanitizeLogAttr(string(decision.Axis)),
		"outcome", SanitizeLogAttr(string(decision.Outcome)),
		"clamp_applied", decision.ClampApplied,
		"range_days", decision.RangeDays,
	}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric interpreted time bound", args...)
}

func (t SlogEngineTelemetry) RecordStructureOfferCount(ctx context.Context, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind, source contractsv1.ContextFabricStructureOfferSource, count int) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "member", SanitizeLogAttr(string(member)), "source", SanitizeLogAttr(string(source)), "count", count}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric structure offer count", args...)
}

// RecordStructureReceipt (CHAOS-3900 P1.F). member/outcome are both closed
// enums -- see StructureReceiptOutcome's own doc comment (structure.go)
// for the three-value vocabulary and its atomicity guarantee.
func (t SlogEngineTelemetry) RecordStructureReceipt(ctx context.Context, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind, outcome StructureReceiptOutcome) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "member", SanitizeLogAttr(string(member)), "outcome", SanitizeLogAttr(string(outcome))}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric structure receipt", args...)
}

// RecordStructureExplicit (CHAOS-3972 P3) mirrors RecordStructureReceipt's
// own logging shape exactly, for the explicit (non-receipt) structure
// fields.
func (t SlogEngineTelemetry) RecordStructureExplicit(ctx context.Context, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind, outcome StructureExplicitOutcome) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "member", SanitizeLogAttr(string(member)), "outcome", SanitizeLogAttr(string(outcome))}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric structure explicit", args...)
}

// RecordPriorConsulted (CHAOS-3977 P5). member/outcome are both closed
// enums -- see PriorConsultedOutcome's own doc comment (priors.go).
func (t SlogEngineTelemetry) RecordPriorConsulted(ctx context.Context, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind, outcome PriorConsultedOutcome) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "member", SanitizeLogAttr(string(member)), "outcome", SanitizeLogAttr(string(outcome))}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric prior consulted", args...)
}

// RecordPriorDegradation (CHAOS-3977 P5) logs at Warn for
// PriorDegradationPointerDangling (design brief §3.4: "additionally raises
// an operator signal because it means a retire outran its grace") and at
// Info for every other state -- an ordinary, expected degrade-and-continue
// outcome, never itself a sign anything is broken.
func (t SlogEngineTelemetry) RecordPriorDegradation(ctx context.Context, principal storage.Principal, state PriorDegradationState) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "state", SanitizeLogAttr(string(state))}, requestIDLogAttrs(ctx)...)
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
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "outcome", SanitizeLogAttr(string(outcome))}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric offer phrasing outcome", args...)
}

// RecordProjectedRowsCount implements EngineTelemetry (CHAOS-4355) -- see
// that method's doc comment for the count/truncated meaning. Content-safe:
// an org id and two closed, non-identifying numbers.
func (t SlogEngineTelemetry) RecordProjectedRowsCount(ctx context.Context, principal storage.Principal, count int, truncated bool) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "rows_count", count, "truncated", truncated}, requestIDLogAttrs(ctx)...)
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
		args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "fact_kind", SanitizeLogAttr(kind), "rows_projected_by_fact_kind", byKind[FactKind(kind)]}, requestIDLogAttrs(ctx)...)
		t.logger.InfoContext(ctx, "context fabric projected rows count by fact kind", args...)
	}
}

// RecordDualTableFacts implements EngineTelemetry (CHAOS-4682, §5.1 P2) --
// see that method's doc comment for the two counts' meaning. Content-safe:
// an org id and two non-identifying counts.
func (t SlogEngineTelemetry) RecordDualTableFacts(ctx context.Context, principal storage.Principal, dualTableClaims, secondaryRowsBytes int) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "dual_table_claims", dualTableClaims, "secondary_rows_bytes", secondaryRowsBytes}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric dual table facts", args...)
}

// RecordModelRowsStripped implements EngineTelemetry (CHAOS-4355
// follow-up). Content-safe: an org id and one closed, non-identifying
// count -- never the stripped rows themselves.
func (t SlogEngineTelemetry) RecordModelRowsStripped(ctx context.Context, principal storage.Principal, claims int) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "cf_model_rows_stripped", claims}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric model-authored claimed fact rows stripped before validation", args...)
}

// RecordDriverIdentityCollisions implements EngineTelemetry (CHAOS-5364).
// See the interface's own doc comment for why every field is logged on every
// call, zeros included. Content-safe: an org id and three counts.
func (t SlogEngineTelemetry) RecordDriverIdentityCollisions(ctx context.Context, principal storage.Principal, collisions DriverIdentityCollisions) {
	args := append([]any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"cf_driver_identity_collisions", collisions.Total(),
		"cf_driver_identity_restated", collisions.Restated,
		"cf_driver_identity_reidentified", collisions.Reidentified,
	}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric driver identity collisions resolved before validation", args...)
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
// factScopeDecisionReasonUnrecognized is what the sink writes in place of a
// decision reason outside the closed vocabulary. It is a VALUE, not a dropped
// field: silently omitting the key would make an invalid reason and a
// producer that never set one indistinguishable, and silently forwarding it
// would let the allow-list be widened from a caller.
const factScopeDecisionReasonUnrecognized FactScopeDecisionReason = "unrecognized"

func loggableFactScopeDecisionReason(reason FactScopeDecisionReason) FactScopeDecisionReason {
	if validFactScopeDecisionReason(reason) {
		return reason
	}
	return factScopeDecisionReasonUnrecognized
}

func (t SlogEngineTelemetry) RecordFactScopeExpansion(ctx context.Context, principal storage.Principal, event FactScopeExpansionEvent) {
	args := append([]any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"requirement_kind", SanitizeLogAttr(string(event.RequirementKind)),
		"origin_kind", SanitizeLogAttr(string(event.OriginKind)),
		"target_kind", SanitizeLogAttr(string(event.TargetKind)),
		"policy", SanitizeLogAttr(string(event.Policy)),
		"basis", SanitizeLogAttr(string(event.Basis)),
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
		"axis", SanitizeLogAttr(string(event.Axis)),
		"outcome", SanitizeLogAttr(string(event.Outcome)),
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
		"failure_class", SanitizeLogAttr(string(event.FailureClass)),
		// AttributionSourceCounts (CHAOS-4101): closed-vocabulary source
		// breakdown for a team-origin expansion, nil for every project-origin
		// one. Logged unconditionally, nil included, for the SAME reason
		// every zero count above is: "the filter dropped nothing" and
		// "nobody ever counted" must stay distinguishable in a log
		// aggregator.
		"attribution_source_counts", event.AttributionSourceCounts,
		// CHAOS-5405 (D-e). Emitted unconditionally, zero/false included,
		// for the same reason every count above is: a field that vanishes
		// when it is zero makes "the filter dropped nothing" and "nobody
		// ever counted" the same line in an aggregator.
		//
		// decision_reason is VALIDATED here rather than passed through. The
		// sink is the last point before an operator's alert rule sees it, and
		// a free-text reason is one a regression can silently rename; an
		// unrecognised value is reported AS unrecognised rather than
		// forwarded verbatim, so the allow-list cannot be widened by
		// accident from a producer.
		"target_limit", event.TargetLimit,
		"census_complete", event.CensusComplete,
		"authorized_count", event.AuthorizedCount,
		"repo_less_candidate_count", event.RepoLessCandidateCount,
		"repo_less_admitted_count", event.RepoLessAdmittedCount,
		"repo_less_authorization_dropped_count", event.RepoLessAuthorizationDroppedCount,
		"orphaned_repository_count", event.OrphanedRepositoryCount,
		"ambiguous_origin_count", event.AmbiguousOriginCount,
		"unknown_attribution_source_count", event.UnknownAttributionSourceCount,
		"scope_query_count", event.ScopeQueryCount,
		"scope_rows_returned", event.ScopeRowsReturned,
		"decision_reason", SanitizeLogAttr(string(loggableFactScopeDecisionReason(event.DecisionReason))),
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
		"org_id", SanitizeLogAttr(principal.OrgID),
		"cohort_kind", SanitizeLogAttr(string(event.CohortKind)),
		"member_count", event.MemberCount,
		"formula_version", SanitizeLogAttr(event.FormulaVersion),
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
		"org_id", SanitizeLogAttr(principal.OrgID),
		"outcome", SanitizeLogAttr(string(event.Outcome)),
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
		// narration_allocator is the CONSUMER-side proof that narration is
		// bounded by the item budget rather than by the static contract
		// caps: the two emit identical counts on a small cohort, so a
		// regression to the caps would otherwise be invisible in the
		// artifacts. Routed through the closed vocabulary so a value
		// escaping it reports as unclassified rather than as free text.
		"narration_allocator", SanitizeLogAttr(string(validNarrationAllocatorOrUnclassified(event.Allocator))),
		"narration_allocated_items", event.AllocatedItems,
	}, requestIDLogAttrs(ctx)...)...)
}

// RecordEvidenceLabelFallback implements EngineTelemetry (CHAOS-4690 item
// 4). Content-safe: org id and one closed count -- never the unlabeled
// evidence ref or its entity-type segment (r2 F5; see the interface's own
// doc comment). Called only when count > 0, same gated-telemetry
// discipline as RecordModelRowsStripped above it on the interface.
func (t SlogEngineTelemetry) RecordEvidenceLabelFallback(ctx context.Context, principal storage.Principal, count int) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "cf_evidence_label_fallback", count}, requestIDLogAttrs(ctx)...)
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
		"org_id", SanitizeLogAttr(principal.OrgID),
		"outcome", SanitizeLogAttr(string(outcome)),
		"violation", SanitizeLogAttr(string(violation)),
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
		"org_id", SanitizeLogAttr(principal.OrgID),
		"requirement_kind", SanitizeLogAttr(string(event.RequirementKind)),
		"subject_kind", SanitizeLogAttr(string(event.SubjectKind)),
		"composed_kinds", SanitizeLogStrings(composedKinds),
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
		args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "question_shape", SanitizeLogAttr(string(event.Shape))}, extra...)
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
			"render_shape_accounting", SanitizeLogAttr(accounting))...)
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
		"org_id", SanitizeLogAttr(principal.OrgID),
		"family", SanitizeLogAttr(string(event.Family)),
		"source", SanitizeLogAttr(string(event.Source)),
		"ensemble_size", event.EnsembleSize,
		"downgraded_count", event.DowngradedCount,
		"consensus_field_divergence", event.ConsensusFieldDivergence,
		"family_version", SanitizeLogAttr(event.FamilyVersion),
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
	// The SHADOW comparison. Every field, on the same sink discipline as
	// the rest of this line: a field populated on the struct and never
	// logged is not telemetry, it is a field.
	//
	// `shadow_frame_observed` is logged even though the four keys after it
	// are empty without it. It is the DENOMINATOR: without it, "no frame
	// was emitted", "the frame was refused" and "the comparison agreed"
	// all render as an absent class, and the flip decision reads this
	// stream.
	args = append(args,
		"shadow_frame_observed", event.Shadow.FrameObserved,
		"shadow_frame_outcome", string(event.Shadow.FrameOutcome),
		"shadow_projection_version", SanitizeLogAttr(event.Shadow.ProjectionVersion),
		"shadow_projected_family", string(event.Shadow.Agreement.ProjectedFamily),
		"shadow_projected_row", string(event.Shadow.Agreement.ProjectedRow),
		"shadow_precedence_family", string(event.Shadow.Agreement.PrecedenceFamily),
		"shadow_precedence_row", string(event.Shadow.Agreement.PrecedenceRow),
		"shadow_agreement_class", string(event.Shadow.Agreement.Class),
		"shadow_agreed", event.Shadow.Agreement.Agreed,
	)
	// SEAM 7's ROUTING DECISION (CHAOS-4736), on the same sink discipline:
	// every field on the struct reaches this line.
	//
	// TWO KEYS ON THIS LINE BOTH SPELL "carried", AND THEY ARE NOT THE SAME
	// FACT. `source` is ContextFabricQuestionFamilySource -- where the
	// PRECEDENCE table's inputs came from: model / carried / none.
	// `family_source` is FamilyRouteSource -- which table decided the served
	// family: projected / precedence / carried. Splicing two different
	// questions into one key is how a rate stops meaning anything, so they
	// stay separate and both vocabularies are named here rather than one of
	// them being described as "the token list".
	//
	// `family_source` CANNOT read `carried` on this line: it is built and
	// sent from the interpreter, and applyCarriedPlan runs later in the
	// engine. A carried turn is identified by joining this line's request id
	// against the "context fabric plan carry" event, which is emitted at the
	// replacement itself and carries the same four route fields.
	//
	// `route_switched` is logged beside `family_source` because they are
	// not the same fact: the `agreed` class serves the PROJECTED family and
	// switches nothing, which is exactly why 21 of the 25 measured rows
	// were safe. A reader counting switches off family_source alone would
	// report every agreeing answer as a behaviour change.
	args = append(args,
		"family_source", string(event.Route.Source),
		"route_class", string(event.Route.Class),
		"route_disposition", string(event.Route.Disposition),
		"route_switched", event.Route.Switched,
	)
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric question family resolution", args...)
}

// RecordServerStatusShadow (CHAOS-4452 stage 2, B8) logs at Info, once per
// investigation that reaches assembly with a plan.
//
// EVERY field on the event reaches this line, per the CHAOS-4085 sink
// discipline. `derived` in particular is logged even though it is false
// only on the no_plan basis: without it, "the server declined to derive a
// status" and "the server derived one and agreed" both render as
// disagreed=false, and the flip decision reads exactly that difference.
//
// `version` is on every line because this derivation is explicitly a
// placeholder for T6's requirement-outcome rule. Two disagreement rates
// measured under different rules must never be spliced into one series,
// and the version is what makes the splice visible.
func (t SlogEngineTelemetry) RecordServerStatusShadow(ctx context.Context, principal storage.Principal, event ServerStatusShadow) {
	args := []any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"model_status", SanitizeLogAttr(string(event.ModelStatus)),
		"server_status", SanitizeLogAttr(string(event.ServerStatus)),
		"basis", SanitizeLogAttr(string(event.Basis)),
		"derived", event.Derived,
		"disagreed", event.Disagreed,
		"version", SanitizeLogAttr(event.Version),
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric server status shadow", args...)
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
		"org_id", SanitizeLogAttr(principal.OrgID),
		"outcome", SanitizeLogAttr(string(event.Outcome)),
		"failed_invariant", SanitizeLogAttr(string(event.FailedInvariant)),
		"failed_phase", SanitizeLogAttr(string(event.FailedPhase)),
		"failure_detail", SanitizeLogAttr(string(event.FailureDetail)),
		"proposed_kind", SanitizeLogAttr(string(event.ProposedKind)),
		"proposed_goals", goalsLogValue(event.ProposedGoals),
		"derived_obligation_count", event.DerivedObligationCount,
		"widened_obligation_count", event.WidenedObligationCount,
		"shape_diverged", event.ShapeDiverged,
		"emitted_shape", SanitizeLogAttr(string(event.EmittedShape)),
		"derived_shape", SanitizeLogAttr(string(event.DerivedShape)),
		"frame_version", SanitizeLogAttr(event.FrameVersion),
		// WHY the frame can or cannot produce a discovered cohort. It
		// disambiguates the `unresolvable_member_set` arm below, whose two
		// causes -- an expression that enumerates nothing, and a declared
		// member kind with no discovery arm -- send an operator to opposite
		// ends of the pipeline. Emitted on EVERY frame-validation line,
		// including a refused frame, where it is empty: the empty value is
		// not a member of the vocabulary, so "no validated expression" and a
		// real reason cannot be confused, and this line's own `outcome` key
		// says which.
		"cohort_discoverability", SanitizeLogAttr(string(event.CohortDiscoverability)),
		// WHETHER THE FINDING WAS ACTED ON. Every key above reports what
		// validation observed; these two report what the server then did
		// about it, which for the whole life of the shadow slice was
		// nothing. Both are ALWAYS present with an explicit token
		// (`not_proposed`, `none`, `unset`), so a line missing them means
		// the emitter predates this seam -- never that the gate passed.
		"frame_gate", SanitizeLogAttr(event.Gate.Observable()),
		"refuse_basis", SanitizeLogAttr(event.Gate.ObservableRefuseBasis()),
		// THE INTERPRETATION BOUNDARY (see chaos5390_interpretation_boundary.go):
		// what the hints requested beside what the frame proposed, and what
		// became of the group axis. Every value is a closed token or an
		// explicit absence token, never an empty string.
		"requested_group_hint", SanitizeLogAttr(event.Boundary.RequestedGroupHint),
		"requested_member_hint", SanitizeLogAttr(event.Boundary.RequestedMemberHint),
		"proposed_group_kind", SanitizeLogAttr(event.Boundary.ProposedGroupKind),
		"proposed_member_kind", SanitizeLogAttr(event.Boundary.ProposedMemberKind),
		"group_axis", SanitizeLogAttr(observableGroupAxis(event.Boundary.GroupAxis)),
	}
	args = append(args, requirementDerivationLogAttrs(event.RequirementDerivation)...)
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric frame validation", args...)
}

// requirementDerivationLogAttrs flattens the requirement summary onto the
// frame-validation line.
//
// EVERY CLOSED TOKEN GETS A KEY ON EVERY EVENT, including the ones that
// counted zero. An omitted zero is indistinguishable from a classifier
// that never reached that tier, which is the same failure as a gate tier
// with no positive fixture: "0" and "never ran" must not look alike to an
// operator reading the log. The keys are derived from the vocabularies
// themselves, so a member added to one appears here without an edit.
//
// requirement_accounting is the positive statement, on the pattern
// render_shape_accounting already set: "ok" when served + unserved
// accounts for every derived row, so a healthy derivation SAYS so rather
// than merely not complaining.
func requirementDerivationLogAttrs(summary RequirementDerivationSummary) []any {
	accounting := "ok"
	if !summary.Balanced() {
		accounting = "violated"
	}
	args := []any{
		"requirement_derivation_version", SanitizeLogAttr(summary.Version),
		"requirement_cells_derived", summary.Derived,
		"requirement_cells_served", summary.Served,
		"requirement_cells_unserved", summary.Unserved,
		"requirement_accounting", SanitizeLogAttr(accounting),
	}
	for index, reason := range RequirementUnavailableReasonVocabulary() {
		args = append(args, "requirement_unavailable_"+string(reason), summary.UnavailableCells[index])
	}
	// The two arms of `computed_population_absent`, both keys on every
	// event including the zeroes. An operator reading the aggregate key
	// alone cannot tell an interpreter defect (a frame asking to rank the
	// organization) from a wiring one (a legitimate coordinate whose step
	// needs a cohort the frame resolves none of), and those send them to
	// opposite ends of the pipeline. Their sum is checkable against the
	// aggregate key above without leaving the line.
	args = append(args,
		"requirement_computed_population_absent_not_a_population", summary.ComputedPopulationAbsentNotAPopulation,
		"requirement_computed_population_absent_unresolvable_member_set", summary.ComputedPopulationAbsentUnresolvableMemberSet,
		// The residual, always emitted: with it the three keys are a TOTAL
		// partition of requirement_unavailable_computed_population_absent, so
		// an operator can check the split adds up without leaving the line.
		"requirement_computed_population_absent_non_computed_row", summary.ComputedPopulationAbsentNonComputedRow,
	)
	// What those decisions COST -- the declared inputs no read was planned
	// for -- in the same per-kind shape as requirement_computed_input_kind_
	// above, so the two are subtractable on one line. Every member present
	// including the zeroes, for the reason every histogram here has.
	//
	// The FRAME KIND those counts belong to is already on this line as
	// `proposed_kind` (RecordFrameValidation's own field, the subject
	// expression's closed discriminator), so the arms and their cost are
	// attributable to a topology without adding a second copy of it.
	for index, kind := range contractsv1.ContextFabricFactKindVocabulary() {
		args = append(args, "requirement_computed_input_kind_unplanned_"+string(kind), summary.ComputedInputKindsUnplanned[index])
	}
	for index, quantifier := range CompletionQuantifierVocabulary() {
		args = append(args, "requirement_quantifier_"+string(quantifier), summary.Quantifiers[index])
	}
	for index, role := range SubjectRoleVocabulary() {
		args = append(args, "requirement_role_"+string(role), summary.Roles[index])
	}
	// The §13.2.3 amendment's resolved inputs. Emitted as counts over the
	// closed vocabularies, every member present including the zeroes, for
	// the reason the loops above exist: an absent key cannot be told apart
	// from a tier that never ran.
	args = append(args, "requirement_computed_rows_with_inputs", summary.ComputedRowsWithDeclaredInputs)
	for index, class := range ComputedStepInputClassVocabulary() {
		args = append(args, "requirement_computed_input_class_"+string(class), summary.ComputedInputClasses[index])
	}
	for index, kind := range contractsv1.ContextFabricFactKindVocabulary() {
		args = append(args, "requirement_computed_input_kind_"+string(kind), summary.ComputedInputKinds[index])
	}
	for index, execution := range ComputedStepExecutionVocabulary() {
		args = append(args, "requirement_computed_step_"+string(execution), summary.ComputedStepExecutions[index])
	}
	return args
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
		"org_id", SanitizeLogAttr(principal.OrgID),
		"family", SanitizeLogAttr(string(event.Family)),
		"family_version", SanitizeLogAttr(event.FamilyVersion),
		"stage", SanitizeLogAttr(string(event.Stage)),
		"basis", SanitizeLogAttr(string(event.Basis)),
		// CHAOS-4809: whether that basis was REPORTED by a selection or
		// DEFAULTED by planStageBasis. Without it a reader must assume the
		// order named actually ran, and the same ticket is the proof that
		// assumption is unsafe.
		"basis_observed", event.BasisObserved,
		"before", event.Before,
		"after", event.After,
		"groups", event.Groups,
		"overrun", SanitizeLogAttr(string(validBudgetOverrunOrUnclassified(event.Overrun))),
		"measured_items", event.MeasuredItems,
		// Beside measured_items on purpose: the plan's own arithmetic for this
		// cohort (members + reserved SynthesisHeadroom), NOT a per-member rate
		// -- see PlanNarrowingEvent.PredictedItems. Zero only when there is no
		// cohort to predict for.
		"predicted_items", event.PredictedItems,
		// The per-bucket split of measured_items (S5, observing half):
		// WHAT the charged items were about, from the closed four-member
		// bucket vocabulary. measured_items says how big the answer was;
		// these four say where it went, which is the difference between a
		// number an operator can escalate and one they can act on. The four
		// sum to measured_items by construction -- they are computed from
		// the same document in the same measurement -- so a line where they
		// do not is itself the signal.
		//
		// Counts only. No group label, no subject id: the bucket names are
		// a closed vocabulary and the values are integers, so this stays a
		// bounded set of dimensions.
		"attribution_global", event.Attribution.Global,
		"attribution_member", event.Attribution.Member,
		"attribution_group", event.Attribution.Group,
		"attribution_multi_group", event.Attribution.MultiGroup,
		"measured_bytes", event.MeasuredBytes,
		"max_items", event.MaxItems,
		"max_serialized_bytes", SanitizeLogInt(event.MaxSerializedBytes),
		"retry_attempted", event.RetryAttempted,
		"retry_fit", event.RetryFit,
		"retry_failed", event.RetryFailed,
		"refusal_planned", event.RefusalPlanned,
		"deadline_reserved", event.DeadlineReserved,
		"retry_declined", SanitizeLogAttr(string(event.RetryDeclined)),
		// CHAOS-4735 criterion 6: the continuation the refusal offered, as a
		// closed token. Empty unless a refusal was planned. Never free text
		// -- the field it replaces held an English sentence, which could not
		// be a log dimension without becoming unbounded-cardinality prose.
		"narrower_continuation_axis", SanitizeLogAttr(string(event.NarrowerContinuationAxis)),
		// S7c: the narrow-instead-of-refuse decision, its numbers, and
		// what the served answer claims about its own completeness. All
		// closed tokens and counts, same discipline as every dimension
		// above.
		"outcome_reduction_applied", event.OutcomeReductionApplied,
		"outcome_reduction_inner_fit", event.OutcomeReductionInnerFit,
		"outcome_items_served", event.OutcomeItemsServed,
		"outcome_items_declared", event.OutcomeItemsDeclared,
		"outcome_completeness_state", SanitizeLogAttr(string(event.OutcomeCompletenessState)),
		"outcome_reduction_declined", SanitizeLogAttr(string(event.OutcomeReductionDeclined)),
		// The item ACCOUNT and the per-group quota for the document this
		// line describes. quota_availability is what makes the four
		// numbers readable: a zero allowance, no ceiling, no group axis
		// and an account that did not reconcile used to be one
		// indistinguishable zero, which is the whole of class B. Routed
		// through a fail-closed helper so a value outside the closed
		// vocabulary reports as unclassified rather than reaching the log
		// as free text.
		"ledger_status", SanitizeLogAttr(string(validLedgerStatusOrUnclassified(event.LedgerStatus))),
		"quota_availability", SanitizeLogAttr(string(validQuotaAvailabilityOrUnclassified(event.QuotaAvailability))),
		"quota_group_allowance", event.QuotaGroupAllowance,
		"quota_groups_granted", event.QuotaGroupsGranted,
		"quota_groups_measured", event.QuotaGroupsMeasured,
		"quota_groups_over_allowance", event.QuotaGroupsOverAllowance,
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
		"org_id", SanitizeLogAttr(principal.OrgID),
		"family", SanitizeLogAttr(string(event.Family)),
		"pre_grouping_complete", event.PreGroupingComplete,
		"pre_grouping_truncated", event.PreGroupingTruncated,
		"group_count", event.GroupCount,
		"groups_marked_incomplete", event.GroupsMarkedIncomplete,
		"complete", event.Complete,
		"truncated", event.Truncated,
	}
	// Emitted only on a refusal, so an ordinary grouped answer's line is
	// byte-for-byte what it was before this field existed, and a reader
	// filtering on grouping_refusal sees refusals alone. The value is routed
	// through the canonical table so a value escaping the vocabulary is
	// reported as unclassified rather than emitted verbatim -- the same
	// fail-closed posture every other closed enum in this file applies.
	if event.Refusal != CohortGroupingRefusalNone {
		refusal := event.Refusal
		if !ValidCohortGroupingRefusal(refusal) {
			refusal = CohortGroupingRefusal("unclassified")
		}
		args = append(args, "grouping_refusal", string(refusal),
			"planned_group_kind", string(event.PlannedGroupKind),
			// INSIDE this guard, not beside it: an ordinary grouped answer's
			// line must stay byte-for-byte what it was before any of these
			// three fields existed, which is the property the comment above
			// claims and TestSlogGroupedCohortCompletenessOmitsTheRefusalKeys
			// WithoutARefusal enforces. Emitting a constant 0 on every
			// grouped line would break it while looking harmless.
			"ungrouped_members", event.UngroupedMembers)
		// Only when the source named an axis. A no-placement refusal has no
		// source kind to report -- the facts were silent -- and emitting an
		// empty value would make "the source disagreed" and "the source said
		// nothing" look alike to a filter, which is the distinction this whole
		// vocabulary exists to draw.
		if event.SourceGroupKind != "" {
			args = append(args, "source_group_kind", string(event.SourceGroupKind))
		}
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric grouped cohort completeness", args...)
}

// RecordMembershipCardinality emits the `membership_cardinality` step's own
// result for one served answer.
//
// Every field is a count or a closed token. `cohort_complete`/
// `cohort_truncated` ride the SAME line as the number rather than a sibling
// event, because a cardinality without them reads as a claim about the
// population that the step does not make -- it counts the RESOLVED member
// set, and those two are what say whether that set is the whole of it.
// RecordReadRequirementPopulation emits one line per distributive read row of
// the served answer.
//
// EVERY KEY ON EVERY LINE, INCLUDING THE ZEROES -- no omitempty posture for any
// of them. A field that vanishes when it is false or zero makes "0" and "never
// measured" look alike to a reader filtering on it, which is the distinction
// this whole event exists to preserve.
//
// `count_units` is what makes `row_served`/`row_declared` readable: three arms
// publish KIND counts and the rest publish POPULATION counts, and without the
// token an operator cannot tell one served subject from one served fact kind.
//
// `population_census` comes from the population AUTHORITY rather than from the
// row's cause, so a precedence decision about which cause a row names can never
// silently restate what the census was.
func (t SlogEngineTelemetry) RecordReadRequirementPopulation(ctx context.Context, principal storage.Principal, event ReadRequirementPopulationEvent) {
	args := []any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"family", SanitizeLogAttr(string(event.Family)),
		"requirement", SanitizeLogAttr(event.Requirement),
		"scope", SanitizeLogAttr(event.Scope),
		"outcome", SanitizeLogAttr(string(event.Outcome)),
		"impact", SanitizeLogAttr(string(event.Impact)),
		"cause_coverage", SanitizeLogAttr(string(event.Cause)),
		"cause_observed", event.CauseObserved,
		"row_served", event.Served,
		"row_declared", event.Declared,
		"count_units", SanitizeLogAttr(string(event.Units)),
		"population_census", SanitizeLogAttr(string(event.Census)),
		"cohort_complete", event.CohortComplete,
		"cohort_truncated", event.CohortTruncated,
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric read requirement population", args...)
}

// RecordReadRequirementObservationCover emits the observation-cover decision
// at Info, with EVERY field on the event -- the same "delta is the point"
// discipline ReadRequirementObservationCoverEvent's own doc comment states:
// the kind count and the cover are both logged for served and observed alike,
// because the cover alone cannot say whether it collapsed anything, and a
// field omitted at its zero value would make "nothing collapsed" and
// "nobody counted" look alike to a reader filtering on it.
//
// Content-safe by construction: two closed identity strings, one closed
// subject-kind token, one closed withheld-reason token, and the rest
// integers/booleans -- no key values and no kind lists, which would grow with
// the fact registry. Every string still routes through SanitizeLogAttr, the
// package's one log-injection barrier, like every sibling emitter.
func (t SlogEngineTelemetry) RecordReadRequirementObservationCover(ctx context.Context, principal storage.Principal, event ReadRequirementObservationCoverEvent) {
	args := append([]any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		// PRE-ENTRY: what this requirement asked for.
		"requirement", SanitizeLogAttr(event.Requirement),
		"obligation", SanitizeLogAttr(event.Obligation),
		"subject_kind", SanitizeLogAttr(string(event.Subject)),
		"threshold", event.Threshold,
		"observed_kinds", event.ObservedKinds,
		"served_kinds", event.ServedKinds,
		// PRE-DECISION: what the declaration measured those kinds to be.
		"observed_cover", event.ObservedCover,
		"served_cover", event.ServedCover,
		"collapsed_observations", event.CollapsedObservations,
		"tainted_observations", event.TaintedObservations,
		// DECISION + REASON, and POST-DECISION: the numbers the row publishes.
		"declared", event.Declared,
		"declared_raised_to_standard", event.DeclaredRaisedToStandard,
		"meets_threshold", event.MeetsThreshold,
		// pass/served: WHICH finalization produced this row and whether it is
		// the one actually served. Cover events are kept for every pass, not
		// just the last -- see assemblyTelemetry.ObservationCover -- so a
		// reader filtering on served=true gets the served document's own
		// numbers, and a reader who wants the discarded pass's has pass to
		// group on.
		"pass", event.Pass,
		"evaluated_pass", event.EvaluatedPass,
		"row_withheld", SanitizeLogAttr(string(event.RowWithheld)),
		"reused", event.Reused,
		"answer_withheld", event.AnswerWithheld,
		"served", event.Served,
	}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric observation cover", args...)
}

func (t SlogEngineTelemetry) RecordMembershipCardinality(ctx context.Context, principal storage.Principal, event MembershipCardinalityEvent) {
	args := []any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"family", SanitizeLogAttr(string(event.Family)),
		"requirement", SanitizeLogAttr(event.Requirement),
		"outcome", SanitizeLogAttr(string(event.Outcome)),
		"served", event.Served,
		"declared", event.Declared,
		"cohort_complete", event.CohortComplete,
		"cohort_truncated", event.CohortTruncated,
	}
	// Emitted only where a narrowing actually ran, so an exact count's line
	// is byte-for-byte what it would be with no narrowing vocabulary at all,
	// and a reader filtering on `basis` sees reductions alone. Same posture
	// as the grouping-refusal fields on the completeness line.
	if event.Basis != "" {
		args = append(args, "basis", string(event.Basis))
	}
	if event.Overrun != "" {
		args = append(args, "overrun", string(validBudgetOverrunOrUnclassified(event.Overrun)))
	}
	// Same non-empty guard as the two above, for the same reason: a count that
	// nothing cut emits no cut vocabulary at all. The difference is that this
	// key is the ONLY one populated when the cut had no recorded plan step, so
	// omitting it left that path with a narrowed outcome and no mechanism.
	if event.Cause != "" {
		args = append(args, "cause_coverage", SanitizeLogAttr(string(event.Cause)))
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric membership cardinality", args...)
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
		"org_id", SanitizeLogAttr(principal.OrgID),
		"assert_stage", SanitizeLogAttr(string(event.Stage)),
		"fits", event.Fits,
		"overrun", SanitizeLogAttr(string(validBudgetOverrunOrUnclassified(event.Overrun))),
		"measured_items", event.MeasuredItems,
		"measured_bytes_post_label", event.MeasuredBytesPostLabel,
		"max_items", event.MaxItems,
		"max_serialized_bytes", SanitizeLogInt(event.MaxSerializedBytes),
		// The FINISHED document's own account and the ledger-backed
		// capacity verdict. `fits` and `certified_fit` are both here on
		// purpose: an unbounded answer fits and is not certified, and a
		// dashboard reading only the first cannot tell the two apart.
		"ledger_status", SanitizeLogAttr(string(validLedgerStatusOrUnclassified(event.LedgerStatus))),
		"ledger_debits", event.LedgerDebits,
		"capacity", SanitizeLogAttr(string(validCapacityVerdictOrUnclassified(event.Capacity))),
		"certified_fit", event.CertifiedFit,
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric budget assertion", args...)
}

// RecordItemAccounting emits the one line that says an answer's own numbers do
// not add up.
//
// At ERROR level, and that is the only level in this file that is: every other
// event here describes a decision the engine is entitled to make, and this one
// describes a server defect that turned a servable answer into an internal
// error. An operator alerting on this is alerting on a bug in this program, not
// on a caller asking too much.
func (t SlogEngineTelemetry) RecordItemAccounting(ctx context.Context, principal storage.Principal, event ItemAccountingEvent) {
	args := []any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"accounting_stage", SanitizeLogAttr(event.Stage),
		"ledger_status", SanitizeLogAttr(string(validLedgerStatusOrUnclassified(event.Status))),
		// The collection or bucket that disagreed. A status without a name
		// sends the reader back to the source; this is the field that makes
		// the defect diagnosable from the artifact.
		"ledger_disagreement", SanitizeLogAttr(event.Disagreement),
		"ledger_debits", event.Debits,
		"budgeted_items", event.Budgeted,
		"max_items", event.MaxItems,
		// The ALLOCATOR's own verdict, as its own key. Empty when the grants
		// agree, which is the ordinary case even on a ledger disagreement --
		// the two checks fail independently and a reader must be able to tell
		// which one did. Routed through the closed vocabulary so a corrupted
		// or future value reports as unclassified rather than as free text.
		"allocation_disagreement", SanitizeLogAttr(string(validAllocationDisagreementOrUnclassified(event.AllocationDisagreement))),
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.ErrorContext(ctx, "context fabric item accounting disagreement", args...)
}

// validAllocationDisagreementOrUnclassified fails closed on a value outside the
// closed vocabulary. The EMPTY value is a member -- it is `AllocationAgrees`,
// the ordinary case -- so it passes through as empty rather than as
// `unclassified`: an allocation that agrees has not failed to be classified.
func validAllocationDisagreementOrUnclassified(disagreement AllocationDisagreement) AllocationDisagreement {
	for _, member := range AllocationDisagreementVocabulary() {
		if member == disagreement {
			return disagreement
		}
	}
	return AllocationDisagreement("unclassified")
}

// validLedgerStatusOrUnclassified, validQuotaAvailabilityOrUnclassified and
// validCapacityVerdictOrUnclassified fail CLOSED on a value outside their
// closed vocabulary, so a corrupted or future enum value cannot reach a log
// field as free text -- the same posture every other closed enum in this file
// takes.
func validLedgerStatusOrUnclassified(status contractsv1.ContextFabricLedgerStatus) contractsv1.ContextFabricLedgerStatus {
	if contractsv1.ValidContextFabricLedgerStatus(status) {
		return status
	}
	return contractsv1.ContextFabricLedgerStatus("unclassified")
}

func validQuotaAvailabilityOrUnclassified(availability ItemQuotaAvailability) ItemQuotaAvailability {
	if ValidItemQuotaAvailability(availability) {
		return availability
	}
	// The UNSET zero value lands here too, and that is the point: an
	// attempt nobody measured must not emit as though it had been.
	return ItemQuotaAvailability("unclassified")
}

func validCapacityVerdictOrUnclassified(verdict contractsv1.ContextFabricCapacityVerdict) contractsv1.ContextFabricCapacityVerdict {
	if contractsv1.ValidContextFabricCapacityVerdict(verdict) {
		return verdict
	}
	return contractsv1.ContextFabricCapacityVerdict("unclassified")
}

// RecordPlanCarry (CHAOS-4736, seam 7) logs at Info, once per applied carry.
//
// Every field on the event reaches this line, per the CHAOS-4085 sink
// discipline: a field populated on the struct and never logged is not
// telemetry, it is a field. The four route fields use the SAME key names as
// the family-resolution line so the two can be read side by side, and the
// request id is what joins them.
func (t SlogEngineTelemetry) RecordPlanCarry(ctx context.Context, principal storage.Principal, event PlanCarryEvent) {
	args := []any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"family_replaced", SanitizeLogAttr(string(event.FamilyReplaced)),
		"family_carried", SanitizeLogAttr(string(event.FamilyCarried)),
		"source_result_id", SanitizeLogAttr(event.SourceResultID),
		"family_source", SanitizeLogAttr(string(event.Route.Source)),
		"route_class", SanitizeLogAttr(string(event.Route.Class)),
		"route_disposition", SanitizeLogAttr(string(event.Route.Disposition)),
		"route_switched", event.Route.Switched,
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric plan carry", args...)
}

// RecordPlanCarryOutcome (CHAOS-5003) logs at Info, mirroring
// RecordWindowCarry and RecordKindCarry: a closed outcome vocabulary and a
// closed seed-source vocabulary, content-safe by construction -- never a
// question, subject label or family. A DISTINCT message from "context fabric
// plan carry" above, which fires only on an applied carry: folding the two
// would make the applied-carry counter and the attempt counter the same
// number and destroy the hit rate this line exists to publish.
//
// source_result_id is empty on every miss and is the origin on a hit -- the
// join key that ties this line to the applied-carry line for the same turn.
func (t SlogEngineTelemetry) RecordPlanCarryOutcome(ctx context.Context, principal storage.Principal, outcome PlanCarryOutcome, sourceResultID string, seedSource CarrySeedSource) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "outcome", SanitizeLogAttr(string(outcome)), "source_result_id", SanitizeLogAttr(sourceResultID), "seed_source", SanitizeLogAttr(string(seedSource))}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric plan carry outcome", args...)
}

// RecordWindowContinuationDecision (CHAOS-5465) logs at Info, once per request
// carrying a window receipt -- applied, withheld, ineligible and early-veto
// alike.
//
// EVERY FIELD REACHES THIS LINE, per the CHAOS-4085 sink discipline: a field
// populated on the decision and never logged is not telemetry, it is a field.
//
// EXPLICIT ZEROS, NOT OMITTED KEYS. `conflict_reason` is the literal "none"
// rather than an absent key, `conflict_count` is 0 rather than absent, and the
// three family keys and three context ids are the empty string rather than
// absent when that context does not exist. An omitted key and a measured zero
// are indistinguishable to every downstream query, and telling them apart is
// the entire reason this line exists.
//
// `comparison_evaluated` and `agreement` are SEPARATE booleans on purpose. An
// unevaluated comparison reports false for both; it is never counted as
// disagreement and never as agreement.
// continuationTelemetryUnrecognised is what a closed field carries when the
// value handed to the emitter is not a member of its vocabulary.
//
// IT IS DELIBERATELY NOT A MEMBER OF EITHER VOCABULARY. Mapping an unrecognised
// value onto a real member (`unspecified`, `not_evaluated`) would fold a bug
// into a legitimate bucket and make it uncountable; dropping the field would
// make the line's shape vary with its content. This token says "a decision site
// produced something this vocabulary does not define", which is the only honest
// thing to publish and is greppable on sight.
const continuationTelemetryUnrecognised = "unrecognised"

// closedDecisionField is one CLOSED field on the continuation event: a field
// whose values come from a fixed vocabulary and which consumers group on.
//
// THE REGISTRY IS THE PRODUCER, and that is the point of it. The first attempt
// at this membership check guarded the two fields its author happened to be
// thinking about, and the pin written beside it asserted those same two -- an
// instrument that enumerates only the inputs its author chose. Five other
// closed fields were reaching the line as free text. Both the emitter and the
// pin now walk THIS list, so a field added here is a field both of them must
// account for, and a field the emitter forgets to route through the registry
// fails the pin by leaking its invented value.
type closedDecisionField struct {
	// Key is the log key, byte-for-byte as it appears on the line.
	Key string
	// Token returns what the line should carry: the value when it is a member
	// of its vocabulary, the unrecognised sentinel when it is not.
	Token func(windowContinuationDecision) string
	// Invent installs an out-of-vocabulary value, so the pin can prove this
	// field rejects one. It lives beside the reader for the same reason the
	// vocabulary lives beside the members: a driver kept somewhere else drifts.
	//
	// NIL MEANS DERIVED, and the pin treats that as a claim to check rather
	// than a field to skip: a nil Invent asserts there is no input that can put
	// a non-member in this field, which is only true of values the decision
	// computes rather than stores.
	Invent func(*windowContinuationDecision)
}

func closedDecisionFields() []closedDecisionField {
	guard := func(valid bool, value string) string {
		if !valid {
			return continuationTelemetryUnrecognised
		}
		return value
	}
	return []closedDecisionField{
		{
			Key: "seed_source",
			Token: func(d windowContinuationDecision) string {
				return guard(ValidCarrySeedSource(d.SeedSource), string(d.SeedSource))
			},
			Invent: func(d *windowContinuationDecision) { d.SeedSource = CarrySeedSource("invented-seed") },
		},
		{
			Key: "family_carried",
			Token: func(d windowContinuationDecision) string {
				return guard(d.FamilyCarried() == "" || ValidQuestionFamily(d.FamilyCarried()), string(d.FamilyCarried()))
			},
			Invent: func(d *windowContinuationDecision) {
				d.Carried = &continuationCarriedContext{Family: QuestionFamily("invented-carried")}
			},
		},
		{
			Key: "family_fresh",
			Token: func(d windowContinuationDecision) string {
				return guard(d.FamilyFresh() == "" || ValidQuestionFamily(d.FamilyFresh()), string(d.FamilyFresh()))
			},
			Invent: func(d *windowContinuationDecision) {
				d.Fresh = continuationFreshProposal{Available: true, Family: QuestionFamily("invented-fresh")}
			},
		},
		{
			Key: "family_accepted",
			Token: func(d windowContinuationDecision) string {
				return guard(d.FamilyAccepted() == "" || ValidQuestionFamily(d.FamilyAccepted()), string(d.FamilyAccepted()))
			},
			Invent: func(d *windowContinuationDecision) {
				d.Accepted = &continuationCarriedContext{Family: QuestionFamily("invented-accepted")}
			},
		},
		{
			Key: "family_source",
			Token: func(d windowContinuationDecision) string {
				source := d.AcceptedFamilySource()
				// CHAOS-5582: the accept set is exactly what the derivation can
				// produce, and exactly what ContinuationDecisionLineVocabulary
				// declares -- a wider guard would certify a value this line
				// can never legitimately carry.
				return guard(source == "" || source == QuestionFamilySourceCarried, string(source))
			},
			// DERIVED: AcceptedFamilySource returns "" or `carried` from a
			// pointer test, so no caller can seat a non-member here. The guard
			// stays because the field is closed and the derivation could change;
			// the nil driver states, checkably, that it has no free-text path.
			Invent: nil,
		},
		{
			Key: "continuation_disposition",
			Token: func(d windowContinuationDecision) string {
				return guard(ValidContinuationDisposition(d.Disposition), string(d.Disposition))
			},
			Invent: func(d *windowContinuationDecision) { d.Disposition = ContinuationDisposition("invented-disposition") },
		},
		{
			Key: "decision_reason",
			Token: func(d windowContinuationDecision) string {
				return guard(ValidContinuationDecisionReason(d.Reason), string(d.Reason))
			},
			Invent: func(d *windowContinuationDecision) { d.Reason = ContinuationDecisionReason("invented-reason") },
		},
		{
			Key: "conflict_reason",
			Token: func(d windowContinuationDecision) string {
				return guard(ValidContinuationConflictReason(d.ConflictReason), string(d.ConflictReason))
			},
			Invent: func(d *windowContinuationDecision) {
				d.ConflictReason = ContinuationConflictReason("invented-conflict")
			},
		},
		{
			Key: "conflict_fields",
			// EVERY MEMBER IS CHECKED, not the joined string. A list field with
			// one invented entry is still a line consumers cannot group on.
			Token: func(d windowContinuationDecision) string {
				tokens := make([]string, 0, len(d.ConflictFields))
				for _, field := range d.ConflictFields {
					tokens = append(tokens, guard(ValidContinuationConflictField(field), string(field)))
				}
				return strings.Join(tokens, ",")
			},
			Invent: func(d *windowContinuationDecision) {
				d.ConflictFields = []ContinuationConflictField{ContinuationConflictField("invented-field")}
			},
		},
		{
			Key: "composition_outcome",
			Token: func(d windowContinuationDecision) string {
				return guard(ValidCompositionOutcome(d.CompositionOutcome), string(d.CompositionOutcome))
			},
			Invent: func(d *windowContinuationDecision) { d.CompositionOutcome = CompositionOutcome("invented-outcome") },
		},
		{
			Key: "composition_failed_invariant",
			// Two vocabularies meet in one field: the frame invariants, and the
			// composition's own. Empty is legitimate -- most turns fail nothing.
			Token: func(d windowContinuationDecision) string {
				value := d.CompositionFailedInvariant
				valid := value == "" ||
					value == CompositionInvariantCarriedAxisUnexpressible ||
					ValidFrameInvariant(FrameInvariant(value))
				return guard(valid, value)
			},
			Invent: func(d *windowContinuationDecision) { d.CompositionFailedInvariant = "invented-invariant" },
		},
		{
			Key: "carrier_read",
			Token: func(d windowContinuationDecision) string {
				return guard(ValidContinuationCarrierRead(d.ObservableCarrierRead()), string(d.ObservableCarrierRead()))
			},
			Invent: func(d *windowContinuationDecision) { d.CarrierRead = ContinuationCarrierRead("invented-read") },
		},
		{
			Key: "refusal_basis",
			// The WIRE vocabulary, and only the member this decision can
			// serve: a continuation refuses on its carrier, never on a frame,
			// so a frame member here is as wrong as an invented one. "none"
			// is the explicit not-refused token.
			Token: func(d windowContinuationDecision) string {
				valid := d.RefusalBasis == "" ||
					d.RefusalBasis == contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable
				return guard(valid, d.ObservableRefusalBasis())
			},
			Invent: func(d *windowContinuationDecision) {
				d.RefusalBasis = contractsv1.ContextFabricRefusalBasisMemberKindUnservable
			},
		},
		{
			// CHAOS-5582. Empty is legitimate on all three axes: interpretation
			// never ran, no carrier was loaded, the turn ended before the axis
			// was decided. Anything else must be a temporal axis member -- the
			// carried axis is read from a STORED result, which is exactly where
			// a value outside the vocabulary could come from.
			Key: "interpreted_axis",
			Token: func(d windowContinuationDecision) string {
				return guard(d.InterpretedAxis == "" || contractsv1.ValidContextFabricTemporalAxis(d.InterpretedAxis), string(d.InterpretedAxis))
			},
			Invent: func(d *windowContinuationDecision) { d.InterpretedAxis = "invented-interpreted-axis" },
		},
		{
			Key: "carried_axis",
			Token: func(d windowContinuationDecision) string {
				return guard(d.CarriedAxis == "" || contractsv1.ValidContextFabricTemporalAxis(d.CarriedAxis), string(d.CarriedAxis))
			},
			Invent: func(d *windowContinuationDecision) { d.CarriedAxis = "invented-carried-axis" },
		},
		{
			Key: "executed_axis",
			Token: func(d windowContinuationDecision) string {
				return guard(d.ExecutedAxis == "" || contractsv1.ValidContextFabricTemporalAxis(d.ExecutedAxis), string(d.ExecutedAxis))
			},
			Invent: func(d *windowContinuationDecision) { d.ExecutedAxis = "invented-executed-axis" },
		},
		{
			Key: "interpreted_axis_outcome",
			Token: func(d windowContinuationDecision) string {
				return guard(ValidContinuationAxisOutcome(d.AxisOutcome), string(d.AxisOutcome))
			},
			Invent: func(d *windowContinuationDecision) { d.AxisOutcome = ContinuationAxisOutcome("invented-axis-outcome") },
		},
	}
}

// ContinuationDecisionLineVocabulary returns every value one CLOSED field of
// the continuation decision line may carry, read from the production
// vocabularies the emitter's own guards consult (CHAOS-5582).
//
// IT EXISTS SO THE EVENT SPECIFICATION DECLARES FROM PRODUCTION. eventspec
// cannot be imported here (it imports this package), so the specification
// calls this instead of retyping member lists -- a second list is the drift
// this package has paid for before. The unrecognised sentinel is NEVER a
// member: a line carrying it is a defect a certificate must refuse.
//
// nil for a key that is not a closed field of the line; the pin beside the
// registry asserts every closed key returns a non-empty list and that every
// member it returns passes that field's own guard unchanged.
func ContinuationDecisionLineVocabulary(key string) []string {
	// The explicit empty member goes LAST, so the first member of every list
	// is a real value rather than the "did not apply" one.
	optional := func(members []string) []string { return append(members, "") }
	switch key {
	case "seed_source":
		return tokenStrings(carrySeedSources())
	case "family_carried", "family_fresh", "family_accepted":
		families := QuestionFamilyVocabulary()
		return optional(tokenStrings(families[:]))
	case "family_source":
		// Derived: AcceptedFamilySource is `carried` or empty, never another
		// source, so the vocabulary is exactly those two.
		return optional([]string{string(QuestionFamilySourceCarried)})
	case "continuation_disposition":
		return tokenStrings(continuationDispositions())
	case "decision_reason":
		return tokenStrings(continuationDecisionReasons())
	case "conflict_reason":
		return tokenStrings(continuationConflictReasons())
	case "composition_outcome":
		return tokenStrings(compositionOutcomeVocabulary())
	case "composition_failed_invariant":
		invariants := []string{CompositionInvariantCarriedAxisUnexpressible}
		for _, spec := range FrameInvariantSpecs() {
			invariants = append(invariants, string(spec.ID))
		}
		return optional(invariants)
	case "interpreted_axis", "carried_axis", "executed_axis":
		axes := contractsv1.ContextFabricTemporalAxisVocabulary()
		return optional(tokenStrings(axes[:]))
	case "interpreted_axis_outcome":
		return tokenStrings(continuationAxisOutcomes())
	case "carrier_read":
		return tokenStrings([]ContinuationCarrierRead{ContinuationCarrierNotRead, ContinuationCarrierReadOK, ContinuationCarrierReadFailed})
	case "refusal_basis":
		// The one wire member a continuation can serve, and the explicit
		// not-refused token ObservableRefusalBasis renders.
		return []string{string(contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable), "none"}
	default:
		return nil
	}
}

func tokenStrings[T ~string](members []T) []string {
	out := make([]string, 0, len(members))
	for _, member := range members {
		out = append(out, string(member))
	}
	return out
}

// closedDecisionToken reads ONE closed field through the registry.
//
// An unknown key is itself the unrecognised sentinel rather than a panic or an
// empty string: a mistyped key in the emitter must be visible on the line, not
// silently blank.
func closedDecisionToken(key string, decision windowContinuationDecision) string {
	for _, field := range closedDecisionFields() {
		if field.Key == key {
			return field.Token(decision)
		}
	}
	return continuationTelemetryUnrecognised
}

func (t SlogEngineTelemetry) RecordWindowContinuationDecision(ctx context.Context, principal storage.Principal, decision windowContinuationDecision) {
	args := []any{
		// REQUEST-DERIVED VALUES GO THROUGH THE ONE RECOGNIZED BARRIER.
		//
		// The context ids are the caller's: a window receipt names a prior
		// result id and the event echoes it back -- SanitizeLogAttr
		// (CHAOS-5544) is this package's one answer to go/log-injection,
		// replacing the local sanitizeLogString this line used to call (a
		// second, drifted implementation of the same concern). org_id is
		// normally a different trust class elsewhere in this file
		// (backend-validated, not caller-supplied free text, so logged
		// raw) -- but THIS line's own pre-existing test
		// (TestBoundary_NoRequestDerivedValueCanForgeALogLine,
		// chaos5465_boundary_pins_test.go) already asserts org_id is
		// sanitized here specifically; kept exactly as it already behaved,
		// not widened or narrowed. The closed fields above and below need
		// neither: they can only be a vocabulary member or the
		// unrecognised sentinel.
		"org_id", SanitizeLogAttr(principal.OrgID),
		"source_result_id", SanitizeLogAttr(decision.CarriedContextID()),
		"seed_source", SanitizeLogAttr(closedDecisionToken("seed_source", decision)),
		"family_carried", SanitizeLogAttr(closedDecisionToken("family_carried", decision)),
		"family_fresh", SanitizeLogAttr(closedDecisionToken("family_fresh", decision)),
		"family_accepted", SanitizeLogAttr(closedDecisionToken("family_accepted", decision)),
		"family_source", SanitizeLogAttr(closedDecisionToken("family_source", decision)),
		"continuation_disposition", SanitizeLogAttr(closedDecisionToken("continuation_disposition", decision)),
		"decision_reason", SanitizeLogAttr(closedDecisionToken("decision_reason", decision)),
		"comparison_evaluated", decision.ComparisonEvaluated,
		"agreement", decision.Agreement,
		"conflict_reason", SanitizeLogAttr(closedDecisionToken("conflict_reason", decision)),
		"conflict_count", decision.ConflictCount(),
		"conflict_fields", SanitizeLogAttr(closedDecisionToken("conflict_fields", decision)),
		"applied_window", SanitizeLogAttr(decision.AppliedWindowToken()),
		"carried_context_id", SanitizeLogAttr(decision.CarriedContextID()),
		"fresh_context_id", SanitizeLogAttr(decision.FreshContextID()),
		"accepted_context_id", SanitizeLogAttr(decision.AcceptedContextID()),
		"composition_outcome", SanitizeLogAttr(closedDecisionToken("composition_outcome", decision)),
		"composition_failed_invariant", SanitizeLogAttr(closedDecisionToken("composition_failed_invariant", decision)),
		"refusal_basis", SanitizeLogAttr(closedDecisionToken("refusal_basis", decision)),
		// The carrier the request names, on every decision -- the join from a
		// refusal to the result that could not be verified.
		"referenced_result_id", SanitizeLogAttr(decision.ReferencedResultID),
		"carrier_read", SanitizeLogAttr(closedDecisionToken("carrier_read", decision)),
		// CHAOS-5582: the axis decision's inputs, outcome and result. The two
		// request counts are the values the receipt conflicts veto on; the
		// three axes are fresh (proposed), carried (recorded) and executed
		// (served), explicitly empty when that stage never ran.
		"window_receipt_count", decision.WindowReceiptCount,
		"explicit_window_present", decision.ExplicitWindowPresent,
		"interpreted_axis", SanitizeLogAttr(closedDecisionToken("interpreted_axis", decision)),
		"carried_axis", SanitizeLogAttr(closedDecisionToken("carried_axis", decision)),
		"executed_axis", SanitizeLogAttr(closedDecisionToken("executed_axis", decision)),
		"interpreted_axis_outcome", SanitizeLogAttr(closedDecisionToken("interpreted_axis_outcome", decision)),
	}
	// requestIDLogAttrs already returns its value through SanitizeLogAttr --
	// no second strip needed here.
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric window continuation decision", args...)
}

// validBudgetOverrunOrUnclassified fails closed on a value outside the closed
// vocabulary, so an UNMEASURED overrun cannot reach a log field as an empty
// string that reads like a measurement of nothing.
//
// The zero value of ContextFabricBudgetOverrun is "" -- not `fits`, and not a
// member -- so an arm that published an attempt nobody measured used to emit
// `overrun=` and say nothing at all. That is the same absence-versus-measured
// distinction this package keeps everywhere else; it was simply missing here.
func validBudgetOverrunOrUnclassified(overrun contractsv1.ContextFabricBudgetOverrun) contractsv1.ContextFabricBudgetOverrun {
	if contractsv1.ValidContextFabricBudgetOverrun(overrun) {
		return overrun
	}
	return contractsv1.ContextFabricBudgetOverrun("unclassified")
}

// validNarrationAllocatorOrUnclassified fails closed on a value outside the
// closed vocabulary, so a corrupted or future enum value cannot reach a log
// field as free text.
func validNarrationAllocatorOrUnclassified(allocator CohortDriverNarrationAllocator) CohortDriverNarrationAllocator {
	if ValidCohortDriverNarrationAllocator(allocator) {
		return allocator
	}
	return CohortDriverNarrationAllocator("unclassified")
}

// RecordCohortGroupRead emits the grouped path's own decision about its group
// axis, at Info, on every grouped turn that reached the stage.
//
// The line is written so the decision graph can be rebuilt from it alone:
// `proposed` is the pre-entry state (what grouping produced), `admitted` and
// `denied` are the pre-decision measurement (what the authorizer said),
// `read` and `group_read_refusal` are the decision itself, and
// `facts_returned` is the post-decision result. A reader who has only this
// line can say what was asked, what was allowed, what ran and what came back.
//
// `contract_bound` travels beside `proposed` because a refusal at the edge and
// a refusal far past it are different operational facts, and a reader should
// not have to know this build's constant to tell them apart.
//
// Ids, counts, booleans and closed enums only -- no group ids, no payload.
func (t SlogEngineTelemetry) RecordCohortGroupRead(ctx context.Context, principal storage.Principal, event CohortGroupReadEvent) {
	if t.logger == nil {
		return
	}
	// Routed through the vocabulary's own membership check, not emitted
	// verbatim: a value escaping the closed set reaches a field consumers
	// group on, and free text there is indistinguishable from a member until
	// someone tries to aggregate it.
	refusal := event.Refusal
	if !ValidGroupReadRefusal(refusal) {
		refusal = GroupReadRefusal("unclassified")
	}
	disclosure := event.Disclosure
	if !ValidGroupReadDisclosure(disclosure) {
		disclosure = GroupReadDisclosure("unclassified")
	}
	// request_id rides every line, as it does on the frame-validation line,
	// so each line joins to the turn whose decision graph it belongs to.
	args := []any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"family", string(event.Family),
		"group_kind", string(event.GroupKind),
		"groups_proposed", event.Proposed,
		"groups_admitted", event.Admitted,
		"groups_denied", event.Denied,
		"contract_bound", event.ContractBound,
		"group_read_issued", event.Read,
		"group_read_refused", event.Refused,
		"group_read_refusal", string(refusal),
		"group_facts_returned", event.FactsReturned,
		"group_facts_unadmitted_dropped", event.UnadmittedFactsDropped,
		"group_facts_cap_omitted", event.FactsCapOmitted,
		"group_facts_merged", event.FactsMerged,
		"fact_bundle_cap", event.FactBundleCap,
		// How the proposed set was authorized. A capped call reports the
		// groups past its cap as denied, so `groups_denied` is a statement
		// about authorization only when no call carried more than the batch
		// size -- and these two let a reader check that from this line.
		"authorization_batches", event.AuthorizationBatches,
		"authorization_batch_size", event.AuthorizationBatchSize,
		// What the served document says about this read; through the
		// vocabulary's membership check like the refusal above.
		"group_read_disclosure", string(disclosure),
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric cohort group read", args...)
}

// RecordPlanGroupAxisCollapsed emits the plan seam's I6 refusal, at Info.
//
// The keys that name the failure are the frame-validation line's own
// (`failed_invariant`, `failed_phase`, `failure_detail`, `frame_gate`), so a
// query for an I6 refusal finds both seams with one predicate; `seam` says
// which one refused. The two kinds are the ones that collapsed, captured
// before the plan clears its axis.
//
// Closed enums and kinds only -- no ids, no payload.
func (t SlogEngineTelemetry) RecordPlanGroupAxisCollapsed(ctx context.Context, principal storage.Principal, event PlanGroupAxisCollapsedEvent) {
	if t.logger == nil {
		return
	}
	// The invariant is the field an I6 query groups on, so it goes through
	// the vocabulary's own membership check; phase and detail are emitted
	// exactly as the frame-validation line emits them.
	invariant := event.Failure.Invariant
	if !ValidFrameInvariant(invariant) {
		invariant = FrameInvariant("unclassified")
	}
	args := []any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"family", string(event.Family),
		"seam", "plan",
		// Through the published kind vocabulary, as the interpretation
		// boundary's kinds are: the group kind came from the model's hint,
		// so no model text may reach this line through a kind slot.
		"group_kind", SanitizeLogAttr(closedKindToken(event.GroupKind)),
		"member_kind", SanitizeLogAttr(closedKindToken(event.MemberKind)),
		"failed_invariant", string(invariant),
		"failed_phase", string(event.Failure.Phase),
		"failure_detail", string(event.Failure.Detail),
		"frame_gate", SanitizeLogAttr(event.Gate.Observable()),
		// The WIRE basis the served document discloses, under the key the
		// subjectless terminal already uses for it. Not the gate's own
		// refuse_basis: that names a refused-basis outcome, and on a
		// rejected-invalid gate like this one it reads `none`.
		"refusal_basis", string(event.Gate.RefusalBasis()),
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric plan group axis collapsed", args...)
}

// RecordGroupReadCoverageState emits one read's observation of one coverage
// source, at Info, BEFORE the two reads' coverage is folded.
//
// The fold keeps the worst state per source name, which is right for the
// served answer and lossy for the trace: a group gap erases the member read's
// `available`, and nothing downstream can recover which population the gap was
// in. This line is where that survives. `read` is the discriminator the event
// exists for; without it the two observations are indistinguishable, which is
// exactly the state the merged coverage is in.
//
// At Info deliberately, not Debug: this is a routine, per-turn statement about
// what the server saw, and a reader who has to raise the level to find out
// which population a coverage gap was in cannot answer it about a turn that
// has already happened.
func (t SlogEngineTelemetry) RecordGroupReadCoverageState(ctx context.Context, principal storage.Principal, event GroupReadCoverageStateEvent) {
	if t.logger == nil {
		return
	}
	// Both closed fields go through their own membership checks. A value
	// outside either vocabulary reaches a field consumers group on, and free
	// text there is indistinguishable from a member until someone aggregates.
	arm := event.Read
	if !ValidGroupReadArm(arm) {
		arm = GroupReadArm("unclassified")
	}
	// The PUBLISHED vocabulary, not the provider-legal one: `pruned` is a
	// planner verdict the served coverage carries, and the provider predicate
	// excludes it by design -- which published it as `unclassified`.
	state := event.State
	if !contractsv1.ValidContextFabricSourceState(state) {
		state = SourceState("unclassified")
	}
	// request_id rides every line, as it does on the frame-validation line,
	// so each line joins to the turn whose decision graph it belongs to.
	args := []any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"family", string(event.Family),
		"group_kind", string(event.GroupKind),
		"read", string(arm),
		"source", SanitizeLogAttr(event.Source),
		"source_state", string(state),
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric group read coverage state", args...)
}

// RecordCohortMemberAllowance emits the cohort member allowance and whether it
// was clamped, at Info, on every turn that has a cohort.
//
// `max_items` beside `headroom` is the pair that makes the line worth having:
// an allowance of one is unremarkable under a one-item budget and is a
// reserve swallowing the whole budget under a twenty-item one, and the
// allowance alone cannot tell them apart. `clamped` states which happened
// rather than leaving a reader to redo the subtraction.
//
// `groups` is on the line because `members_after` does not equal `allowance`
// for a grouped cohort: the set cover keeps one member per group, so a cohort
// narrowed to an allowance of one still carries as many members as it has
// groups, and without the group count that looks like the allowance being
// ignored.
func (t SlogEngineTelemetry) RecordCohortMemberAllowance(ctx context.Context, principal storage.Principal, event CohortMemberAllowanceEvent) {
	if t.logger == nil {
		return
	}
	// request_id rides every line, as it does on the frame-validation line,
	// so each line joins to the turn whose decision graph it belongs to.
	args := []any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"family", string(event.Family),
		"group_kind", string(event.GroupKind),
		// The three budget fields derive from the caller's own request
		// options (MaxCohortMembers flows into the allowance), so they cross
		// the one barrier for request-derived integers before they are
		// logged -- see requestDerivedLogInt.
		"max_items", requestDerivedLogInt(event.MaxItems),
		"synthesis_headroom", requestDerivedLogInt(event.Headroom),
		"member_allowance", requestDerivedLogInt(event.Allowance),
		"allowance_clamped", event.Clamped,
		"groups", event.Groups,
		"members_before", event.MembersBefore,
		"members_after", event.MembersAfter,
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric cohort member allowance", args...)
}

// RecordFactRetention emits one retention decision, at Info.
//
// The anchor fields name the committed resolution subjects the pass admitted
// (`anchor_ids`) and every one whose facts it nevertheless dropped
// (`dropped_anchor_ids`, only ever an anchor that was also a removed member).
// Without them a narrowed answer that lost the evidence for a subject the
// question named was indistinguishable, on this line, from one that lost a
// group's: the facts were counted under `dropped_groups` and no field said
// whose they were.
//
// `dropped_groups` is the field this line was added for: a group narrowed out
// of the answer used to keep its evidence, synthesis was handed facts about a
// population the served document did not contain, and evidence closure
// rejected the whole result with nothing anywhere explaining it. A non-zero
// count here is now the visible half of that decision.
//
// `group_kind` travels with it because `dropped_groups` alone is ambiguous: on
// a flat cohort the field is structurally zero, and zero-because-nothing-was-
// dropped and zero-because-there-is-no-group-axis are different facts wearing
// the same number.
func (t SlogEngineTelemetry) RecordFactRetention(ctx context.Context, principal storage.Principal, event FactRetentionEvent) {
	if t.logger == nil {
		return
	}
	// request_id rides every line, as it does on the frame-validation line,
	// so each line joins to the turn whose decision graph it belongs to.
	args := []any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"family", string(event.Family),
		"group_kind", string(event.GroupKind),
		"stage", string(event.Stage),
		"facts_before", event.Decision.FactsBefore,
		"facts_after", event.Decision.FactsAfter,
		"dropped_members", event.Decision.DroppedMembers,
		"dropped_groups", event.Decision.DroppedGroups,
		"retained_groups", event.Decision.RetainedGroups,
		"group_rule_applied", event.Decision.GroupRuleApplied,
		"anchors", len(event.Decision.Anchors),
		"anchor_ids", SanitizeLogStrings(retentionSubjectLogIDs(event.Decision.Anchors)),
		"anchor_facts_retained", event.Decision.AnchorFactsRetained,
		"anchor_facts_dropped", event.Decision.AnchorFactsDropped,
		"dropped_anchor_ids", SanitizeLogStrings(retentionSubjectLogIDs(event.Decision.DroppedAnchors)),
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric fact retention", args...)
}

// retentionSubjectLogIDs renders subjects for the retention line as
// "<kind>/<canonical id>", in the order given. Kind travels with the id
// because a canonical id alone is not unique across kinds.
func retentionSubjectLogIDs(subjects []SubjectRef) []string {
	ids := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		ids = append(ids, string(subject.Kind)+"/"+subject.CanonicalID)
	}
	return ids
}

// observableGroupAxis routes the group-axis decision through its own
// membership check. The zero value -- an event built without a boundary --
// reads `unset`, never a member, so a line from an emitter that did not fill
// the boundary cannot pass for a real decision.
func observableGroupAxis(value GroupAxisDecision) string {
	if value == "" {
		return "unset"
	}
	if !ValidGroupAxisDecision(value) {
		return "unclassified"
	}
	return string(value)
}

// requestDerivedLogInt is the log barrier for an INTEGER that derives from a
// decoded request, returned as the same integer so the field stays a JSON
// number.
//
// An int cannot carry the newline CWE-117 is about, and slog's JSON handler
// would quote one anyway -- but go/log-injection's dataflow has no numeric
// barrier, so a request option that flows into a logged count (the caller's
// MaxCohortMembers into the member allowance) is reported as a forgery path.
// The decimal rendering is passed through SanitizeLogAttr, the package's one
// recognised barrier, and parsed back. For every int the
// round trip is the identity: strconv.Itoa never emits a line break, so the
// strip removes nothing and Atoi always succeeds; the zero on the error arm
// is unreachable and exists only so the function is total.
func requestDerivedLogInt(value int) int {
	parsed, err := strconv.Atoi(SanitizeLogAttr(strconv.Itoa(value)))
	if err != nil {
		return 0
	}
	return parsed
}
