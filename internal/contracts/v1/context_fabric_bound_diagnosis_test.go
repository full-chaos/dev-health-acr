package v1

import (
	"strings"
	"testing"
)

func validDiagnosisInterpretedQuestion() ContextFabricInterpretedQuestion {
	return ContextFabricInterpretedQuestion{
		Shape: ContextFabricShapeOpen, RequestedJudgment: "status_and_drivers",
		TimeContext: ContextFabricTimeContext{Axis: ContextFabricTemporalCurrent},
	}
}

type diagnosisCase[T any] struct {
	name   string
	mutate func(T) T
	bound  string
}

// interpretedQuestionDiagnosisCases is the single source both
// TestDiagnoseContextFabricInterpretedQuestionBound and
// TestContextFabricModelFacingBoundRegistryDiagnosisCoverage draw from, so
// the two cannot drift apart (CHAOS-3784 round-2 F4): a bound name used
// here that ContextFabricModelFacingBounds doesn't declare, or a
// registered bound with no case here, both fail the coverage test.
func interpretedQuestionDiagnosisCases() []diagnosisCase[ContextFabricInterpretedQuestion] {
	return []diagnosisCase[ContextFabricInterpretedQuestion]{
		{"requested_judgment", func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
			q.RequestedJudgment = strings.Repeat("a", ContextFabricRequestedJudgmentMaxLength+1)
			return q
		}, "interpretation.requested_judgment.max_length"},
		{"subject_terms count", func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
			q.SubjectTerms = make([]string, ContextFabricSubjectTermsMaxCount+1)
			for i := range q.SubjectTerms {
				q.SubjectTerms[i] = strings.Repeat("x", i+1)
			}
			return q
		}, "interpretation.subject_terms.max_count"},
		{"comparison_terms count", func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
			q.ComparisonTerms = make([]string, ContextFabricComparisonTermsMaxCount+1)
			for i := range q.ComparisonTerms {
				q.ComparisonTerms[i] = strings.Repeat("y", i+1)
			}
			return q
		}, "interpretation.comparison_terms.max_count"},
		{"subject_term length", func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
			q.SubjectTerms = []string{strings.Repeat("a", ContextFabricSubjectOrComparisonTermMaxLength+1)}
			return q
		}, "interpretation.subject_term.max_length"},
		{"comparison_term length", func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
			q.ComparisonTerms = []string{strings.Repeat("a", ContextFabricSubjectOrComparisonTermMaxLength+1)}
			return q
		}, "interpretation.subject_term.max_length"},
		{"fact_requirements count", func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
			q.FactRequirements = make([]ContextFabricFactRequirement, ContextFabricFactRequirementsMaxCount+1)
			for i := range q.FactRequirements {
				q.FactRequirements[i] = ContextFabricFactRequirement{Kind: ContextFabricFactKind(strings.Repeat("k", i+1))}
			}
			return q
		}, "interpretation.fact_requirements.max_count"},
		{"clarification_reason length", func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
			q.ClarificationReason = strings.Repeat("a", ContextFabricClarificationReasonMaxLength+1)
			return q
		}, "interpretation.clarification_reason.max_length"},
		{"fact_requirement.parameters count", func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
			params := make(map[string]string, ContextFabricFactRequirementParametersMaxCount+1)
			for i := 0; i < ContextFabricFactRequirementParametersMaxCount+1; i++ {
				params[strings.Repeat("k", i+1)] = "v"
			}
			q.FactRequirements = []ContextFabricFactRequirement{{Kind: "status", Parameters: params}}
			return q
		}, "interpretation.fact_requirement.parameters.max_count"},
		{"fact_requirement.parameter_key length", func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
			q.FactRequirements = []ContextFabricFactRequirement{{
				Kind:       "status",
				Parameters: map[string]string{strings.Repeat("k", ContextFabricFactRequirementParameterKeyMaxLength+1): "v"},
			}}
			return q
		}, "interpretation.fact_requirement.parameter_key.max_length"},
		{"fact_requirement.parameter_value length", func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
			q.FactRequirements = []ContextFabricFactRequirement{{
				Kind:       "status",
				Parameters: map[string]string{"k": strings.Repeat("v", ContextFabricFactRequirementParameterValueMaxLength+1)},
			}}
			return q
		}, "interpretation.fact_requirement.parameter_value.max_length"},
	}
}

// TestDiagnoseContextFabricInterpretedQuestionBound pins each interpretation
// bound this file diagnoses to Validate()'s own behavior: a value one past
// the limit is both rejected by Validate() and diagnosed with the matching
// registry name.
func TestDiagnoseContextFabricInterpretedQuestionBound(t *testing.T) {
	for _, testCase := range interpretedQuestionDiagnosisCases() {
		t.Run(testCase.name, func(t *testing.T) {
			q := testCase.mutate(validDiagnosisInterpretedQuestion())
			bound, ok := DiagnoseContextFabricInterpretedQuestionBound(q)
			if !ok {
				t.Fatalf("DiagnoseContextFabricInterpretedQuestionBound() ok = false, want true")
			}
			if bound != testCase.bound {
				t.Fatalf("DiagnoseContextFabricInterpretedQuestionBound() bound = %q, want %q", bound, testCase.bound)
			}
		})
	}
}

func TestDiagnoseContextFabricInterpretedQuestionBoundAcceptsValidQuestion(t *testing.T) {
	if bound, ok := DiagnoseContextFabricInterpretedQuestionBound(validDiagnosisInterpretedQuestion()); ok {
		t.Fatalf("DiagnoseContextFabricInterpretedQuestionBound() = (%q, true), want ok=false for a valid question", bound)
	}
}

// TestDiagnoseContextFabricInterpretedQuestionBoundDoesNotFlagBusinessRules
// guards the ok=false contract: an invalid enum value is not a length/count
// bound this registry covers, even though Validate() rejects it too.
func TestDiagnoseContextFabricInterpretedQuestionBoundDoesNotFlagBusinessRules(t *testing.T) {
	q := validDiagnosisInterpretedQuestion()
	q.Shape = "not_a_real_shape"
	if bound, ok := DiagnoseContextFabricInterpretedQuestionBound(q); ok {
		t.Fatalf("DiagnoseContextFabricInterpretedQuestionBound() = (%q, true), want ok=false for an invalid enum (not a registry bound)", bound)
	}
}

func validDiagnosisDriverJudgment() ContextFabricDriverJudgment {
	return ContextFabricDriverJudgment{
		DriverID: "driver_12345678", Standing: ContextFabricDriverPrincipal, Category: "relationship",
		Title: "Title", Summary: "Summary", Derivation: ContextFabricDerivationRuleInferred,
		EpistemicStatus: ContextFabricEpistemicInferred, Confidence: 0.5,
		AffectedSubjects: []ContextFabricSubjectRef{{Kind: ContextFabricSubjectProject, CanonicalID: "project_1", Label: "Project"}},
		EvidenceRefIDs:   []string{"evidence_1"},
	}
}

func driverJudgmentDiagnosisCases() []diagnosisCase[ContextFabricDriverJudgment] {
	return []diagnosisCase[ContextFabricDriverJudgment]{
		{"driver_id too long", func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
			d.DriverID = strings.Repeat("d", ContextFabricModelMintedIDMaxLength+1)
			return d
		}, "synthesis.driver.driver_id.max_length"},
		{"title length", func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
			d.Title = strings.Repeat("a", ContextFabricDriverTitleMaxLength+1)
			return d
		}, "synthesis.driver.title.max_length"},
		{"summary length", func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
			d.Summary = strings.Repeat("a", ContextFabricDriverSummaryMaxLength+1)
			return d
		}, "synthesis.driver.summary.max_length"},
		{"qualification length", func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
			d.Qualification = strings.Repeat("a", ContextFabricDriverQualificationMaxLength+1)
			return d
		}, "synthesis.driver.qualification.max_length"},
		{"affected_subjects count", func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
			subjects := make([]ContextFabricSubjectRef, ContextFabricDriverAffectedSubjectsMaxCount+1)
			for i := range subjects {
				subjects[i] = ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: strings.Repeat("p", i+1), Label: "P"}
			}
			d.AffectedSubjects = subjects
			return d
		}, "synthesis.driver.affected_subjects.max_count"},
		{"path_ids count", func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
			d.PathIDs = make([]string, ContextFabricDriverPathIDsMaxCount+1)
			for i := range d.PathIDs {
				d.PathIDs[i] = strings.Repeat("p", i+1)
			}
			return d
		}, "synthesis.driver.path_ids.max_count"},
		{"path_id item length", func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
			d.PathIDs = []string{strings.Repeat("p", ContextFabricIdentifierRefMaxLength+1)}
			return d
		}, "synthesis.driver.path_ids.item_max_length"},
		{"claimed_fact_ids count", func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
			d.ClaimedFactIDs = make([]string, ContextFabricDriverClaimedFactIDsMaxCount+1)
			for i := range d.ClaimedFactIDs {
				d.ClaimedFactIDs[i] = strings.Repeat("c", i+1)
			}
			return d
		}, "synthesis.driver.claimed_fact_ids.max_count"},
		{"claimed_fact_id item length", func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
			d.ClaimedFactIDs = []string{strings.Repeat("c", ContextFabricIdentifierRefMaxLength+1)}
			return d
		}, "synthesis.driver.claimed_fact_ids.item_max_length"},
		{"evidence_ref_ids count", func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
			d.EvidenceRefIDs = make([]string, ContextFabricEvidenceRefIDsMaxCount+1)
			for i := range d.EvidenceRefIDs {
				d.EvidenceRefIDs[i] = strings.Repeat("e", i+1)
			}
			return d
		}, "synthesis.driver.evidence_ref_ids.max_count"},
	}
}

func TestDiagnoseContextFabricDriverJudgmentBound(t *testing.T) {
	for _, testCase := range driverJudgmentDiagnosisCases() {
		t.Run(testCase.name, func(t *testing.T) {
			d := testCase.mutate(validDiagnosisDriverJudgment())
			bound, ok := DiagnoseContextFabricDriverJudgmentBound(d)
			if !ok {
				t.Fatalf("DiagnoseContextFabricDriverJudgmentBound() ok = false, want true")
			}
			if bound != testCase.bound {
				t.Fatalf("DiagnoseContextFabricDriverJudgmentBound() bound = %q, want %q", bound, testCase.bound)
			}
		})
	}
}

func TestDiagnoseContextFabricDriverJudgmentBoundDoesNotFlagBusinessRules(t *testing.T) {
	d := validDiagnosisDriverJudgment()
	d.Standing = "not_a_real_standing"
	if bound, ok := DiagnoseContextFabricDriverJudgmentBound(d); ok {
		t.Fatalf("DiagnoseContextFabricDriverJudgmentBound() = (%q, true), want ok=false for an invalid enum (not a registry bound)", bound)
	}
}

func validDiagnosisFinding() ContextFabricFinding {
	return ContextFabricFinding{FindingID: "finding_12345678", Kind: "readiness_gap", Summary: "Summary"}
}

func findingDiagnosisCases() []diagnosisCase[ContextFabricFinding] {
	return []diagnosisCase[ContextFabricFinding]{
		{"finding_id too long", func(f ContextFabricFinding) ContextFabricFinding {
			f.FindingID = strings.Repeat("f", ContextFabricModelMintedIDMaxLength+1)
			return f
		}, "synthesis.finding.finding_id.max_length"},
		{"kind length", func(f ContextFabricFinding) ContextFabricFinding {
			f.Kind = strings.Repeat("a", ContextFabricFindingKindMaxLength+1)
			return f
		}, "synthesis.finding.kind.max_length"},
		{"summary length", func(f ContextFabricFinding) ContextFabricFinding {
			f.Summary = strings.Repeat("a", ContextFabricFindingSummaryMaxLength+1)
			return f
		}, "synthesis.finding.summary.max_length"},
		{"subjects count", func(f ContextFabricFinding) ContextFabricFinding {
			subjects := make([]ContextFabricSubjectRef, ContextFabricFindingSubjectsMaxCount+1)
			for i := range subjects {
				subjects[i] = ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: strings.Repeat("p", i+1), Label: "P"}
			}
			f.Subjects = subjects
			return f
		}, "synthesis.finding.subjects.max_count"},
		{"evidence_ref_ids count", func(f ContextFabricFinding) ContextFabricFinding {
			f.EvidenceRefIDs = make([]string, ContextFabricEvidenceRefIDsMaxCount+1)
			for i := range f.EvidenceRefIDs {
				f.EvidenceRefIDs[i] = strings.Repeat("e", i+1)
			}
			return f
		}, "synthesis.finding.evidence_ref_ids.max_count"},
		{"claimed_fact_ids count", func(f ContextFabricFinding) ContextFabricFinding {
			f.ClaimedFactIDs = make([]string, ContextFabricDriverClaimedFactIDsMaxCount+1)
			for i := range f.ClaimedFactIDs {
				f.ClaimedFactIDs[i] = strings.Repeat("c", i+1)
			}
			return f
		}, "synthesis.finding.claimed_fact_ids.max_count"},
		{"claimed_fact_id item length", func(f ContextFabricFinding) ContextFabricFinding {
			f.ClaimedFactIDs = []string{strings.Repeat("c", ContextFabricIdentifierRefMaxLength+1)}
			return f
		}, "synthesis.finding.claimed_fact_ids.item_max_length"},
	}
}

func TestDiagnoseContextFabricFindingBound(t *testing.T) {
	for _, testCase := range findingDiagnosisCases() {
		t.Run(testCase.name, func(t *testing.T) {
			f := testCase.mutate(validDiagnosisFinding())
			bound, ok := DiagnoseContextFabricFindingBound(f)
			if !ok {
				t.Fatalf("DiagnoseContextFabricFindingBound() ok = false, want true")
			}
			if bound != testCase.bound {
				t.Fatalf("DiagnoseContextFabricFindingBound() bound = %q, want %q", bound, testCase.bound)
			}
		})
	}
}

func validDiagnosisClaimedFact() ContextFabricClaimedFact {
	return ContextFabricClaimedFact{ClaimID: "claim_12345678", Kind: "status", Field: "release_ready"}
}

func claimedFactDiagnosisCases() []diagnosisCase[ContextFabricClaimedFact] {
	return []diagnosisCase[ContextFabricClaimedFact]{
		{"claim_id too long", func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
			c.ClaimID = strings.Repeat("c", ContextFabricModelMintedIDMaxLength+1)
			return c
		}, "synthesis.claimed_fact.claim_id.max_length"},
		{"field length", func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
			c.Field = strings.Repeat("a", ContextFabricClaimedFieldMaxLength+1)
			return c
		}, "synthesis.claimed_fact.field.max_length"},
	}
}

func TestDiagnoseContextFabricClaimedFactBound(t *testing.T) {
	for _, testCase := range claimedFactDiagnosisCases() {
		t.Run(testCase.name, func(t *testing.T) {
			c := testCase.mutate(validDiagnosisClaimedFact())
			bound, ok := DiagnoseContextFabricClaimedFactBound(c)
			if !ok {
				t.Fatalf("DiagnoseContextFabricClaimedFactBound() ok = false, want true")
			}
			if bound != testCase.bound {
				t.Fatalf("DiagnoseContextFabricClaimedFactBound() bound = %q, want %q", bound, testCase.bound)
			}
		})
	}
}

// contextFabricSynthesisCollectionBoundsUncoveredByDiagnosis lists every
// ContextFabricModelFacingBounds entry no Diagnose* function in this
// package can ever attribute, and why (CHAOS-3784 round-2 F4): these are
// the top-level synthesis draft/result collection caps
// (drivers.max_count, remaining_work.max_count, and siblings).
// SynthesisDraft.ValidateAgainst -- the only call site that runs BEFORE a
// draft is discarded on rejection -- never checks them; only
// ContextFabricInvestigationResult.Validate() does, later, against the
// already-composed result, classified ErrInvalidResult, not
// ErrSynthesisRejected (see internal/contextfabric/bound_diagnosis.go's
// diagnoseSynthesisDraftBound doc comment). A synthesis draft violating one
// of these therefore never reaches a Diagnose* call with a value to
// diagnose FROM. This is a pre-existing, tracked gap (CHAOS-3790), not an
// oversight this test should paper over -- but it must be an EXPLICIT,
// reviewed exclusion, not silence: adding a new bound to the registry
// without adding it here (if genuinely undiagnosable) or a Diagnose case
// (if it should be covered) fails TestContextFabricModelFacingBoundRegistryDiagnosisCoverage.
var contextFabricSynthesisCollectionBoundsUncoveredByDiagnosis = map[string]struct{}{
	"synthesis.strongest_pressures.max_count":       {},
	"synthesis.strongest_pressures.item_max_length": {},
	"synthesis.drivers.max_count":                   {},
	"synthesis.remaining_work.max_count":            {},
	"synthesis.readiness_gaps.max_count":            {},
	"synthesis.conflicts.max_count":                 {},
	"synthesis.limitations.max_count":               {},
	"synthesis.limitations.item_max_length":         {},
	"synthesis.warnings.max_count":                  {},
	"synthesis.warnings.item_max_length":            {},
	"synthesis.evidence_ref_ids.max_count":          {},
	"synthesis.claimed_facts.max_count":             {},
}

// TestContextFabricModelFacingBoundRegistryDiagnosisCoverage is
// modelFacingBounds's mechanical-oracle counterpart for diagnosis (CHAOS-3784
// round-2 F4): every ContextFabricModelFacingBounds entry must either have
// a matching case across interpretedQuestionDiagnosisCases/
// driverJudgmentDiagnosisCases/findingDiagnosisCases/
// claimedFactDiagnosisCases, or be an explicit, documented exclusion in
// contextFabricSynthesisCollectionBoundsUncoveredByDiagnosis -- so a new
// bound cannot silently ship with neither.
func TestContextFabricModelFacingBoundRegistryDiagnosisCoverage(t *testing.T) {
	covered := make(map[string]struct{})
	for _, testCase := range interpretedQuestionDiagnosisCases() {
		covered[testCase.bound] = struct{}{}
	}
	for _, testCase := range driverJudgmentDiagnosisCases() {
		covered[testCase.bound] = struct{}{}
	}
	for _, testCase := range findingDiagnosisCases() {
		covered[testCase.bound] = struct{}{}
	}
	for _, testCase := range claimedFactDiagnosisCases() {
		covered[testCase.bound] = struct{}{}
	}
	for _, bound := range ContextFabricModelFacingBounds {
		_, isCovered := covered[bound.Name]
		_, isExcluded := contextFabricSynthesisCollectionBoundsUncoveredByDiagnosis[bound.Name]
		if !isCovered && !isExcluded {
			t.Errorf("contracts/v1 registers model-facing bound %q with no Diagnose* case and no documented exclusion -- add a case (if a Diagnose* function can reach it) or add it to contextFabricSynthesisCollectionBoundsUncoveredByDiagnosis with a reason (if it genuinely cannot)", bound.Name)
		}
		if isCovered && isExcluded {
			t.Errorf("bound %q is both diagnosed by a case and listed as excluded -- remove it from contextFabricSynthesisCollectionBoundsUncoveredByDiagnosis", bound.Name)
		}
	}
	for name := range contextFabricSynthesisCollectionBoundsUncoveredByDiagnosis {
		found := false
		for _, bound := range ContextFabricModelFacingBounds {
			if bound.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("contextFabricSynthesisCollectionBoundsUncoveredByDiagnosis names %q, which ContextFabricModelFacingBounds does not declare -- remove it", name)
		}
	}
}

// TestDiagnoseModelMintedIDBoundsDoNotFlagTooShortValues is the CHAOS-3784
// round-2 F3 regression: driver_id/finding_id/claim_id share one registry
// entry named "max_length" for their [8,256] shape -- there is no separate
// "min_length" entry (see runeLongerThan's doc comment) -- so a too-SHORT
// value must not be misreported under the "max_length" name. Validate()
// still rejects it (via stringLengthBetween's two-sided check); Diagnose
// just correctly declines to attribute that specific rejection to a
// registry bound.
func TestDiagnoseModelMintedIDBoundsDoNotFlagTooShortValues(t *testing.T) {
	driver := validDiagnosisDriverJudgment()
	driver.DriverID = "short"
	if bound, ok := DiagnoseContextFabricDriverJudgmentBound(driver); ok {
		t.Fatalf("DiagnoseContextFabricDriverJudgmentBound() = (%q, true), want ok=false for a too-short driver_id", bound)
	}

	finding := validDiagnosisFinding()
	finding.FindingID = "short"
	if bound, ok := DiagnoseContextFabricFindingBound(finding); ok {
		t.Fatalf("DiagnoseContextFabricFindingBound() = (%q, true), want ok=false for a too-short finding_id", bound)
	}

	claim := validDiagnosisClaimedFact()
	claim.ClaimID = "short"
	if bound, ok := DiagnoseContextFabricClaimedFactBound(claim); ok {
		t.Fatalf("DiagnoseContextFabricClaimedFactBound() = (%q, true), want ok=false for a too-short claim_id", bound)
	}
}

// TestDiagnoseBoundsMeasureRuneCountNotByteLength is the CHAOS-3784
// round-2 F3 regression for the other half of the fidelity gap:
// Validate() measures every length bound in Unicode code points
// (stringLengthBetween, utf8.RuneCountInString), never raw bytes. "é" is 2
// bytes but 1 rune, so a string of exactly MaxLength copies of it is
// EXACTLY at the rune-count limit -- Validate() accepts it -- while its
// byte length is double the limit. A byte-counting Diagnose would
// misreport this as a violation; the correct one must not.
func TestDiagnoseBoundsMeasureRuneCountNotByteLength(t *testing.T) {
	atLimitMultiByte := strings.Repeat("é", ContextFabricRequestedJudgmentMaxLength)
	if len(atLimitMultiByte) <= ContextFabricRequestedJudgmentMaxLength {
		t.Fatalf("test fixture is not actually multi-byte: byte length %d, rune-limit %d", len(atLimitMultiByte), ContextFabricRequestedJudgmentMaxLength)
	}
	question := validDiagnosisInterpretedQuestion()
	question.RequestedJudgment = atLimitMultiByte
	if err := question.Validate(); err != nil {
		t.Fatalf("fixture question.Validate() error = %v, want the at-rune-limit multi-byte value to be accepted", err)
	}
	if bound, ok := DiagnoseContextFabricInterpretedQuestionBound(question); ok {
		t.Fatalf("DiagnoseContextFabricInterpretedQuestionBound() = (%q, true), want ok=false: Validate() accepted this value (rune count == limit), so it must not be diagnosed as a violation of its byte length", bound)
	}

	driver := validDiagnosisDriverJudgment()
	driver.Title = strings.Repeat("é", ContextFabricDriverTitleMaxLength)
	if bound, ok := DiagnoseContextFabricDriverJudgmentBound(driver); ok {
		t.Fatalf("DiagnoseContextFabricDriverJudgmentBound() = (%q, true), want ok=false for an at-rune-limit multi-byte title", bound)
	}
}

// TestDiagnoseContextFabricFactRequirementBoundIsDeterministicAcrossMultipleViolatedParameters
// is the CHAOS-3784 round-2 F4 regression: r.Parameters is a Go map, whose
// iteration order is randomized per run. With two DIFFERENT parameters
// each violating a DIFFERENT bound (one key too long, one value too long),
// an unsorted iteration would make the returned bound name
// order-dependent -- flaky across runs/processes. DiagnoseContextFabricFactRequirementBound
// sorts keys first, so the result must be the SAME bound on every call,
// deterministically: the lexicographically-first key's own violation wins.
func TestDiagnoseContextFabricFactRequirementBoundIsDeterministicAcrossMultipleViolatedParameters(t *testing.T) {
	requirement := ContextFabricFactRequirement{
		Kind: "status",
		Parameters: map[string]string{
			"z_key": strings.Repeat("v", ContextFabricFactRequirementParameterValueMaxLength+1),
			"a_key": "short",
		},
	}
	// "a_key" itself is not over the KEY length bound, so the only
	// violation here is z_key's value -- this pins that a short,
	// lexicographically-earlier key never masks a real violation on a
	// later key.
	var last string
	for i := 0; i < 50; i++ {
		bound, ok := DiagnoseContextFabricFactRequirementBound(requirement)
		if !ok {
			t.Fatalf("iteration %d: ok = false, want true", i)
		}
		if bound != "interpretation.fact_requirement.parameter_value.max_length" {
			t.Fatalf("iteration %d: bound = %q, want interpretation.fact_requirement.parameter_value.max_length", i, bound)
		}
		if i > 0 && bound != last {
			t.Fatalf("iteration %d: bound = %q, want the same result as the previous iteration %q (non-deterministic)", i, bound, last)
		}
		last = bound
	}
}
