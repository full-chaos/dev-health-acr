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

// TestDiagnoseContextFabricInterpretedQuestionBound pins each interpretation
// bound this file diagnoses to Validate()'s own behavior: a value one past
// the limit is both rejected by Validate() and diagnosed with the matching
// registry name, and a value at the limit is accepted (so this test would
// fail if the diagnosis threshold drifted looser than the validator's).
func TestDiagnoseContextFabricInterpretedQuestionBound(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion
		bound  string
	}{
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
	for _, testCase := range cases {
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

func TestDiagnoseContextFabricDriverJudgmentBound(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(ContextFabricDriverJudgment) ContextFabricDriverJudgment
		bound  string
	}{
		{"driver_id too short", func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
			d.DriverID = "short"
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
		{"claimed_fact_ids count", func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
			d.ClaimedFactIDs = make([]string, ContextFabricDriverClaimedFactIDsMaxCount+1)
			for i := range d.ClaimedFactIDs {
				d.ClaimedFactIDs[i] = strings.Repeat("c", i+1)
			}
			return d
		}, "synthesis.driver.claimed_fact_ids.max_count"},
		{"evidence_ref_ids count", func(d ContextFabricDriverJudgment) ContextFabricDriverJudgment {
			d.EvidenceRefIDs = make([]string, ContextFabricEvidenceRefIDsMaxCount+1)
			for i := range d.EvidenceRefIDs {
				d.EvidenceRefIDs[i] = strings.Repeat("e", i+1)
			}
			return d
		}, "synthesis.driver.evidence_ref_ids.max_count"},
	}
	for _, testCase := range cases {
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

func TestDiagnoseContextFabricFindingBound(t *testing.T) {
	valid := ContextFabricFinding{FindingID: "finding_12345678", Kind: "readiness_gap", Summary: "Summary"}
	cases := []struct {
		name   string
		mutate func(ContextFabricFinding) ContextFabricFinding
		bound  string
	}{
		{"finding_id too short", func(f ContextFabricFinding) ContextFabricFinding { f.FindingID = "short"; return f }, "synthesis.finding.finding_id.max_length"},
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
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := testCase.mutate(valid)
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

func TestDiagnoseContextFabricClaimedFactBound(t *testing.T) {
	valid := ContextFabricClaimedFact{ClaimID: "claim_12345678", Kind: "status", Field: "release_ready"}
	cases := []struct {
		name   string
		mutate func(ContextFabricClaimedFact) ContextFabricClaimedFact
		bound  string
	}{
		{"claim_id too short", func(c ContextFabricClaimedFact) ContextFabricClaimedFact { c.ClaimID = "short"; return c }, "synthesis.claimed_fact.claim_id.max_length"},
		{"field length", func(c ContextFabricClaimedFact) ContextFabricClaimedFact {
			c.Field = strings.Repeat("a", ContextFabricClaimedFieldMaxLength+1)
			return c
		}, "synthesis.claimed_fact.field.max_length"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			c := testCase.mutate(valid)
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
