package contextfabric

import (
	"errors"
	"fmt"
)

// SynthesisRejectionReason is the CLOSED vocabulary naming which rule in
// SynthesisDraft.ValidateAgainst rejected a draft (CHAOS-4522).
//
// Before this existed, every ValidateAgainst rejection collapsed into the
// single receipt outcome "invalid_output": the model decision line said
// `outcome=invalid_output` and the route line said
// `failure_classification=synthesis_rejected`, and nothing anywhere named
// the rule. ModelBoundViolation only ever covers the small subset of
// rejections attributable to a contracts/v1 model-facing BOUND -- every
// business-rule and grounding rejection (the majority, and the entire
// CHAOS-4522 class) returned no name at all, so diagnosing a 422 required
// re-running the server with instrumentation added after the fact. That is
// exactly the failure mode AGENTS.md's diagnosis-in-artifacts rule bans.
//
// Every value is a fixed identifier chosen at the rejecting statement. NO
// value is derived from model output, question text, subject labels, field
// names, or any other corpus content -- see
// TestSynthesisRejectionReasonVocabularyIsClosed and
// TestDecisionEventNeverCarriesCorpusText.
type SynthesisRejectionReason string

const (
	// RejectionReasonUnclassified is the explicit "a rejection happened and
	// this vocabulary has no entry for it" value. It is never a silent
	// empty string: an unnamed rejection must still be visible as one.
	RejectionReasonUnclassified SynthesisRejectionReason = "unclassified"

	// Top-level draft shape.
	RejectionReasonStatusInvalid              SynthesisRejectionReason = "status_invalid"
	RejectionReasonDirectJudgmentMissing      SynthesisRejectionReason = "direct_judgment_missing"
	RejectionReasonDeterministicAnswerMissing SynthesisRejectionReason = "deterministic_answer_missing"
	RejectionReasonEvidenceUnknown            SynthesisRejectionReason = "evidence_unknown"

	// Claimed facts.
	RejectionReasonClaimInvalid              SynthesisRejectionReason = "claim_invalid"
	RejectionReasonClaimRowsModelAuthored    SynthesisRejectionReason = "claim_rows_model_authored"
	RejectionReasonClaimIDDuplicate          SynthesisRejectionReason = "claim_id_duplicate"
	RejectionReasonClaimSubjectOutOfScope    SynthesisRejectionReason = "claim_subject_out_of_scope"
	RejectionReasonClaimSubjectLabelMismatch SynthesisRejectionReason = "claim_subject_label_mismatch"
	RejectionReasonClaimNoCanonicalFact      SynthesisRejectionReason = "claim_no_canonical_fact"
	RejectionReasonClaimFieldUnobserved      SynthesisRejectionReason = "claim_field_unobserved"
	RejectionReasonClaimValueContradicts     SynthesisRejectionReason = "claim_value_contradicts_canonical"

	// Drivers.
	RejectionReasonDriverInvalid              SynthesisRejectionReason = "driver_invalid"
	RejectionReasonDriverSubjectOutOfScope    SynthesisRejectionReason = "driver_subject_out_of_scope"
	RejectionReasonDriverSubjectLabelMismatch SynthesisRejectionReason = "driver_subject_label_mismatch"
	RejectionReasonDriverPathUnknown          SynthesisRejectionReason = "driver_path_unknown"
	RejectionReasonDriverEvidenceUnknown      SynthesisRejectionReason = "driver_evidence_unknown"
	RejectionReasonDriverClaimUngrounded      SynthesisRejectionReason = "driver_claim_ungrounded"

	// Findings (remaining_work / readiness_gaps / conflicts share one set:
	// WHICH section is already carried by the wrapped error's own prefix,
	// and splitting the vocabulary three ways would triple it without
	// making any diagnosis sharper).
	RejectionReasonFindingInvalid              SynthesisRejectionReason = "finding_invalid"
	RejectionReasonFindingSubjectOutOfScope    SynthesisRejectionReason = "finding_subject_out_of_scope"
	RejectionReasonFindingSubjectLabelMismatch SynthesisRejectionReason = "finding_subject_label_mismatch"
	RejectionReasonFindingEvidenceUnknown      SynthesisRejectionReason = "finding_evidence_unknown"
	RejectionReasonFindingClaimUngrounded      SynthesisRejectionReason = "finding_claim_ungrounded"

	// RejectionReasonOutputUndecodable covers a model response that never
	// became a SynthesisDraft at all (genkitruntime's output.toDomain()).
	// It shares the same telemetry field because a consumer diagnosing a
	// 422 needs "the model's JSON did not decode" and "the draft failed
	// rule X" to be distinguishable, and both previously read
	// `invalid_output`.
	RejectionReasonOutputUndecodable SynthesisRejectionReason = "output_undecodable"
)

// synthesisRejectionReasons is the enumeration behind
// ValidSynthesisRejectionReason. Kept as one list so a value added above
// without being added here fails the closed-vocabulary test rather than
// silently escaping the guarantee.
var synthesisRejectionReasons = map[SynthesisRejectionReason]struct{}{
	RejectionReasonUnclassified:                {},
	RejectionReasonStatusInvalid:               {},
	RejectionReasonDirectJudgmentMissing:       {},
	RejectionReasonDeterministicAnswerMissing:  {},
	RejectionReasonEvidenceUnknown:             {},
	RejectionReasonClaimInvalid:                {},
	RejectionReasonClaimRowsModelAuthored:      {},
	RejectionReasonClaimIDDuplicate:            {},
	RejectionReasonClaimSubjectOutOfScope:      {},
	RejectionReasonClaimSubjectLabelMismatch:   {},
	RejectionReasonClaimNoCanonicalFact:        {},
	RejectionReasonClaimFieldUnobserved:        {},
	RejectionReasonClaimValueContradicts:       {},
	RejectionReasonDriverInvalid:               {},
	RejectionReasonDriverSubjectOutOfScope:     {},
	RejectionReasonDriverSubjectLabelMismatch:  {},
	RejectionReasonDriverPathUnknown:           {},
	RejectionReasonDriverEvidenceUnknown:       {},
	RejectionReasonDriverClaimUngrounded:       {},
	RejectionReasonFindingInvalid:              {},
	RejectionReasonFindingSubjectOutOfScope:    {},
	RejectionReasonFindingSubjectLabelMismatch: {},
	RejectionReasonFindingEvidenceUnknown:      {},
	RejectionReasonFindingClaimUngrounded:      {},
	RejectionReasonOutputUndecodable:           {},
}

// ValidSynthesisRejectionReason reports whether reason is a member of the
// closed vocabulary. Used by the telemetry seam so a value that somehow
// escapes the constants above is reported as "unclassified" rather than
// emitted verbatim -- the same fail-closed posture every other
// closed-vocabulary field in this package applies at its own boundary.
func ValidSynthesisRejectionReason(reason SynthesisRejectionReason) bool {
	_, ok := synthesisRejectionReasons[reason]
	return ok
}

// SynthesisRejection carries a SynthesisRejectionReason alongside the
// underlying error. It wraps rather than replaces, so every existing
// errors.Is(err, ErrModelOutput) / errors.Is(err, ErrSynthesisRejected)
// caller and every message-text assertion is unaffected.
type SynthesisRejection struct {
	Reason SynthesisRejectionReason
	err    error
}

func (e *SynthesisRejection) Error() string { return e.err.Error() }
func (e *SynthesisRejection) Unwrap() error { return e.err }

// rejectSynthesis builds the error a ValidateAgainst statement returns:
// the identical formatted message it produced before CHAOS-4522, now
// carrying the closed-vocabulary reason for that exact statement.
func rejectSynthesis(reason SynthesisRejectionReason, format string, args ...any) error {
	return &SynthesisRejection{Reason: reason, err: fmt.Errorf(format, args...)}
}

// SynthesisRejectionReasonOf extracts the closed-vocabulary reason from
// err, or RejectionReasonUnclassified when err carries none (a rejection
// path that predates this vocabulary, or a non-rejection error). It never
// returns the empty string: telemetry must always be able to say SOMETHING
// about why a draft was rejected, and "unclassified" is a diagnosable
// answer where "" is not.
func SynthesisRejectionReasonOf(err error) SynthesisRejectionReason {
	var rejection *SynthesisRejection
	if errors.As(err, &rejection) && ValidSynthesisRejectionReason(rejection.Reason) {
		return rejection.Reason
	}
	return RejectionReasonUnclassified
}
