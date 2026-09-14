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
// PRECEDENCE: THE CARRIER WINS. The same-conversation window carry
// (resolveCarriedWindow, chaos4360_carry.go) already reproduces a confirmed
// window from the parent's own EffectiveEvidenceWindow, and it runs under the
// same condition this consumer does (this turn's own window would otherwise
// be an inferred default). When the carrier hits, the ledger applies nothing
// and discloses nothing, so one turn can never carry two window values or two
// window entries. The ledger applies only where the carrier missed.
//
// WHERE THE CARRIER MISSES AND THE LEDGER DOES NOT. Both read the same one
// parent under the same epoch and same-question checks, so the population is
// a parent that confirmed a window without persisting an effective window of
// its own: a structure-veto terminal (structureVetoResult) echoes a window
// redeemed on the same request as applied, saves no EffectiveEvidenceWindow,
// and so leaves the carrier nothing to read at depth zero.
//
// NOT A THIRD WINDOW AUTHORITY. The value applied is a caller's own earlier
// confirmation, identity-checked on admission; it enters where a carried
// window enters and is disclosed the way a carried window is (Source=carried),
// so every downstream consumer of the effective window treats it as a carry.

// ConfirmedNeedLedgerWindowDecision is the closed vocabulary for what the
// window consumer did with an admitted remembered window, once per
// Investigate call that reaches the window carry and holds one.
type ConfirmedNeedLedgerWindowDecision string

const (
	// ConfirmedNeedLedgerWindowNotApplicable: this turn's own window is not an
	// inferred default -- stated or confirmed on this request, or the question
	// has no window axis at all -- so there is no silence to fill.
	ConfirmedNeedLedgerWindowNotApplicable ConfirmedNeedLedgerWindowDecision = "not_applicable"
	// ConfirmedNeedLedgerWindowCarrierPrecedence: the same-conversation window
	// carry hit and supplied the effective window; the ledger stood down.
	ConfirmedNeedLedgerWindowCarrierPrecedence ConfirmedNeedLedgerWindowDecision = "carrier_precedence"
	// ConfirmedNeedLedgerWindowApplied: the carrier missed and the remembered
	// window became the effective window.
	ConfirmedNeedLedgerWindowApplied ConfirmedNeedLedgerWindowDecision = "applied"
)

func confirmedNeedLedgerWindowDecisions() []ConfirmedNeedLedgerWindowDecision {
	return []ConfirmedNeedLedgerWindowDecision{
		ConfirmedNeedLedgerWindowNotApplicable, ConfirmedNeedLedgerWindowCarrierPrecedence, ConfirmedNeedLedgerWindowApplied,
	}
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

// decideLedgerWindow decides the window consumer. effective is this turn's
// effective window BEFORE the carrier replaced it; carry is the carrier's own
// result. Checked in precedence order: a carrier hit first, then whether this
// turn has a silence to fill at all.
func decideLedgerWindow(ledger confirmedNeedLedgerResult, effective *contractsv1.ContextFabricEffectiveEvidenceWindow, carry windowCarryResult) ledgerWindowApplication {
	entry, ok := rememberedWindowEntry(ledger.Entries)
	if !ok {
		return ledgerWindowApplication{}
	}
	app := ledgerWindowApplication{Present: true, AppliedValue: entry.AppliedValue, SourceResultID: ledger.SourceResultID}
	switch {
	case carry.Outcome == WindowCarryHit:
		app.Decision = ConfirmedNeedLedgerWindowCarrierPrecedence
	case effective == nil || effective.Provenance != WindowInferredDefault:
		app.Decision = ConfirmedNeedLedgerWindowNotApplicable
	default:
		app.Decision = ConfirmedNeedLedgerWindowApplied
		app.Window = rememberedEffectiveWindow(entry)
	}
	return app
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
// window -- nil for every other decision, so a carrier-precedence turn keeps
// exactly the one window entry composeCarriedWindowEntry already gives it.
// Source=carried, the same member/source shape a carried window has: the
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
// is exactly the turns this consumer decided something for.
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
