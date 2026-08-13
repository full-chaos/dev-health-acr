package v1

import "strings"

// This file complements context_fabric_model_bounds.go: that file's
// Validate()/ValidateAgainst() methods collapse every violation into one
// generic "violates v1 bounds" error BY DESIGN -- a single whole-draft
// rejection, see that file's doc comment. This file never changes that
// accept/reject decision; it only DIAGNOSES, for a rejection already
// decided elsewhere, which specific model-facing bound (if any) caused it,
// so a caller (the context-fabric investigations route, CHAOS-3784) can
// report a stable bound name instead of an opaque, undifferentiated
// rejection.
//
// Every name a Diagnose* function returns is a literal entry in
// ContextFabricModelFacingBounds, never a hand-written string, so the two
// registries cannot drift apart. A Diagnose* function returns ok=false when
// the rejection is not attributable to one of these length/count bounds --
// an invalid enum value, a missing required field, or a cross-field
// business rule such as claim-binding/grounding closure -- because
// reporting no bound is safer than guessing one.

// DiagnoseContextFabricInterpretedQuestionBound returns the name of the
// first model-facing bound (see ContextFabricModelFacingBounds) that q
// violates, checked in the same field order
// ContextFabricInterpretedQuestion.Validate does, or ok=false if q's
// rejection (if any) is not one of these bounds.
func DiagnoseContextFabricInterpretedQuestionBound(q ContextFabricInterpretedQuestion) (bound string, ok bool) {
	if len(strings.TrimSpace(q.RequestedJudgment)) > ContextFabricRequestedJudgmentMaxLength {
		return "interpretation.requested_judgment.max_length", true
	}
	if len(q.SubjectTerms) > ContextFabricSubjectTermsMaxCount {
		return "interpretation.subject_terms.max_count", true
	}
	if len(q.ComparisonTerms) > ContextFabricComparisonTermsMaxCount {
		return "interpretation.comparison_terms.max_count", true
	}
	for _, term := range q.SubjectTerms {
		if len(strings.TrimSpace(term)) > ContextFabricSubjectOrComparisonTermMaxLength {
			return "interpretation.subject_term.max_length", true
		}
	}
	for _, term := range q.ComparisonTerms {
		if len(strings.TrimSpace(term)) > ContextFabricSubjectOrComparisonTermMaxLength {
			return "interpretation.subject_term.max_length", true
		}
	}
	if len(q.FactRequirements) > ContextFabricFactRequirementsMaxCount {
		return "interpretation.fact_requirements.max_count", true
	}
	if len(strings.TrimSpace(q.ClarificationReason)) > ContextFabricClarificationReasonMaxLength {
		return "interpretation.clarification_reason.max_length", true
	}
	for _, requirement := range q.FactRequirements {
		if bound, ok := DiagnoseContextFabricFactRequirementBound(requirement); ok {
			return bound, true
		}
	}
	return "", false
}

// DiagnoseContextFabricFactRequirementBound is the
// ContextFabricFactRequirement counterpart, used standalone and as
// DiagnoseContextFabricInterpretedQuestionBound's per-item helper.
func DiagnoseContextFabricFactRequirementBound(r ContextFabricFactRequirement) (bound string, ok bool) {
	if len(r.Parameters) > ContextFabricFactRequirementParametersMaxCount {
		return "interpretation.fact_requirement.parameters.max_count", true
	}
	for key, value := range r.Parameters {
		if len(key) > ContextFabricFactRequirementParameterKeyMaxLength {
			return "interpretation.fact_requirement.parameter_key.max_length", true
		}
		if len(value) > ContextFabricFactRequirementParameterValueMaxLength {
			return "interpretation.fact_requirement.parameter_value.max_length", true
		}
	}
	return "", false
}

// DiagnoseContextFabricDriverJudgmentBound is the ContextFabricDriverJudgment
// counterpart, checked in the same field order
// ContextFabricDriverJudgment.Validate does. Only the length/count bounds
// are diagnosed here -- an invalid Standing/Category/Derivation/
// EpistemicStatus enum, a missing evidence closure, or a category->claimed
// fact requirement is a business rule, not a registry bound, and returns
// ok=false.
func DiagnoseContextFabricDriverJudgmentBound(d ContextFabricDriverJudgment) (bound string, ok bool) {
	if !stringLengthBetween(d.DriverID, ContextFabricModelMintedIDMinLength, ContextFabricModelMintedIDMaxLength) {
		return "synthesis.driver.driver_id.max_length", true
	}
	if len(strings.TrimSpace(d.Title)) > ContextFabricDriverTitleMaxLength {
		return "synthesis.driver.title.max_length", true
	}
	if len(strings.TrimSpace(d.Summary)) > ContextFabricDriverSummaryMaxLength {
		return "synthesis.driver.summary.max_length", true
	}
	if len(d.Qualification) > ContextFabricDriverQualificationMaxLength {
		return "synthesis.driver.qualification.max_length", true
	}
	if len(d.AffectedSubjects) > ContextFabricDriverAffectedSubjectsMaxCount {
		return "synthesis.driver.affected_subjects.max_count", true
	}
	if len(d.PathIDs) > ContextFabricDriverPathIDsMaxCount {
		return "synthesis.driver.path_ids.max_count", true
	}
	if len(d.ClaimedFactIDs) > ContextFabricDriverClaimedFactIDsMaxCount {
		return "synthesis.driver.claimed_fact_ids.max_count", true
	}
	if len(d.EvidenceRefIDs) > ContextFabricEvidenceRefIDsMaxCount {
		return "synthesis.driver.evidence_ref_ids.max_count", true
	}
	return "", false
}

// DiagnoseContextFabricFindingBound is the ContextFabricFinding counterpart.
func DiagnoseContextFabricFindingBound(f ContextFabricFinding) (bound string, ok bool) {
	if !stringLengthBetween(f.FindingID, ContextFabricModelMintedIDMinLength, ContextFabricModelMintedIDMaxLength) {
		return "synthesis.finding.finding_id.max_length", true
	}
	if len(strings.TrimSpace(f.Kind)) > ContextFabricFindingKindMaxLength {
		return "synthesis.finding.kind.max_length", true
	}
	if len(strings.TrimSpace(f.Summary)) > ContextFabricFindingSummaryMaxLength {
		return "synthesis.finding.summary.max_length", true
	}
	if len(f.Subjects) > ContextFabricFindingSubjectsMaxCount {
		return "synthesis.finding.subjects.max_count", true
	}
	if len(f.EvidenceRefIDs) > ContextFabricEvidenceRefIDsMaxCount {
		return "synthesis.finding.evidence_ref_ids.max_count", true
	}
	if len(f.ClaimedFactIDs) > ContextFabricDriverClaimedFactIDsMaxCount {
		return "synthesis.finding.claimed_fact_ids.max_count", true
	}
	return "", false
}

// DiagnoseContextFabricClaimedFactBound is the ContextFabricClaimedFact
// counterpart.
func DiagnoseContextFabricClaimedFactBound(c ContextFabricClaimedFact) (bound string, ok bool) {
	if !stringLengthBetween(c.ClaimID, ContextFabricModelMintedIDMinLength, ContextFabricModelMintedIDMaxLength) {
		return "synthesis.claimed_fact.claim_id.max_length", true
	}
	if len(c.Field) > ContextFabricClaimedFieldMaxLength {
		return "synthesis.claimed_fact.field.max_length", true
	}
	return "", false
}
