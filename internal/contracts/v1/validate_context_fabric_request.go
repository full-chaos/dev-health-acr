package v1

import (
	"fmt"
	"math"
	"strings"
	"time"
)

func (r ContextFabricInvestigationRequest) Validate() error {
	if r.SchemaVersion != ContextFabricInvestigationRequestSchema {
		return fmt.Errorf("schema_version must be %q", ContextFabricInvestigationRequestSchema)
	}
	if !stringLengthBetween(r.RequestID, 8, 256) || !rawBoundedText(r.Question, 1, 8000) {
		return fmt.Errorf("request_id or question violates v1 bounds")
	}
	if len(r.Conversation) > 50 || len(r.PriorSubjectReceipts) > 20 {
		return fmt.Errorf("conversation or prior subject receipts exceed v1 bounds")
	}
	seenTurns := make(map[string]struct{}, len(r.Conversation))
	for _, turn := range r.Conversation {
		if err := turn.Validate(); err != nil {
			return fmt.Errorf("conversation: %w", err)
		}
		if _, exists := seenTurns[turn.TurnID]; exists {
			return fmt.Errorf("conversation turn_id must be unique")
		}
		seenTurns[turn.TurnID] = struct{}{}
	}
	seenReceipts := make(map[string]struct{}, len(r.PriorSubjectReceipts))
	for _, receipt := range r.PriorSubjectReceipts {
		if err := receipt.Validate(); err != nil {
			return fmt.Errorf("prior_subject_receipts: %w", err)
		}
		// CHAOS-3900 P1 (pivot brief §2.5 row 1, generalizing W1's own
		// winr_-only check to the CLOSED SET of all four structure-receipt
		// namespaces): none of kindr_/ancr_/handr_/winr_ belongs in
		// prior_subject_receipts -- fail loudly here rather than let it
		// fall through to subject matching, where it could never match
		// anything and would silently degrade instead of surfacing as
		// malformed.
		if err := validateNoStructureReceiptPrefix("prior_subject_receipts", receipt.ReceiptID, ""); err != nil {
			return err
		}
		key := receipt.ResultID + "\x00" + receipt.ReceiptID
		if _, exists := seenReceipts[key]; exists {
			return fmt.Errorf("prior_subject_receipts must be unique")
		}
		seenReceipts[key] = struct{}{}
	}
	// CHAOS-3900 W1 (design brief §5 "m4") / P1 (§2.1): each of the four
	// structure-receipt fields validates identically -- shared bound,
	// its OWN required namespace prefix (which by construction also
	// rejects the other three), and uniqueness. See
	// validateStructureReceiptField's own doc comment.
	if err := validateStructureReceiptField("prior_window_receipts", r.PriorWindowReceipts, ContextFabricWindowOptionReceiptPrefix); err != nil {
		return err
	}
	if err := validateStructureReceiptField("prior_kind_receipts", r.PriorKindReceipts, ContextFabricKindOptionReceiptPrefix); err != nil {
		return err
	}
	if err := validateStructureReceiptField("prior_anchor_receipts", r.PriorAnchorReceipts, ContextFabricAnchorOptionReceiptPrefix); err != nil {
		return err
	}
	if err := validateStructureReceiptField("prior_handle_receipts", r.PriorHandleReceipts, ContextFabricHandleOptionReceiptPrefix); err != nil {
		return err
	}
	// CHAOS-3972 P3 (design brief §2.3): explicit structure fields, bounded
	// the same order-of-magnitude as their receipt counterparts.
	if len(r.ExpectedKinds) > ContextFabricExpectedKindsMaxCount {
		return fmt.Errorf("expected_kinds exceeds v1 bounds")
	}
	seenExpectedKinds := make(map[ContextFabricSubjectKind]struct{}, len(r.ExpectedKinds))
	for _, kind := range r.ExpectedKinds {
		if !validContextFabricSubjectKind(kind) {
			return fmt.Errorf("expected_kinds entry is invalid")
		}
		if _, exists := seenExpectedKinds[kind]; exists {
			return fmt.Errorf("expected_kinds entries must be unique")
		}
		seenExpectedKinds[kind] = struct{}{}
	}
	if len(r.SubjectHandles) > 20 {
		return fmt.Errorf("subject_handles exceeds v1 bounds")
	}
	for _, handle := range r.SubjectHandles {
		if err := handle.Validate(); err != nil {
			return fmt.Errorf("subject_handles: %w", err)
		}
	}
	if err := r.RequestedScope.Validate(); err != nil {
		return fmt.Errorf("requested_scope: %w", err)
	}
	if err := r.TimeContext.Validate(); err != nil {
		return fmt.Errorf("time_context: %w", err)
	}
	if err := r.Options.Validate(); err != nil {
		return fmt.Errorf("options: %w", err)
	}
	if err := r.Consumer.Validate(); err != nil {
		return fmt.Errorf("consumer: %w", err)
	}
	return nil
}

func (t ContextFabricConversationTurn) Validate() error {
	if !stringLengthBetween(t.TurnID, 1, 256) || !stringLengthBetween(strings.TrimSpace(t.Content), 1, 12000) || t.CreatedAt.IsZero() {
		return fmt.Errorf("turn identity, content, or timestamp violates v1 bounds")
	}
	switch t.Role {
	case ContextFabricConversationUser, ContextFabricConversationAssistant:
		return nil
	default:
		return fmt.Errorf("role is invalid")
	}
}

func (r ContextFabricBoundSubjectReceipt) Validate() error {
	if !stringLengthBetween(r.ResultID, 8, 256) || !stringLengthBetween(r.ReceiptID, 8, 256) {
		return fmt.Errorf("bound subject receipt violates v1 bounds")
	}
	return nil
}

func (s ContextFabricRequestedScope) Validate() error {
	if len(s.RepositorySlugs) > 200 || len(s.ProjectIDs) > 200 || len(s.TeamIDs) > 200 || len(s.SubjectHints) > 50 {
		return fmt.Errorf("scope exceeds v1 bounds")
	}
	if !uniqueTrimmedStrings(s.RepositorySlugs, 512) || !uniqueTrimmedStrings(s.ProjectIDs, 256) || !uniqueTrimmedStrings(s.TeamIDs, 256) {
		return fmt.Errorf("scope identifiers violate v1 bounds")
	}
	for _, hint := range s.SubjectHints {
		if err := hint.Validate(); err != nil {
			return fmt.Errorf("subject_hints: %w", err)
		}
	}
	return nil
}

func (h ContextFabricSubjectHint) Validate() error {
	if !validContextFabricSubjectKind(h.Kind) || !stringLengthBetween(h.ID, 0, 256) || !stringLengthBetween(h.Label, 0, 512) || !stringLengthBetween(h.Source, 1, 64) {
		return fmt.Errorf("subject hint violates v1 bounds")
	}
	if strings.TrimSpace(h.ID) != h.ID || strings.TrimSpace(h.Label) != h.Label || strings.TrimSpace(h.Source) != h.Source || (h.ID == "" && h.Label == "") {
		return fmt.Errorf("subject hint identity is invalid")
	}
	return nil
}

// representableInstant reports whether an instant survives conversion to
// epoch NANOSECONDS, the representation every temporal comparison in this
// system uses (CHAOS-3781 round-4 R4-4).
//
// The bound is derived from the representation, not chosen: time.Time
// carries a far wider range than int64 nanoseconds can hold, so an instant
// outside roughly 1677-09-21..2262-04-11 wraps rather than saturating when
// UnixNano() is called on it. A wrapped value is not merely wrong, it is
// wrong in an adversarially useful way -- year 1 wraps to a plausible
// modern instant, which would admit graph elements at the wrong time and
// let two different requests collide on one reuse key.
//
// Checked HERE, at validation, rather than at each UnixNano() call site:
// the conversion happens in the graph adapter, in the reuse key and in the
// fact bounds, and a check at any one of them leaves the others open.
//
// time.Time.UnixNano's own documentation states the undefined range; the
// constants below are its endpoints, expressed as instants so the reason
// is legible rather than appearing as two magic integers.
var (
	minRepresentableInstant = time.Unix(0, math.MinInt64).UTC()
	maxRepresentableInstant = time.Unix(0, math.MaxInt64).UTC()
)

func representableInstant(value time.Time) bool {
	return !value.Before(minRepresentableInstant) && !value.After(maxRepresentableInstant)
}

// RepresentableInstant is representableInstant, exported so callers that
// enforce the same guarantee at a different trust boundary share this
// definition instead of restating the range (CHAOS-3781 round-5 R5-4:
// contextfabric.resolveTimeContext bounds what an INTERPRETER returned,
// which the request contract never sees). A second copy of the endpoints
// is a second thing to get wrong.
func RepresentableInstant(value time.Time) bool { return representableInstant(value) }

func (t ContextFabricTimeContext) Validate() error {
	// Every bound this axis requires must survive the epoch-nanosecond
	// representation. An out-of-range instant is refused as a malformed
	// request rather than clamped: clamping would answer a DIFFERENT
	// question than the one asked, silently, which is the defect class
	// this whole time axis exists to remove.
	for _, instant := range []*time.Time{t.AsOf, t.Start, t.End} {
		if instant != nil && !instant.IsZero() && !representableInstant(*instant) {
			return fmt.Errorf("time context instant is outside the representable range (%s..%s)",
				minRepresentableInstant.Format("2006-01-02"), maxRepresentableInstant.Format("2006-01-02"))
		}
	}
	switch t.Axis {
	case ContextFabricTemporalCurrent:
		if t.AsOf != nil || t.Start != nil || t.End != nil {
			return fmt.Errorf("current time context cannot include explicit timestamps")
		}
	case ContextFabricTemporalValidTime, ContextFabricTemporalObservedTime:
		if t.AsOf == nil || t.AsOf.IsZero() || t.Start != nil || t.End != nil {
			return fmt.Errorf("point-in-time context requires only as_of")
		}
	case ContextFabricTemporalRange:
		if t.AsOf != nil || t.Start == nil || t.End == nil || t.Start.IsZero() || t.End.IsZero() || t.End.Before(*t.Start) {
			return fmt.Errorf("range context requires an ordered start and end")
		}
	default:
		return fmt.Errorf("time axis is invalid")
	}
	// CHAOS-3900 W1 (design brief §1.2): an evidence window is representable
	// ONLY on the current axis -- every other axis's own bounds already ARE
	// the window that axis answers for, so a window alongside one is a
	// named invariant violation (ErrEvidenceWindowAxisInvalid), never a
	// silent drop. Checked as the LAST step, after the axis-shape switch
	// above, so a malformed axis itself is reported through the more
	// specific "time axis is invalid" error rather than this one.
	if t.EvidenceWindow != nil {
		if t.Axis != ContextFabricTemporalCurrent {
			return fmt.Errorf("%w: axis %q", ErrEvidenceWindowAxisInvalid, t.Axis)
		}
		if err := t.EvidenceWindow.validate(); err != nil {
			return fmt.Errorf("evidence_window: %w", err)
		}
	}
	return nil
}

func (o ContextFabricInvestigationOptions) Validate() error {
	if o.MaxSubjectCandidates < 1 || o.MaxSubjectCandidates > 50 ||
		o.MaxCohortMembers < 1 || o.MaxCohortMembers > 250 ||
		o.MaxRelationshipPaths < 1 || o.MaxRelationshipPaths > 250 ||
		o.MaxDrivers < 1 || o.MaxDrivers > 50 ||
		o.MaxEvidenceRefs < 1 || o.MaxEvidenceRefs > 500 ||
		o.MaxSerializedBytes < ContextFabricSerializedBytesMin || o.MaxSerializedBytes > ContextFabricSerializedBytesMax {
		return fmt.Errorf("investigation options violate v1 bounds")
	}
	if !ValidContextFabricWindowConfirmationMode(o.WindowConfirmationMode) {
		return fmt.Errorf("investigation options window_confirmation_mode is invalid")
	}
	return nil
}

// Validate checks h's own wire shape: a closed subject kind, a non-empty
// pattern_id/value pair. It deliberately does NOT re-validate value against
// the registry's own grammar for that (kind, pattern_id) pair -- that
// re-validation needs the closed handle-grammar registry
// (internal/contextfabric/graphrank), which this package cannot import
// (the same package-boundary wall every other structure re-verification
// dependency in this epic works around); the engine's own HandleGrammarChecker
// dependency (internal/contextfabric/structure.go) performs that check
// before this value may ever become an offer.
func (h ContextFabricRequestedHandle) Validate() error {
	if !validContextFabricSubjectKind(h.Kind) {
		return fmt.Errorf("requested handle kind is invalid")
	}
	if !stringLengthBetween(h.PatternID, 1, 128) || !stringLengthBetween(h.Value, 1, 256) {
		return fmt.Errorf("requested handle pattern_id or value violates v1 bounds")
	}
	return nil
}

func (c ContextFabricConsumerInfo) Validate() error {
	if !stringLengthBetween(c.Name, 1, 200) || !stringLengthBetween(c.Version, 1, 200) || !stringLengthBetween(c.Surface, 1, 200) {
		return fmt.Errorf("consumer metadata violates v1 bounds")
	}
	if strings.TrimSpace(c.Name) != c.Name || strings.TrimSpace(c.Version) != c.Version || strings.TrimSpace(c.Surface) != c.Surface {
		return fmt.Errorf("consumer metadata must be trimmed")
	}
	return nil
}
