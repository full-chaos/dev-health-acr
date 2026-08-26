package v1

import "time"

// CHAOS-3900 W1 (design brief v5.2 §1-§5, as amended by the pivot-intent
// design brief's DP12(b) ruling). W0 (chaos3900_window_vocab.go's own
// history, package contextfabric) shipped WindowClass/WindowConfidence/
// RelativeWindowID as SHADOW-ONLY, measurement-only internal types with
// deliberately zero wire-contract surface. W1 is exactly the promotion this
// file makes real: an evidence window now rides a served answer, so its
// vocabulary and shapes become a published wire contract like every other
// closed-vocabulary Context Fabric field. internal/contextfabric's own
// WindowClass/WindowConfidence/RelativeWindowID become type ALIASES to the
// types below (see chaos3900_window_vocab.go) so no W0 call site (graphrank,
// genkitruntime) needs to change.

// ContextFabricRelativeWindowID is the closed, server-owned relative-window
// identifier registry (design brief §5.1). A caller may NAME one of these
// identifiers on a request; it may never supply its own -- membership is
// entirely server-defined, exactly like ContextFabricTemporalAxis. Absolute
// bounds for a member are derived ONLY server-side, from the engine's own
// `now()` at canonicalization time (internal/contextfabric/window.go) --
// never accepted from a caller and never asserted by a model.
type ContextFabricRelativeWindowID string

const (
	ContextFabricRelativeWindowTrailing30D  ContextFabricRelativeWindowID = "trailing_30d"
	ContextFabricRelativeWindowTrailing90D  ContextFabricRelativeWindowID = "trailing_90d"
	ContextFabricRelativeWindowTrailing365D ContextFabricRelativeWindowID = "trailing_365d"
	// ContextFabricRelativeWindowAllTime is the typed sentinel meaning "no
	// bound at all" -- distinct from omitting a window entirely (see
	// ContextFabricEffectiveEvidenceWindow's doc comment: a confirmed
	// all-time answer and an unwindowed answer are different commitments
	// and must not share a reuse-key row).
	ContextFabricRelativeWindowAllTime ContextFabricRelativeWindowID = "all_time"
)

// contextFabricRelativeWindowIDs is the closed vocabulary's backing array --
// the unexported-array-plus-copy-returning-accessor pattern
// ContextFabricFactKindVocabulary already establishes.
var contextFabricRelativeWindowIDs = [...]ContextFabricRelativeWindowID{
	ContextFabricRelativeWindowTrailing30D,
	ContextFabricRelativeWindowTrailing90D,
	ContextFabricRelativeWindowTrailing365D,
	ContextFabricRelativeWindowAllTime,
}

// ContextFabricRelativeWindowIDCount is the closed vocabulary's size.
const ContextFabricRelativeWindowIDCount = len(contextFabricRelativeWindowIDs)

// ContextFabricRelativeWindowIDVocabulary returns the closed relative-window
// vocabulary in published order. An array return, not a slice -- see
// ContextFabricFactKindVocabulary for why that distinction matters.
func ContextFabricRelativeWindowIDVocabulary() [ContextFabricRelativeWindowIDCount]ContextFabricRelativeWindowID {
	return contextFabricRelativeWindowIDs
}

// ValidContextFabricRelativeWindowID reports whether value is a member of
// the closed registry. The empty value is deliberately not valid -- an
// absent RelativeID is a distinct, explicit "not this kind of window"
// state, never a member standing in for it.
func ValidContextFabricRelativeWindowID(value ContextFabricRelativeWindowID) bool {
	for _, id := range contextFabricRelativeWindowIDs {
		if value == id {
			return true
		}
	}
	return false
}

// ContextFabricWindowClass is the closed, growable slice-1 class vocabulary
// (design brief §2.1's "class" column) an interpretation may propose. See
// internal/contextfabric.WindowClass (a type alias to this, post-W1) for the
// engine-side post-pass that validates a proposal and owns the
// class-to-default table -- a model's only latitude is this one enum pick,
// never a timestamp.
type ContextFabricWindowClass string

const (
	ContextFabricWindowClassTrendAssessment      ContextFabricWindowClass = "trend_assessment"
	ContextFabricWindowClassRecentActivityLookup ContextFabricWindowClass = "recent_activity_lookup"
	ContextFabricWindowClassStateSnapshot        ContextFabricWindowClass = "state_snapshot"
	ContextFabricWindowClassExplicitWindow       ContextFabricWindowClass = "explicit_window"
)

var contextFabricWindowClasses = [...]ContextFabricWindowClass{
	ContextFabricWindowClassTrendAssessment,
	ContextFabricWindowClassRecentActivityLookup,
	ContextFabricWindowClassStateSnapshot,
	ContextFabricWindowClassExplicitWindow,
}

// ContextFabricWindowClassCount is the closed vocabulary's size.
const ContextFabricWindowClassCount = len(contextFabricWindowClasses)

// ContextFabricWindowClassVocabulary returns the closed window-class
// vocabulary in published order.
func ContextFabricWindowClassVocabulary() [ContextFabricWindowClassCount]ContextFabricWindowClass {
	return contextFabricWindowClasses
}

// ValidContextFabricWindowClass reports whether value is a member of the
// closed vocabulary. The empty value is deliberately not valid -- see
// SanitizeWindowClass (internal/contextfabric) for the "absent" case.
func ValidContextFabricWindowClass(value ContextFabricWindowClass) bool {
	for _, class := range contextFabricWindowClasses {
		if value == class {
			return true
		}
	}
	return false
}

// ContextFabricWindowConfidence is the closed "blasé detection" vocabulary
// (design brief §2.1's window_confidence field).
type ContextFabricWindowConfidence string

const (
	ContextFabricWindowConfidenceHigh ContextFabricWindowConfidence = "high"
	ContextFabricWindowConfidenceLow  ContextFabricWindowConfidence = "low"
)

func ValidContextFabricWindowConfidence(value ContextFabricWindowConfidence) bool {
	return value == ContextFabricWindowConfidenceHigh || value == ContextFabricWindowConfidenceLow
}

// ContextFabricWindowProvenance is the closed vocabulary for HOW an
// effective evidence window's value was established (CHAOS-3900 W1, as
// amended by the pivot-intent design brief's DP12(b) ruling -- see
// ContextFabricEffectiveEvidenceWindow's doc comment). It is a function of
// SURFACE and confirmation state alone, uniform across every frame member
// per the pivot brief's §2.0 rule -- never a per-caller or per-member
// special case.
type ContextFabricWindowProvenance string

const (
	// ContextFabricWindowInferredDefault: the window carries no caller
	// AUTHORITY, whether or not a caller supplied a VALUE. Covers TWO
	// distinct origins (CHAOS-4040 fix, doc corrected: this comment
	// previously said "no caller-asserted window", omitting the second
	// origin the sibling QuestionStated const below already documented
	// correctly): the class-to-default table or a proposal-only
	// temporal-expression binder span picked this window (no caller
	// input at all), OR a caller's own bare explicit evidence_window on
	// MCP entered here per DP12(b) -- present on the wire, but carrying
	// no decisive authority of its own until confirmed. Always disclosed,
	// never decisive without either a winr_ confirmation or the §3/W4
	// window-insensitivity proof (CHAOS-4040 gates both origins out of
	// every decisive terminal pending it).
	ContextFabricWindowInferredDefault ContextFabricWindowProvenance = "inferred_default"
	// ContextFabricWindowQuestionStated: the caller's own request carried
	// an explicit evidence_window this canonicalization accepted. On the
	// hosted/web surface this includes echoing server-disclosed bounds
	// back per 3900 §4's stated-echo clause; on MCP, per DP12(b), this
	// provenance is reachable ONLY through winr_ receipt redemption --
	// MCP's own bare explicit evidence_window field enters at
	// inferred_default (source explicit_unattributed) and can never
	// become question_stated by itself.
	ContextFabricWindowQuestionStated ContextFabricWindowProvenance = "question_stated"
	// ContextFabricWindowClarificationConfirmed: the caller redeemed a
	// winr_ receipt naming one of this investigation's own prior stored
	// WindowOptions -- the caller confirmed exactly what was offered.
	ContextFabricWindowClarificationConfirmed ContextFabricWindowProvenance = "clarification_confirmed"
)

func ValidContextFabricWindowProvenance(value ContextFabricWindowProvenance) bool {
	switch value {
	case ContextFabricWindowInferredDefault, ContextFabricWindowQuestionStated, ContextFabricWindowClarificationConfirmed:
		return true
	default:
		return false
	}
}

// ContextFabricRequestedEvidenceWindow is the caller's own evidence-window
// INTENT (design brief §1.2): what was asked for, before canonicalization.
// It structurally cannot carry provenance, class, or confidence -- those
// are things the SERVER computes, never something a caller sends.
//
// Exactly one of RelativeID or the Start/End pair is meaningful on any one
// request; both together are legal ONLY when they agree (beyond ordinary
// clock skew) -- see Validate. Sending RelativeID == all_time requires both
// Start and End to be absent (the biconditional this type's Validate pins);
// every OTHER RelativeID, and an explicit Start/End pair, requires BOTH
// bounds (no partial window is representable).
type ContextFabricRequestedEvidenceWindow struct {
	Start      *time.Time                    `json:"start,omitempty"`
	End        *time.Time                    `json:"end,omitempty"`
	RelativeID ContextFabricRelativeWindowID `json:"relative_id,omitempty"`
}

// ContextFabricEffectiveEvidenceWindow is the window an answer actually
// speaks for once canonicalization has run (design brief §1.2/§5.1) --
// server-computed, never accepted from the wire and never emitted by a
// model (it is not part of the interpretation output schema). Present only
// when a window is genuinely in play for this investigation (axis=current
// AND the resolved class carries a window at all -- a state_snapshot
// question, for instance, carries none).
//
// Every non-sentinel member carries BOTH Start and End (no partial window
// is representable, mirroring ContextFabricRequestedEvidenceWindow); the
// RelativeWindowAllTime sentinel carries neither. Confidence is meaningful
// only when Provenance == inferred_default -- a caller- or
// receipt-confirmed window carries no "confidence" of its own, since
// nothing was inferred.
type ContextFabricEffectiveEvidenceWindow struct {
	Start       *time.Time                    `json:"start,omitempty"`
	End         *time.Time                    `json:"end,omitempty"`
	RelativeID  ContextFabricRelativeWindowID `json:"relative_id,omitempty"`
	WindowClass ContextFabricWindowClass      `json:"window_class,omitempty"`
	Provenance  ContextFabricWindowProvenance `json:"provenance"`
	Confidence  ContextFabricWindowConfidence `json:"confidence,omitempty"`
}

// ContextFabricWindowConfirmationMode (CHAOS-3900 W2, design brief §4/DW3)
// is the closed vocabulary selecting how a caller wants an INFERRED
// (non-confirmed) evidence window disclosed. Both modes carry the SAME
// WindowClarification/EffectiveEvidenceWindow data -- the mode only
// controls whether the disclosure is ALSO nudged through Warnings.
//
// CHAOS-4040 (sol-max ruling 2026-08-21) superseded design brief §5's
// original "answer-rate unchanged" pin: every inferred window is now gated
// out of decisive terminals regardless of this field (windowConfirmationRequiredResult,
// internal/contextfabric/window.go), so the mode no longer selects between
// "nudge" and "an otherwise-decisive answer" -- it only decides whether the
// confirmation-required terminal ALSO carries the nudge sentence in
// Warnings, exactly as it decided for a decisive answer before this ruling.
type ContextFabricWindowConfirmationMode string

const (
	// ContextFabricWindowConfirmationHeadless is the DW3-ruled default: an
	// inferred window is disclosed structurally (EffectiveEvidenceWindow.Provenance,
	// WindowClarification) without an additional Warnings sentence, on
	// whichever terminal the window reaches (see the type's own doc
	// comment for the CHAOS-4040 change to which terminal that is).
	ContextFabricWindowConfirmationHeadless ContextFabricWindowConfirmationMode = "headless"
	// ContextFabricWindowConfirmationNudge additionally appends a fixed,
	// closed-vocabulary disclosure sentence to Warnings whenever the
	// effective window is inferred_default -- "answering with the last 90
	// days by default; confirm a window to change it" -- so a caller
	// rendering only Warnings (never Structured directly) still sees the
	// nudge.
	ContextFabricWindowConfirmationNudge ContextFabricWindowConfirmationMode = "nudge"
)

// ValidContextFabricWindowConfirmationMode reports whether value is a
// member of the closed registry. The empty value is deliberately valid
// here (unlike most closed enums in this contract): it is the wire's own
// "caller did not say" state, mapped to ContextFabricWindowConfirmationHeadless
// by the engine, never a value the wire itself must reject.
func ValidContextFabricWindowConfirmationMode(value ContextFabricWindowConfirmationMode) bool {
	switch value {
	case "", ContextFabricWindowConfirmationHeadless, ContextFabricWindowConfirmationNudge:
		return true
	default:
		return false
	}
}

// ContextFabricWindowOptionReceiptPrefix is the CLOSED winr_ receipt
// namespace prefix (design brief §5, pivot brief §2's closed
// kindr_/ancr_/handr_/winr_ set) -- every WindowOption.ReceiptID and every
// ContextFabricInvestigationRequest.PriorWindowReceipts[].ReceiptID MUST
// carry it. A receipt ID outside this namespace in a window-receipt field
// is malformed and fails validation; it is never treated as a subject
// receipt or silently skipped (pivot brief §2.5's closed failure-branch
// table, row 1).
const ContextFabricWindowOptionReceiptPrefix = "winr_"

// ContextFabricWindowOption is one server-offered window choice, minted
// onto a stored InvestigationResult so a later turn can confirm exactly
// what was offered, byte-for-byte, via winr_ receipt redemption (design
// brief §5). Start/End are the FROZEN absolute bounds computed at offer
// time -- redemption applies these exact values, never re-derives them from
// RelativeID at redemption time, so a stored option's meaning can never
// drift from what it said when it was offered.
type ContextFabricWindowOption struct {
	// ReceiptID is this option's own opaque, winr_-prefixed identifier --
	// unique within its issuing result (see ContextFabricWindowClarification.Validate).
	ReceiptID string `json:"receipt_id"`
	// OptionID is a stable identifier for this option within its result,
	// independent of ReceiptID -- unique within its issuing result
	// alongside ReceiptID.
	OptionID string `json:"option_id"`
	// Label is server-rendered, closed-vocabulary display text derived
	// from RelativeID (e.g. "the last 90 days") -- never free text, and
	// never derived from question content.
	Label      string                        `json:"label"`
	RelativeID ContextFabricRelativeWindowID `json:"relative_id,omitempty"`
	Start      *time.Time                    `json:"start,omitempty"`
	End        *time.Time                    `json:"end,omitempty"`
}

// ContextFabricWindowClarification carries every window option a stored
// result offered (design brief §5). Stored on the canonical
// ContextFabricInvestigationResult itself (not projection-only), so a
// later turn's receipt redemption can load it directly off the prior
// result it names.
type ContextFabricWindowClarification struct {
	Options []ContextFabricWindowOption `json:"options"`
}

// ContextFabricWindowExpandOption (CHAOS-4314; semantics widened CHAOS-4336,
// 2026-08-26) is a gated window's recommendation: "a wider window tier is
// available to try." Presence is a statement about the TIER ORDERING alone
// (RelativeID is strictly wider than the currently-bound window) -- it is
// NOT evidence that any offers-only pool exists or was found non-empty.
// CHAOS-4314 originally scoped this to the class-default gate's own
// offers-only resolution finding a real, non-empty pool; CHAOS-4336 also
// emits it for the explicit-unconfirmed gate (which never runs an
// offers-only resolution at all) and for the class-default gate's own
// Empty outcome (a resolution that genuinely ran and found nothing) --
// suppressed only when the gate could not learn anything informative about
// the current window's content at all (a failed/unavailable/disabled/
// refused offers-only read) or when no wider tier exists. A client must
// not infer "there is a real subject pool waiting in a wider window" from
// this field's mere presence -- see CandidateLabel/CandidateKind below for
// the (now genuinely optional) pool-derived hint. ReceiptID/OptionID/Label/RelativeID
// are copied VERBATIM
// from one entry this same result's own WindowOptions already carries
// (internal/contextfabric's composeWindowExpandOption picks the next
// registry tier wider than the currently-bound window) -- deliberately a
// duplicate, not a fresh mint: redeeming it is byte-identical to redeeming
// that WindowOption directly through the existing winr_/PriorWindowReceipts/
// resolveWindowReceipts path (fail-closed, explicit, receipted turn-2 hint;
// no new grammar). ContextFabricStructureNeeds.Validate enforces the
// referential tie -- a window_expand receipt_id/option_id pair must name an
// existing window_options entry -- rather than the cross-list uniqueness
// every OTHER StructureNeeds offer list carries, since duplicating an
// existing receipt_id here is the intended shape.
type ContextFabricWindowExpandOption struct {
	ReceiptID  string                        `json:"receipt_id"`
	OptionID   string                        `json:"option_id"`
	Label      string                        `json:"label"`
	RelativeID ContextFabricRelativeWindowID `json:"relative_id,omitempty"`
	// WindowClass is the class actually bound for THIS investigation
	// (EffectiveEvidenceWindow.WindowClass) -- the "why" behind the
	// recommendation, never the target window's own class (RelativeID
	// alone identifies the target; window classes are a question-class
	// property, not a per-tier one).
	WindowClass ContextFabricWindowClass `json:"window_class,omitempty"`
	// CandidateLabel/CandidateKind (optional) name the top pool member the
	// gate's own offers-only resolution found (when one ran and found
	// anything) -- server-rendered label text from
	// ContextFabricCandidateOption/AnchorOption/HandleOption/KindOption
	// (priority order, first non-empty), never question- or model-derived
	// prose. Absent (CHAOS-4336) whenever no offers-only pool exists to hint
	// from at all -- the explicit-unconfirmed gate (no resolution ever
	// runs) and the class-default gate's own Empty outcome (a resolution
	// that ran and found nothing) both leave this genuinely unset; a client
	// must treat it as optional, not as a signal something went wrong.
	CandidateLabel string                   `json:"candidate_label,omitempty"`
	CandidateKind  ContextFabricSubjectKind `json:"candidate_kind,omitempty"`
}
