package v1

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrEvidenceWindowAxisInvalid identifies a request whose EvidenceWindow is
// illegal for its own TimeContext.Axis (CHAOS-3900 W1, design brief §1.2):
// a window is representable ONLY on the current axis -- every other axis's
// own Start/End/AsOf already IS the window that axis answers for, so a
// window alongside one is a named invariant violation, never silently
// dropped. errors.Is-matchable so a caller (the API boundary's
// evidence_window_axis_rejected counter, internal/api/context_fabric_routes.go)
// can classify this specific rejection without string-matching the message.
var ErrEvidenceWindowAxisInvalid = errors.New("context fabric evidence window is not legal for this time axis")

// validate checks w's own SHAPE -- representable instants, ordering, and the
// RelativeWindowAllTime biconditional -- independent of the axis it is
// attached to (the axis-legality check lives on ContextFabricTimeContext.Validate,
// which is what knows the axis) and independent of "now" (the
// RelativeID-vs-explicit-bounds AGREEMENT check needs the server's own
// derivation and so lives in internal/contextfabric's
// canonicalizeEvidenceWindow, not here). Every non-sentinel window must
// carry BOTH bounds: no partial window is representable.
func (w ContextFabricRequestedEvidenceWindow) validate() error {
	for _, instant := range []*time.Time{w.Start, w.End} {
		if instant != nil && !instant.IsZero() && !representableInstant(*instant) {
			return fmt.Errorf("evidence window instant is outside the representable range")
		}
	}
	if w.RelativeID != "" && !ValidContextFabricRelativeWindowID(w.RelativeID) {
		return fmt.Errorf("evidence window relative_id is invalid")
	}
	if w.RelativeID == ContextFabricRelativeWindowAllTime {
		if w.Start != nil || w.End != nil {
			return fmt.Errorf("evidence window all_time must not carry explicit bounds")
		}
		return nil
	}
	if w.Start == nil && w.End == nil {
		if w.RelativeID == "" {
			return fmt.Errorf("evidence window requires a relative_id or explicit bounds")
		}
		return nil
	}
	if w.Start == nil || w.End == nil || w.Start.IsZero() || w.End.IsZero() || w.End.Before(*w.Start) {
		return fmt.Errorf("evidence window requires an ordered start and end")
	}
	return nil
}

// validate checks e's own wire shape: representable/ordered bounds (or the
// all_time biconditional, sharing ContextFabricRequestedEvidenceWindow's
// rule since the two types carry the identical bounds shape), a valid
// Provenance (required -- every effective window states how it was
// established), and a WindowClass/Confidence that are either absent or
// closed-vocabulary members.
func (e ContextFabricEffectiveEvidenceWindow) validate() error {
	requested := ContextFabricRequestedEvidenceWindow{Start: e.Start, End: e.End, RelativeID: e.RelativeID}
	if err := requested.validate(); err != nil {
		return err
	}
	if !ValidContextFabricWindowProvenance(e.Provenance) {
		return fmt.Errorf("effective evidence window provenance is invalid")
	}
	if e.WindowClass != "" && !ValidContextFabricWindowClass(e.WindowClass) {
		return fmt.Errorf("effective evidence window class is invalid")
	}
	if e.Confidence != "" && !ValidContextFabricWindowConfidence(e.Confidence) {
		return fmt.Errorf("effective evidence window confidence is invalid")
	}
	return nil
}

// hasWindowReceiptPrefix reports whether id carries the closed winr_
// namespace prefix (ContextFabricWindowOptionReceiptPrefix).
func hasWindowReceiptPrefix(id string) bool {
	return strings.HasPrefix(id, ContextFabricWindowOptionReceiptPrefix)
}

// Validate checks o's own wire-shape bounds: the winr_ namespace prefix
// (enforced at BOTH mint and redemption boundaries -- see
// ContextFabricWindowClarification.Validate, the mint-side caller, and
// PriorWindowReceipts validation, the redemption-side caller), and that any
// carried RelativeID/Start/End are a legitimate, ordered, representable
// window shape.
func (o ContextFabricWindowOption) Validate() error {
	if !stringLengthBetween(o.ReceiptID, 8, 256) || !hasWindowReceiptPrefix(o.ReceiptID) {
		return fmt.Errorf("window option receipt_id must carry the %q namespace prefix and satisfy v1 bounds", ContextFabricWindowOptionReceiptPrefix)
	}
	if !stringLengthBetween(o.OptionID, 1, 256) || !stringLengthBetween(o.Label, 1, 200) {
		return fmt.Errorf("window option option_id or label violates v1 bounds")
	}
	window := ContextFabricRequestedEvidenceWindow{Start: o.Start, End: o.End, RelativeID: o.RelativeID}
	if err := window.validate(); err != nil {
		return fmt.Errorf("window option bounds: %w", err)
	}
	return nil
}

// contextFabricWindowClarificationMaxOptions bounds how many options one
// WindowClarification may carry -- the same order-of-magnitude ceiling
// ContextFabricRequestedScope.SubjectHints uses for a comparable
// caller-facing offer list; the closed RelativeWindowID registry itself
// has only four members, so this is a generous ceiling, not a tight ceiling
// derived from that count.
const contextFabricWindowClarificationMaxOptions = 20

// Validate checks c's own wire-shape bounds and pins the mint-time
// uniqueness invariant redemption depends on (CHAOS-3900 W1 design brief
// §5, "m5"): both ReceiptID and OptionID must be unique within Options, so
// that (result_id, receipt_id) selects EXACTLY ONE frozen bound at
// redemption time. A duplicate here is a composition-time bug -- caught at
// StageValidation before Save, never a first-match-wins runtime choice.
func (c ContextFabricWindowClarification) Validate() error {
	if len(c.Options) == 0 || len(c.Options) > contextFabricWindowClarificationMaxOptions {
		return fmt.Errorf("window clarification options violate v1 bounds")
	}
	seenReceipt := make(map[string]struct{}, len(c.Options))
	seenOption := make(map[string]struct{}, len(c.Options))
	for _, option := range c.Options {
		if err := option.Validate(); err != nil {
			return fmt.Errorf("window clarification: %w", err)
		}
		if _, exists := seenReceipt[option.ReceiptID]; exists {
			return fmt.Errorf("window clarification option receipt_id must be unique")
		}
		seenReceipt[option.ReceiptID] = struct{}{}
		if _, exists := seenOption[option.OptionID]; exists {
			return fmt.Errorf("window clarification option_id must be unique")
		}
		seenOption[option.OptionID] = struct{}{}
	}
	return nil
}
