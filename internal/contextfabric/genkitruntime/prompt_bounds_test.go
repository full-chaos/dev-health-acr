package genkitruntime

import (
	"fmt"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestPromptsStateEveryModelFacingBound closes a defect class rather than a
// defect. Three separate CHAOS-3770 live-acceptance failures had the same
// shape: contracts/v1 enforces a bound on model output, the system prompt
// never states it, and the model -- having no way to infer it -- produces
// output that is rejected in full. Interpretation v3 was the fact-kind
// vocabulary, synthesis v4 was the standing/derivation/epistemic/claim-kind
// vocabularies, interpretation v4 was the 256-character requested_judgment
// cap that cost gpt-5-mini 5 of 12 investigations.
//
// Each case pins one bound with three independent assertions:
//
//	atLimit  -- a value exactly AT the limit validates, so the limit in this
//	            table is not merely <= the real one.
//	over     -- a value one past the limit is rejected, so the limit in this
//	            table is not merely >= the real one.
//	mentions -- the prompt text names the field and the limit.
//
// Together, atLimit and over pin the table's limit to the validator's exactly.
// So if someone widens or tightens a bound in contracts/v1 without updating
// the prompt, this test fails: the table no longer matches the validator, and
// correcting the table forces the prompt statement to be corrected with it.
// That is the property that makes a fourth instance of this class unable to
// ship silently.
func TestPromptsStateEveryModelFacingBound(t *testing.T) {
	for _, testCase := range modelFacingBounds() {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.atLimit(); err != nil {
				t.Fatalf("a value at the documented limit was rejected (%v); the limit in this table is smaller than the validator's, so the prompt is understating it", err)
			}
			if err := testCase.over(); err == nil {
				t.Fatal("a value past the documented limit was accepted; the limit in this table is larger than the validator's, so the prompt is overstating it")
			}
			for _, mention := range testCase.mentions {
				if !strings.Contains(testCase.prompt, mention) {
					t.Errorf("the prompt never states %q, so the model cannot honour this bound", mention)
				}
			}
		})
	}
}

type boundCase struct {
	name     string
	prompt   string
	mentions []string
	atLimit  func() error
	over     func() error
}

func modelFacingBounds() []boundCase {
	return []boundCase{
		{
			name: "interpretation/requested_judgment max length", prompt: interpretationSystemPrompt,
			mentions: []string{"requested_judgment", "256"},
			atLimit:  func() error { return interpretationWithJudgment(256).Validate() },
			over:     func() error { return interpretationWithJudgment(257).Validate() },
		},
		{
			name: "interpretation/subject_terms max count", prompt: interpretationSystemPrompt,
			mentions: []string{"subject_terms", "100"},
			atLimit:  func() error { return interpretationWithSubjectTerms(100).Validate() },
			over:     func() error { return interpretationWithSubjectTerms(101).Validate() },
		},
		{
			name: "interpretation/subject term max length", prompt: interpretationSystemPrompt,
			mentions: []string{"512"},
			atLimit:  func() error { return interpretationWithTermLength(512).Validate() },
			over:     func() error { return interpretationWithTermLength(513).Validate() },
		},
		{
			name: "interpretation/comparison_terms max count", prompt: interpretationSystemPrompt,
			mentions: []string{"comparison_terms", "100"},
			atLimit:  func() error { return interpretationWithComparisonTerms(100).Validate() },
			over:     func() error { return interpretationWithComparisonTerms(101).Validate() },
		},
		{
			name: "interpretation/clarification_reason max length", prompt: interpretationSystemPrompt,
			mentions: []string{"clarification_reason", "2000"},
			atLimit:  func() error { return interpretationWithClarification(2000).Validate() },
			over:     func() error { return interpretationWithClarification(2001).Validate() },
		},
		{
			name: "interpretation/fact requirement parameter value max length", prompt: interpretationSystemPrompt,
			mentions: []string{"parameters", "1024"},
			atLimit:  func() error { return interpretationWithParameterValue(1024).Validate() },
			over:     func() error { return interpretationWithParameterValue(1025).Validate() },
		},
		{
			name: "interpretation/fact requirement parameter key max length", prompt: interpretationSystemPrompt,
			mentions: []string{"128"},
			atLimit:  func() error { return interpretationWithParameterKey(128).Validate() },
			over:     func() error { return interpretationWithParameterKey(129).Validate() },
		},
		{
			name: "synthesis/driver_id max length", prompt: synthesisSystemPrompt,
			mentions: []string{"driver_id", "256"},
			atLimit:  func() error { return driverWithID(256).Validate() },
			over:     func() error { return driverWithID(257).Validate() },
		},
		{
			name: "synthesis/driver title max length", prompt: synthesisSystemPrompt,
			mentions: []string{"title", "512"},
			atLimit:  func() error { return driverWithTitle(512).Validate() },
			over:     func() error { return driverWithTitle(513).Validate() },
		},
		{
			name: "synthesis/driver summary max length", prompt: synthesisSystemPrompt,
			mentions: []string{"summary", "4000"},
			atLimit:  func() error { return driverWithSummary(4000).Validate() },
			over:     func() error { return driverWithSummary(4001).Validate() },
		},
		{
			name: "synthesis/driver qualification max length", prompt: synthesisSystemPrompt,
			mentions: []string{"qualification", "2000"},
			atLimit:  func() error { return driverWithQualification(2000).Validate() },
			over:     func() error { return driverWithQualification(2001).Validate() },
		},
		{
			name: "synthesis/driver affected_subjects max count", prompt: synthesisSystemPrompt,
			mentions: []string{"affected_subjects", "250"},
			atLimit:  func() error { return driverWithSubjects(250).Validate() },
			over:     func() error { return driverWithSubjects(251).Validate() },
		},
		{
			name: "synthesis/driver path_ids max count", prompt: synthesisSystemPrompt,
			mentions: []string{"path_ids", "250"},
			atLimit:  func() error { return driverWithPathIDs(250).Validate() },
			over:     func() error { return driverWithPathIDs(251).Validate() },
		},
		{
			name: "synthesis/driver claimed_fact_ids max count", prompt: synthesisSystemPrompt,
			mentions: []string{"claimed_fact_ids", "250"},
			atLimit:  func() error { return driverWithClaimedFactIDs(250).Validate() },
			over:     func() error { return driverWithClaimedFactIDs(251).Validate() },
		},
		{
			name: "synthesis/finding summary max length", prompt: synthesisSystemPrompt,
			mentions: []string{"summary", "4000"},
			atLimit:  func() error { return findingWithSummary(4000).Validate() },
			over:     func() error { return findingWithSummary(4001).Validate() },
		},
		{
			name: "synthesis/finding subjects max count", prompt: synthesisSystemPrompt,
			mentions: []string{"subjects", "250"},
			atLimit:  func() error { return findingWithSubjects(250).Validate() },
			over:     func() error { return findingWithSubjects(251).Validate() },
		},
		{
			name: "synthesis/claimed fact field max length", prompt: synthesisSystemPrompt,
			mentions: []string{"field", "128"},
			atLimit:  func() error { return claimWithField(128).Validate() },
			over:     func() error { return claimWithField(129).Validate() },
		},
	}
}

func filler(n int) string { return strings.Repeat("a", n) }

func boundsSubject(index int) contractsv1.ContextFabricSubjectRef {
	return contractsv1.ContextFabricSubjectRef{
		Kind:        contractsv1.ContextFabricSubjectProject,
		CanonicalID: fmt.Sprintf("project_%04d", index),
		Label:       fmt.Sprintf("Project %04d", index),
	}
}

func baseInterpretation() contractsv1.ContextFabricInterpretedQuestion {
	return contractsv1.ContextFabricInterpretedQuestion{
		Shape:             contractsv1.ContextFabricShapeOpen,
		RequestedJudgment: "actual status and current drivers",
		TimeContext:       contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
		FactRequirements:  []contractsv1.ContextFabricFactRequirement{},
	}
}

func interpretationWithJudgment(length int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.RequestedJudgment = filler(length)
	return question
}

func interpretationWithSubjectTerms(count int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.SubjectTerms = uniqueTerms(count)
	return question
}

func interpretationWithComparisonTerms(count int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.ComparisonTerms = uniqueTerms(count)
	return question
}

func interpretationWithTermLength(length int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.SubjectTerms = []string{filler(length)}
	return question
}

func interpretationWithClarification(length int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.ClarificationNeeded = true
	question.ClarificationReason = filler(length)
	return question
}

func interpretationWithParameterValue(length int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.FactRequirements = []contractsv1.ContextFabricFactRequirement{{
		Kind:       contractsv1.ContextFabricFactStatus,
		Parameters: map[string]string{"scope": filler(length)},
	}}
	return question
}

func interpretationWithParameterKey(length int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.FactRequirements = []contractsv1.ContextFabricFactRequirement{{
		Kind:       contractsv1.ContextFabricFactStatus,
		Parameters: map[string]string{filler(length): "scope"},
	}}
	return question
}

func uniqueTerms(count int) []string {
	terms := make([]string, 0, count)
	for i := 0; i < count; i++ {
		terms = append(terms, fmt.Sprintf("term_%04d", i))
	}
	return terms
}

// baseDriver is deliberately in the relationship category: relationship and
// narrative are the two categories that require no claimed fact, so these
// cases isolate the bound under test from value-level closure.
func baseDriver() contractsv1.ContextFabricDriverJudgment {
	return contractsv1.ContextFabricDriverJudgment{
		DriverID:         "driver_00000001",
		Standing:         contractsv1.ContextFabricDriverPrincipal,
		Category:         string(contractsv1.ContextFabricDriverCategoryRelationship),
		Title:            "Release acceptance is open",
		Summary:          "The release acceptance work item is still open.",
		AffectedSubjects: []contractsv1.ContextFabricSubjectRef{boundsSubject(0)},
		PathIDs:          []string{"path_00000001"},
		EvidenceRefIDs:   []string{},
		Derivation:       contractsv1.ContextFabricDerivationCanonicalStructured,
		EpistemicStatus:  contractsv1.ContextFabricEpistemicObserved,
		Confidence:       0.5,
	}
}

func driverWithID(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.DriverID = filler(length)
	return driver
}

func driverWithTitle(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.Title = filler(length)
	return driver
}

func driverWithSummary(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.Summary = filler(length)
	return driver
}

func driverWithQualification(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.Qualification = filler(length)
	return driver
}

func driverWithSubjects(count int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	subjects := make([]contractsv1.ContextFabricSubjectRef, 0, count)
	for i := 0; i < count; i++ {
		subjects = append(subjects, boundsSubject(i))
	}
	driver.AffectedSubjects = subjects
	return driver
}

func driverWithPathIDs(count int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.PathIDs = uniqueTerms(count)
	return driver
}

func driverWithClaimedFactIDs(count int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.ClaimedFactIDs = uniqueTerms(count)
	return driver
}

func baseFinding() contractsv1.ContextFabricFinding {
	return contractsv1.ContextFabricFinding{
		FindingID:      "finding_00000001",
		Kind:           string(contractsv1.ContextFabricDriverCategoryNarrative),
		Summary:        "Release acceptance remains open.",
		Subjects: []contractsv1.ContextFabricSubjectRef{boundsSubject(0)},
		// Unlike a driver, a finding may not have an empty evidence list:
		// boundedEvidenceRefs is called with allowEmpty=false for findings
		// and allowEmpty=true for drivers.
		EvidenceRefIDs: []string{"evidence_00000001"},
	}
}

func findingWithSummary(length int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.Summary = filler(length)
	return finding
}

func findingWithSubjects(count int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	subjects := make([]contractsv1.ContextFabricSubjectRef, 0, count)
	for i := 0; i < count; i++ {
		subjects = append(subjects, boundsSubject(i))
	}
	finding.Subjects = subjects
	return finding
}

func claimWithField(length int) contractsv1.ContextFabricClaimedFact {
	value := "in_progress"
	return contractsv1.ContextFabricClaimedFact{
		ClaimID: "claim_00000001",
		Kind:    contractsv1.ContextFabricFactStatus,
		Subject: boundsSubject(0),
		Field:   filler(length),
		Value:   contractsv1.ContextFabricScalarValue{String: &value},
	}
}
