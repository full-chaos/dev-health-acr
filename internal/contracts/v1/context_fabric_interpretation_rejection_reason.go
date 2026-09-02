package v1

import (
	"sort"
	"strings"
)

// ContextFabricInterpretationRejectionReason is the CLOSED vocabulary
// naming which rule in ContextFabricInterpretedQuestion.Validate() rejected
// a model interpretation.
//
// This is the INTERPRET-side counterpart of
// contextfabric.SynthesisRejectionReason, which has named the synthesis
// side's rejecting rule since CHAOS-4522. The interpret side never got its
// half: every rejection here collapsed into the single receipt outcome
// "invalid_output" plus the route class "interpretation_rejected", and
// nothing anywhere named the rule. The cost was measured, not assumed --
// a rig pass observed two interpretation_rejected rows and could not
// establish WHY either was rejected from the run's own artifacts, which is
// exactly the failure mode AGENTS.md's diagnosis-in-artifacts rule bans.
//
// DiagnoseContextFabricInterpretedQuestionBound (context_fabric_bound_diagnosis.go)
// is NOT this and does not replace it. By deliberate CHAOS-3784 round-4
// design that function names only MAX-side entries of the registered
// ContextFabricModelFacingBounds registry, and returns ("", false) for
// every enum, min-length, duplicate and business-rule clause -- which is
// the majority of this validator's surface. The two are complementary: the
// bound name answers "which registered numeric limit", this answers "which
// rule", and a rejection can legitimately have the second without the
// first.
//
// Every value is a fixed identifier chosen at the mirrored clause. NO value
// is derived from model output, question text, subject terms, parameter
// keys, or any other corpus content -- see
// TestContextFabricInterpretationRejectionReasonVocabularyIsClosed and
// genkitruntime's TestDecisionEventNeverCarriesCorpusText.
type ContextFabricInterpretationRejectionReason string

const (
	// ContextFabricInterpretationRejectionUnclassified is the explicit "a
	// rejection happened and this vocabulary has no entry for it" value.
	// It is never a silent empty string: an unnamed rejection must still
	// be visible as one.
	ContextFabricInterpretationRejectionUnclassified ContextFabricInterpretationRejectionReason = "unclassified"

	// Statement 1 -- the single `||` expression, in its own left-to-right
	// short-circuit order.
	ContextFabricInterpretationRejectionShapeInvalid             ContextFabricInterpretationRejectionReason = "shape_invalid"
	ContextFabricInterpretationRejectionRequestedJudgmentInvalid ContextFabricInterpretationRejectionReason = "requested_judgment_invalid"
	ContextFabricInterpretationRejectionSubjectTermsMaxCount     ContextFabricInterpretationRejectionReason = "subject_terms_max_count"
	ContextFabricInterpretationRejectionComparisonTermsMaxCount  ContextFabricInterpretationRejectionReason = "comparison_terms_max_count"
	ContextFabricInterpretationRejectionSubjectTermsInvalid      ContextFabricInterpretationRejectionReason = "subject_terms_invalid"
	ContextFabricInterpretationRejectionComparisonTermsInvalid   ContextFabricInterpretationRejectionReason = "comparison_terms_invalid"
	ContextFabricInterpretationRejectionFactRequirementsMaxCount ContextFabricInterpretationRejectionReason = "fact_requirements_max_count"
	ContextFabricInterpretationRejectionClarificationReasonMax   ContextFabricInterpretationRejectionReason = "clarification_reason_max_length"

	// Statement 2 -- TimeContext.
	ContextFabricInterpretationRejectionTimeContextInvalid ContextFabricInterpretationRejectionReason = "time_context_invalid"

	// Statement 3 -- the fact_requirements loop, first failing entry wins.
	ContextFabricInterpretationRejectionFactRequirementKindInvalid      ContextFabricInterpretationRejectionReason = "fact_requirement_kind_invalid"
	ContextFabricInterpretationRejectionFactRequirementSubjectsInvalid  ContextFabricInterpretationRejectionReason = "fact_requirement_subjects_invalid"
	ContextFabricInterpretationRejectionFactRequirementParamsMaxCount   ContextFabricInterpretationRejectionReason = "fact_requirement_parameters_max_count"
	ContextFabricInterpretationRejectionFactRequirementParameterInvalid ContextFabricInterpretationRejectionReason = "fact_requirement_parameter_invalid"
	ContextFabricInterpretationRejectionFactRequirementKindDuplicate    ContextFabricInterpretationRejectionReason = "fact_requirement_kind_duplicate"

	// Statement 4 -- the clarification business rule.
	ContextFabricInterpretationRejectionClarificationReasonMissing ContextFabricInterpretationRejectionReason = "clarification_reason_missing"

	// Statements 5 and 6 -- the CHAOS-3900 W1 shadow classifications. The
	// EMPTY value is legitimate for both ("the model made no pick"), so
	// only a non-empty out-of-vocabulary value rejects.
	ContextFabricInterpretationRejectionWindowClassInvalid      ContextFabricInterpretationRejectionReason = "window_class_invalid"
	ContextFabricInterpretationRejectionWindowConfidenceInvalid ContextFabricInterpretationRejectionReason = "window_confidence_invalid"

	// Discovered one layer LATER than this validator, in the fact
	// registry, and deliberately part of the SAME vocabulary: it carries
	// the same ErrInterpretationRejected sentinel and reaches the same
	// 422, so splitting it into a second vocabulary would make the one
	// telemetry field unable to describe one of its own outcomes. See
	// internal/contextfabric/fact_registry.go's CHAOS-3854 comment for why
	// that rejection reuses this taxonomy rather than minting its own.
	ContextFabricInterpretationRejectionFactCapabilityParameterNotAllowed ContextFabricInterpretationRejectionReason = "fact_capability_parameter_not_allowed"
)

// canonicalContextFabricInterpretationRejectionReasons maps each vocabulary
// member to ITSELF, and is the single enumeration behind both
// ValidContextFabricInterpretationRejectionReason and every lookup.
//
// Mapping a member to itself looks redundant and is not -- the reasoning is
// synthesis_rejection_reason.go's canonicalSynthesisRejectionReasons
// verbatim, and applies identically here. Every VALUE in this table is a
// package constant, so a lookup RETURNS a compile-time constant rather than
// the caller's own input. That is what makes "nothing derived from model
// output ever reaches a log field" a property the compiler and CodeQL can
// both see, instead of one that merely holds because a membership check
// happens to run first. Validating a tainted value and then logging the
// tainted value is a real distinction from validating it and logging the
// matched constant: the former is correct only as long as the check and the
// use stay in sync, and this codebase has already been bitten by exactly
// that shape of coupling.
var canonicalContextFabricInterpretationRejectionReasons = map[ContextFabricInterpretationRejectionReason]ContextFabricInterpretationRejectionReason{
	ContextFabricInterpretationRejectionUnclassified:                      ContextFabricInterpretationRejectionUnclassified,
	ContextFabricInterpretationRejectionShapeInvalid:                      ContextFabricInterpretationRejectionShapeInvalid,
	ContextFabricInterpretationRejectionRequestedJudgmentInvalid:          ContextFabricInterpretationRejectionRequestedJudgmentInvalid,
	ContextFabricInterpretationRejectionSubjectTermsMaxCount:              ContextFabricInterpretationRejectionSubjectTermsMaxCount,
	ContextFabricInterpretationRejectionComparisonTermsMaxCount:           ContextFabricInterpretationRejectionComparisonTermsMaxCount,
	ContextFabricInterpretationRejectionSubjectTermsInvalid:               ContextFabricInterpretationRejectionSubjectTermsInvalid,
	ContextFabricInterpretationRejectionComparisonTermsInvalid:            ContextFabricInterpretationRejectionComparisonTermsInvalid,
	ContextFabricInterpretationRejectionFactRequirementsMaxCount:          ContextFabricInterpretationRejectionFactRequirementsMaxCount,
	ContextFabricInterpretationRejectionClarificationReasonMax:            ContextFabricInterpretationRejectionClarificationReasonMax,
	ContextFabricInterpretationRejectionTimeContextInvalid:                ContextFabricInterpretationRejectionTimeContextInvalid,
	ContextFabricInterpretationRejectionFactRequirementKindInvalid:        ContextFabricInterpretationRejectionFactRequirementKindInvalid,
	ContextFabricInterpretationRejectionFactRequirementSubjectsInvalid:    ContextFabricInterpretationRejectionFactRequirementSubjectsInvalid,
	ContextFabricInterpretationRejectionFactRequirementParamsMaxCount:     ContextFabricInterpretationRejectionFactRequirementParamsMaxCount,
	ContextFabricInterpretationRejectionFactRequirementParameterInvalid:   ContextFabricInterpretationRejectionFactRequirementParameterInvalid,
	ContextFabricInterpretationRejectionFactRequirementKindDuplicate:      ContextFabricInterpretationRejectionFactRequirementKindDuplicate,
	ContextFabricInterpretationRejectionClarificationReasonMissing:        ContextFabricInterpretationRejectionClarificationReasonMissing,
	ContextFabricInterpretationRejectionWindowClassInvalid:                ContextFabricInterpretationRejectionWindowClassInvalid,
	ContextFabricInterpretationRejectionWindowConfidenceInvalid:           ContextFabricInterpretationRejectionWindowConfidenceInvalid,
	ContextFabricInterpretationRejectionFactCapabilityParameterNotAllowed: ContextFabricInterpretationRejectionFactCapabilityParameterNotAllowed,
}

// ValidContextFabricInterpretationRejectionReason reports whether reason is
// a member of the closed vocabulary. Used by the telemetry seam so a value
// that somehow escapes the constants above is reported as "unclassified"
// rather than emitted verbatim -- the same fail-closed posture every other
// closed-vocabulary field in this package applies at its own boundary.
func ValidContextFabricInterpretationRejectionReason(reason ContextFabricInterpretationRejectionReason) bool {
	_, ok := canonicalContextFabricInterpretationRejectionReasons[reason]
	return ok
}

// CanonicalContextFabricInterpretationRejectionReason returns the TABLE's
// own constant for reason, or Unclassified when reason is not a member.
// Callers outside this package must route every value through this before
// it reaches a log field or a receipt -- see the canonical table's doc
// comment for why returning the table's constant, rather than the input,
// is the load-bearing part.
func CanonicalContextFabricInterpretationRejectionReason(reason ContextFabricInterpretationRejectionReason) ContextFabricInterpretationRejectionReason {
	if canonical, ok := canonicalContextFabricInterpretationRejectionReasons[reason]; ok {
		return canonical
	}
	return ContextFabricInterpretationRejectionUnclassified
}

// DiagnoseContextFabricInterpretedQuestionRejection names WHICH rule in
// ContextFabricInterpretedQuestion.validate() rejected q.
//
// It is a literal, clause-by-clause MIRROR of that method's own
// left-to-right, short-circuit evaluation order -- the identical discipline
// DiagnoseContextFabricInterpretedQuestionBound already follows, and for
// the identical reason (CHAOS-3784 round-4): a reason must be SOUND before
// it is complete. Naming a LATER failing clause when an EARLIER one is what
// actually rejected tells an operator to fix a field that was not the
// reason, which is worse than naming nothing.
//
// It never changes the accept/reject decision; it only diagnoses a
// rejection already decided by validate(). Called on a question validate()
// ACCEPTS, it returns Unclassified and ok=false -- callers must only
// consult it for a value validate() actually rejected.
//
// The mirrored method is validate(contextFabricWriteBounds), the bounds
// Validate() itself passes. A change to validate()'s clause ORDER must be
// mirrored here; TestDiagnoseContextFabricInterpretedQuestionRejectionMatchesValidateStatementOrder
// exists to catch that drift rather than merely to exercise a single call.
func DiagnoseContextFabricInterpretedQuestionRejection(q ContextFabricInterpretedQuestion) (ContextFabricInterpretationRejectionReason, bool) {
	bounds := contextFabricWriteBounds
	// Statement 1, one `||` expression: shape, then requested_judgment,
	// then subject_terms count, then comparison_terms count, then
	// subject_terms (trim+length+unique), then comparison_terms, then
	// fact_requirements count, then clarification_reason length.
	if !validInvestigationShape(q.Shape) {
		return ContextFabricInterpretationRejectionShapeInvalid, true
	}
	if !boundedText(q.RequestedJudgment, 1, ContextFabricRequestedJudgmentMaxLength, bounds) {
		return ContextFabricInterpretationRejectionRequestedJudgmentInvalid, true
	}
	if len(q.SubjectTerms) > bounds.interpretationTerms {
		return ContextFabricInterpretationRejectionSubjectTermsMaxCount, true
	}
	if len(q.ComparisonTerms) > bounds.interpretationTerms {
		return ContextFabricInterpretationRejectionComparisonTermsMaxCount, true
	}
	if !uniqueTrimmedStrings(q.SubjectTerms, ContextFabricSubjectOrComparisonTermMaxLength) {
		return ContextFabricInterpretationRejectionSubjectTermsInvalid, true
	}
	if !uniqueTrimmedStrings(q.ComparisonTerms, ContextFabricSubjectOrComparisonTermMaxLength) {
		return ContextFabricInterpretationRejectionComparisonTermsInvalid, true
	}
	if len(q.FactRequirements) > ContextFabricFactRequirementsMaxCount {
		return ContextFabricInterpretationRejectionFactRequirementsMaxCount, true
	}
	// validate() checks ClarificationReason RAW here, not trimmed -- the
	// trimmed comparison is the separate, LATER "clarification_needed
	// requires a reason" business rule below. Minimum 0, so this can only
	// fail on the max side.
	if !stringLengthBetween(q.ClarificationReason, 0, ContextFabricClarificationReasonMaxLength) {
		return ContextFabricInterpretationRejectionClarificationReasonMax, true
	}
	// Statement 2: TimeContext.
	if err := q.TimeContext.Validate(); err != nil {
		return ContextFabricInterpretationRejectionTimeContextInvalid, true
	}
	// Statement 3: the fact_requirements loop -- per entry, validate()
	// first, then the duplicate-kind check, so a requirement that is BOTH
	// invalid and a duplicate reports invalid, matching validate().
	seen := make(map[ContextFabricFactKind]struct{}, len(q.FactRequirements))
	for _, requirement := range q.FactRequirements {
		if requirement.validate(bounds) != nil {
			return diagnoseContextFabricFactRequirementRejection(requirement, bounds), true
		}
		if _, exists := seen[requirement.Kind]; exists {
			return ContextFabricInterpretationRejectionFactRequirementKindDuplicate, true
		}
		seen[requirement.Kind] = struct{}{}
	}
	// Statement 4: clarification_needed requires a reason.
	if q.ClarificationNeeded && strings.TrimSpace(q.ClarificationReason) == "" {
		return ContextFabricInterpretationRejectionClarificationReasonMissing, true
	}
	// Statements 5 and 6: the W1 shadow classifications. Empty is
	// legitimate for both, so only a non-empty invalid value rejects.
	if q.WindowClass != "" && !ValidContextFabricWindowClass(q.WindowClass) {
		return ContextFabricInterpretationRejectionWindowClassInvalid, true
	}
	if q.WindowConfidence != "" && !ValidContextFabricWindowConfidence(q.WindowConfidence) {
		return ContextFabricInterpretationRejectionWindowConfidenceInvalid, true
	}
	// validate() accepts q. A caller consulting this function for a
	// question that was not rejected gets no name and ok=false, never a
	// fabricated one.
	return ContextFabricInterpretationRejectionUnclassified, false
}

// diagnoseContextFabricFactRequirementRejection mirrors
// ContextFabricFactRequirement.validate()'s own clause order: kind enum,
// then subjects count/uniqueness, then parameters count, THEN (a separate
// later loop) each parameter key/value pair.
//
// r.Parameters is a Go map, whose iteration order is randomized per range.
// validate() sorts the keys first (CHAOS-3784 round-5 R5-1) precisely so
// that WHICH parameter it rejects on is deterministic; this mirror sorts
// identically. Every per-key clause maps to ONE reason, because validate()
// itself collapses key length, value length, key trim and value trim into a
// single rejection statement -- splitting them here would name a
// distinction the validator does not make.
func diagnoseContextFabricFactRequirementRejection(r ContextFabricFactRequirement, bounds contextFabricBounds) ContextFabricInterpretationRejectionReason {
	if !validFactKind(r.Kind) {
		return ContextFabricInterpretationRejectionFactRequirementKindInvalid
	}
	if len(r.Subjects) > 250 || !uniqueSubjects(r.Subjects) {
		return ContextFabricInterpretationRejectionFactRequirementSubjectsInvalid
	}
	if len(r.Parameters) > ContextFabricFactRequirementParametersMaxCount {
		return ContextFabricInterpretationRejectionFactRequirementParamsMaxCount
	}
	keys := make([]string, 0, len(r.Parameters))
	for key := range r.Parameters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := r.Parameters[key]
		if !stringLengthBetween(key, 1, ContextFabricFactRequirementParameterKeyMaxLength) ||
			!stringLengthBetween(value, 0, bounds.factParameterValueLength) ||
			strings.TrimSpace(key) != key || strings.TrimSpace(value) != value {
			return ContextFabricInterpretationRejectionFactRequirementParameterInvalid
		}
	}
	// Unreachable for a requirement validate() rejected -- every clause
	// above mirrors one of its own. Reached only if validate() gains a
	// clause this mirror has not; Unclassified rather than a guess, and
	// the order-drift test is what turns that into a red build.
	return ContextFabricInterpretationRejectionUnclassified
}
