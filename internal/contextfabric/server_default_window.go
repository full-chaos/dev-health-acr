package contextfabric

import (
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-11601 (lead ruling 2026-10-30, option A): an answer on the current
// axis always states the window it speaks for. When the question names no
// period and its window class carries no default (state_snapshot, or no class
// at all), composeEffectiveWindowFor returns nil and the reads use their own
// default. The answer then states that default as server_default:
//
//   - the platform default trailing window (relative_id trailing_90d, start,
//     end) when a read of a kind that counts over a window completed: the
//     devhealthfacts rollups' default_trailing window, [start of today - 90
//     days, start of today) in UTC, the same bounds relativeWindowBounds
//     derives, so the numbers are those of the reads;
//   - otherwise as_of, the instant the state was read, window class
//     state_snapshot: a current-state snapshot has no read window, and
//     stamping 90 days on it would claim one.
//
// It is stamped on the assembled result only. The fact reads, the synthesis
// input, the window gate and the reuse key are those of the request, so no
// number changes. A server_default window carries no caller authority: it is
// never gated, never offered for confirmation and never carried to a later
// turn as a committed window (carriableWindow).

// serverDefaultWindowKinds are the fact kinds whose current-axis read, given
// no evidence window, counts over the platform default trailing window. The
// devhealthfacts provider-set test fails when a provider starts or stops
// reading that window, so this list cannot drift from the providers.
var serverDefaultWindowKinds = [...]FactKind{FactDeployments, FactIncidents, FactInvestment, FactMetrics, FactPullRequests}

// serverDefaultRelativeID is the platform default trailing window: the width
// the default_trailing rollup window and windowDefaultPolicy share.
const serverDefaultRelativeID = RelativeWindowTrailing90D

// ServerDefaultWindowKinds returns the fact kinds that read the platform
// default trailing window when no evidence window is given.
func ServerDefaultWindowKinds() []FactKind {
	return append([]FactKind(nil), serverDefaultWindowKinds[:]...)
}

// serverDefaultWindow returns window unchanged when it is set or the axis is
// not current, and the server_default window the reads used otherwise. read
// holds the kinds whose read completed; readAt is the instant the reads ran.
func serverDefaultWindow(axis TemporalAxis, window *EffectiveEvidenceWindow, read FactReadSubjects, readAt time.Time) *EffectiveEvidenceWindow {
	if window != nil || axis != TemporalCurrent || readAt.IsZero() {
		return window
	}
	for _, kind := range serverDefaultWindowKinds {
		if len(read[kind]) == 0 {
			continue
		}
		start, end, ok := relativeWindowBounds(serverDefaultRelativeID, readAt)
		if !ok {
			// Unreachable: trailing_90d is a registry member. Fall back to the
			// snapshot form rather than state bounds no read used.
			break
		}
		return &EffectiveEvidenceWindow{Start: &start, End: &end, RelativeID: serverDefaultRelativeID, Provenance: WindowServerDefault}
	}
	asOf := readAt.UTC()
	return &EffectiveEvidenceWindow{AsOf: &asOf, WindowClass: contractsv1.ContextFabricWindowClassStateSnapshot, Provenance: WindowServerDefault}
}

// callerWindow reports whether window carries caller or class authority: a
// window the request, the question, a confirmation or a class default
// established. A server_default window is a disclosure of what the reads
// used, never such a window.
func callerWindow(window *EffectiveEvidenceWindow) bool {
	return window != nil && window.Provenance != WindowServerDefault
}
