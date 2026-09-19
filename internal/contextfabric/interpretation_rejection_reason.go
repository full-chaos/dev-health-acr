package contextfabric

import (
	"errors"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// InterpretationRejectionReason is the closed vocabulary naming which rule
// in InterpretedQuestion.Validate() rejected a model interpretation. It is
// a type ALIAS to the contracts/v1 vocabulary rather than a parallel
// declaration: the clause order it names is a property of that package's
// validator, and the mirror that derives it lives beside that validator so
// one drift guard covers both (see
// contractsv1.DiagnoseContextFabricInterpretedQuestionRejection).
//
// This file is the INTERPRET-side counterpart of
// synthesis_rejection_reason.go. Read that file's doc comment first: the
// wrapper shape, the "never the empty string" rule, and the reason the
// canonical table returns its own constant instead of the caller's input
// are all established there and hold identically here.
type InterpretationRejectionReason = contractsv1.ContextFabricInterpretationRejectionReason

// The one member this package names directly. Every other member is
// selected by the contracts/v1 mirror, never by a caller here, so
// re-exporting the full vocabulary would create a second list to keep in
// sync for no reader's benefit.
const InterpretationRejectionUnclassified = contractsv1.ContextFabricInterpretationRejectionUnclassified

// InterpretationRejection carries an InterpretationRejectionReason
// alongside the underlying error. It wraps rather than replaces, so every
// existing errors.Is(err, ErrInterpretationRejected) / errors.Is(err,
// ErrModelOutput) caller and every message-text assertion is unaffected --
// the same guarantee SynthesisRejection makes on the synthesis side.
type InterpretationRejection struct {
	Reason InterpretationRejectionReason
	// RejectedFactKind is the raw, out-of-vocabulary fact_requirements[].kind
	// value the model proposed. Set ONLY when Reason is
	// FactRequirementKindInvalid (empty for every other reason, including
	// the two fact_registry.go callers below, which never set it) -- see
	// contractsv1.RejectedFactRequirementKind's doc comment for why this
	// field deliberately carries raw text where every other field on this
	// type and its contracts/v1 counterpart is a fixed, ACR-owned
	// identifier, and why a caller must sanitize it before it reaches a log
	// line (InterpretationRejectedFactKindOf below makes no safety claim
	// about the value it returns).
	RejectedFactKind string
	// RejectedFactKindSet is the explicit presence signal for
	// RejectedFactKind, distinct from RejectedFactKind's own zero value.
	// An out-of-vocabulary kind that is empty or all whitespace is still
	// a rejected kind the model proposed -- treating RejectedFactKind ==
	// "" as "absent" silently dropped that exact case, indistinguishable
	// from a rejection this field was never attached to at all. Set true
	// whenever ClassifyInterpretationRejection actually attempted the
	// attach (the reason WAS FactRequirementKindInvalid), false on every
	// other path, including the two fact_registry.go callers that never
	// set the field at all.
	RejectedFactKindSet bool
	err                 error
}

func (e *InterpretationRejection) Error() string { return e.err.Error() }
func (e *InterpretationRejection) Unwrap() error { return e.err }

// NewInterpretationRejection attaches a closed-vocabulary reason to an
// error produced OUTSIDE the contracts/v1 validator.
//
// One caller exists today and it is not an afterthought: fact_registry.go
// rejects a fact-capability parameter the capability's own wiring does not
// allow, one layer LATER than InterpretedQuestion.Validate() can see. That
// rejection deliberately reuses the ErrInterpretationRejected sentinel
// (see its CHAOS-3854 comment) so the whole existing taxonomy applies to
// it end to end; it must therefore name its rule on the SAME telemetry
// field, or the vocabulary would silently exclude one of its own outcomes.
//
// reason is canonicalized here rather than trusted: a caller in another
// package cannot be relied on to pass a member, and a non-member must
// surface as "unclassified" rather than reach a log field verbatim.
func NewInterpretationRejection(reason InterpretationRejectionReason, err error) error {
	return &InterpretationRejection{
		Reason: contractsv1.CanonicalContextFabricInterpretationRejectionReason(reason),
		err:    err,
	}
}

// InterpretationRejectionReasonOf extracts the closed-vocabulary reason
// from err, or Unclassified when err carries none (a rejection path that
// predates this vocabulary, or a non-rejection error).
//
// It never returns the empty string: telemetry must always be able to say
// SOMETHING about why an interpretation was rejected, and "unclassified"
// is a diagnosable answer where "" is not. Callers distinguish "no
// rejection happened" from "an unnamed rejection happened" by whether they
// are on a rejection path at all, exactly as the synthesis side does.
func InterpretationRejectionReasonOf(err error) InterpretationRejectionReason {
	var rejection *InterpretationRejection
	if errors.As(err, &rejection) {
		// Returns the TABLE's constant, never rejection.Reason itself --
		// see the canonical table in contracts/v1 for why that
		// distinction is load-bearing rather than cosmetic.
		return contractsv1.CanonicalContextFabricInterpretationRejectionReason(rejection.Reason)
	}
	return InterpretationRejectionUnclassified
}

// InterpretationRejectedFactKindOf extracts the raw fact_requirements[].kind
// value from err, when err carries an InterpretationRejection whose
// (canonicalized) Reason is FactRequirementKindInvalid. ok is false for
// every other rejection reason, a non-rejection error, or a rejection that
// never set the field (the two fact_registry.go NewInterpretationRejection
// callers, and a rejection reason that canonicalized to Unclassified).
//
// ok reports RejectedFactKindSet, NOT RejectedFactKind != "": an
// all-whitespace or genuinely empty out-of-vocabulary kind is a real,
// present rejected value -- the model proposed it and it is why
// the interpretation was rejected -- and a caller must be able to tell
// that apart from "no kind was ever attached". A string-emptiness check
// here collapsed those two cases and silently dropped the first.
//
// UNLIKE InterpretationRejectionReasonOf, the string this returns is NOT
// safe to log unsanitized -- see RejectedFactKind's own doc comment. Every
// caller of this function must route the value through SanitizeLogAttr (or
// an equivalent bound-and-strip barrier) before it becomes a log attribute.
func InterpretationRejectedFactKindOf(err error) (string, bool) {
	var rejection *InterpretationRejection
	if !errors.As(err, &rejection) {
		return "", false
	}
	if contractsv1.CanonicalContextFabricInterpretationRejectionReason(rejection.Reason) != contractsv1.ContextFabricInterpretationRejectionFactRequirementKindInvalid {
		return "", false
	}
	return rejection.RejectedFactKind, rejection.RejectedFactKindSet
}
