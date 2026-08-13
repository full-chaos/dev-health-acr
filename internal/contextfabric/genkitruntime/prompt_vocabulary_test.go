package genkitruntime

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// This file closes the prompt-vocabulary drift CLASS (codex round-11 F1).
//
// The synthesis prompt hardcoded the claimed-fact kind list while validation
// read the shared declaration, so adding or pruning a kind desynchronized
// them in one of two ways: the prompt advertises a kind the validator
// rejects (the model's whole answer is discarded), or omits one the
// validator accepts (silent underuse of a legal family). Neither had a test.
//
// The kind list is now interpolated, so THAT list cannot drift. The prompt
// still states four other closed vocabularies as literal prose, and the same
// failure is available to each. The tests below check every one of them
// against the REAL validators rather than against a second copy of the
// vocabulary, which would just be another list to drift.
//
// Exhaustiveness is asserted only where the prompt genuinely claims a
// complete set -- see TestSynthesisPromptStandingIsADeliberateSubset for the
// one place it does not.

// promptVocabulary extracts the comma-separated vocabulary the prompt states
// after prefix, stopping at the end of the sentence or at a parenthetical.
func promptVocabulary(t *testing.T, prompt, prefix string) []string {
	t.Helper()
	start := strings.Index(prompt, prefix)
	if start < 0 {
		t.Fatalf("the prompt no longer states %q, so this vocabulary is unchecked", prefix)
	}
	rest := prompt[start+len(prefix):]
	end := len(rest)
	for _, terminator := range []string{".", " (", "\n"} {
		if index := strings.Index(rest, terminator); index >= 0 && index < end {
			end = index
		}
	}
	terms := strings.Split(rest[:end], ", ")
	for i, term := range terms {
		terms[i] = strings.TrimSpace(term)
	}
	if len(terms) == 0 || terms[0] == "" {
		t.Fatalf("extracted no terms after %q", prefix)
	}
	return terms
}

// probeDriver is a driver that validates cleanly, so a probe that changes ONE
// vocabulary field measures that field and nothing else. It carries a
// claimed-fact reference and a qualification unconditionally, because a
// canonical-fact-shaped category requires the former and a withheld standing
// requires the latter -- without both, a probe would fail for a reason that
// has nothing to do with the term under test.
func probeDriver() contractsv1.ContextFabricDriverJudgment {
	return contractsv1.ContextFabricDriverJudgment{
		DriverID: "driver_probe_0001",
		Standing: contractsv1.ContextFabricDriverPrincipal,
		Category: "status",
		Title:    "Probe title",
		Summary:  "Probe summary.",
		AffectedSubjects: []contractsv1.ContextFabricSubjectRef{
			{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_probe", Label: "Probe"},
		},
		EvidenceRefIDs:  []string{"evidence_probe_0001"},
		ClaimedFactIDs:  []string{"claim_probe_00001"},
		Derivation:      contractsv1.ContextFabricDerivationCanonicalStructured,
		EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
		Confidence:      0.5,
		Qualification:   "Probe qualification.",
		Current:         true,
	}
}

// TestSynthesisPromptStatesOnlyValidVocabularyTerms is the half that matters
// most: a term the prompt offers but the validator rejects discards the
// model's entire answer.
func TestSynthesisPromptStatesOnlyValidVocabularyTerms(t *testing.T) {
	probeValue := "amber"

	for _, vocabulary := range []struct {
		name   string
		prefix string
		accept func(term string) error
	}{
		{
			name:   "claimed fact kind",
			prefix: "A claimed fact's kind MUST be one of: ",
			accept: func(term string) error {
				return contractsv1.ContextFabricClaimedFact{
					ClaimID: "claim_probe_00001",
					Kind:    contractsv1.ContextFabricFactKind(term),
					Subject: contractsv1.ContextFabricSubjectRef{
						Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_probe", Label: "Probe",
					},
					Field: "probe_field",
					Value: contractsv1.ContextFabricScalarValue{String: &probeValue},
				}.Validate()
			},
		},
		{
			name:   "driver category",
			prefix: "A driver's category MUST be exactly one of this closed set -- no other spelling is accepted: ",
			accept: func(term string) error {
				driver := probeDriver()
				driver.Category = term
				return driver.Validate()
			},
		},
		{
			name:   "driver standing",
			prefix: "A driver's standing MUST be one of: ",
			accept: func(term string) error {
				driver := probeDriver()
				driver.Standing = contractsv1.ContextFabricDriverStanding(term)
				return driver.Validate()
			},
		},
		{
			name:   "derivation",
			prefix: "Every derivation MUST be one of: ",
			accept: func(term string) error {
				driver := probeDriver()
				driver.Derivation = contractsv1.ContextFabricDerivationMethod(term)
				return driver.Validate()
			},
		},
		{
			name:   "epistemic status",
			prefix: "Every epistemic_status MUST be one of: ",
			accept: func(term string) error {
				driver := probeDriver()
				driver.EpistemicStatus = contractsv1.ContextFabricEpistemicStatus(term)
				return driver.Validate()
			},
		},
	} {
		t.Run(vocabulary.name, func(t *testing.T) {
			terms := promptVocabulary(t, synthesisSystemPrompt, vocabulary.prefix)
			for _, term := range terms {
				if err := vocabulary.accept(term); err != nil {
					t.Errorf("the prompt offers %q, which the validator rejects (%v); a model that obeys the prompt would have its whole answer discarded", term, err)
				}
			}
			// The probe must be capable of failing, or every term would
			// "pass" no matter what the prompt said.
			if err := vocabulary.accept("definitely_not_a_member"); err == nil {
				t.Errorf("the %s probe accepts an invented term, so it proves nothing about the real ones", vocabulary.name)
			}
		})
	}
}

// TestSynthesisPromptFactKindListIsTheWholeVocabulary is the other half for
// the flagged vocabulary: omitting a legal kind is silent underuse, not a
// rejected answer, so nothing would ever surface it.
//
// The list is interpolated, so this holds by construction -- the assertion
// exists to keep it that way if someone re-inlines the prose.
func TestSynthesisPromptFactKindListIsTheWholeVocabulary(t *testing.T) {
	vocabulary := contractsv1.ContextFabricFactKindVocabulary()
	want := make([]string, 0, len(vocabulary))
	for _, kind := range vocabulary {
		want = append(want, string(kind))
	}
	got := promptVocabulary(t, synthesisSystemPrompt, "A claimed fact's kind MUST be one of: ")

	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("the synthesis prompt's claimed-fact kind list is not the declared vocabulary, in order:\n  prompt: %v\n  go:     %v", got, want)
	}

	// The interpretation prompt states the same closed set for
	// fact_requirements[].kind, and both must mean the same vocabulary.
	interpreted := promptVocabulary(t, interpretationSystemPrompt,
		"Each fact_requirements[].kind MUST be exactly one of this closed set -- no other spelling, no invented family, no free text: ")
	if strings.Join(interpreted, ", ") != strings.Join(want, ", ") {
		t.Errorf("the two prompts state different fact-kind vocabularies:\n  interpretation: %v\n  go:             %v", interpreted, want)
	}
}

// TestSynthesisPromptStandingIsADeliberateSubset pins the one vocabulary the
// prompt states NARROWER than the contract accepts.
//
// ContextFabricDriverStanding has five members; the prompt offers three. That
// is not drift and must not be "fixed" by deriving it from the declaration:
// doing so would start inviting the model to emit standings the prompt
// currently withholds from it. The subset relationship is pinned here so a
// future round changing it has to change this assertion deliberately, and so
// the exhaustiveness checks above are not quietly extended to cover it.
func TestSynthesisPromptStandingIsADeliberateSubset(t *testing.T) {
	offered := promptVocabulary(t, synthesisSystemPrompt, "A driver's standing MUST be one of: ")
	want := []string{"principal", "contributing", "withheld"}
	if strings.Join(offered, ", ") != strings.Join(want, ", ") {
		t.Errorf("the standings offered to the model changed: %v, previously %v.\nThis list is deliberately narrower than ContextFabricDriverStanding; changing it changes what the model may produce.", offered, want)
	}

	// And every standing the contract accepts but the prompt withholds must
	// still be a real member -- otherwise this "subset" claim is hiding a
	// vocabulary that has actually drifted.
	for _, withheldFromModel := range []contractsv1.ContextFabricDriverStanding{
		contractsv1.ContextFabricDriverSymptom, contractsv1.ContextFabricDriverContext,
	} {
		driver := probeDriver()
		driver.Standing = withheldFromModel
		if err := driver.Validate(); err != nil {
			t.Errorf("%q is documented as contract-valid but withheld from the prompt, yet the validator rejects it: %v", withheldFromModel, err)
		}
		for _, term := range offered {
			if term == string(withheldFromModel) {
				t.Errorf("%q is both offered to the model and listed as withheld from it", term)
			}
		}
	}
}
