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
// CHAOS-3784 round-4: violated_bound must be SOUND before it is complete --
// an absent bound name is acceptable (the caller still gets
// interpretation_rejected/synthesis_rejected), but a WRONG one is never
// acceptable, because it tells an operator to fix a field that was not
// actually the reason the model's output was rejected. Rounds 1-3 built
// Diagnose* functions that scanned a struct for ANY registrable violation,
// independently of whether an EARLIER, non-registrable check (an invalid
// enum, a too-short value, a duplicate) would have made Validate() reject
// first. Every function below is instead a literal, clause-by-clause
// MIRROR of its Validate() counterpart's own left-to-right, short-circuit
// evaluation order (Go's || stops at the first true operand) -- including
// clauses that are NEVER bound-nameable -- and returns bound/ok for the
// FIRST clause that fails, exactly the one Validate() itself would have
// rejected on. A later clause, even a genuine registered bound, is never
// reached if an earlier one already failed, matching Validate() exactly.
//
// Each function's doc comment names the exact Validate() method (and, for
// internal/contextfabric's diagnoseSynthesisDraftBound, ValidateAgainst)
// its clause order is pinned to; a change to that method's clause order
// must be mirrored here, and the regression tests alongside this file
// (TestDiagnose*MatchesValidateStatementOrder and friends) exist to catch
// drift, not merely to exercise a single call.
//
// Every length comparison measures Unicode code points via
// utf8.RuneCountInString, exactly matching stringLengthBetween
// (validation_helpers.go), which every Validate() method in
// context_fabric_model_bounds.go and validate_context_fabric_result.go
// actually enforces against. A raw len() byte count would over-report
// violations for any multi-byte input a validator would still accept
// (CHAOS-3784 round-2 F3).

// diagnoseLengthBound mirrors one stringLengthBetween(value, minimum,
// maximum)-shaped OR-clause. passed=true means this clause did NOT fail
// (the caller must continue to Validate()'s next clause); passed=false
// means this clause is the one Validate() rejected on -- ok=true with a
// bound name if the failure is on the MAX side (the only side this
// registry ever names -- CHAOS-3784 round-1 F3: min_length has no entry
// of its own in ContextFabricModelFacingBounds, e.g. driver_id/
// finding_id/claim_id's shared [8,256] shape has exactly one registry
// entry, named "max_length" -- so a too-SHORT value is never reported
// under that name), ok=false with no name if it's the MIN side (a
// business-rule-shaped rejection with nothing to attribute). A clause
// with no effective minimum (minimum=0) can only ever fail on the max
// side.
func diagnoseLengthBound(value string, minimum, maximum int, boundName string) (bound string, ok bool, passed bool) {
	length := utf8.RuneCountInString(value)
	if length >= minimum && length <= maximum {
		return "", false, true
	}
	if length > maximum {
		return boundName, true, false
	}
	return "", false, false
}

// diagnoseUniqueTrimmedStringsBound mirrors uniqueTrimmedStrings(values,
// maximum)'s exact per-item clause order (validate_context_fabric_helpers.go):
// for each item, in slice order, trim FIRST, then length [1,maximum],
// then (after the whole loop) duplicate. The first item whose own clause
// fails wins, matching uniqueTrimmedStrings' early return.
func diagnoseUniqueTrimmedStringsBound(values []string, maximum int, boundName string) (bound string, ok bool, passed bool) {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != value {
			return "", false, false
		}
		if bound, ok, itemPassed := diagnoseLengthBound(value, 1, maximum, boundName); !itemPassed {
			return bound, ok, false
		}
		if _, exists := seen[value]; exists {
			return "", false, false
		}
		seen[value] = struct{}{}
	}
	return "", false, true
}

// diagnoseBoundedEvidenceRefsBound mirrors boundedEvidenceRefs(values,
// maximum, allowEmpty)'s exact clause order (validate_context_fabric_helpers.go):
// nil/count/empty first (count is the only registrable one), then per
// item in slice order -- length [8,256] FIRST, then trim, then the '|'
// separator check -- then (after the whole loop) duplicate.
func diagnoseBoundedEvidenceRefsBound(values []string, maximum int, allowEmpty bool, countBoundName, itemBoundName string) (bound string, ok bool, passed bool) {
	if values == nil || len(values) > maximum || (!allowEmpty && len(values) == 0) {
		if len(values) > maximum {
			return countBoundName, true, false
		}
		return "", false, false
	}
	for _, value := range values {
		if bound, ok, itemPassed := diagnoseLengthBound(value, 8, 256, itemBoundName); !itemPassed {
			return bound, ok, false
		}
		if strings.TrimSpace(value) != value {
			return "", false, false
		}
		if strings.Contains(value, "|") {
			return "", false, false
		}
	}
	if !uniqueStrings(values) {
		return "", false, false
	}
	return "", false, true
}

// diagnoseSubjectRefBound mirrors ContextFabricSubjectRef.Validate()'s
// exact clause order: kind enum FIRST (no bound -- if it fails, the length
// clauses below are never reached, exactly as Validate()'s own
// short-circuiting || means CanonicalID/Label are never even inspected),
// then CanonicalID length, then Label length, then -- a separate LATER
// statement in Validate() -- the trim checks (no bound either).
// canonicalIDBound/labelBound are the CALLER's own registry names for its
// (struct,field) pair (driver.affected_subjects, finding.subjects, or
// claimed_fact.subject all enforce this identical shape but name separate
// registry entries).
func diagnoseSubjectRefBound(s ContextFabricSubjectRef, canonicalIDBound, labelBound string) (bound string, ok bool, passed bool) {
	if !validContextFabricSubjectKind(s.Kind) {
		return "", false, false
	}
	if bound, ok, itemPassed := diagnoseLengthBound(s.CanonicalID, 1, ContextFabricSubjectRefCanonicalIDMaxLength, canonicalIDBound); !itemPassed {
		return bound, ok, false
	}
	if bound, ok, itemPassed := diagnoseLengthBound(s.Label, 1, ContextFabricSubjectRefLabelMaxLength, labelBound); !itemPassed {
		return bound, ok, false
	}
	if strings.TrimSpace(s.CanonicalID) != s.CanonicalID || strings.TrimSpace(s.Label) != s.Label {
		return "", false, false
	}
	return "", false, true
}

// diagnoseSubjectRefsBound mirrors uniqueSubjects(values)'s exact per-item
// order (validate_context_fabric_helpers.go): each subject's OWN
// Validate() (diagnoseSubjectRefBound above) FIRST, then -- only once
// every subject individually passes -- the cross-item duplicate check, in
// slice order, first-failing-item wins.
func diagnoseSubjectRefsBound(values []ContextFabricSubjectRef, canonicalIDBound, labelBound string) (bound string, ok bool, passed bool) {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if bound, ok, itemPassed := diagnoseSubjectRefBound(value, canonicalIDBound, labelBound); !itemPassed {
			return bound, ok, false
		}
		key := subjectKey(value)
		if _, exists := seen[key]; exists {
			return "", false, false
		}
		seen[key] = struct{}{}
	}
	return "", false, true
}

// DiagnoseContextFabricInterpretedQuestionBound mirrors
// ContextFabricInterpretedQuestion.Validate()'s exact statement/clause
// order and returns the bound (if any) the FIRST failing clause names.
// ok=false, bound="" means q's rejection (if it has one) is not
// attributable to a registered bound -- either every clause passed (q is
// actually valid), or the first FAILING clause is a business rule (an
// invalid enum, a too-short value, a duplicate, a missing required field)
// that this registry does not cover.
func DiagnoseContextFabricInterpretedQuestionBound(q ContextFabricInterpretedQuestion) (bound string, ok bool) {
	// Statement 1 (one `||` expression in Validate()): shape, then
	// requested_judgment, then subject_terms count, then comparison_terms
	// count, then subject_terms (unique+trim+length), then comparison_terms
	// (unique+trim+length), then fact_requirements count, then
	// clarification_reason length.
	if !validInvestigationShape(q.Shape) {
		return "", false
	}
	if bound, ok, passed := diagnoseLengthBound(strings.TrimSpace(q.RequestedJudgment), 1, ContextFabricRequestedJudgmentMaxLength, "interpretation.requested_judgment.max_length"); !passed {
		return bound, ok
	}
	if len(q.SubjectTerms) > ContextFabricSubjectTermsMaxCount {
		return "interpretation.subject_terms.max_count", true
	}
	if len(q.ComparisonTerms) > ContextFabricComparisonTermsMaxCount {
		return "interpretation.comparison_terms.max_count", true
	}
	if bound, ok, passed := diagnoseUniqueTrimmedStringsBound(q.SubjectTerms, ContextFabricSubjectOrComparisonTermMaxLength, "interpretation.subject_term.max_length"); !passed {
		return bound, ok
	}
	if bound, ok, passed := diagnoseUniqueTrimmedStringsBound(q.ComparisonTerms, ContextFabricSubjectOrComparisonTermMaxLength, "interpretation.subject_term.max_length"); !passed {
		return bound, ok
	}
	if len(q.FactRequirements) > ContextFabricFactRequirementsMaxCount {
		return "interpretation.fact_requirements.max_count", true
	}
	// Validate() checks q.ClarificationReason RAW here, not trimmed (the
	// trimmed comparison is a separate, LATER "clarification_needed
	// requires a reason" business rule below) -- min=0 so this can only
	// fail on the max side.
	if bound, ok, passed := diagnoseLengthBound(q.ClarificationReason, 0, ContextFabricClarificationReasonMaxLength, "interpretation.clarification_reason.max_length"); !passed {
		return bound, ok
	}
	// Statement 2: TimeContext -- enum/timestamp logic, no bound.
	if err := q.TimeContext.Validate(); err != nil {
		return "", false
	}
	// Statement 3: fact_requirements loop, first-failing entry wins
	// (Validate() first, then duplicate-kind).
	seen := make(map[ContextFabricFactKind]struct{}, len(q.FactRequirements))
	for _, requirement := range q.FactRequirements {
		if err := requirement.Validate(); err != nil {
			return DiagnoseContextFabricFactRequirementBound(requirement)
		}
		if _, exists := seen[requirement.Kind]; exists {
			return "", false
		}
		seen[requirement.Kind] = struct{}{}
	}
	// Statement 4: clarification_needed requires a reason -- business rule.
	return "", false
}

// DiagnoseContextFabricFactRequirementBound mirrors
// ContextFabricFactRequirement.Validate()'s exact clause order: kind enum,
// then subjects count/uniqueness (structurally unreachable from the model
// -- see ContextFabricModelFacingBounds's doc comment -- included here
// only because Validate() itself checks it, never actually true in
// practice since the model has no wire field for Subjects), then
// parameters count, THEN (a separate later loop) each parameter key/value
// pair. r.Parameters is a Go map, whose iteration order is randomized per
// range; keys are sorted first so which parameter is "first" is
// deterministic (CHAOS-3784 round-2 F4), matching neither more nor less
// than Validate()'s own per-key clause order (key length, then value
// length, then key trim, then value trim).
func DiagnoseContextFabricFactRequirementBound(r ContextFabricFactRequirement) (bound string, ok bool) {
	if !validFactKind(r.Kind) {
		return "", false
	}
	if len(r.Subjects) > 250 || !uniqueSubjects(r.Subjects) {
		return "", false
	}
	if len(r.Parameters) > ContextFabricFactRequirementParametersMaxCount {
		return "interpretation.fact_requirement.parameters.max_count", true
	}
	keys := make([]string, 0, len(r.Parameters))
	for key := range r.Parameters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := r.Parameters[key]
		if bound, ok, passed := diagnoseLengthBound(key, 1, ContextFabricFactRequirementParameterKeyMaxLength, "interpretation.fact_requirement.parameter_key.max_length"); !passed {
			return bound, ok
		}
		if bound, ok, passed := diagnoseLengthBound(value, 0, ContextFabricFactRequirementParameterValueMaxLength, "interpretation.fact_requirement.parameter_value.max_length"); !passed {
			return bound, ok
		}
		if strings.TrimSpace(key) != key || strings.TrimSpace(value) != value {
			return "", false
		}
	}
	return "", false
}

// DiagnoseContextFabricDriverJudgmentBound mirrors
// ContextFabricDriverJudgment.Validate()'s exact statement order: identity
// (driver_id/standing/category/title/summary/derivation/epistemic_status/
// confidence/qualification), THEN subject/path/evidence references, THEN
// claimed-fact references, THEN three business-only statements (evidence
// closure, category-requires-claim, withheld-requires-qualification) that
// never name a bound but still must gate everything after them.
func DiagnoseContextFabricDriverJudgmentBound(d ContextFabricDriverJudgment) (bound string, ok bool) {
	// Statement 1.
	if bound, ok, passed := diagnoseLengthBound(d.DriverID, ContextFabricModelMintedIDMinLength, ContextFabricModelMintedIDMaxLength, "synthesis.driver.driver_id.max_length"); !passed {
		return bound, ok
	}
	if !validDriverStanding(d.Standing) {
		return "", false
	}
	if !validDriverCategory(ContextFabricDriverCategory(d.Category)) {
		return "", false
	}
	if bound, ok, passed := diagnoseLengthBound(strings.TrimSpace(d.Title), 1, ContextFabricDriverTitleMaxLength, "synthesis.driver.title.max_length"); !passed {
		return bound, ok
	}
	if bound, ok, passed := diagnoseLengthBound(strings.TrimSpace(d.Summary), 1, ContextFabricDriverSummaryMaxLength, "synthesis.driver.summary.max_length"); !passed {
		return bound, ok
	}
	if !validDerivationMethod(d.Derivation) {
		return "", false
	}
	if !validEpistemicStatus(d.EpistemicStatus) {
		return "", false
	}
	if d.Confidence < 0 || d.Confidence > 1 {
		return "", false
	}
	if bound, ok, passed := diagnoseLengthBound(d.Qualification, 0, ContextFabricDriverQualificationMaxLength, "synthesis.driver.qualification.max_length"); !passed {
		return bound, ok
	}
	// Statement 2: affected_subjects, path_ids, evidence_ref_ids -- ONE
	// OR-expression in Validate(), so this order (subjects, then paths,
	// then evidence) is Validate()'s own left-to-right clause order.
	// affected_subjects' min side (no bound) is Validate()'s own FIRST
	// sub-clause, checked before max (the registered bound).
	if len(d.AffectedSubjects) < ContextFabricDriverAffectedSubjectsMinCount {
		return "", false
	}
	if len(d.AffectedSubjects) > ContextFabricDriverAffectedSubjectsMaxCount {
		return "synthesis.driver.affected_subjects.max_count", true
	}
	if bound, ok, passed := diagnoseSubjectRefsBound(d.AffectedSubjects, "synthesis.driver.affected_subjects.item_canonical_id_max_length", "synthesis.driver.affected_subjects.item_label_max_length"); !passed {
		return bound, ok
	}
	if len(d.PathIDs) > ContextFabricDriverPathIDsMaxCount {
		return "synthesis.driver.path_ids.max_count", true
	}
	if bound, ok, passed := diagnoseUniqueTrimmedStringsBound(d.PathIDs, ContextFabricIdentifierRefMaxLength, "synthesis.driver.path_ids.item_max_length"); !passed {
		return bound, ok
	}
	if bound, ok, passed := diagnoseBoundedEvidenceRefsBound(d.EvidenceRefIDs, ContextFabricEvidenceRefIDsMaxCount, true, "synthesis.driver.evidence_ref_ids.max_count", "synthesis.driver.evidence_ref_ids.item_max_length"); !passed {
		return bound, ok
	}
	// Statement 3: claimed_fact_ids.
	if len(d.ClaimedFactIDs) > ContextFabricDriverClaimedFactIDsMaxCount {
		return "synthesis.driver.claimed_fact_ids.max_count", true
	}
	if bound, ok, passed := diagnoseUniqueTrimmedStringsBound(d.ClaimedFactIDs, ContextFabricIdentifierRefMaxLength, "synthesis.driver.claimed_fact_ids.item_max_length"); !passed {
		return bound, ok
	}
	// Statements 4-6: business rules only, no bound, but each still gates
	// what follows (there is nothing after statement 6, so this is only
	// for fidelity/documentation).
	if d.Standing != ContextFabricDriverWithheld && len(d.PathIDs) == 0 && len(d.EvidenceRefIDs) == 0 {
		return "", false
	}
	if _, required := ContextFabricDriverCategoryRequiresClaimedFact(ContextFabricDriverCategory(d.Category)); required && len(d.ClaimedFactIDs) == 0 {
		return "", false
	}
	if d.Standing == ContextFabricDriverWithheld && strings.TrimSpace(d.Qualification) == "" {
		return "", false
	}
	return "", false
}

// DiagnoseContextFabricFindingBound mirrors ContextFabricFinding.Validate()'s
// exact statement order: identity/kind/summary/subjects/evidence_ref_ids,
// THEN claimed_fact_ids, THEN a business-only "kind requires claim"
// statement.
func DiagnoseContextFabricFindingBound(f ContextFabricFinding) (bound string, ok bool) {
	// Statement 1.
	if bound, ok, passed := diagnoseLengthBound(f.FindingID, ContextFabricModelMintedIDMinLength, ContextFabricModelMintedIDMaxLength, "synthesis.finding.finding_id.max_length"); !passed {
		return bound, ok
	}
	if bound, ok, passed := diagnoseLengthBound(strings.TrimSpace(f.Kind), 1, ContextFabricFindingKindMaxLength, "synthesis.finding.kind.max_length"); !passed {
		return bound, ok
	}
	if bound, ok, passed := diagnoseLengthBound(strings.TrimSpace(f.Summary), 1, ContextFabricFindingSummaryMaxLength, "synthesis.finding.summary.max_length"); !passed {
		return bound, ok
	}
	if len(f.Subjects) > ContextFabricFindingSubjectsMaxCount {
		return "synthesis.finding.subjects.max_count", true
	}
	if bound, ok, passed := diagnoseSubjectRefsBound(f.Subjects, "synthesis.finding.subjects.item_canonical_id_max_length", "synthesis.finding.subjects.item_label_max_length"); !passed {
		return bound, ok
	}
	if bound, ok, passed := diagnoseBoundedEvidenceRefsBound(f.EvidenceRefIDs, ContextFabricEvidenceRefIDsMaxCount, false, "synthesis.finding.evidence_ref_ids.max_count", "synthesis.finding.evidence_ref_ids.item_max_length"); !passed {
		return bound, ok
	}
	// Statement 2: claimed_fact_ids.
	if len(f.ClaimedFactIDs) > ContextFabricDriverClaimedFactIDsMaxCount {
		return "synthesis.finding.claimed_fact_ids.max_count", true
	}
	if bound, ok, passed := diagnoseUniqueTrimmedStringsBound(f.ClaimedFactIDs, ContextFabricIdentifierRefMaxLength, "synthesis.finding.claimed_fact_ids.item_max_length"); !passed {
		return bound, ok
	}
	// Statement 3: business rule only.
	if _, required := ContextFabricDriverCategoryRequiresClaimedFact(ContextFabricDriverCategory(f.Kind)); required && len(f.ClaimedFactIDs) == 0 {
		return "", false
	}
	return "", false
}

// DiagnoseContextFabricClaimedFactBound mirrors
// ContextFabricClaimedFact.Validate()'s exact statement order: claim_id/
// kind/field identity, THEN c.Subject.Validate() (a separate, later
// statement), THEN c.Value.Validate() (later still).
func DiagnoseContextFabricClaimedFactBound(c ContextFabricClaimedFact) (bound string, ok bool) {
	if bound, ok, passed := diagnoseLengthBound(c.ClaimID, ContextFabricModelMintedIDMinLength, ContextFabricModelMintedIDMaxLength, "synthesis.claimed_fact.claim_id.max_length"); !passed {
		return bound, ok
	}
	if !validFactKind(c.Kind) {
		return "", false
	}
	if bound, ok, passed := diagnoseLengthBound(c.Field, 1, ContextFabricClaimedFieldMaxLength, "synthesis.claimed_fact.field.max_length"); !passed {
		return bound, ok
	}
	if strings.TrimSpace(c.Field) != c.Field {
		return "", false
	}
	if bound, ok, passed := diagnoseSubjectRefBound(c.Subject, "synthesis.claimed_fact.subject.canonical_id_max_length", "synthesis.claimed_fact.subject.label_max_length"); !passed {
		return bound, ok
	}
	// ContextFabricScalarValue.Validate(): only the String variant carries
	// a length bound (validate_context_fabric_projection.go); Integer/
	// Number/Boolean/Null carry none (Number is only checked for
	// finiteness, a business rule), and "exactly one variant must be set"
	// (Validate()'s final `set != 1` check) is a business rule too -- both
	// need no explicit mirror here: whether Value has zero, multiple, or
	// exactly one non-String-length-violating variant, none of those
	// conditions is diagnosable, so this function already falls through to
	// ok=false for all of them without checking "set" itself.
	if c.Value.String != nil {
		if bound, ok, passed := diagnoseLengthBound(*c.Value.String, 0, ContextFabricClaimedFactValueMaxLength, "synthesis.claimed_fact.value.max_length"); !passed {
			return bound, ok
		}
	}
	return "", false
}
