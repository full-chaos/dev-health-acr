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
	RejectionReasonClaimInvalid           SynthesisRejectionReason = "claim_invalid"
	RejectionReasonClaimRowsModelAuthored SynthesisRejectionReason = "claim_rows_model_authored"
	// RejectionReasonClaimTimeSeriesRowsModelAuthored (CHAOS-4682, §5.1 P2)
	// is claim_rows_model_authored's own rule applied to the additive
	// TimeSeriesRows pair: a distinct reason, not a reuse, so a reader
	// diagnosing a rejection can tell which of the two model-authorable
	// row fields the model actually set.
	RejectionReasonClaimTimeSeriesRowsModelAuthored SynthesisRejectionReason = "claim_time_series_rows_model_authored"
	RejectionReasonClaimIDDuplicate                 SynthesisRejectionReason = "claim_id_duplicate"
	RejectionReasonClaimSubjectOutOfScope           SynthesisRejectionReason = "claim_subject_out_of_scope"
	RejectionReasonClaimSubjectLabelMismatch        SynthesisRejectionReason = "claim_subject_label_mismatch"
	RejectionReasonClaimNoCanonicalFact             SynthesisRejectionReason = "claim_no_canonical_fact"
	RejectionReasonClaimFieldUnobserved             SynthesisRejectionReason = "claim_field_unobserved"
	RejectionReasonClaimValueContradicts            SynthesisRejectionReason = "claim_value_contradicts_canonical"

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
)

// canonicalSynthesisRejectionReasons maps each vocabulary member to
// ITSELF, and is the single enumeration behind both
// ValidSynthesisRejectionReason and SynthesisRejectionReasonOf. A value
// added to the constants above without being added here fails the
// closed-vocabulary test rather than silently escaping the guarantee.
//
// Mapping a member to itself looks redundant and is not. Every VALUE in
// this table is a package constant, so a lookup RETURNS a compile-time
// constant rather than the caller's own input -- which is what makes
// "nothing derived from model output ever reaches a log field" a property
// the compiler and CodeQL can both see, instead of one that merely holds
// because a membership check happens to run first (CodeQL go/log-injection,
// severity error, raised on the decision line's rejection_reason field).
// Validating a tainted value and then logging the tainted value is a real
// distinction from validating it and logging the matched constant: the
// former is correct only as long as the check and the use stay in sync,
// and this codebase has already been bitten by exactly that shape of
// coupling.
var canonicalSynthesisRejectionReasons = map[SynthesisRejectionReason]SynthesisRejectionReason{
	RejectionReasonUnclassified:                     RejectionReasonUnclassified,
	RejectionReasonStatusInvalid:                    RejectionReasonStatusInvalid,
	RejectionReasonDirectJudgmentMissing:            RejectionReasonDirectJudgmentMissing,
	RejectionReasonDeterministicAnswerMissing:       RejectionReasonDeterministicAnswerMissing,
	RejectionReasonEvidenceUnknown:                  RejectionReasonEvidenceUnknown,
	RejectionReasonClaimInvalid:                     RejectionReasonClaimInvalid,
	RejectionReasonClaimRowsModelAuthored:           RejectionReasonClaimRowsModelAuthored,
	RejectionReasonClaimTimeSeriesRowsModelAuthored: RejectionReasonClaimTimeSeriesRowsModelAuthored,
	RejectionReasonClaimIDDuplicate:                 RejectionReasonClaimIDDuplicate,
	RejectionReasonClaimSubjectOutOfScope:           RejectionReasonClaimSubjectOutOfScope,
	RejectionReasonClaimSubjectLabelMismatch:        RejectionReasonClaimSubjectLabelMismatch,
	RejectionReasonClaimNoCanonicalFact:             RejectionReasonClaimNoCanonicalFact,
	RejectionReasonClaimFieldUnobserved:             RejectionReasonClaimFieldUnobserved,
	RejectionReasonClaimValueContradicts:            RejectionReasonClaimValueContradicts,
	RejectionReasonDriverInvalid:                    RejectionReasonDriverInvalid,
	RejectionReasonDriverSubjectOutOfScope:          RejectionReasonDriverSubjectOutOfScope,
	RejectionReasonDriverSubjectLabelMismatch:       RejectionReasonDriverSubjectLabelMismatch,
	RejectionReasonDriverPathUnknown:                RejectionReasonDriverPathUnknown,
	RejectionReasonDriverEvidenceUnknown:            RejectionReasonDriverEvidenceUnknown,
	RejectionReasonDriverClaimUngrounded:            RejectionReasonDriverClaimUngrounded,
	RejectionReasonFindingInvalid:                   RejectionReasonFindingInvalid,
	RejectionReasonFindingSubjectOutOfScope:         RejectionReasonFindingSubjectOutOfScope,
	RejectionReasonFindingSubjectLabelMismatch:      RejectionReasonFindingSubjectLabelMismatch,
	RejectionReasonFindingEvidenceUnknown:           RejectionReasonFindingEvidenceUnknown,
	RejectionReasonFindingClaimUngrounded:           RejectionReasonFindingClaimUngrounded,
}

// ValidSynthesisRejectionReason reports whether reason is a member of the
// closed vocabulary. Used by the telemetry seam so a value that somehow
// escapes the constants above is reported as "unclassified" rather than
// emitted verbatim -- the same fail-closed posture every other
// closed-vocabulary field in this package applies at its own boundary.
func ValidSynthesisRejectionReason(reason SynthesisRejectionReason) bool {
	_, ok := canonicalSynthesisRejectionReasons[reason]
	return ok
}

// SynthesisRejection carries a SynthesisRejectionReason alongside the
// underlying error. It wraps rather than replaces, so every existing
// errors.Is(err, ErrModelOutput) / errors.Is(err, ErrSynthesisRejected)
// caller and every message-text assertion is unaffected.
type SynthesisRejection struct {
	Reason SynthesisRejectionReason
	// FactGroupSize is how many canonical facts shared the REJECTING
	// claim's (Kind, Subject) -- the ambiguity groundClaim closed over for
	// that one claim (CHAOS-4522 codex R1 finding 1). It is set only by the
	// claim-grounding rejections, where a group exists to measure, and is 0
	// for every other rule.
	//
	// It is deliberately NOT a maximum over the whole draft. ValidateAgainst
	// short-circuits at the first failing statement, so a scan of every
	// claim can report the group size of a LATER claim that was never
	// evaluated -- which would make the documented "1 versus >1" reading of
	// this number wrong in exactly the case it exists to diagnose.
	FactGroupSize int
	// SubjectInPayload is set ONLY by the three subject-scope rejections
	// (claim/driver/finding "references subject outside the investigation"),
	// and reports whether the rejected subject was one the synthesis payload
	// had actually SHOWN the model -- synthesisPayloadSubjects, derived from
	// the serialization shape, not from the allow-set that just rejected it.
	//
	// It is a pointer because the two states it distinguishes are both
	// meaningful and neither is a sensible zero: false means the model named
	// a subject nothing in its input mentioned (the model's error, the
	// expected steady state), true means ACR displayed a subject and then
	// refused it (ACR's error, and precisely the CHAOS-4962 defect -- a
	// grouped cohort's group entity was serialized in every payload and
	// admitted by nothing). nil means the rejection was not a subject-scope
	// one, and the telemetry seam omits the field entirely rather than
	// printing a false-looking default on rejections it does not describe.
	//
	// A non-nil TRUE in production is therefore a standing alarm for a NEW
	// display/validate asymmetry, which is the class this field exists to
	// stop from being discovered by a family that silently fails to serve.
	SubjectInPayload *bool
	err              error
}

func (e *SynthesisRejection) Error() string { return e.err.Error() }
func (e *SynthesisRejection) Unwrap() error { return e.err }

// NewSynthesisRejection attaches a closed-vocabulary reason to an error
// produced OUTSIDE this package -- genkitruntime's own
// synthesisOutput.toDomain enforces one required-field rule before
// ValidateAgainst ever runs, and that rejection must name its rule on the
// same telemetry field as every rule inside ValidateAgainst, or the
// vocabulary would silently exclude the one check that happens first.
func NewSynthesisRejection(reason SynthesisRejectionReason, err error) error {
	return &SynthesisRejection{Reason: reason, err: err}
}

// rejectSynthesis builds the error a ValidateAgainst statement returns:
// the identical formatted message it produced before CHAOS-4522, now
// carrying the closed-vocabulary reason for that exact statement.
func rejectSynthesis(reason SynthesisRejectionReason, format string, args ...any) error {
	return &SynthesisRejection{Reason: reason, err: fmt.Errorf(format, args...)}
}

// rejectSynthesisClaim is rejectSynthesis for the claim-grounding rules,
// carrying the REJECTING claim's own (Kind, Subject) group size -- see
// SynthesisRejection.FactGroupSize for why it must be that claim's and not
// a maximum over the draft.
func rejectSynthesisClaim(reason SynthesisRejectionReason, groupSize int, format string, args ...any) error {
	return &SynthesisRejection{Reason: reason, FactGroupSize: groupSize, err: fmt.Errorf(format, args...)}
}

// rejectSynthesisSubject is rejectSynthesis for the three subject-scope
// rules, carrying whether the rejected subject was in the payload the model
// was shown -- see SynthesisRejection.SubjectInPayload.
func rejectSynthesisSubject(reason SynthesisRejectionReason, inPayload bool, format string, args ...any) error {
	return &SynthesisRejection{Reason: reason, SubjectInPayload: &inPayload, err: fmt.Errorf(format, args...)}
}

// SynthesisSubjectInPayloadOf returns the rejected subject's payload
// membership and true when err carries one, and (false, false) otherwise --
// so a telemetry seam can tell "not a subject-scope rejection" from "a
// subject-scope rejection on a subject we never showed the model" without
// reaching into the error type.
func SynthesisSubjectInPayloadOf(err error) (bool, bool) {
	var rejection *SynthesisRejection
	if errors.As(err, &rejection) && rejection.SubjectInPayload != nil {
		return *rejection.SubjectInPayload, true
	}
	return false, false
}

// SynthesisFactGroupSizeOf returns the rejecting claim's (Kind, Subject)
// group size when err carries one, and 0 otherwise -- so the telemetry seam
// never has to reach into the error type or re-derive the number from a
// draft the rejection may not have reached.
func SynthesisFactGroupSizeOf(err error) int {
	var rejection *SynthesisRejection
	if errors.As(err, &rejection) {
		return rejection.FactGroupSize
	}
	return 0
}

// SynthesisRejectionReasonOf extracts the closed-vocabulary reason from
// err, or RejectionReasonUnclassified when err carries none (a rejection
// path that predates this vocabulary, or a non-rejection error). It never
// returns the empty string: telemetry must always be able to say SOMETHING
// about why a draft was rejected, and "unclassified" is a diagnosable
// answer where "" is not.
func SynthesisRejectionReasonOf(err error) SynthesisRejectionReason {
	var rejection *SynthesisRejection
	if errors.As(err, &rejection) {
		// Returns the TABLE's constant, never rejection.Reason itself --
		// see canonicalSynthesisRejectionReasons for why that distinction
		// is load-bearing rather than cosmetic.
		if canonical, ok := canonicalSynthesisRejectionReasons[rejection.Reason]; ok {
			return canonical
		}
	}
	return RejectionReasonUnclassified
}
