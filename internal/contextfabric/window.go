package contextfabric

import (
	"context"
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
// receipt-bound echo are W2/W2b), and §3/W4's window-insensitivity
// decisive-gating consumes Provenance but is not implemented here -- W1
// only sets Provenance correctly for W4 to consume later.

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
)

// requestWindowCanonicalization is canonicalizeEvidenceWindow's own
// result: the REQUEST-side half of window canonicalization (design brief
// §1.2 precedence step 1), computed BEFORE tryReuse and BEFORE Interpret.
type requestWindowCanonicalization struct {
	// Effective is set only when a question_stated or clarification_confirmed
	// window fully resolved at this step -- precedence step 1. nil means
	// step 2 (composeEffectiveWindow, post-Interpret) still decides.
	Effective *contractsv1.ContextFabricEffectiveEvidenceWindow
	// KeyComponent is TimeAxisKeyFor's window fragment for Effective --
	// "" whenever Effective is nil. An INFERRED default (step 2) never
	// contributes a KeyComponent; see WindowInferenceVersion's own doc
	// comment on ReuseKey for why that dimension exists instead.
	KeyComponent string
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

	if request.TimeContext.Axis != TemporalCurrent {
		// Window is representable ONLY on the current axis
		// (ContextFabricTimeContext.Validate already refuses an explicit
		// evidence_window here at the wire boundary) -- nothing to
		// canonicalize regardless of any stray PriorWindowReceipts: there
		// is no confirmable window slot on this axis to veto over.
		return requestWindowCanonicalization{BinderProposal: binderProposal}
	}

	if len(request.PriorWindowReceipts) > 0 {
		return e.resolveWindowReceipts(ctx, principal, request, binderProposal)
	}

	if request.TimeContext.EvidenceWindow == nil {
		return requestWindowCanonicalization{BinderProposal: binderProposal}
	}
	effective, ok := e.deriveRequestedWindow(*request.TimeContext.EvidenceWindow, request.Consumer)
	if !ok {
		return requestWindowCanonicalization{Veto: windowVetoConfirmationConflict, BinderProposal: binderProposal}
	}
	return requestWindowCanonicalization{
		Effective:      &effective,
		KeyComponent:   windowKeyComponent(effective),
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
	return requestWindowCanonicalization{
		Effective:      &effective,
		KeyComponent:   windowKeyComponent(effective),
		BinderProposal: binderProposal,
	}
}

// windowsAgree reports whether a receipt-confirmed effective window agrees
// with a caller's own explicit request, beyond ordinary clock skew.
func windowsAgree(effective contractsv1.ContextFabricEffectiveEvidenceWindow, requested contractsv1.ContextFabricRequestedEvidenceWindow) bool {
	if requested.RelativeID != "" {
		return requested.RelativeID == effective.RelativeID
	}
	if requested.Start == nil || requested.End == nil {
		// Nothing concrete to disagree with.
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
func composeEffectiveWindow(interpretation InterpretedQuestion, requestWindow *contractsv1.ContextFabricEffectiveEvidenceWindow, binderProposal WindowBindOutcome, now time.Time) *contractsv1.ContextFabricEffectiveEvidenceWindow {
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
		// constraint.
		return nil
	}
	if binderProposal.Reason == WindowBindRoutedInferred {
		// A guards-passing binder span PROPOSES a RelativeID that
		// overrides the class table's own pick (design brief §1.2) -- it
		// still never mints question_stated authority; the provenance
		// below stays inferred_default either way.
		relativeID = binderProposal.RelativeID
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
func windowCanonicalizationOutcome(canon requestWindowCanonicalization, effective *contractsv1.ContextFabricEffectiveEvidenceWindow) WindowCanonicalizationOutcome {
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
	if effective != nil {
		return WindowCanonicalizationInferredDefault
	}
	return WindowCanonicalizationNone
}

// windowKeyComponent renders effective's TimeAxisKeyFor fragment (design
// brief §5.1's rel:/abs: namespaces): "rel:<id>" for a caller-confirmed
// relative window (including the all_time sentinel, "rel:all_time",
// distinct from no window component at all -- a confirmed all-time answer
// and an unwindowed answer are different commitments and must not share a
// reuse-key row), or "abs:<start_ns>:<end_ns>" for caller-supplied absolute
// bounds. Injective by construction: every rel: row was computed from the
// engine's own single now() derivation, and abs: carries the caller's exact
// nanosecond bounds.
func windowKeyComponent(effective contractsv1.ContextFabricEffectiveEvidenceWindow) string {
	if effective.RelativeID != "" {
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
}

func windowVetoLimitation(veto windowVetoReason) string {
	if limitation, ok := windowVetoLimitations[veto]; ok {
		return limitation
	}
	return windowVetoLimitations[windowVetoConfirmationUnresolved]
}

func windowCanonicalizationOutcomeForVeto(veto windowVetoReason) WindowCanonicalizationOutcome {
	if veto == windowVetoConfirmationConflict {
		return WindowCanonicalizationVetoConflict
	}
	return WindowCanonicalizationVetoUnresolved
}

// windowVetoResult composes the model-free, no_match result for a window
// canonicalization veto (design brief's own closed-failure-table
// discipline, applied to windows) -- reached BEFORE Interpret and BEFORE
// any graph resolution, so there is no real interpretation, subject
// resolution, or graph context to carry: every answer-bearing field is
// empty by construction, mirroring terminalResult's own discipline for the
// subjectless-resolution case (unresolved.go).
//
// AllowClarification is NEVER consulted here (design brief W1 scope,
// "AllowClarification=false per-terminal pins"): every window veto is
// no_match, unconditionally -- there is nothing yet to offer a
// clarification prompt ABOUT.
//
// binding is the SAME ResolvedGraphBinding Investigate resolves before
// this call (needed only to stamp Save's graphEpoch column honestly, since
// binding resolution -- a graph read, never a model call -- has already
// run by the time canonicalizeEvidenceWindow's own veto is known).
func (e *Engine) windowVetoResult(ctx context.Context, principal storage.Principal, request InvestigationRequest, veto windowVetoReason, binding ResolvedGraphBinding) (InvestigationResult, error) {
	if e.telemetry != nil {
		e.telemetry.RecordWindowCanonicalization(ctx, principal, windowCanonicalizationOutcomeForVeto(veto))
	}
	limitation := windowVetoLimitation(veto)
	result := InvestigationResult{
		SchemaVersion: InvestigationResultSchemaV1,
		ResultID:      e.newResultID(),
		RequestID:     request.RequestID,
		GeneratedAt:   e.now().UTC(),
		Status:        InvestigationNoMatch,
		Question:      request.Question,
		Reused:        false,
		Interpretation: InterpretedQuestion{
			Shape:             ShapeOpen,
			RequestedJudgment: windowVetoPlaceholderJudgment,
			TimeContext:       request.TimeContext,
			FactRequirements:  []FactRequirement{},
		},
		SubjectResolution:   SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		DirectJudgment:      "",
		CurrentState:        "",
		StrongestPressures:  []string{},
		Drivers:             []DriverJudgment{},
		RemainingWork:       []Finding{},
		ReadinessGaps:       []Finding{},
		Paths:               []RelationshipPath{},
		Conflicts:           []Finding{},
		Limitations:         []string{limitation},
		EvidenceRefIDs:      []string{},
		ClaimedFacts:        []ClaimedFact{},
		Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		Versions:            e.terminalVersions(),
		DeterministicAnswer: limitation,
		Warnings:            []string{},
	}
	if err := result.Validate(); err != nil {
		return InvestigationResult{}, stageError(StageValidation, fmt.Errorf("%w: %w", ErrInvalidResult, err))
	}
	if e.results != nil {
		// Keyed on the REQUEST context alone (no window key component --
		// unresolved/conflicting receipts never earned one), matching
		// tryReuse's OWN lookup key for this same request: a window veto
		// is never itself a reusable answer (its own status is a refusal,
		// not a judgment), but the key must still agree with what a lookup
		// for this exact request would compute, for the same symmetry
		// reason every other Save call site in this package documents.
		if err := e.results.Save(ctx, principal, result, nil, nil, TimeAxisKeyFor(request.TimeContext), e.reuseRetrievalIdentity, e.reusePromptVersions, e.reuseVersionAuthorities, binding.Epoch); err != nil {
			return InvestigationResult{}, stageError(StagePersistence, fmt.Errorf("save investigation result: %w", err))
		}
	}
	return result, nil
}
