package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3900 W1 (design brief v5.2 §1-§5, as amended by the pivot-intent
// design brief's DP12(b) ruling). This file is the window contract's real
// MECHANISM: Validate/clamp already lives on the contract types
// (internal/contracts/v1/validate_context_fabric_window.go); this file is
// what canonicalizes a request's evidence window BEFORE answer reuse, and
// what composes the EFFECTIVE window once interpretation has run.
//
// SCOPE NOTE (read before extending): W1 ships the underlying mechanism --
// canonicalization, the receipt-resolution-before-reuse ordering, the
// unresolved-receipt veto, the proposal-only binder, TimeAxisKeyFor's
// window dimension, and WindowInferenceVersion's reuse-key dimension. It
// deliberately does NOT ship the disclosure/UX surface built on top of it:
// no fresh WindowClarification is minted for a NON-veto ambiguous window
// (that clarification-offer machinery, EvidenceWindow-on-projection,
// WindowConfirmationMode, and the MCP explicit-value same-response
// receipt-bound echo are W2/W2b). §3/W4's window-insensitivity
// decisive-gating, which consumes Provenance, WAS added to this file later
// by CHAOS-4040 (sol-max ruling 2026-08-21, windowConfirmationRequiredResult
// below) -- W1 itself still only sets Provenance correctly; it does not
// gate on it.

// WindowInferenceVersion (CHAOS-3900 W1) is the ReuseKey.WindowInferenceVersion
// conjunctive dimension's own deployment-current value -- composition wires
// this into EngineOptions.ReuseVersionAuthorities.WindowInferenceVersion,
// mirroring graphrank.IdentityNormalizationVersion's own composition
// precedent exactly (internal/runtime/hosted/open.go). Bump this whenever
// the window class vocabulary, the class-to-default table
// (windowDefaultPolicy), or the binder's post-pass rules change in any way
// that could change what INFERRED window an otherwise-identical question
// receives.
const WindowInferenceVersion = "win_v1"

// windowDefaultPolicy pins the DW0-ruled default-width candidate (design
// brief §9 decision matrix row DW0, chris: "quarter-to-year... quarter is
// the tighter cardinality lever") as the ONE policy that actually ships in
// W1 -- W0's shadow harness measured both WindowDefaultPolicy90D and
// WindowDefaultPolicy365D; this is the ruled winner.
var windowDefaultPolicy = WindowDefaultPolicy90D

// relativeWindowDurations is the closed registry's own DURATION table --
// the ONLY place an absolute bound is derived for a RelativeWindowID,
// always from the engine's own e.now(), never from a caller-supplied
// instant (design brief §5.1). RelativeWindowAllTime deliberately has NO
// entry: it is the typed "no bound at all" sentinel, not a very-wide
// duration.
var relativeWindowDurations = map[RelativeWindowID]time.Duration{
	RelativeWindowTrailing30D:  30 * 24 * time.Hour,
	RelativeWindowTrailing90D:  90 * 24 * time.Hour,
	RelativeWindowTrailing365D: 365 * 24 * time.Hour,
}

// windowRegistryMinDurationFactor is the K in design brief §5.1's registry
// pin: "composition fails at startup if any registered relative window's
// duration < K × maxAnswerReuseMaxAge" -- a future narrow registry entry
// (e.g. a hypothetical trailing_48h) would otherwise silently violate the
// assumption every reuse-key window fragment leans on: that a window's own
// width is wide enough relative to the staleness window that watermark/
// epoch invalidation, not window narrowness, is what actually bounds
// answer freshness.
const windowRegistryMinDurationFactor = 7

// ValidateWindowRegistry is the design brief §5.1 registry pin, exposed for
// composition to call once at startup (mirroring how config.go's own
// bounds are asserted at composition time, never lazily on first use). It
// is deliberately NOT called from NewEngine: EngineOptions carries no
// answer-reuse-max-age of its own (that value lives in the Postgres store's
// own configuration, internal/contextfabric/pginvestigation), so the
// composition root -- wherever ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE is
// read -- is what can call this with the SAME value it configures the
// store with.
func ValidateWindowRegistry(maxAnswerReuseMaxAge time.Duration) error {
	if maxAnswerReuseMaxAge <= 0 {
		// Answer reuse is disabled entirely -- the registry pin exists to
		// protect a reuse-key assumption, so it is vacuous when there is no
		// reuse key to protect.
		return nil
	}
	minimum := maxAnswerReuseMaxAge * windowRegistryMinDurationFactor
	for id, duration := range relativeWindowDurations {
		if duration < minimum {
			return &windowRegistryTooNarrowError{id: id, duration: duration, minimum: minimum}
		}
	}
	return nil
}

type windowRegistryTooNarrowError struct {
	id       RelativeWindowID
	duration time.Duration
	minimum  time.Duration
}

func (e *windowRegistryTooNarrowError) Error() string {
	return "context fabric: relative window " + string(e.id) + " duration " + e.duration.String() +
		" is narrower than " + strconv.Itoa(windowRegistryMinDurationFactor) + "x the configured answer-reuse max age (" + e.minimum.String() + ")"
}

// relativeWindowBounds derives absolute [start, end] bounds for id as of
// now -- the ONLY function in this codebase that may do so, and only ever
// from the engine's own now(). ok=false for RelativeWindowAllTime (no
// bounds by definition) or an id outside the closed registry.
func relativeWindowBounds(id RelativeWindowID, now time.Time) (start, end time.Time, ok bool) {
	duration, known := relativeWindowDurations[id]
	if !known {
		return time.Time{}, time.Time{}, false
	}
	now = now.UTC()
	return now.Add(-duration), now, true
}

// withinWindowSkew mirrors futureSkewTolerance's role for the
// RelativeID-vs-explicit-bounds AGREEMENT check (design brief §1.2): a
// caller's own clock is not this service's clock, so a caller ECHOING the
// server's own derivation back is expected to differ by ordinary clock
// skew, never treated as a mismatch for that alone.
func withinWindowSkew(a, b time.Time) bool {
	delta := a.Sub(b)
	if delta < 0 {
		delta = -delta
	}
	return delta <= futureSkewTolerance
}

// windowVetoReason is the closed vocabulary a window canonicalization
// failure resolves to. Every non-empty value here is PINNED to no_match
// regardless of the caller's own Options.AllowClarification (design brief
// W1 scope: "AllowClarification=false per-terminal pins") -- a window
// canonicalization failure never becomes clarification_required.
type windowVetoReason string

const (
	windowVetoNone windowVetoReason = ""
	// windowVetoConfirmationUnresolved: a PriorWindowReceipts entry could
	// not be resolved/applied -- the named prior result could not be
	// loaded, carried no WindowClarification, or named no matching option.
	// Full R2-style veto: no reuse lookup, no interpretation-side
	// inference, no answer of any kind is substituted. Recovery is retry
	// the same receipt, or re-ask with an explicit evidence_window.
	windowVetoConfirmationUnresolved windowVetoReason = "window_confirmation_unresolved"
	// windowVetoConfirmationConflict: plural PriorWindowReceipts entries
	// (>=2), or a resolved receipt disagreeing with a request-carried
	// explicit evidence_window beyond ordinary clock skew. Agreeing bounds
	// are fine (receipt provenance wins).
	windowVetoConfirmationConflict windowVetoReason = "window_confirmation_conflict"
	// windowVetoAxisConflict: a question_stated/clarification_confirmed
	// window resolved at precedence step 1 (against the REQUEST's own
	// current-axis TimeContext, the only axis a window can be canonicalized
	// against), but Interpret went on to move the axis away from current.
	// Checked POST-Interpret (Investigate, after clampedInterpretedTime is
	// computed) -- codex review finding (W1 round 1): without this check, a
	// model axis flip silently dropped the confirmed window from the
	// answer (composeEffectiveWindow returns nil off the current-axis
	// gate) while STILL saving/keying the result as if the window applied,
	// with no disclosed reason for either. This veto makes the disagreement
	// a NAMED terminal instead: no answer is synthesized under a window
	// commitment interpretation itself no longer honors.
	windowVetoAxisConflict windowVetoReason = "window_axis_conflict"
	// windowVetoStaleSupersededOffer (CHAOS-4003, mirrors
	// structureVetoStaleSupersededOffer exactly): the (org, PriorResultID,
	// "window") claim this winr_ receipt would redeem was already won by a
	// LATER result that redeemed a window receipt from the SAME prior
	// result -- detected by the SAME StructureSupersessionChecker pre-flight
	// consult canonicalizeStructure's own loop uses (structure.go), just
	// from window's own entry point (resolveWindowReceipts). Prior to
	// CHAOS-4003, window never consulted the P4 claim table at all, so a
	// stale winr_ receipt against a since-superseded prior result was
	// silently honored -- the only one of the four structure-receipt
	// members with that gap. Recovery is retry against the newer result's
	// own fresh window offers, never the same receipt again.
	windowVetoStaleSupersededOffer windowVetoReason = "window_stale_superseded_offer"
)

// windowBindingOutcome is the closed, counted vocabulary
// RecordWindowBinderOutcome reports (design brief §1.2(d)/D2 flow
// diagram), promoted from W0's own WindowBindReason -- see
// chaos3900_window_binder.go. It stays a distinct alias here rather than
// widening WindowBindReason itself, since a future binder outcome that has
// no bearing on telemetry should not be forced to also be a valid
// WindowBindReason value.
type windowBindingOutcome = WindowBindReason

// WindowCanonicalizationOutcome is the closed, content-safe vocabulary
// EngineTelemetry.RecordWindowCanonicalization reports (design brief §1.2's
// window_temporal_reconciliation signal, consolidated into one outcome
// enum -- mirroring AnswerReuseOutcome's own shape rather than several
// separate boolean counters).
type WindowCanonicalizationOutcome string

const (
	// WindowCanonicalizationNone: no window is in play for this
	// investigation at all (non-current axis, or a resolved class that
	// carries no window, e.g. state_snapshot).
	WindowCanonicalizationNone WindowCanonicalizationOutcome = "none"
	// WindowCanonicalizationRequestStated: precedence step 1 resolved a
	// question_stated window from the wire request (surface-appropriate
	// per DP12(b)).
	WindowCanonicalizationRequestStated WindowCanonicalizationOutcome = "request_stated"
	// WindowCanonicalizationReceiptConfirmed: precedence step 1 resolved a
	// clarification_confirmed window via winr_ receipt redemption.
	WindowCanonicalizationReceiptConfirmed WindowCanonicalizationOutcome = "receipt_confirmed"
	// WindowCanonicalizationInferredDefault: precedence step 2 -- no
	// request-side window, so the class-to-default table (optionally
	// overridden by a guards-passing binder proposal) picked one.
	WindowCanonicalizationInferredDefault WindowCanonicalizationOutcome = "inferred_default"
	// WindowCanonicalizationVetoUnresolved/VetoConflict mirror
	// windowVetoReason's own two non-empty values -- see that type's doc
	// comment.
	WindowCanonicalizationVetoUnresolved WindowCanonicalizationOutcome = "veto_unresolved"
	WindowCanonicalizationVetoConflict   WindowCanonicalizationOutcome = "veto_conflict"
	// WindowCanonicalizationVetoStaleSupersededOffer (CHAOS-4003) mirrors
	// windowVetoStaleSupersededOffer -- its own counted outcome, distinct
	// from VetoUnresolved/VetoConflict, matching structure's own dedicated
	// stale_superseded_offer counter (cf_structure_receipt) rather than
	// folding a distinguishable failure mode into an existing bucket.
	WindowCanonicalizationVetoStaleSupersededOffer WindowCanonicalizationOutcome = "veto_stale_superseded_offer"
	// WindowCanonicalizationGatedExplicitUnconfirmed/GatedClassDefault
	// (CHAOS-4040, sol-max ruling 2026-08-21: "GATE ALL INFERRED WINDOWS
	// out of decisive terminals"): the request reached
	// windowConfirmationRequiredResult -- an inferred window (either
	// origin) was disclosed and NOT permitted to drive a decisive
	// terminal. Split by origin, not folded into
	// WindowCanonicalizationInferredDefault, because that value still
	// fires on every inferred window regardless of whether the gate
	// applied (kept for existing consumers) -- these two report
	// specifically that the gate WAS the reason nothing decisive
	// happened, and distinguish precedence step 1 (explicit-unconfirmed,
	// gated before tryReuse/Interpret) from step 2 (class-default, gated
	// after Interpret) for CHAOS-4040's own run-3 non-vacuity bar
	// ("at least one explicit-unconfirmed AND one engine-default case
	// gated").
	WindowCanonicalizationGatedExplicitUnconfirmed WindowCanonicalizationOutcome = "gated_explicit_unconfirmed"
	WindowCanonicalizationGatedClassDefault        WindowCanonicalizationOutcome = "gated_class_default"
	// WindowCanonicalizationGatedRefusedNoClarification (CHAOS-4040) is
	// windowConfirmationRequiredResult's own AllowClarification=false
	// path: the caller declined the only thing a confirmation prompt
	// could ask for (unresolved.go's own established rule, applied here),
	// so the wire terminal is the closed vocabulary's no_match -- but
	// this outcome value keeps that refusal TELEMETRY-DISTINGUISHABLE
	// from a genuine D0 absence (empty candidate pool): the window was
	// never proven unconfirmable-because-nothing-exists, only
	// unconfirmable-because-clarification-was-declined. Never reported as
	// WindowCanonicalizationNone or folded into any veto value -- a
	// window WAS in play and WOULD have been offered.
	WindowCanonicalizationGatedRefusedNoClarification WindowCanonicalizationOutcome = "gated_refused_no_clarification"
	// WindowCanonicalizationCarried (CHAOS-4360) is precedence step 2's
	// OWN carry outcome: no request-side window resolved on THIS turn, but
	// resolveCarriedWindow found one genuinely confirmed on an earlier turn
	// in the same conversation and this call used it in place of a fresh
	// class-table/binder-default guess. Split from
	// WindowCanonicalizationInferredDefault for the same reason the two
	// Gated* values above are split from it: a caller reading this stream
	// needs to tell "guessed" from "inherited a real confirmation" apart,
	// and this is also the value that PROVES the CHAOS-4234 gate did not
	// fire for a request this outcome describes.
	WindowCanonicalizationCarried WindowCanonicalizationOutcome = "carried"
)

// requestWindowCanonicalization is canonicalizeEvidenceWindow's own
// result: the REQUEST-side half of window canonicalization (design brief
// §1.2 precedence step 1), computed BEFORE tryReuse and BEFORE Interpret.
type requestWindowCanonicalization struct {
	// Effective is set whenever precedence step 1 resolved ANY request-side
	// window -- question_stated, clarification_confirmed, OR an MCP bare
	// explicit evidence_window entering at inferred_default per DP12(b)
	// (doc corrected, CHAOS-4040: this previously said "only when
	// question_stated or clarification_confirmed", omitting the third
	// case windowCanonicalizationOutcome's own doc comment already
	// documented correctly -- see that function). nil means step 2
	// (composeEffectiveWindow, post-Interpret) still decides.
	Effective *contractsv1.ContextFabricEffectiveEvidenceWindow
	// ExplicitUnconfirmed (CHAOS-4040) is true iff Effective was resolved
	// at THIS step (precedence step 1) with Provenance==WindowInferredDefault
	// -- i.e. the MCP-bare-explicit-field case, never question_stated or
	// clarification_confirmed. Internal only, no wire/enum exposure (the
	// closed 3-value ContextFabricWindowProvenance vocabulary is
	// unchanged -- sol-max ruling: "origin != authority"): distinguishes,
	// for Investigate's own gating and telemetry, an EXPLICIT-but-
	// unconfirmed caller window (gated before tryReuse/Interpret) from the
	// class-table/binder default step 2 alone can produce (gated later,
	// after Interpret -- see Investigate's own two window-gate call
	// sites). Always false when Effective is nil.
	ExplicitUnconfirmed bool
	// KeyComponent is TimeAxisKeyFor's window fragment for Effective --
	// "" whenever Effective is nil. An INFERRED default (step 2) never
	// contributes a KeyComponent; see WindowInferenceVersion's own doc
	// comment on ReuseKey for why that dimension exists instead.
	KeyComponent string
	// KeyEncoding is WHICH windowKeyEncoding computed KeyComponent --
	// meaningless when KeyComponent == "". tryReuse threads this through to
	// its own recheck (codex review, W1 round 5 consolidation) so the
	// recheck can verify a candidate using the SAME trusted, in-process
	// encoding THIS request's own lookup used, rather than inferring an
	// encoding from the candidate's own stored (untrusted) Provenance --
	// see windowKeyComponent's own doc comment for why that distinction is
	// load-bearing.
	KeyEncoding windowKeyEncoding
	// Veto is non-empty when this request must short-circuit to a no_match
	// terminal WITHOUT reuse, WITHOUT interpretation, and WITHOUT any
	// window inference substituted (see windowVetoReason).
	Veto windowVetoReason
	// BinderProposal is the proposal-only temporal-expression binder's own
	// verdict, computed over the verbatim question text -- carried forward
	// to composeEffectiveWindow regardless of whether a request-side
	// window already resolved at this step, so it is available unconditionally
	// once Interpret returns without a second question-text scan.
	BinderProposal WindowBindOutcome
	// ConfirmedMember (CHAOS-4003) is set only when resolveWindowReceipts
	// redeemed a winr_ receipt (Effective.Provenance == clarification_confirmed)
	// -- window's own confirmedStructureMember, in the EXACT shape
	// canonicalizeStructure's kind/anchor/handle loop already produces
	// (structure.go). Engine merges this into structureCanon's own
	// Confirmed list (mergeConfirmedMembers) before composing
	// ConfirmedStructure, so window rides the SAME
	// composeConfirmedStructure/structureSupersessionClaims/
	// staleConfirmedStructureEntries pipeline those three members already
	// use -- giving window the same P4 atomic-claim staleness guard
	// without inventing a second mechanism. Window's own ELICITATION
	// (winr_ receipts, WindowOption/WindowClarification, this file's own
	// precedence rules) stays entirely separate, per the CHAOS-3984
	// Option A ruling: only this shared storage-layer echo/claim
	// bookkeeping is reused, never the elicitation flow itself.
	ConfirmedMember *confirmedStructureMember
	// StaleEntry (CHAOS-4003) is populated ONLY alongside
	// Veto==windowVetoStaleSupersededOffer -- the single
	// vetoed_stale-dispositioned echo entry for the member the pre-flight
	// StructureSupersessionChecker consult caught, in the exact shape
	// canonicalizeStructure's own pre-flight consult builds (structure.go).
	// windowVetoResult sets result.ConfirmedStructure from this, mirroring
	// structureVetoResult's own echoEntries parameter.
	StaleEntry *contractsv1.ContextFabricConfirmedStructureEntry
}

// canonicalizeEvidenceWindow is the design brief §1.2 window-contract
// mechanism's own entry point, called from Investigate at the SAME point
// as resolveTimeContext's own request clamp -- BEFORE tryReuse, BEFORE
// Interpret. It never returns an error: every failure mode it recognizes is
// a VETO (windowVetoReason), not a Go error, because a window
// canonicalization failure is an ordinary, expected outcome
// (clarification-shaped, always no_match) rather than an exceptional one.
//
// Ordering is the entire point (design brief §1.2, "receipt-resolution-
// BEFORE-reuse"): without it, a follow-up confirming a window via receipt
// would reach tryReuse before the confirmation entered the reuse key, and
// could be served a cached answer generated under the UNCONFIRMED inferred
// default instead -- the confirmation silently dropped. Resolving receipts
// here, before the caller below ever calls tryReuse, closes that.
func (e *Engine) canonicalizeEvidenceWindow(ctx context.Context, principal storage.Principal, request InvestigationRequest) requestWindowCanonicalization {
	// The binder runs over the verbatim question text alone -- no
	// dependency on Interpret -- so it always runs, even when this request
	// carries no window field at all (its RelativeID may still end up
	// unused, at composeEffectiveWindow's own discretion, once state_snapshot
	// or another windowless class is resolved).
	binderProposal := ProposeWindowFromSpans(request.Question)
	if e.telemetry != nil {
		e.telemetry.RecordWindowBinderOutcome(ctx, principal, binderProposal.Reason)
	}

	// PriorWindowReceipts is checked BEFORE the axis gate, deliberately: a
	// receipt can never apply outside the current axis (window is
	// representable ONLY on axis=current), but that is a reason for the
	// receipt to VETO, not a reason to skip it. Codex review finding (W1
	// round 1): the axis gate used to short-circuit BEFORE this check, so a
	// non-current-axis request carrying an unresolvable/inapplicable
	// receipt (the request contract does not forbid PriorWindowReceipts on
	// a non-current axis, only EvidenceWindow) silently proceeded to
	// interpretation and reuse as if no receipt had been sent -- exactly
	// the "no answer of any kind is substituted" invariant this file's own
	// package doc comment promises, broken on this one axis.
	if len(request.PriorWindowReceipts) > 0 {
		if request.TimeContext.Axis != TemporalCurrent {
			return requestWindowCanonicalization{Veto: windowVetoConfirmationUnresolved, BinderProposal: binderProposal}
		}
		return e.resolveWindowReceipts(ctx, principal, request, binderProposal)
	}

	if request.TimeContext.Axis != TemporalCurrent {
		// Window is representable ONLY on the current axis
		// (ContextFabricTimeContext.Validate already refuses an explicit
		// evidence_window here at the wire boundary) -- nothing further to
		// canonicalize.
		return requestWindowCanonicalization{BinderProposal: binderProposal}
	}

	if request.TimeContext.EvidenceWindow == nil {
		return requestWindowCanonicalization{BinderProposal: binderProposal}
	}
	effective, ok := e.deriveRequestedWindow(*request.TimeContext.EvidenceWindow, request.Consumer)
	if !ok {
		return requestWindowCanonicalization{Veto: windowVetoConfirmationConflict, BinderProposal: binderProposal}
	}
	return requestWindowCanonicalization{
		Effective: &effective,
		// CHAOS-4040: set purely from the just-derived Provenance -- an
		// MCP bare explicit field always resolves here (this branch) with
		// Provenance==inferred_default (windowExplicitProvenance), while a
		// question_stated/clarification_confirmed value can never reach
		// this specific branch with that provenance (deriveRequestedWindow
		// only ever returns inferred_default OR question_stated, and
		// clarification_confirmed only ever resolves via
		// resolveWindowReceipts above, a different return path entirely).
		ExplicitUnconfirmed: effective.Provenance == WindowInferredDefault,
		// ALWAYS keyed, regardless of Provenance tier -- codex review
		// finding (W1 round 3), correcting round 1's own fix #5, which had
		// this backwards. Round 1 reasoned "inferred tier -> no key
		// fragment, WindowInferenceVersion guards it instead" and applied
		// that uniformly to every inferred-tier window; that conflates two
		// different things DP12(b)'s tier concept does NOT conflate:
		// AUTHORITY (does this value get to drive a commit decision --
		// orthogonal to reuse) versus VALUE IDENTITY (does this value need
		// to be part of the cache key -- exactly what reuse cares about).
		// WindowInferenceVersion is a single DEPLOYMENT-WIDE constant: it
		// only guards the class-table/binder's OWN deterministic-from-the-
		// question inference (composeEffectiveWindow's post-Interpret step
		// 2, reached only when NO request-side window resolved at all) --
		// consistent with how a reuse hit already trusts a cached
		// interpretation's own Shape/etc. without re-deriving it (CHAOS-3782's
		// own established tradeoff). A window resolved HERE, at precedence
		// step 1, is never that: it is always a CALLER-SUPPLIED value
		// (explicit field or receipt), arbitrary and independent of the
		// question text, whether or not MCP's own tier rule (DP12(b))
		// grants it decisive authority. Two MCP requests for the identical
		// question but DIFFERENT explicit windows (e.g. trailing_30d vs
		// trailing_90d) must never collapse onto the same "current" reuse
		// key just because both happen to be tier=inferred_default -- that
		// key collision is exactly what round 1's fix (KeyComponent="" for
		// inferred_default here) produced, and what this revert closes.
		// windowKeyRederivable: this window's bounds are re-derived from
		// RelativeID at every call (relativeWindowBounds, above), never a
		// frozen commitment -- see windowKeyRederivable's own doc comment.
		KeyComponent:   windowKeyComponent(effective, windowKeyRederivable),
		KeyEncoding:    windowKeyRederivable,
		BinderProposal: binderProposal,
	}
}

// deriveRequestedWindow canonicalizes a caller's explicit
// ContextFabricRequestedEvidenceWindow into a server-owned Effective window,
// applying the DP12(b) surface split (design brief pivot amendment): on
// MCP, a bare explicit evidence_window carries no decisive authority of its
// own -- it enters at inferred_default -- while every other surface's
// stated-echo semantics (3900 §4, untouched by DP12(b)) grant
// question_stated directly. ok=false means a RelativeID was supplied
// alongside explicit bounds that disagree with the server's own derivation
// beyond ordinary clock skew -- a conflict, never a silent preference of
// one side.
func (e *Engine) deriveRequestedWindow(requested contractsv1.ContextFabricRequestedEvidenceWindow, consumer ConsumerInfo) (contractsv1.ContextFabricEffectiveEvidenceWindow, bool) {
	provenance := windowExplicitProvenance(consumer)
	if requested.RelativeID == RelativeWindowAllTime {
		return contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: RelativeWindowAllTime, Provenance: provenance}, true
	}
	if requested.RelativeID != "" {
		start, end, ok := relativeWindowBounds(requested.RelativeID, e.now())
		if !ok {
			return contractsv1.ContextFabricEffectiveEvidenceWindow{}, false
		}
		if requested.Start != nil && requested.End != nil &&
			(!withinWindowSkew(*requested.Start, start) || !withinWindowSkew(*requested.End, end)) {
			return contractsv1.ContextFabricEffectiveEvidenceWindow{}, false
		}
		return contractsv1.ContextFabricEffectiveEvidenceWindow{Start: &start, End: &end, RelativeID: requested.RelativeID, Provenance: provenance}, true
	}
	start, end := requested.Start.UTC(), requested.End.UTC()
	return contractsv1.ContextFabricEffectiveEvidenceWindow{Start: &start, End: &end, Provenance: provenance}, true
}

// windowExplicitProvenance implements the DP12(b) uniform surface split
// (pivot-intent design brief, ratified 07:28 08-19): tier is a function of
// SURFACE alone. MCP's own bare explicit evidence_window field can never
// itself grant question_stated -- only winr_ receipt redemption
// (resolveWindowReceipts) can. Every other surface keeps 3900 §4's
// ratified stated-echo semantics, untouched by this ruling.
func windowExplicitProvenance(consumer ConsumerInfo) WindowProvenance {
	if strings.TrimSpace(consumer.Surface) == "mcp" {
		return WindowInferredDefault
	}
	return WindowQuestionStated
}

// resolveWindowReceipts implements design brief §5's winr_ redemption path:
// PriorWindowReceipts names a stored ContextFabricWindowClarification's
// own option by (result_id, receipt_id), and redemption applies that
// option's FROZEN bounds byte-for-byte -- never re-derived from RelativeID
// at redemption time. Any failure to fully confirm is a VETO (design brief
// §2.5-style closed failure table applied to windows): no partial
// application, no fallback to inference.
func (e *Engine) resolveWindowReceipts(ctx context.Context, principal storage.Principal, request InvestigationRequest, binderProposal WindowBindOutcome) requestWindowCanonicalization {
	veto := requestWindowCanonicalization{Veto: windowVetoConfirmationUnresolved, BinderProposal: binderProposal}
	if len(request.PriorWindowReceipts) > 1 {
		// Plural receipts: ambiguous by construction, never first-match-wins.
		return requestWindowCanonicalization{Veto: windowVetoConfirmationConflict, BinderProposal: binderProposal}
	}
	if e.results == nil {
		// No store to resolve against -- cannot confirm, so this cannot
		// proceed as if nothing had been asked. Fail closed exactly like
		// an unloadable receipt.
		return veto
	}
	receipt := request.PriorWindowReceipts[0]
	resultID := strings.TrimSpace(receipt.ResultID)
	receiptID := strings.TrimSpace(receipt.ReceiptID)
	if resultID == "" || receiptID == "" {
		return veto
	}
	stored, err := e.results.Get(ctx, principal, resultID)
	if err != nil || stored.Result.WindowClarification == nil {
		return veto
	}
	var option *contractsv1.ContextFabricWindowOption
	for i := range stored.Result.WindowClarification.Options {
		if stored.Result.WindowClarification.Options[i].ReceiptID == receiptID {
			option = &stored.Result.WindowClarification.Options[i]
			break
		}
	}
	if option == nil {
		return veto
	}
	// CHAOS-4003 (design brief §2.1 offer-supersession rule, extended to
	// window): a winr_ receipt that resolves cleanly against its stored
	// offer can still name an offer a LATER result has already redeemed
	// for this same (org, prior result, "window") tuple -- the option
	// lookup above proves the offer's own content still exists, not that
	// it is still the CURRENT one. Consult the SAME claim table
	// canonicalizeStructure's own pre-flight consult uses (structure.go),
	// when the wired store supports it, BEFORE trusting this redemption.
	// checker is a type assertion, not a required dependency
	// (StructureSupersessionChecker's own doc comment) -- absent, this
	// consult is simply skipped and Save's own atomic claim (once this
	// result's ConfirmedMember reaches it) remains the sole enforcement
	// point.
	if checker, ok := e.results.(StructureSupersessionChecker); ok {
		superseded, err := checker.IsStructureSuperseded(ctx, principal.OrgID, resultID, contractsv1.ContextFabricStructureNeedWindow)
		if err != nil || superseded {
			// Fail-closed on an authority-relevant read, exactly like
			// canonicalizeStructure's own rule: an unreadable claim table
			// is treated identically to a confirmed-stale claim, never as
			// "assume fresh."
			stale := contractsv1.ContextFabricConfirmedStructureEntry{
				Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: windowConfirmedAppliedValue(*option),
				Source: contractsv1.ContextFabricStructureSourceReceipt, PriorResultID: resultID, ReceiptID: receiptID,
				OfferSource: contractsv1.ContextFabricStructureOfferEngine,
				Provenance:  contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionVetoedStale,
			}
			return requestWindowCanonicalization{Veto: windowVetoStaleSupersededOffer, StaleEntry: &stale, BinderProposal: binderProposal}
		}
	}
	effective := contractsv1.ContextFabricEffectiveEvidenceWindow{
		Start: option.Start, End: option.End, RelativeID: option.RelativeID,
		Provenance: WindowClarificationConfirmed,
	}
	if explicit := request.TimeContext.EvidenceWindow; explicit != nil {
		// A caller who ALSO sent an explicit evidence_window alongside the
		// receipt must agree with what the receipt confirms, beyond
		// ordinary clock skew -- agreeing bounds are fine, and receipt
		// provenance wins (design brief §5). Disagreement is a conflict,
		// never a silent preference of one side.
		if !windowsAgree(effective, *explicit) {
			return requestWindowCanonicalization{Veto: windowVetoConfirmationConflict, BinderProposal: binderProposal}
		}
	}
	confirmedMember := &confirmedStructureMember{
		Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: windowConfirmedAppliedValue(*option),
		PriorResultID: resultID, ReceiptID: receiptID, OfferSource: contractsv1.ContextFabricStructureOfferEngine,
	}
	// CHAOS-4314: this SAME receiptID redeems byte-identically whether it
	// was offered as a plain WindowOption or ALSO annotated as the
	// window_expand recommendation (composeWindowExpandOption deliberately
	// copies, never mints fresh -- see that function's own doc comment), so
	// the only way to tell "accepted" apart from an ordinary window
	// confirmation is to check whether the STORED result's own
	// StructureNeeds.WindowExpandOptions named this receipt. Checked only on
	// the full-success path (not the stale-superseded veto branch above):
	// "accepted" is a claim about a redemption that actually applied.
	if e.telemetry != nil && stored.Result.StructureNeeds != nil {
		for _, expand := range stored.Result.StructureNeeds.WindowExpandOptions {
			if expand.ReceiptID == receiptID {
				e.telemetry.RecordWindowExpandOfferRedeemed(ctx, principal)
				break
			}
		}
	}
	return requestWindowCanonicalization{
		Effective:       &effective,
		ConfirmedMember: confirmedMember,
		// windowKeyFrozen, NOT windowKeyRederivable -- codex review finding
		// (W1 round 4): a receipt confirms one specific, already-minted
		// option's FROZEN bounds, not a re-derivable-from-RelativeID value;
		// see windowKeyFrozen's own doc comment for why two different prior
		// offers named the same RelativeID must key differently unless
		// their frozen bounds genuinely agree.
		KeyComponent:   windowKeyComponent(effective, windowKeyFrozen),
		KeyEncoding:    windowKeyFrozen,
		BinderProposal: binderProposal,
	}
}

// recordWindowSupersessionRaceTelemetry (CHAOS-4003, codex xhigh review
// finding) corrects RecordWindowCanonicalization's own outcome when a
// window redemption that looked confirmed at the point Investigate/
// terminalResult recorded it (engine.go/unresolved.go's own unconditional
// pre-Save RecordWindowCanonicalization call) is then discovered, at Save
// time, to have lost its atomic claim race -- structureSupersessionVetoResult
// itself stays member-agnostic (it never learns which member is "window"),
// so this lives at the two call sites that DO know, right where they
// dispatch to it. Mirrors structure's own members exactly: THEIR
// confirmation telemetry (recordStructureConfirmationOutcome) is deferred
// until AFTER Save succeeds specifically so it can never say "confirmed"
// about a round that was actually discarded; window's own
// RecordWindowCanonicalization call cannot be deferred the same way (it
// also covers the non-receipt inferred/stated cases, which have no Save
// dependency at all), so this is a targeted correction instead of a
// structural reordering.
func recordWindowSupersessionRaceTelemetry(ctx context.Context, telemetry EngineTelemetry, principal storage.Principal, superseded *ErrStructureOfferSuperseded) {
	if telemetry == nil || superseded == nil {
		return
	}
	for _, member := range superseded.Members {
		if member == contractsv1.ContextFabricStructureNeedWindow {
			telemetry.RecordWindowCanonicalization(ctx, principal, WindowCanonicalizationVetoStaleSupersededOffer)
			return
		}
	}
}

// mergeConfirmedMembers (CHAOS-4003) appends windowConfirmed (when
// non-nil) to structureConfirmed -- the single point every caller composing
// ConfirmedStructure or resolving a Save-time supersession race merges
// window's own confirmed member into structure's list, so both ride the
// SAME composeConfirmedStructure/staleConfirmedStructureEntries functions.
// Always returns a fresh slice, never appends onto structureConfirmed's own
// backing array (structureCanon.Confirmed is read again, unmerged, by
// recordStructureConfirmationOutcome at the SAME call sites -- window
// deliberately does not participate in that receipt-telemetry/
// PendingSelections machinery, which stays scoped to kind/anchor/handle;
// window keeps its own RecordWindowCanonicalization telemetry instead).
func mergeConfirmedMembers(structureConfirmed []confirmedStructureMember, windowConfirmed *confirmedStructureMember) []confirmedStructureMember {
	if windowConfirmed == nil {
		return structureConfirmed
	}
	merged := make([]confirmedStructureMember, 0, len(structureConfirmed)+1)
	merged = append(merged, structureConfirmed...)
	return append(merged, *windowConfirmed)
}

// windowConfirmedAppliedValue (CHAOS-4003) is the confirmedStructureMember/
// ContextFabricConfirmedStructureEntry AppliedValue for a redeemed window
// receipt -- the schema-required (minLength 1) echo of "what value this
// member confirmed," mirroring expected_kind's own AppliedValue (the kind
// string itself).
//
// option.RelativeID is non-empty for every option THIS binary's own
// composeWindowClarification mints (it iterates the closed
// ContextFabricRelativeWindowIDVocabulary, so every minted option carries
// one) -- but option here is read back from a PRIOR persisted result's own
// stored WindowClarification (resolveWindowReceipts' own e.results.Get
// call), which the wire contract does NOT require to have come from
// composeWindowClarification: WindowOption.Validate legally admits an
// explicit-bounds-only option carrying NO relative_id (mirroring
// RequestedEvidenceWindow's own "explicit bounds alone" legality;
// schema_go_bound_agreement_test.go pins this as a valid control). Falling
// back to RelativeID alone would return "" for that shape, and
// ConfirmedStructureEntry.Validate rejects an empty applied_value --
// turning a legally-redeemable receipt into a StageValidation error instead
// of a clean confirm. Fall back to the SAME "abs:start:end" bounds
// encoding windowKeyComponent's own frozen-bounds branch uses, so the
// echo is never empty regardless of which shape minted the stored option.
// Start/End are both non-nil here by construction whenever RelativeID is
// empty (WindowOption.Validate's own "RelativeID empty implies BOTH bounds
// present" rule, already enforced on this option before it could ever have
// been persisted) -- defensively re-checked rather than trusted blindly.
func windowConfirmedAppliedValue(option contractsv1.ContextFabricWindowOption) string {
	if option.RelativeID != "" {
		return string(option.RelativeID)
	}
	if option.Start != nil && option.End != nil {
		return "abs:" + formatUnixNano(*option.Start) + ":" + formatUnixNano(*option.End)
	}
	// Unreachable given WindowOption.Validate's own invariant -- defensive
	// fallback only, matching this file's own "skipped/handled defensively
	// rather than offered with an invalid shape" precedent.
	return "abs:unbounded"
}

// windowsAgree reports whether a receipt-confirmed effective window agrees
// with a caller's own explicit request, beyond ordinary clock skew. BOTH a
// carried RelativeID and carried explicit Start/End are checked when
// present -- the wire contract legally admits a RequestedEvidenceWindow
// carrying a RelativeID TOGETHER WITH explicit bounds (validate_context_fabric_window.go's
// own shape rule only requires them to be internally ordered, never
// mutually exclusive with RelativeID), so a caller could otherwise send a
// RelativeID that happens to match the receipt while ALSO sending explicit
// bounds that contradict it -- Codex review finding (W1 round 1): checking
// RelativeID alone let that contradiction silently pass as "agree".
func windowsAgree(effective contractsv1.ContextFabricEffectiveEvidenceWindow, requested contractsv1.ContextFabricRequestedEvidenceWindow) bool {
	if requested.RelativeID != "" && requested.RelativeID != effective.RelativeID {
		return false
	}
	if requested.Start == nil || requested.End == nil {
		// Nothing concrete to disagree with beyond RelativeID, already
		// checked above.
		return true
	}
	if effective.Start == nil || effective.End == nil {
		return false
	}
	return withinWindowSkew(*requested.Start, *effective.Start) && withinWindowSkew(*requested.End, *effective.End)
}

// composeEffectiveWindow is design brief §1.2 precedence steps 2-3, run
// AFTER Interpret (mirroring where composeTemporalLabel already runs) --
// the ONLY point a window's INFERRED default can be decided, because the
// class-to-default table needs the interpreted Shape and the model's own
// (already-sanitized) WindowClass/WindowConfidence pick, neither of which
// exists before Interpret returns.
//
// requestWindow is precedence step 1's own Effective, if canonicalizeEvidenceWindow
// already resolved one -- when non-nil it is returned UNCHANGED: a
// confirmed or stated window is never overridden by an inferred pick.
//
// The interpreted axis, not the requested one, gates this: an interpreter
// may legitimately move a current-axis request onto a historical axis
// (engine.go's own established precedent), and a window inferred for an
// axis the answer no longer speaks for would be a mismatched, misleading
// disclosure -- so this returns nil whenever the INTERPRETED axis is not
// current, regardless of what precedence step 1 found on the wire request.
// windowPriorProposal (CHAOS-3977 P5, design brief §3.4, DP4(a) site two)
// is Engine's own pre-consulted prior proposal for the inferred-default
// slot -- computed BEFORE composeEffectiveWindow runs (Engine has ctx/
// principal; this function does not), ONLY when precedence steps 1-2 both
// declined to decide (Investigate's own call-site gate, engine.go). OK
// false is the zero value, so an unset windowPriorProposal{} behaves
// exactly like "no prior consulted" -- byte-identical to pre-P5 behavior
// for every caller that does not thread one.
type windowPriorProposal struct {
	RelativeID     RelativeWindowID
	PriorVersionID string
	PriorEntryID   string
	OK             bool
}

func composeEffectiveWindow(interpretation InterpretedQuestion, requestWindow *contractsv1.ContextFabricEffectiveEvidenceWindow, binderProposal WindowBindOutcome, priorWindow windowPriorProposal, now time.Time) *contractsv1.ContextFabricEffectiveEvidenceWindow {
	if interpretation.TimeContext.Axis != TemporalCurrent {
		return nil
	}
	if requestWindow != nil {
		return requestWindow
	}
	outcome := ClassifyWindow(interpretation, interpretation.WindowClass, interpretation.WindowConfidence)
	relativeID, ok := DefaultRelativeID(outcome, windowDefaultPolicy)
	if !ok {
		// state_snapshot, or no class could be determined at all --
		// "refuse to guess" (design brief §2): no window, never a wrong
		// constraint. A prior proposal is deliberately NOT consulted to
		// rescue this case (CHAOS-3977 P5) -- design brief §3.4 names the
		// prior as substituting for "the engine's own fallback... would
		// otherwise guess," not as a second-chance override of a refusal;
		// see windowPriorProposal's own doc comment.
		return nil
	}
	switch {
	case binderProposal.Reason == WindowBindRoutedInferred:
		// A guards-passing binder span PROPOSES a RelativeID that
		// overrides the class table's own pick (design brief §1.2) -- it
		// still never mints question_stated authority; the provenance
		// below stays inferred_default either way. Takes priority over any
		// prior proposal (a deterministic read of THIS question's own text
		// beats a historical aggregate).
		relativeID = binderProposal.RelativeID
	case priorWindow.OK:
		// CHAOS-3977 P5 (design brief §3.4, DP4(a) site two): a prior may
		// propose the RelativeID the class table would otherwise guess --
		// same inferred_default provenance either way (disclosed via
		// cf_prior_consulted telemetry, PriorVersionID/PriorEntryID
		// included -- see priors_consult.go's own scoping note on why this
		// slot's disclosure is telemetry-only in v1, not a new wire field).
		relativeID = priorWindow.RelativeID
	}
	if relativeID == RelativeWindowAllTime {
		return &contractsv1.ContextFabricEffectiveEvidenceWindow{
			RelativeID: relativeID, WindowClass: outcome.Class,
			Provenance: WindowInferredDefault, Confidence: outcome.Confidence,
		}
	}
	start, end, ok := relativeWindowBounds(relativeID, now)
	if !ok {
		// Unreachable in practice: DefaultRelativeID only ever returns a
		// registry member or all_time (handled above), and
		// binderProposal.RelativeID is only ever set from the SAME closed
		// registry (chaos3900_window_binder.go's windowGrammarRegistry).
		// Refuse rather than guess if that invariant is ever violated.
		return nil
	}
	return &contractsv1.ContextFabricEffectiveEvidenceWindow{
		Start: &start, End: &end, RelativeID: relativeID, WindowClass: outcome.Class,
		Provenance: WindowInferredDefault, Confidence: outcome.Confidence,
	}
}

// windowCanonicalizationOutcome classifies the FINAL window outcome for
// EngineTelemetry.RecordWindowCanonicalization, from canonicalization's own
// veto/effective state and, once available, composeEffectiveWindow's own
// result. Classified by canon.Effective.Provenance rather than by WHICH
// step produced it, so an MCP bare explicit evidence_window -- which
// resolves at precedence step 1 but carries inferred_default provenance
// per DP12(b) -- is correctly reported as inferred, not as stated.
// carried is CHAOS-4360's own signal: true iff resolveCarriedWindow (this
// call's own Investigate/terminalResult caller) replaced an otherwise
// inferred_default effective window with one inherited from an earlier turn
// in the same conversation. canon.Effective stays nil either way at THIS
// call site (a carry only ever fires when precedence step 1 resolved
// nothing of its own -- see composeEffectiveWindow's own gate), so without
// this parameter a carried window would fall through to the SAME
// "effective != nil" branch an ordinary class-table/binder-default guess
// does and misreport as inferred_default -- exactly the outcome CHAOS-4040
// exists to distinguish this window FROM.
func windowCanonicalizationOutcome(canon requestWindowCanonicalization, effective *contractsv1.ContextFabricEffectiveEvidenceWindow, carried bool) WindowCanonicalizationOutcome {
	switch canon.Veto {
	case windowVetoConfirmationUnresolved:
		return WindowCanonicalizationVetoUnresolved
	case windowVetoConfirmationConflict:
		return WindowCanonicalizationVetoConflict
	}
	if canon.Effective != nil {
		switch canon.Effective.Provenance {
		case WindowClarificationConfirmed:
			return WindowCanonicalizationReceiptConfirmed
		case WindowQuestionStated:
			return WindowCanonicalizationRequestStated
		default:
			return WindowCanonicalizationInferredDefault
		}
	}
	if carried {
		return WindowCanonicalizationCarried
	}
	if effective != nil {
		return WindowCanonicalizationInferredDefault
	}
	return WindowCanonicalizationNone
}

// windowKeyEncoding is the discriminator EVERY window-key derivation call
// site must supply EXPLICITLY (codex review, W1 round 5 consolidation,
// replacing four bespoke per-source-path derivations across rounds 1-4
// with this ONE canonical function). The discriminator always comes from
// in-process, TRUSTED knowledge of which derivation path produced a
// window -- at mint time, the call site simply knows; at tryReuse's own
// recheck time, it is THIS SAME REQUEST's own requestWindowCanonicalization.KeyEncoding,
// never inferred from a candidate's stored (and therefore, per the "prove
// it, don't assume it" discipline every reuse recheck in this package
// already follows, UNTRUSTED) Provenance field. This is the fix for the
// round-5 finding that provenance-based dispatch could pick the wrong
// encoding for a malformed or foreign-written row.
type windowKeyEncoding int

const (
	// windowKeyRederivable: the window's bounds are DETERMINISTICALLY
	// RE-DERIVABLE from RelativeID alone at any later moment
	// (deriveRequestedWindow's own RelativeID branch; composeEffectiveWindow's
	// inferred-default path, which contributes no key fragment at all --
	// see WindowInferenceVersion's own doc comment for why that path needs
	// no encoding here). The key DROPS the specific Start/End this call
	// happened to compute and uses RelativeID alone, mirroring
	// reuseCurrentAxisKey's own "as of whenever you answer this" precedent
	// (answer_reuse.go): two "trailing_90d" requests asked at different
	// wall-clock moments mean the SAME standing commitment and must share
	// a reuse key -- safe because the staleness window (TRD §19.7.3
	// condition 4), already enforced for EVERY reuse candidate regardless
	// of window, is what bounds how much a "trailing_90d" window's actual
	// bounds may have drifted between when a candidate was generated and
	// when it is served.
	windowKeyRederivable windowKeyEncoding = iota
	// windowKeyFrozen: the window's bounds are ONE SPECIFIC, already-minted
	// commitment (resolveWindowReceipts) that must NEVER be treated as
	// interchangeable with a different commitment merely because both
	// happen to share a RelativeID -- two different prior
	// WindowClarification offers named "trailing_90d" but minted (and
	// frozen) at different wall-clock moments freeze DIFFERENT absolute
	// bounds and are different answers.
	windowKeyFrozen
)

// windowKeyComponent is the ONE canonical window reuse-key fragment
// derivation (design brief §5.1's rel:/abs: namespaces), covering every
// window source through the SAME function rather than four independently-
// maintained ones. Behavior:
//
//   - RelativeWindowAllTime: always "rel:all_time", regardless of encoding
//     -- there are no bounds to freeze or drift, so no ambiguity exists to
//     guard against either way. Distinct from no window component at all:
//     a confirmed all-time answer and an unwindowed answer are different
//     commitments and must not share a reuse-key row.
//   - windowKeyRederivable with a RelativeID: "rel:<id>" (bounds dropped).
//   - windowKeyFrozen, or windowKeyRederivable with NO RelativeID (an
//     explicit caller-supplied absolute range carries no RelativeID to
//     abbreviate by): "abs:<start_ns>:<end_ns>" when bounds are present,
//     "" when they are not (a malformed/incomplete value contributes no
//     fragment rather than a misleading one).
//
// Injectivity holds WITHIN each encoding's own equivalence: two
// windowKeyFrozen (or bare-absolute) values produce the same fragment iff
// their bounds are byte-identical; two windowKeyRederivable values with a
// RelativeID produce the same fragment iff the RelativeID is identical (by
// design -- see windowKeyRederivable's own doc comment for why collapsing
// distinct-but-re-derivable bounds is the INTENDED behavior, not a gap).
// Mixing encodings for the SAME underlying bounds is exactly what the
// windowKeyEncoding parameter exists to prevent: a caller can never
// accidentally derive the "rel:" abbreviation for a value this package
// knows is actually frozen, because the encoding is passed explicitly by
// callers who know which derivation path they are on, not inferred from
// the value itself.
func windowKeyComponent(effective contractsv1.ContextFabricEffectiveEvidenceWindow, encoding windowKeyEncoding) string {
	if effective.RelativeID == RelativeWindowAllTime {
		return "rel:all_time"
	}
	if encoding == windowKeyRederivable && effective.RelativeID != "" {
		return "rel:" + string(effective.RelativeID)
	}
	if effective.Start == nil || effective.End == nil {
		return ""
	}
	return "abs:" + formatUnixNano(*effective.Start) + ":" + formatUnixNano(*effective.End)
}

func formatUnixNano(value time.Time) string {
	return strconv.FormatInt(value.UTC().UnixNano(), 10)
}

// composeTimeAxisKey appends a REQUEST-side window's reuse-key fragment
// (windowKey, from windowKeyComponent) onto an axis key TimeAxisKeyFor
// already computed. windowKey == "" (or axisKey == "", TimeAxisKeyFor's own
// fail-closed "never reusable" sentinel) leaves axisKey byte-identical to
// what TimeAxisKeyFor alone would have produced -- the ordinary case for
// every investigation with no request-side window in play, which is every
// investigation before CHAOS-3900 W1 and the overwhelming majority after it
// too. An INFERRED window never reaches this function at all -- see
// ReuseKey.WindowInferenceVersion's own field doc comment for why that
// dimension exists instead of a key fragment for the inferred case.
func composeTimeAxisKey(axisKey, windowKey string) string {
	if windowKey == "" || axisKey == "" {
		return axisKey
	}
	return axisKey + "+w:" + windowKey
}

// windowVetoPlaceholderJudgment is the fixed, non-interpolated
// RequestedJudgment a window-veto terminal's placeholder Interpretation
// carries (contract-required, non-empty) -- reached before Interpret ever
// ran, so there is no real interpreted judgment to report. Names no
// subject, term, or error text.
const windowVetoPlaceholderJudgment = "evidence window confirmation"

// windowVetoLimitations are the fixed, non-interpolated disclosures for
// each windowVetoReason -- answer-facing prose naming no subject, term, or
// error text, matching this codebase's existing terminal-limitation
// discipline (unresolved.go's clarificationRequiredLimitation and
// siblings).
var windowVetoLimitations = map[windowVetoReason]string{
	windowVetoConfirmationUnresolved: "A referenced evidence-window confirmation could not be resolved, so no canonical facts were read. Retry with the same receipt, or re-ask with an explicit evidence window.",
	windowVetoConfirmationConflict:   "The evidence-window confirmation conflicted with another window signal on this request, so no canonical facts were read. Resend a single, agreeing window confirmation.",
	windowVetoAxisConflict:           "The confirmed evidence window no longer applies once the question was understood to be about a different time axis, so no canonical facts were read. Re-ask without a window confirmation, or confirm the window for a current-state question.",
	windowVetoStaleSupersededOffer:   "A referenced evidence-window confirmation names an offer a newer result has already superseded, so no canonical facts were read. Re-ask without a window confirmation to receive fresh window offers.",
}

func windowVetoLimitation(veto windowVetoReason) string {
	if limitation, ok := windowVetoLimitations[veto]; ok {
		return limitation
	}
	return windowVetoLimitations[windowVetoConfirmationUnresolved]
}

func windowCanonicalizationOutcomeForVeto(veto windowVetoReason) WindowCanonicalizationOutcome {
	switch veto {
	case windowVetoConfirmationConflict, windowVetoAxisConflict:
		return WindowCanonicalizationVetoConflict
	case windowVetoStaleSupersededOffer:
		return WindowCanonicalizationVetoStaleSupersededOffer
	default:
		return WindowCanonicalizationVetoUnresolved
	}
}

// windowVetoResult composes the model-free, no_match result for a window
// canonicalization veto (design brief's own closed-failure-table
// discipline, applied to windows) -- every answer-bearing field is empty by
// construction, mirroring terminalResult's own discipline for the
// subjectless-resolution case (unresolved.go).
//
// interpretation is nil for the PRE-Interpret vetoes (unresolved/conflict,
// reached before Interpret ever ran -- a placeholder Interpretation is
// synthesized, mirroring the request's own TimeContext) and non-nil for the
// POST-Interpret windowVetoAxisConflict case, where a real interpretation
// already exists and must be reported verbatim rather than replaced with a
// placeholder.
//
// AllowClarification is NEVER consulted here (design brief W1 scope,
// "AllowClarification=false per-terminal pins"): every window veto is
// no_match, unconditionally.
//
// binding is the SAME ResolvedGraphBinding Investigate resolves before
// this call (needed only to stamp Save's graphEpoch column honestly, since
// binding resolution -- a graph read, never a model call -- has already
// run by the time any window veto is known, pre- or post-Interpret).
// composeWindowClarification (CHAOS-3900 W2, design brief §4) mints the
// fresh disclosure W1's own scope note named as NOT YET shipped: "no
// fresh WindowClarification is minted for a NON-veto ambiguous window
// (that clarification-offer machinery... is W2/W2b)". Present whenever
// effective is genuinely INFERRED (Provenance == WindowInferredDefault,
// regardless of which precedence step produced it -- a request-side
// explicit_unattributed evidence_window on the MCP surface and a
// class-table/binder default both qualify), so a caller can confirm a
// window instead of silently trusting the pick. nil when effective is
// nil (no window in play) or already caller-asserted/confirmed
// (Provenance != inferred_default -- nothing to disambiguate).
//
// Offers the SAME closed four-member relative-window registry every
// window mechanism already uses, receipt-bound via structure.go's own
// member-generic minting primitive (mintStructureReceiptID/
// mintStructureOptionID -- "extended to window per the team-lead ruling
// that minting be member-generic so W2's window offers ride this SAME
// primitive", structure.go's own doc comment).
func composeWindowClarification(effective *contractsv1.ContextFabricEffectiveEvidenceWindow, resultID string, now time.Time) *contractsv1.ContextFabricWindowClarification {
	if effective == nil || effective.Provenance != WindowInferredDefault {
		return nil
	}
	registry := contractsv1.ContextFabricRelativeWindowIDVocabulary()
	options := make([]contractsv1.ContextFabricWindowOption, 0, len(registry))
	for _, id := range registry {
		opt := contractsv1.ContextFabricWindowOption{Label: windowOptionLabel(id), RelativeID: id}
		if id != RelativeWindowAllTime {
			start, end, ok := relativeWindowBounds(id, now)
			if !ok {
				// Unreachable given the registry's own closed membership
				// (relativeWindowDurations carries an entry for every
				// non-all_time member) -- skipped defensively rather than
				// offered with unfrozen bounds.
				continue
			}
			opt.Start, opt.End = &start, &end
		}
		content := opt
		content.ReceiptID, content.OptionID = "", ""
		contentStr := structureOfferContent(content)
		opt.ReceiptID = mintStructureReceiptID(contractsv1.ContextFabricStructureNeedWindow, resultID, contentStr)
		opt.OptionID = mintStructureOptionID(contractsv1.ContextFabricStructureNeedWindow, resultID, contentStr)
		options = append(options, opt)
	}
	if len(options) == 0 {
		return nil
	}
	return &contractsv1.ContextFabricWindowClarification{Options: options}
}

// windowOptionLabel renders a server-owned, closed-vocabulary label for
// one window offer -- mirrors graphrank's kindOfferLabel/handleOfferLabel
// discipline exactly: never model- or source-derived prose, a fixed
// sentence per closed-enum member.
func windowOptionLabel(id RelativeWindowID) string {
	switch id {
	case RelativeWindowTrailing30D:
		return "the last 30 days"
	case RelativeWindowTrailing90D:
		return "the last 90 days"
	case RelativeWindowTrailing365D:
		return "the last 365 days"
	case RelativeWindowAllTime:
		return "all time"
	default:
		// Unreachable given ContextFabricRelativeWindowIDVocabulary's own
		// closed membership.
		return string(id)
	}
}

// windowConfirmationNudgeSentence (CHAOS-3900 W2, design brief §4's own
// "prose pattern") is the ONE fixed, closed-vocabulary disclosure
// ContextFabricWindowConfirmationNudge mode appends to Warnings whenever
// the effective window is inferred -- never interpolated with the actual
// window bounds (those already live in EffectiveEvidenceWindow/
// WindowClarification, structured; this sentence is a pointer to them,
// not a restatement).
const windowConfirmationNudgeSentence = "This answer used a default evidence window because none was confirmed -- see window_clarification to pick one."

// appendUniqueWarning appends sentence to warnings unless it is already
// present (a caller that redeemed no fresh receipt and re-hit the SAME
// inferred window on a subsequent turn must not accumulate duplicate
// copies of the identical fixed sentence), and only while the contract's
// own bound still has room -- a Warnings list already at
// ContextFabricWarningsMaxCount from model-authored content must not be
// pushed over the wire bound by this nudge; Validate() would refuse the
// result outright rather than silently truncating, so this checks first
// and simply omits the nudge in that (exceedingly unlikely) case.
func appendUniqueWarning(warnings []string, sentence string) []string {
	for _, w := range warnings {
		if w == sentence {
			return warnings
		}
	}
	if len(warnings) >= contractsv1.ContextFabricWarningsMaxCount {
		return warnings
	}
	return append(warnings, sentence)
}

func (e *Engine) windowVetoResult(ctx context.Context, principal storage.Principal, request InvestigationRequest, veto windowVetoReason, interpretation *InterpretedQuestion, staleEntry *contractsv1.ContextFabricConfirmedStructureEntry, binding ResolvedGraphBinding, priorSubjectReceiptDispositions []contractsv1.ContextFabricPriorSubjectReceiptEntry,
	// explicitStructure (CHAOS-4335): the request's OWN bare
	// ExpectedKinds/SubjectHandles explicit-field echo -- EITHER
	// preInterpretExplicitStructure's cheap, store-free, pre-Interpret result
	// for the veto BEFORE canonicalizeStructure ever runs (engine.go's
	// windowCanon.Veto branch), or structureCanon.Explicit (the already-run
	// canonicalizeStructure's own Explicit field, NEVER its Confirmed field)
	// for the axis-conflict veto (engine.go's windowVetoAxisConflict branch,
	// which fires after canonicalizeStructure has already run). nil means
	// genuinely nothing to echo -- never a signal to skip echoing something
	// that exists.
	//
	// Deliberately typed []explicitStructureMember, not a full
	// requestStructureCanonicalization: this function must never receive
	// receipt-derived Confirmed data on this path (see
	// preInterpretExplicitStructure's own doc comment for why -- a raw
	// Confirmed entry here would risk an unhandled persistence race and
	// silently drop receipt-confirmation telemetry, since this path has
	// neither the decisive path's ErrStructureOfferSuperseded conversion nor
	// its post-Save recordStructureConfirmationOutcome call).
	//
	// KNOWN GAP, out of scope for CHAOS-4335 (team-lead ruling: "veto
	// branches only"): the axis-conflict veto still does not echo window's
	// OWN confirmed member (windowCanon.ConfirmedMember) even when a receipt
	// successfully redeemed it before the axis conflict fired -- unlike the
	// decisive path (engine.go's mergeConfirmedMembers call) and the
	// STRUCTURE-veto path (engine.go's structureVetoResult call site, which
	// folds windowCanon.ConfirmedMember in for the identical "a confirmed
	// member on a DIFFERENT axis must not vanish" reason). Pre-existing,
	// unchanged by this PR; a genuine fix needs the SAME Save-race handling
	// note above, scoped to window's own ConfirmedMember shape.
	explicitStructure []explicitStructureMember,
	// plan is the caller's AnswerPlan, or nil where none exists yet (the
	// pre-interpret call sites). Passed IN rather than stamped by the caller
	// afterwards -- a caller-side stamp lands after this function has
	// measured and persisted. See finalizeServed.
	plan *AnswerPlan,
) (InvestigationResult, error) {
	if e.telemetry != nil {
		e.telemetry.RecordWindowCanonicalization(ctx, principal, windowCanonicalizationOutcomeForVeto(veto))
	}
	limitation := windowVetoLimitation(veto)
	resolvedInterpretation := InterpretedQuestion{
		Shape:             ShapeOpen,
		RequestedJudgment: windowVetoPlaceholderJudgment,
		TimeContext:       request.TimeContext,
		FactRequirements:  []FactRequirement{},
	}
	// timeAxisKeySource is the TimeContext Save/lookup key symmetry is
	// measured against -- the REQUEST context for a pre-Interpret veto
	// (matching tryReuse's own pre-Interpret lookup key for this exact
	// request), or the INTERPRETED context for the post-Interpret axis
	// conflict (the request-side key was already proved, by construction,
	// to be what a fresh tryReuse call for this identical request would
	// compute -- see canonicalizeEvidenceWindow's own ordering comment).
	timeAxisKeySource := request.TimeContext
	if interpretation != nil {
		resolvedInterpretation = *interpretation
		timeAxisKeySource = interpretation.TimeContext
	}
	emptyCoverage := Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}
	result := InvestigationResult{
		SchemaVersion:  InvestigationResultSchemaV1,
		ResultID:       e.newResultID(),
		RequestID:      request.RequestID,
		GeneratedAt:    e.now().UTC(),
		Status:         InvestigationNoMatch,
		Question:       request.Question,
		Reused:         false,
		Interpretation: resolvedInterpretation,
		// CHAOS-3478/CHAOS-3813 (codex round-1 finding): priorSubjectReceiptDispositions
		// is nil for the pre-Interpret veto (windowVetoNone/windowCanon.Veto
		// -- resolvePriorSubjectHints has not run yet at that call site) and
		// composed against a zero-value resolution for the post-Interpret
		// axis-conflict veto (windowVetoAxisConflict -- this veto returns
		// before ResolveSubjects ever runs, so any matched hint reads
		// skipped_failed_reauth, the same "not re-verified this call"
		// convention every other never-resolved terminal uses).
		SubjectResolution:  SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, PriorSubjectReceiptDispositions: priorSubjectReceiptDispositions},
		DirectJudgment:     "",
		CurrentState:       "",
		StrongestPressures: []string{},
		Drivers:            []DriverJudgment{},
		RemainingWork:      []Finding{},
		ReadinessGaps:      []Finding{},
		Paths:              []RelationshipPath{},
		Conflicts:          []Finding{},
		Limitations:        []string{limitation},
		EvidenceRefIDs:     []string{},
		ClaimedFacts:       []ClaimedFact{},
		Coverage:           emptyCoverage,
		// CHAOS-3900 W1 fix (test failure surfaced this): a non-current
		// axis result REQUIRES a Temporal label (ContextFabricInvestigationResult.Validate,
		// AC-3781-2's own invariant) -- reachable here whenever the veto's
		// own TimeContext (request-side for a pre-Interpret veto, or the
		// real interpreted one for windowVetoAxisConflict) is non-current,
		// e.g. TestCHAOS3900_NonCurrentAxisWithWindowReceipt_Vetoes and
		// TestCHAOS3900_AxisConflict_InterpreterFlipVetoesInsteadOfSilentlyDropping.
		// composeTemporalLabel already returns nil on the current axis, so
		// this is a no-op for the ordinary current-axis veto case.
		Temporal:            composeTemporalLabel(resolvedInterpretation, emptyCoverage, ""),
		Versions:            e.terminalVersions(),
		DeterministicAnswer: limitation,
		Warnings:            []string{},
	}
	// CHAOS-4003: mirrors structureVetoResult's own echoEntries handling --
	// present only on the windowVetoStaleSupersededOffer veto (the one
	// reason this exists for), so a caller can tell "member window is why
	// the whole request was rejected" from the response, matching the
	// disclosure discipline every other structure-receipt member's own
	// stale veto already carries.
	//
	// CHAOS-4335: explicitStructure's own kind|anchor|handle echo is
	// concatenated in ADDITION to staleEntry -- the two are about different
	// members by construction (staleEntry is always the window member;
	// explicitStructure never carries one, window is not part of
	// composeConfirmedStructure at all) -- mirroring engine.go's own
	// structureVetoResult call site, which folds windowCanon.ConfirmedMember
	// in alongside a structure veto's own echo for the identical reason: an
	// explicit member on this SAME request must not silently vanish just
	// because a DIFFERENT member is why the whole request was vetoed. See
	// this parameter's own doc comment for why it is Explicit-only, never
	// Confirmed/receipt-derived.
	echo := composeConfirmedStructure(nil, explicitStructure)
	if staleEntry != nil {
		echo = append(echo, *staleEntry)
	}
	if len(echo) > 0 {
		result.ConfirmedStructure = echo
	}
	// CHAOS-4413: this window-veto path is its own independent exit from
	// Investigate, so it stamps completeness itself, immediately before
	// its own Validate -- same placement rule as everywhere else.
	result.Completeness = ComputeAnswerCompleteness(result)
	// CHAOS-4690: same "own independent exit, stamp immediately before its
	// own Validate" placement rule as Completeness above.
	if fallbacks := applyCoverageDisplayLabels(&result); fallbacks > 0 && e.telemetry != nil {
		e.telemetry.RecordEvidenceLabelFallback(ctx, principal, fallbacks)
	}
	// Y3: the FINAL budget assertion, on the document the route will
	// serialize, immediately before Validate -- the same "own independent
	// exit, immediately before its own Validate" placement rule the
	// Completeness and display-label stamps above already follow. Those two
	// sweeps enumerated these exits and neither added the budget stage, so
	// this exit served an unmeasured document. See budget_assertion.go.
	result, err := e.finalizeServed(ctx, principal, BudgetAssertWindowVeto, result, plan, e.effectiveResponseBudget(request))
	if err != nil {
		return InvestigationResult{}, err
	}
	if err := result.Validate(); err != nil {
		return InvestigationResult{}, stageError(StageValidation, fmt.Errorf("%w: %w", ErrInvalidResult, err))
	}
	if e.results != nil {
		// Keyed on the SAME TimeContext tryReuse's own lookup for this
		// exact request would have used (see timeAxisKeySource above) --
		// never on a window key component: a window veto is never itself
		// a reusable answer (its own status is a refusal, not a judgment).
		if err := e.results.Save(ctx, principal, result, nil, nil, TimeAxisKeyFor(timeAxisKeySource), e.reuseRetrievalIdentity, e.reusePromptVersions, e.reuseVersionAuthorities, binding.Epoch); err != nil {
			return InvestigationResult{}, stageError(StagePersistence, fmt.Errorf("save investigation result: %w", err))
		}
	}
	return result, nil
}

// windowConfirmationRequiredLimitation/windowConfirmationRequiredRefusedLimitation
// are the fixed, non-interpolated disclosures windowConfirmationRequiredResult
// uses -- mirroring windowVetoLimitation's own convention (a caller-facing
// sentence naming no mechanism, provider, or model).
const (
	windowConfirmationRequiredLimitation = "This answer requires confirming the evidence window it should read against. Confirm one of the offered options to proceed."
	// windowConfirmationRequiredRefusedLimitation is DELIBERATELY distinct
	// prose from an ordinary no_match's own statusSentence output (never
	// "nothing matched" / absence framing) -- CHAOS-4040 sol-max ruling:
	// "nonliteral/unattested refusal, never D0 absence". The caller
	// declined the only mechanism (a clarification prompt) that could
	// have confirmed the window, so no answer is possible, but that is
	// NOT the same claim as "no such subject/data exists".
	windowConfirmationRequiredRefusedLimitation = "This answer requires confirming the evidence window it should read against, and clarification was not permitted for this request."
)

// windowConfirmationRequiredPlaceholderPrompt is the FIXED, non-interpolated
// SubjectResolution.ClarificationPrompt value windowConfirmationRequiredResult
// sets whenever Status==clarification_required -- ContextFabricInvestigationResult.Validate
// requires a non-empty ClarificationPrompt for that status (the SAME rule
// unresolved.go's own resolveTerminalStatus satisfies for an ordinary
// ambiguous-subject clarification), even though THIS clarification is
// about the evidence window, not the subject -- SubjectResolution carries
// the ONLY ClarificationPrompt field the contract defines; WindowClarification
// is a sibling disclosure block, not a substitute value for this one.
const windowConfirmationRequiredPlaceholderPrompt = "Confirm the evidence window for this answer."

// windowConfirmationRequiredResult (CHAOS-4040, sol-max ruling 2026-08-21,
// "GATE ALL INFERRED WINDOWS out of decisive terminals") is the shared
// confirmation-required terminal BOTH of Investigate's own window gates
// route to: precedence step 1 (explicit-unconfirmed -- an MCP bare
// evidence_window field, gated before tryReuse/Interpret) and step 2
// (class-default -- the class-table/binder default, gated after Interpret,
// before subject resolution/facts/synthesis). Modeled on terminalResult's
// own shape (unresolved.go) -- NOT windowVetoResult above, which
// hardcodes Status=no_match and carries no offers at all -- because this
// IS an ordinary disclosure-bearing non-decisive terminal, structurally
// identical to "stalled on structure clarification", just for window
// instead of kind/anchor/handle: real receipt-bound WindowClarification
// options, zero committed subjects, empty answer-bearing fields, Reused
// always false, persisted so a follow-up winr_ redemption can confirm it.
//
// interpretation is nil for gate 1 (genuinely pre-Interpret -- mirrors
// windowVetoResult's own pre-Interpret branch, a placeholder Interpretation
// is synthesized) and non-nil for gate 2 (interpretation already ran).
// confirmedStructureEcho is nil for gate 1 (structureCanon has not run
// yet either) and structureCanon-derived for gate 2, mirroring
// terminalResult's own echo (a confirmed kind/anchor/handle receipt on
// THIS SAME request must not silently vanish just because window is why
// the request stalled).
//
// Reuse-source-ineligible BY THE SAME MECHANISM windowVetoResult uses
// above: nil watermark/epoch at Save (see that function's own comment --
// "the fail-CLOSED outcome for reuse specifically"). Deliberately NOT
// relying on reuseColumnsFor's existing StructureNeeds/ConfirmedStructure
// check (pginvestigation/store.go): ConfirmedStructure carries nothing in
// the general case (window is not part of composeConfirmedStructure at
// all), and even though StructureNeeds now carries the window member's own
// disclosure (CHAOS-4118: Missing=[window]/WindowOptions, composed just
// below), that field alone still would not catch every non-reusable case
// here (the AllowClarification=false branch carries neither field, yet
// must be exactly as reuse-ineligible as the branch that does) -- the
// explicit nil/nil watermark/epoch is the same simple, local,
// already-proven pattern windowVetoResult already uses for the identical
// need, independent of what either disclosure field carries.
func (e *Engine) windowConfirmationRequiredResult(
	ctx context.Context,
	principal storage.Principal,
	request InvestigationRequest,
	interpretation *InterpretedQuestion,
	effective contractsv1.ContextFabricEffectiveEvidenceWindow,
	structureCanon *requestStructureCanonicalization,
	origin WindowCanonicalizationOutcome,
	binding ResolvedGraphBinding,
	gatedMaterial StructureOfferMaterial,
	// gatedMaterialWindowExpandUnavailable (CHAOS-4336): true iff
	// gatedMaterial's own caller could not learn anything about the
	// current window's content (gate 2's Refused/Disabled/Failed/
	// NotProjected outcomes) -- see composeWindowExpandOption's own doc
	// comment. false for gate 1 (engine.go's explicit-unconfirmed call
	// site, which never attempts an offers-only read at all but still has
	// enough to safely recommend a wider tier) and for gate 2's own
	// Empty/Composed outcomes.
	gatedMaterialWindowExpandUnavailable bool,
	// carriedStructureEntries (codex round 1, finding 1) are the per-axis
	// carry disclosures for this turn. This gate is a TERMINAL like any
	// other, and on the class-default path a carried kind has already
	// shaped gatedMaterial's own offers -- so a result that omitted these
	// would let a carry change what the caller is offered while telling
	// them nothing about it, which is exactly the silent inheritance the
	// carry mechanism exists to avoid. nil at the gate-1 call site, which
	// fires before any carry has been attempted.
	carriedStructureEntries []*contractsv1.ContextFabricConfirmedStructureEntry,
	// priorSubjectReceiptDispositions (CHAOS-3478/CHAOS-3813): the caller's
	// own composePriorSubjectReceiptDispositions output, or nil when this
	// gate fires before resolvePriorSubjectHints has run at all (gate 1 --
	// see that call site's own comment). Attached to subjectResolution
	// below unconditionally so a caller's PriorSubjectReceipts are
	// disclosed on this terminal too, not only on a decisive answer.
	priorSubjectReceiptDispositions []contractsv1.ContextFabricPriorSubjectReceiptEntry,
	// plan is the caller's AnswerPlan, or nil where none exists yet (the
	// pre-interpret call sites). Passed IN rather than stamped by the caller
	// afterwards -- a caller-side stamp lands after this function has
	// measured and persisted. See finalizeServed.
	plan *AnswerPlan,
) (InvestigationResult, error) {
	resolvedInterpretation := InterpretedQuestion{
		Shape:             ShapeOpen,
		RequestedJudgment: windowVetoPlaceholderJudgment,
		TimeContext:       request.TimeContext,
		FactRequirements:  []FactRequirement{},
	}
	// timeAxisKeySource mirrors windowVetoResult's own choice exactly, for
	// the identical reason -- see that function's own comment.
	timeAxisKeySource := request.TimeContext
	if interpretation != nil {
		resolvedInterpretation = *interpretation
		timeAxisKeySource = interpretation.TimeContext
	}
	// structureCanon is nil for gate 1 (this function's own doc comment:
	// canonicalizeStructure has not run yet at that call site -- there is
	// nothing to echo, snapshot, or Save-race against, exactly like
	// windowVetoResult's own pre-Interpret branch). Non-nil for gate 2,
	// where it mirrors terminalResult's own full structure-aware handling
	// (codex xhigh review round 1, confirmed: the FIRST version of this
	// function only echoed ConfirmedStructure and skipped the
	// supersession-race conversion, recordStructureConfirmationOutcome,
	// and StructureOfferSnapshot entirely -- reachable whenever a request
	// carries a valid structure receipt ALONGSIDE an inferred class/binder
	// window).
	var confirmedStructureEcho []contractsv1.ContextFabricConfirmedStructureEntry
	var offerSnapshot []contractsv1.ContextFabricStructureOfferSnapshotEntry
	if structureCanon != nil {
		confirmedStructureEcho = composeConfirmedStructure(structureCanon.Confirmed, structureCanon.Explicit)
		// CHAOS-3900 P1.G: no guard needed -- structureCanon.OfferSnapshot
		// is only ever non-nil alongside structureCanon.Confirmed, see
		// requestStructureCanonicalization's own doc comment (structure.go).
		offerSnapshot = structureCanon.OfferSnapshot
	}
	// Appended OUTSIDE the structureCanon guard: a carry is disclosed even
	// on a turn that confirmed nothing of its own, which is precisely the
	// turn a carry exists for.
	confirmedStructureEcho = appendCarriedStructureEntry(confirmedStructureEcho, carriedStructureEntries...)
	resultID := e.newResultID()
	windowClarification := composeWindowClarification(&effective, resultID, e.now())
	emptyCoverage := Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}

	status := InvestigationClarificationRequired
	limitation := windowConfirmationRequiredLimitation
	subjectResolution := SubjectResolution{
		Candidates: []SubjectCandidate{}, Committed: []SubjectRef{},
		ClarificationPrompt: windowConfirmationRequiredPlaceholderPrompt,
	}
	if !request.Options.AllowClarification {
		// unresolved.go's own established rule, applied here identically:
		// a caller that declined clarification declined the only thing a
		// prompt could ask for -- the closed status vocabulary leaves
		// no_match as the sole non-decisive terminal, but this is NOT a
		// genuine D0 absence (windowConfirmationRequiredRefusedLimitation's
		// own doc comment) and carries no clarification-shaped data at
		// all, exactly like resolveTerminalStatus's AllowClarification=false
		// branch.
		status = InvestigationNoMatch
		limitation = windowConfirmationRequiredRefusedLimitation
		subjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}
		windowClarification = nil
		origin = WindowCanonicalizationGatedRefusedNoClarification
	}
	// CHAOS-3478/CHAOS-3813: disclosed on BOTH branches above, unlike
	// ClarificationPrompt/windowClarification -- a caller's PriorSubjectReceipts
	// disposition is not "clarification-shaped data" the AllowClarification=false
	// rule strips (that rule is about what this terminal OFFERS the caller
	// going forward, not about disclosing what the caller's own receipts did).
	subjectResolution.PriorSubjectReceiptDispositions = priorSubjectReceiptDispositions

	// CHAOS-4118: this terminal returns BEFORE ResolveSubjects has run (this
	// function's own doc comment above), so no kind/anchor/handle candidate
	// pool exists yet -- those three members stay structurally undisclosable
	// here (CHAOS-4119 tracks the separate handle-path gap; not this
	// ticket's scope). The window member's own offer is a different case:
	// composeWindowClarification above already derived it in full from
	// `effective`, which this function always has. Before this fix, that
	// offer reached ONLY the legacy WindowClarification field -- a caller
	// reading StructureNeeds (the CHAOS-3900 W2 unified member surface every
	// other non-decisive terminal discloses through) saw nothing at all on a
	// turn-1 window-gated response, even though the identical offer was
	// sitting right beside it under the old field. nil exactly when
	// windowClarification is nil (AllowClarification=false declined every
	// clarification, window included), mirroring composeWindowClarification's
	// own nil-means-nothing-in-play convention.
	// CHAOS-4234: gatedMaterial (the class-default gate's offers-only
	// resolution; empty for every other origin) composes kind/handle/
	// candidate offers BESIDE the window offer -- composeGatedStructureNeeds
	// reduces to the pre-CHAOS-4234 window-only block when it is empty.
	var structureNeeds *contractsv1.ContextFabricStructureNeeds
	if windowClarification != nil {
		// CHAOS-4171 PR2: applyOfferPhrasing runs the SAME hook
		// unresolved.go's terminalResult uses, so CHAOS-4234's regime-A
		// gated offers get phrasing too -- see its own doc comment
		// (chaos4171_offer_phrasing.go).
		structureNeeds = e.applyOfferPhrasing(ctx, principal, request.RequestID, composeGatedStructureNeeds(gatedMaterial, gatedMaterialWindowExpandUnavailable, resultID, windowClarification.Options, effective))
	}
	// CHAOS-4234 (codex round-1 finding, confirmed): gatedMaterial's
	// AnchorOptions can carry membership-verify (V2) offers -- the SAME
	// anchorOfferMaterial call resolve.go's decisive path already uses,
	// unconditionally part of every ResolveSubjects return, offers-only
	// mode included. Mirrors unresolved.go's terminalResult schema
	// dispatch exactly: gatedMaterial is the zero value (AnchorOptionsRequireV2
	// false) on every OTHER origin this function serves (explicit-
	// unconfirmed, RegimeAOffersDisabled, a failed offers-only read), so
	// this schemaVersion stays V1 there, unchanged from before this
	// ticket.
	schemaVersion := InvestigationResultSchemaV1
	if gatedMaterial.AnchorOptionsRequireV2 {
		schemaVersion = InvestigationResultSchemaV2
	}

	result := InvestigationResult{
		SchemaVersion:           schemaVersion,
		ResultID:                resultID,
		RequestID:               request.RequestID,
		GeneratedAt:             e.now().UTC(),
		Status:                  status,
		Question:                request.Question,
		Reused:                  false,
		Interpretation:          resolvedInterpretation,
		SubjectResolution:       subjectResolution,
		DirectJudgment:          "",
		CurrentState:            "",
		StrongestPressures:      []string{},
		Drivers:                 []DriverJudgment{},
		RemainingWork:           []Finding{},
		ReadinessGaps:           []Finding{},
		Paths:                   []RelationshipPath{},
		Conflicts:               []Finding{},
		Limitations:             []string{limitation},
		EvidenceRefIDs:          []string{},
		ClaimedFacts:            []ClaimedFact{},
		Coverage:                emptyCoverage,
		Temporal:                composeTemporalLabel(resolvedInterpretation, emptyCoverage, ""),
		EffectiveEvidenceWindow: &effective,
		WindowClarification:     windowClarification,
		ConfirmedStructure:      confirmedStructureEcho,
		StructureOfferSnapshot:  offerSnapshot,
		StructureNeeds:          structureNeeds,
		Versions:                e.terminalVersions(),
		DeterministicAnswer:     limitation,
		Warnings:                []string{},
	}
	// CHAOS-4234 (codex round-1 finding, confirmed): terminalVersions()
	// always stamps V1 -- override to the SAME schemaVersion just chosen,
	// mirroring unresolved.go's terminalResult (result.Versions.ContractVersion
	// = schemaVersion) exactly, so a V2 gated result's Versions block
	// agrees with its own SchemaVersion.
	result.Versions.ContractVersion = schemaVersion
	// Codex round-2 finding (confirmed): mirrors the decisive/terminal
	// paths' own ContextFabricWindowConfirmationNudge handling exactly
	// (engine.go, terminalResult) -- a caller that asked to be nudged must
	// still see the nudge sentence on THIS terminal, not only on a decisive
	// one. Guarded the same way those call sites are: nil on the
	// AllowClarification=false branch (windowClarification already nil
	// there), so the sentence never appears without the disclosure it
	// refers to.
	if windowClarification != nil && request.Options.WindowConfirmationMode == contractsv1.ContextFabricWindowConfirmationNudge {
		result.Warnings = appendUniqueWarning(result.Warnings, windowConfirmationNudgeSentence)
	}
	if e.telemetry != nil {
		e.telemetry.RecordWindowCanonicalization(ctx, principal, origin)
	}
	// CHAOS-4118: recordStructureNeedsTelemetry's own nil-means-nothing guard
	// makes this unconditional call a no-op on the AllowClarification=false
	// branch (structureNeeds is nil there) -- same discipline
	// terminalResult's own call (unresolved.go) already applies.
	recordStructureNeedsTelemetry(ctx, e.telemetry, principal, result.StructureNeeds)
	// CHAOS-4314: window_gated_offered/window_gated_silent split -- offered
	// iff this terminal's own StructureNeeds carries a window_expand
	// recommendation (composeGatedStructureNeeds, gate 2 only; gatedMaterial
	// is always the zero value on gate 1/refused/disabled/failed origins, so
	// this is silent=true there by construction, same scope CHAOS-4234
	// itself shipped). Called unconditionally for every window-gated
	// terminal, exactly like RecordWindowCanonicalization just above, so a
	// zero offered rate is as visible as a nonzero one.
	if e.telemetry != nil {
		offered := structureNeeds != nil && len(structureNeeds.WindowExpandOptions) > 0
		e.telemetry.RecordWindowGateOfferDisclosure(ctx, principal, offered)
	}
	// CHAOS-4234 (codex round-1 finding, confirmed): ValidateResult, not
	// result.Validate() directly -- the latter is UNCONDITIONALLY the V1
	// validator regardless of result.SchemaVersion (ValidateResult's own
	// doc comment: "every storage adapter's Save must use this instead of
	// calling result.Validate() directly, or a genuinely valid v2 result
	// would be rejected"). A V2 gated result (membership-verify anchor
	// offers) would fail V1's own AnchorOptions shape check here
	// otherwise. Mirrors terminalResult's identical dispatch (unresolved.go).
	// CHAOS-4413: this window-gated terminal path is its own independent
	// exit from Investigate, so it stamps completeness itself, immediately
	// before its own Validate -- same placement rule as everywhere else.
	result.Completeness = ComputeAnswerCompleteness(result)
	// CHAOS-4690: same "own independent exit, stamp immediately before its
	// own Validate" placement rule as Completeness above.
	if fallbacks := applyCoverageDisplayLabels(&result); fallbacks > 0 && e.telemetry != nil {
		e.telemetry.RecordEvidenceLabelFallback(ctx, principal, fallbacks)
	}
	// Y3: the FINAL budget assertion, on the document the route will
	// serialize, immediately before Validate -- the same "own independent
	// exit, immediately before its own Validate" placement rule the
	// Completeness and display-label stamps above already follow. Those two
	// sweeps enumerated these exits and neither added the budget stage, so
	// this exit served an unmeasured document. See budget_assertion.go.
	result, err := e.finalizeServed(ctx, principal, BudgetAssertWindowConfirmationRequired, result, plan, e.effectiveResponseBudget(request))
	if err != nil {
		return InvestigationResult{}, err
	}
	if err := ValidateResult(result); err != nil {
		return InvestigationResult{}, stageError(StageValidation, fmt.Errorf("%w: %w", ErrInvalidResult, err))
	}
	if e.results != nil {
		if err := e.results.Save(ctx, principal, result, nil, nil, TimeAxisKeyFor(timeAxisKeySource), e.reuseRetrievalIdentity, e.reusePromptVersions, e.reuseVersionAuthorities, binding.Epoch); err != nil {
			// CHAOS-3927 P4 (codex xhigh review round 1, confirmed): a
			// gate-2 Save carrying confirmed structure can lose the SAME
			// atomic claim race every other structure-bearing Save call
			// site already handles (engine.go's own decisive path,
			// terminalResult) -- surfacing it as a raw persistence error
			// here instead of the established stale_superseded_offer veto
			// terminal would be the ONE call site round 2's own fix left
			// unconverted. Unreachable when structureCanon is nil (gate 1
			// never carries confirmed structure to race over).
			if structureCanon != nil {
				var superseded *ErrStructureOfferSuperseded
				if errors.As(err, &superseded) {
					recordWindowSupersessionRaceTelemetry(ctx, e.telemetry, principal, superseded)
					// CHAOS-3478 (codex round-2 finding): result.SubjectResolution
					// already carries this call's own priorSubjectReceiptDispositions
					// parameter -- the race terminal must not silently drop it.
					return e.structureSupersessionVetoResult(ctx, principal, request, structureCanon.Confirmed, superseded, binding, result.SubjectResolution.PriorSubjectReceiptDispositions, carriedStructureEntries, plan)
				}
			}
			return InvestigationResult{}, stageError(StagePersistence, fmt.Errorf("save investigation result: %w", err))
		}
		if structureCanon != nil {
			// Mirrors terminalResult's own deferred-until-durable success
			// telemetry exactly (unresolved.go) -- cf_structure_explicit/
			// cf_structure_receipt and pending selection events must never
			// fire for a save this function's own race-conversion above
			// just discarded.
			e.recordStructureConfirmationOutcome(ctx, principal, request, *structureCanon)
		}
	}
	return result, nil
}

// factReadQuestion is the interpretation the canonical fact read receives:
// the model's own interpretation, with the SERVER-canonicalized evidence
// window written onto its TimeContext (codex R4 finding 2 / CHAOS-4464).
//
// The interpretation model never emits an evidence window --
// interpretationOutput.toDomain (genkitruntime) populates none -- so
// buildFactQuery's FactQuery.Time carried no bounds for any request whose
// window authority came from a relative_id, a CHAOS-4360 carried window or
// a redeemed window receipt. A provider handed no bounds has to fall back
// to its own default, so the window an answer ADVERTISES
// (EffectiveEvidenceWindow, composed from exactly this value) and the
// window its evidence was actually read over could differ silently --
// devhealthfacts' repository metrics series read its own trailing 90 days
// under an answer claiming trailing_30d.
//
// effective is copied VERBATIM, never re-derived: relativeWindowBounds is
// "the ONLY function in this codebase that may" turn a RelativeWindowID
// into absolute bounds, and it has already run to produce this value. A nil
// effective (no current axis, or a class carrying no window at all) writes
// nothing -- a fact read with no canonical window keeps whatever axis bound
// it already had, and no window is invented for it. The all_time sentinel
// carries its RelativeID with no bounds, exactly as it does upstream, so a
// provider can tell "all of history" apart from "no window given".
func factReadQuestion(interpretation InterpretedQuestion, effective *contractsv1.ContextFabricEffectiveEvidenceWindow) InterpretedQuestion {
	if effective == nil {
		return interpretation
	}
	question := interpretation
	question.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{
		Start: effective.Start, End: effective.End, RelativeID: effective.RelativeID,
	}
	return question
}
