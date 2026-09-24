package v1

import (
	"strings"
	"testing"
)

func cohortNarrowingProdSteps() []ContextFabricCohortNarrowingDisclosureStep {
	return []ContextFabricCohortNarrowingDisclosureStep{
		{Stage: ContextFabricPlanNarrowingCardinality, Before: 11, After: 10, LimitBefore: 20, LimitAfter: 10},
		{Stage: ContextFabricPlanNarrowingAssembledResult, Overrun: ContextFabricBudgetOverrunItems, Before: 10, After: 5},
	}
}

// TestCohortNarrowingLimitationComposesAndIsRecognised pins the prod sentence
// and that the recogniser accepts exactly what the composer writes, for every
// step phrase.
func TestCohortNarrowingLimitationComposesAndIsRecognised(t *testing.T) {
	t.Parallel()
	sentence, ok := ContextFabricCohortNarrowingLimitation(ContextFabricSubjectRepository, 11, 5, cohortNarrowingProdSteps())
	if !ok {
		t.Fatal("prod chain 11 -> 10 -> 5 was refused")
	}
	want := "This answer lists 5 of the 11 repository subjects found, because the list was narrowed to fit the response budget: " +
		"11 to 10 when the plan lowered the member limit from 20 to 10 before retrieval; " +
		"10 to 5 after the assembled answer exceeded the item budget. " +
		"Ask a narrower question or allow a larger response budget to see the rest."
	if sentence != want {
		t.Fatalf("sentence = %q\nwant       %q", sentence, want)
	}
	if !IsContextFabricCohortNarrowingLimitation(sentence) || !IsContextFabricServiceAuthoredLimitation(sentence) {
		t.Fatal("the composed sentence is not recognised as a service-authored disclosure; it would be displaceable")
	}
	if len(sentence) > ContextFabricLimitationMaxLength {
		t.Fatalf("sentence length %d exceeds the limitation bound %d", len(sentence), ContextFabricLimitationMaxLength)
	}
	for name, steps := range map[string][]ContextFabricCohortNarrowingDisclosureStep{
		"clamp with no prior limit": {{Stage: ContextFabricPlanNarrowingCardinality, Before: 11, After: 10, LimitAfter: 10}},
		"synthesis input":           {{Stage: ContextFabricPlanNarrowingSynthesisInput, Before: 11, After: 10}},
		"bytes overrun":             {{Stage: ContextFabricPlanNarrowingAssembledResult, Overrun: ContextFabricBudgetOverrunBytes, Before: 11, After: 10}},
		"no overrun named":          {{Stage: ContextFabricPlanNarrowingAssembledResult, Before: 11, After: 10}},
	} {
		sentence, ok := ContextFabricCohortNarrowingLimitation(ContextFabricSubjectProject, 11, 10, steps)
		if !ok || !IsContextFabricCohortNarrowingLimitation(sentence) {
			t.Fatalf("%s: composed=%v recognised=%v for %q", name, ok, IsContextFabricCohortNarrowingLimitation(sentence), sentence)
		}
	}
}

// TestCohortNarrowingLimitationRefusesChainsItCannotStateTruthfully: the
// composer writes nothing for a chain whose counts do not reconcile, and the
// recogniser does not accept a sentence whose counts were edited.
func TestCohortNarrowingLimitationRefusesChainsItCannotStateTruthfully(t *testing.T) {
	t.Parallel()
	prod := cohortNarrowingProdSteps()
	for name, input := range map[string]struct {
		kind             ContextFabricSubjectKind
		declared, served int
		steps            []ContextFabricCohortNarrowingDisclosureStep
	}{
		"nothing narrowed":        {ContextFabricSubjectRepository, 5, 5, prod},
		"no steps":                {ContextFabricSubjectRepository, 11, 5, nil},
		"chain starts elsewhere":  {ContextFabricSubjectRepository, 12, 5, prod},
		"chain ends elsewhere":    {ContextFabricSubjectRepository, 11, 4, prod},
		"unknown kind":            {ContextFabricSubjectKind("widget"), 11, 5, prod},
		"step does not reduce":    {ContextFabricSubjectRepository, 11, 11, []ContextFabricCohortNarrowingDisclosureStep{{Stage: ContextFabricPlanNarrowingAssembledResult, Before: 11, After: 11}}},
		"unknown stage":           {ContextFabricSubjectRepository, 11, 10, []ContextFabricCohortNarrowingDisclosureStep{{Stage: "elsewhere", Before: 11, After: 10}}},
		"clamp limit above after": {ContextFabricSubjectRepository, 11, 10, []ContextFabricCohortNarrowingDisclosureStep{{Stage: ContextFabricPlanNarrowingCardinality, Before: 11, After: 10, LimitBefore: 20, LimitAfter: 9}}},
	} {
		if sentence, ok := ContextFabricCohortNarrowingLimitation(input.kind, input.declared, input.served, input.steps); ok {
			t.Fatalf("%s: composed %q, want refusal", name, sentence)
		}
	}
	sentence, _ := ContextFabricCohortNarrowingLimitation(ContextFabricSubjectRepository, 11, 5, prod)
	for name, forged := range map[string]string{
		"served edited":    strings.Replace(sentence, "lists 5 of", "lists 4 of", 1),
		"leading zero":     strings.Replace(sentence, "the 11 repository", "the 011 repository", 1),
		"trailing text":    sentence + " Really.",
		"model paraphrase": strings.Replace(sentence, "Ask a narrower", "Try a narrower", 1),
		"unknown kind":     strings.Replace(sentence, "repository subjects", "widget subjects", 1),
	} {
		if IsContextFabricCohortNarrowingLimitation(forged) || IsContextFabricServiceAuthoredLimitation(forged) {
			t.Fatalf("%s: recogniser accepted %q", name, forged)
		}
	}
}
