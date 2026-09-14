package contextfabric

import (
	"context"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The window member's per-need confirmation ledger consumer (CHAOS-5734).
//
// WHAT IT REPRODUCES. A fresh winr_ redemption (resolveWindowReceipts)
// applies its option's frozen bounds as the effective evidence window, with
// provenance clarification_confirmed. A remembered window entry carries the
// same bytes (ConfirmedNeedEntry.WindowStart/WindowEnd, the applied relative
// id) and applies the same value -- nothing more.
//
// DECIDED WHERE THE RECEIPT IS. A fresh redemption resolves request-side, in
// canonicalizeEvidenceWindow, before answer reuse and before Interpret. The
// remembered window is decided at that same point and enters the same
// request-side canonicalization (withRememberedWindow), so every consumer a
// receipt-confirmed window reaches, the remembered one reaches identically:
//   - application does not depend on what Interpret infers -- a question
//     whose class has no default window still runs under it;
//   - the reuse key every Save forms carries its frozen-bounds fragment;
//   - an interpretation that moves the axis off current meets the same
//     axis-conflict veto;
//   - the same-conversation window carry and the prior/class defaults stand
//     down, because the turn's window is no longer an inferred default.
//
// WHEN IT APPLIES. Exactly when a fresh winr_ redemption on this request
// would be the turn's window: request-side canonicalization resolved no window
// and vetoed nothing, the request is on the current axis (the only axis a
// window is representable on), and this turn does not state the window itself
// (statedNeedMembers: no explicit evidence_window, no confirmed window
// receipt).
//
// NOT A THIRD WINDOW AUTHORITY. The value applied is a caller's own earlier
// confirmation, identity-checked on admission; it is disclosed the way a
// carried window is (Source=carried) and its canonicalization outcome is
// carried, never receipt_confirmed: no receipt was redeemed on this request,
// so none is claimed at Save.

// ConfirmedNeedLedgerWindowDecision is the closed vocabulary for what the
// window consumer did with an admitted remembered window, once per
// Investigate call that reaches the window carry and holds one.
type ConfirmedNeedLedgerWindowDecision string

const (
	// ConfirmedNeedLedgerWindowNotApplicable: a fresh winr_ redemption would
	// not be this turn's window either -- the turn states or confirms its own
	// window, request-side canonicalization already resolved or vetoed one,
	// or the request is not on the current axis.
	ConfirmedNeedLedgerWindowNotApplicable ConfirmedNeedLedgerWindowDecision = "not_applicable"
	// ConfirmedNeedLedgerWindowApplied: the remembered window became this
	// turn's request-side window.
	ConfirmedNeedLedgerWindowApplied ConfirmedNeedLedgerWindowDecision = "applied"
)

func confirmedNeedLedgerWindowDecisions() []ConfirmedNeedLedgerWindowDecision {
	return []ConfirmedNeedLedgerWindowDecision{ConfirmedNeedLedgerWindowNotApplicable, ConfirmedNeedLedgerWindowApplied}
}

// ValidConfirmedNeedLedgerWindowDecision reports membership.
func ValidConfirmedNeedLedgerWindowDecision(value ConfirmedNeedLedgerWindowDecision) bool {
	for _, member := range confirmedNeedLedgerWindowDecisions() {
		if member == value {
			return true
		}
	}
	return false
}

// confirmedNeedLedgerWindowAbsolute is the telemetry token for a remembered
// window with no relative id (an absolute-bounds option). The bounds
// themselves never reach a log line.
const confirmedNeedLedgerWindowAbsolute = "absolute"

// windowAbsoluteAppliedValuePrefix is the AppliedValue prefix
// windowConfirmedAppliedValue writes for an option with no relative id.
const windowAbsoluteAppliedValuePrefix = "abs:"

// ledgerWindowApplication is decideLedgerWindow's single answer. Every
// consumer -- the effective window, the disclosure and the telemetry line --
// reads it, so none of them can report a different decision than the others.
type ledgerWindowApplication struct {
	// Present is false when the admitted ledger holds no window entry; nothing
	// else in this value is meaningful then, and no event is emitted.
	Present        bool
	Decision       ConfirmedNeedLedgerWindowDecision
	Window         *contractsv1.ContextFabricEffectiveEvidenceWindow
	AppliedValue   string
	SourceResultID string
}

// Applied reports whether the remembered window became this turn's effective
// window.
func (a ledgerWindowApplication) Applied() bool {
	return a.Present && a.Decision == ConfirmedNeedLedgerWindowApplied && a.Window != nil
}

// decideLedgerWindow decides the window consumer, from the admitted ledger,
// the request and its request-side window canonicalization -- the inputs a
// fresh winr_ redemption is decided from, and nothing Interpret produces.
func decideLedgerWindow(ledger confirmedNeedLedgerResult, request InvestigationRequest, canon requestWindowCanonicalization) ledgerWindowApplication {
	entry, ok := rememberedWindowEntry(ledger.Entries)
	if !ok {
		return ledgerWindowApplication{}
	}
	app := ledgerWindowApplication{Present: true, AppliedValue: entry.AppliedValue, SourceResultID: ledger.SourceResultID, Decision: ConfirmedNeedLedgerWindowNotApplicable}
	if rememberedWindowApplies(request, canon) {
		app.Decision = ConfirmedNeedLedgerWindowApplied
		app.Window = rememberedEffectiveWindow(entry)
	}
	return app
}

// rememberedWindowApplies reports whether a fresh winr_ redemption on this
// request would be the turn's window, so the remembered one is: this turn
// states no window of its own, request-side canonicalization resolved and
// vetoed none, and the request is on the current axis.
func rememberedWindowApplies(request InvestigationRequest, canon requestWindowCanonicalization) bool {
	if statedNeedMembers(request, mergeConfirmedMembers(nil, canon.ConfirmedMember))[contractsv1.ContextFabricStructureNeedWindow] {
		return false
	}
	if canon.Veto != windowVetoNone {
		return false
	}
	if canon.Effective != nil {
		return false
	}
	return request.TimeContext.Axis == TemporalCurrent
}

// withRememberedWindow is canon with an applied remembered window as its
// request-side window: the same Effective and the same frozen-bounds reuse-key
// fragment resolveWindowReceipts gives a redeemed option, and no
// ConfirmedMember, because no receipt was redeemed on this request.
func (canon requestWindowCanonicalization) withRememberedWindow(app ledgerWindowApplication) requestWindowCanonicalization {
	if applied := app.Applied(); !applied {
		return canon
	}
	canon.Effective = app.Window
	canon.KeyComponent = windowKeyComponent(*app.Window, windowKeyFrozen)
	canon.KeyEncoding = windowKeyFrozen
	return canon
}

// rememberedWindowEntry returns the admitted ledger's window entry, when it
// has one with a value.
func rememberedWindowEntry(entries []confirmedStructureMember) (confirmedStructureMember, bool) {
	for _, entry := range entries {
		if entry.Member == contractsv1.ContextFabricStructureNeedWindow && entry.AppliedValue != "" {
			return entry, true
		}
	}
	return confirmedStructureMember{}, false
}

// rememberedEffectiveWindow rebuilds the effective window a fresh winr_
// redemption applied from the same persisted values: the relative id (absent
// for an absolute option), the frozen bounds, clarification_confirmed.
func rememberedEffectiveWindow(entry confirmedStructureMember) *contractsv1.ContextFabricEffectiveEvidenceWindow {
	window := &contractsv1.ContextFabricEffectiveEvidenceWindow{
		Start:      cloneWindowBound(entry.WindowStart),
		End:        cloneWindowBound(entry.WindowEnd),
		Provenance: WindowClarificationConfirmed,
	}
	if !strings.HasPrefix(entry.AppliedValue, windowAbsoluteAppliedValuePrefix) {
		window.RelativeID = contractsv1.ContextFabricRelativeWindowID(entry.AppliedValue)
	}
	return window
}

// composeLedgerWindowEntry is the wire disclosure for an applied remembered
// window -- nil for every other decision. Source=carried, the same member/source shape a carried window has: the
// existing provenance vocabulary has no member that distinguishes a ledger
// carry from a chain carry, and none is added.
func composeLedgerWindowEntry(app ledgerWindowApplication) *contractsv1.ContextFabricConfirmedStructureEntry {
	if !app.Applied() {
		return nil
	}
	return &contractsv1.ContextFabricConfirmedStructureEntry{
		Member:        contractsv1.ContextFabricStructureNeedWindow,
		AppliedValue:  app.AppliedValue,
		Source:        contractsv1.ContextFabricStructureSourceCarried,
		PriorResultID: app.SourceResultID,
		Provenance:    contractsv1.ContextFabricStructureClarificationConfirmed,
		Disposition:   contractsv1.ContextFabricStructureDispositionApplied,
	}
}

// observableLedgerWindowValue renders the applied value for the log line: the
// relative id (a closed vocabulary), or the fixed absolute token.
func observableLedgerWindowValue(appliedValue string) string {
	if strings.HasPrefix(appliedValue, windowAbsoluteAppliedValuePrefix) {
		return confirmedNeedLedgerWindowAbsolute
	}
	return appliedValue
}

// recordConfirmedNeedLedgerWindow reports the window consumer's decision --
// only when the admitted ledger held a window entry, so the line's population
// is exactly the turns this consumer decided something for. Called where the
// decision is made, above every exit that can end the turn.
func (e *Engine) recordConfirmedNeedLedgerWindow(ctx context.Context, principal storage.Principal, app ledgerWindowApplication) {
	if e.telemetry == nil || !app.Present {
		return
	}
	e.telemetry.RecordConfirmedNeedLedgerWindow(ctx, principal, app.Decision, app.SourceResultID, observableLedgerWindowValue(app.AppliedValue))
}

// cloneWindowBound copies a window bound so a persisted or remembered value
// never aliases the stored option it was read from.
func cloneWindowBound(bound *time.Time) *time.Time {
	if bound == nil {
		return nil
	}
	copied := *bound
	return &copied
}
