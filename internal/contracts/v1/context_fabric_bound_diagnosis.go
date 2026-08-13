package v1

import (
	"sort"
	"strings"
	"unicode/utf8"
)

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
// an invalid enum value, a missing required field, a value too SHORT
// rather than too long (see runeLongerThan's doc comment), or a
// cross-field business rule such as claim-binding/grounding closure --
// because reporting no bound is safer than guessing one.
//
// Every length comparison below measures Unicode code points via
// utf8.RuneCountInString, exactly matching stringLengthBetween
// (validation_helpers.go), which every Validate() method in
// context_fabric_model_bounds.go and validate_context_fabric_result.go
// actually enforces against. A raw len() byte count would over-report
// violations for any multi-byte input a validator would still accept
// (CHAOS-3784 round-2 F3).

// runeLongerThan reports whether value's rune count exceeds maximum. Used
// in place of stringLengthBetween(value, min, max) for every field below:
// a length-bound violation this package attributes to a model-facing
// registry entry is ALWAYS an over-length report (the shape CHAOS-3770
// evidence and this whole diagnosis mechanism exist for), never a
// too-SHORT report -- min_length has no entry of its own in
// ContextFabricModelFacingBounds (driver_id/finding_id/claim_id's shared
// [8,256] shape has exactly one registry entry, named "max_length"), so
// claiming that name for a too-short value would misreport the actual
// defect. A too-short minted ID therefore returns ok=false here, same as
// any other business-rule rejection this package doesn't attribute.
func runeLongerThan(value string, maximum int) bool {
	return utf8.RuneCountInString(value) > maximum
}

// DiagnoseContextFabricInterpretedQuestionBound returns the name of the
// first model-facing bound (see ContextFabricModelFacingBounds) that q
// violates, checked in the same field order
// ContextFabricInterpretedQuestion.Validate does, or ok=false if q's
// rejection (if any) is not one of these bounds.
func DiagnoseContextFabricInterpretedQuestionBound(q ContextFabricInterpretedQuestion) (bound string, ok bool) {
	if runeLongerThan(strings.TrimSpace(q.RequestedJudgment), ContextFabricRequestedJudgmentMaxLength) {
		return "interpretation.requested_judgment.max_length", true
	}
	if len(q.SubjectTerms) > ContextFabricSubjectTermsMaxCount {
		return "interpretation.subject_terms.max_count", true
	}
	if len(q.ComparisonTerms) > ContextFabricComparisonTermsMaxCount {
		return "interpretation.comparison_terms.max_count", true
	}
	for _, term := range q.SubjectTerms {
		if runeLongerThan(strings.TrimSpace(term), ContextFabricSubjectOrComparisonTermMaxLength) {
			return "interpretation.subject_term.max_length", true
		}
	}
	for _, term := range q.ComparisonTerms {
		if runeLongerThan(strings.TrimSpace(term), ContextFabricSubjectOrComparisonTermMaxLength) {
			return "interpretation.subject_term.max_length", true
		}
	}
	if len(q.FactRequirements) > ContextFabricFactRequirementsMaxCount {
		return "interpretation.fact_requirements.max_count", true
	}
	if runeLongerThan(strings.TrimSpace(q.ClarificationReason), ContextFabricClarificationReasonMaxLength) {
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
// DiagnoseContextFabricInterpretedQuestionBound's per-item helper. r.Parameters
// is a Go map, whose iteration order is randomized per run -- when MORE
// THAN ONE parameter violates a (possibly different) bound, iterating it
// directly would make the returned bound name order-dependent, flaky
// across runs (CHAOS-3784 round-2 F4). Keys are sorted first so the result
// is deterministic: the lexicographically-first key whose entry violates
// anything wins, always.
func DiagnoseContextFabricFactRequirementBound(r ContextFabricFactRequirement) (bound string, ok bool) {
	if len(r.Parameters) > ContextFabricFactRequirementParametersMaxCount {
		return "interpretation.fact_requirement.parameters.max_count", true
	}
	keys := make([]string, 0, len(r.Parameters))
	for key := range r.Parameters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if runeLongerThan(key, ContextFabricFactRequirementParameterKeyMaxLength) {
			return "interpretation.fact_requirement.parameter_key.max_length", true
		}
		if runeLongerThan(r.Parameters[key], ContextFabricFactRequirementParameterValueMaxLength) {
			return "interpretation.fact_requirement.parameter_value.max_length", true
		}
	}
	return "", false
}

// diagnoseSubjectRefBound checks s's CanonicalID/Label against the shared
// ContextFabricSubjectRef length bounds (SubjectRef.Validate(),
// validate_context_fabric_result.go), returning the CALLER's own registry
// name for whichever is violated -- driver.affected_subjects,
// finding.subjects, and claimed_fact.subject each name their own entry
// even though all three enforce the identical
// ContextFabricSubjectRefCanonicalIDMaxLength/LabelMaxLength shape
// (CHAOS-3784 round-3 R3-1), matching this registry's established
// one-entry-per-(struct,field) convention.
func diagnoseSubjectRefBound(s ContextFabricSubjectRef, canonicalIDBound, labelBound string) (bound string, ok bool) {
	if runeLongerThan(s.CanonicalID, ContextFabricSubjectRefCanonicalIDMaxLength) {
		return canonicalIDBound, true
	}
	if runeLongerThan(s.Label, ContextFabricSubjectRefLabelMaxLength) {
		return labelBound, true
	}
	return "", false
}

// DiagnoseContextFabricDriverJudgmentBound is the ContextFabricDriverJudgment
// counterpart, checked in the same STATEMENT order
// ContextFabricDriverJudgment.Validate does -- not just the same field
// order within one statement, but the same order across its three
// sequential OR-expressions (identity/title/summary/qualification, THEN
// subject/path/evidence references, THEN claimed-fact references): Go
// evaluates and returns on the FIRST statement that fails, so a
// diagnosis checking a LATER statement's field before an EARLIER
// statement's violated field would misattribute the rejection to a bound
// Validate() never actually reached (CHAOS-3784 round-3 R3-1/R3-2 class).
// Only the length/count bounds are diagnosed here -- an invalid Standing/
// Category/Derivation/EpistemicStatus enum, a missing evidence closure, or
// a category->claimed fact requirement is a business rule, not a registry
// bound, and returns ok=false.
func DiagnoseContextFabricDriverJudgmentBound(d ContextFabricDriverJudgment) (bound string, ok bool) {
	// Statement 1: identity/title/summary/qualification.
	if runeLongerThan(d.DriverID, ContextFabricModelMintedIDMaxLength) {
		return "synthesis.driver.driver_id.max_length", true
	}
	if runeLongerThan(strings.TrimSpace(d.Title), ContextFabricDriverTitleMaxLength) {
		return "synthesis.driver.title.max_length", true
	}
	if runeLongerThan(strings.TrimSpace(d.Summary), ContextFabricDriverSummaryMaxLength) {
		return "synthesis.driver.summary.max_length", true
	}
	if runeLongerThan(d.Qualification, ContextFabricDriverQualificationMaxLength) {
		return "synthesis.driver.qualification.max_length", true
	}
	// Statement 2: affected_subjects, path_ids, evidence_ref_ids -- ONE
	// OR-expression in Validate(), so order among these three fields does
	// not matter for fidelity (they share one rejection), but ALL of them
	// must be checked before statement 3 below.
	if len(d.AffectedSubjects) > ContextFabricDriverAffectedSubjectsMaxCount {
		return "synthesis.driver.affected_subjects.max_count", true
	}
	for _, subject := range d.AffectedSubjects {
		if bound, ok := diagnoseSubjectRefBound(subject, "synthesis.driver.affected_subjects.item_canonical_id_max_length", "synthesis.driver.affected_subjects.item_label_max_length"); ok {
			return bound, true
		}
	}
	if len(d.PathIDs) > ContextFabricDriverPathIDsMaxCount {
		return "synthesis.driver.path_ids.max_count", true
	}
	for _, pathID := range d.PathIDs {
		if runeLongerThan(pathID, ContextFabricIdentifierRefMaxLength) {
			return "synthesis.driver.path_ids.item_max_length", true
		}
	}
	if len(d.EvidenceRefIDs) > ContextFabricEvidenceRefIDsMaxCount {
		return "synthesis.driver.evidence_ref_ids.max_count", true
	}
	for _, evidenceRefID := range d.EvidenceRefIDs {
		if runeLongerThan(evidenceRefID, ContextFabricEvidenceRefIDMaxLength) {
			return "synthesis.driver.evidence_ref_ids.item_max_length", true
		}
	}
	// Statement 3: claimed_fact_ids -- must be checked LAST, after every
	// statement-2 field above.
	if len(d.ClaimedFactIDs) > ContextFabricDriverClaimedFactIDsMaxCount {
		return "synthesis.driver.claimed_fact_ids.max_count", true
	}
	for _, claimedFactID := range d.ClaimedFactIDs {
		if runeLongerThan(claimedFactID, ContextFabricIdentifierRefMaxLength) {
			return "synthesis.driver.claimed_fact_ids.item_max_length", true
		}
	}
	return "", false
}

// DiagnoseContextFabricFindingBound is the ContextFabricFinding counterpart.
// See DiagnoseContextFabricDriverJudgmentBound's doc comment: statement
// order (identity/kind/summary/subjects/evidence_ref_ids, THEN
// claimed_fact_ids) matches ContextFabricFinding.Validate()'s exactly.
func DiagnoseContextFabricFindingBound(f ContextFabricFinding) (bound string, ok bool) {
	// Statement 1: identity, kind, summary, subjects, evidence_ref_ids --
	// ONE OR-expression in Validate().
	if runeLongerThan(f.FindingID, ContextFabricModelMintedIDMaxLength) {
		return "synthesis.finding.finding_id.max_length", true
	}
	if runeLongerThan(strings.TrimSpace(f.Kind), ContextFabricFindingKindMaxLength) {
		return "synthesis.finding.kind.max_length", true
	}
	if runeLongerThan(strings.TrimSpace(f.Summary), ContextFabricFindingSummaryMaxLength) {
		return "synthesis.finding.summary.max_length", true
	}
	if len(f.Subjects) > ContextFabricFindingSubjectsMaxCount {
		return "synthesis.finding.subjects.max_count", true
	}
	for _, subject := range f.Subjects {
		if bound, ok := diagnoseSubjectRefBound(subject, "synthesis.finding.subjects.item_canonical_id_max_length", "synthesis.finding.subjects.item_label_max_length"); ok {
			return bound, true
		}
	}
	if len(f.EvidenceRefIDs) > ContextFabricEvidenceRefIDsMaxCount {
		return "synthesis.finding.evidence_ref_ids.max_count", true
	}
	for _, evidenceRefID := range f.EvidenceRefIDs {
		if runeLongerThan(evidenceRefID, ContextFabricEvidenceRefIDMaxLength) {
			return "synthesis.finding.evidence_ref_ids.item_max_length", true
		}
	}
	// Statement 2: claimed_fact_ids -- must be checked LAST.
	if len(f.ClaimedFactIDs) > ContextFabricDriverClaimedFactIDsMaxCount {
		return "synthesis.finding.claimed_fact_ids.max_count", true
	}
	for _, claimedFactID := range f.ClaimedFactIDs {
		if runeLongerThan(claimedFactID, ContextFabricIdentifierRefMaxLength) {
			return "synthesis.finding.claimed_fact_ids.item_max_length", true
		}
	}
	return "", false
}

// DiagnoseContextFabricClaimedFactBound is the ContextFabricClaimedFact
// counterpart. See DiagnoseContextFabricDriverJudgmentBound's doc comment:
// statement order (claim_id/field, THEN subject, THEN value) matches
// ContextFabricClaimedFact.Validate()'s three sequential statements
// exactly (identity block, then c.Subject.Validate(), then
// c.Value.Validate()).
func DiagnoseContextFabricClaimedFactBound(c ContextFabricClaimedFact) (bound string, ok bool) {
	if runeLongerThan(c.ClaimID, ContextFabricModelMintedIDMaxLength) {
		return "synthesis.claimed_fact.claim_id.max_length", true
	}
	if runeLongerThan(c.Field, ContextFabricClaimedFieldMaxLength) {
		return "synthesis.claimed_fact.field.max_length", true
	}
	if bound, ok := diagnoseSubjectRefBound(c.Subject, "synthesis.claimed_fact.subject.canonical_id_max_length", "synthesis.claimed_fact.subject.label_max_length"); ok {
		return bound, true
	}
	if c.Value.String != nil && runeLongerThan(*c.Value.String, ContextFabricClaimedFactValueMaxLength) {
		return "synthesis.claimed_fact.value.max_length", true
	}
	return "", false
}
