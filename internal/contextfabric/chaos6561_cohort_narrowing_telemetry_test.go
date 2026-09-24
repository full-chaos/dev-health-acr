package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestCHAOS6561DisclosureDecisionIsEmittedOncePerServedAnswer: the new branch
// is observable -- one Info-level event for the served answer, naming the
// outcome and the counts -- and an unnarrowed answer emits none.
func TestCHAOS6561DisclosureDecisionIsEmittedOncePerServedAnswer(t *testing.T) {
	t.Parallel()
	maxItems := chaos6561HeadroomItems(t, 10)
	claims := -1
	for c := 0; c < 50; c++ {
		if 10*(1+c) > maxItems && 5*(1+c) <= maxItems {
			claims = c
			break
		}
	}
	telemetry := &recordingTelemetry{}
	chaos6561Investigate(t, 11, claims, maxItems, telemetry)
	want := []CohortNarrowingDisclosureEvent{{
		Family: QuestionFamilyUnclassified, Kind: SubjectProject, Outcome: CohortNarrowingDisclosed,
		Declared: 11, Served: 5, Steps: 2,
	}}
	if len(telemetry.cohortNarrowingDisclosures) != 1 || telemetry.cohortNarrowingDisclosures[0] != want[0] {
		t.Fatalf("disclosure events = %+v, want exactly %+v (once, for the served retry, not the discarded first pass)",
			telemetry.cohortNarrowingDisclosures, want)
	}
	var sawNarrowingRow bool
	for _, event := range telemetry.readRequirementPopulations {
		if event.Requirement == chaos6561MemberRequirement {
			sawNarrowingRow = event.CauseObserved && event.Refinements == 2 &&
				event.CauseNarrowing == contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical &&
				event.CauseOverrun == contractsv1.ContextFabricBudgetOverrunItems
		}
	}
	if !sawNarrowingRow {
		t.Fatalf("read-population line for %s does not carry the narrowing cause: %+v", chaos6561MemberRequirement, telemetry.readRequirementPopulations)
	}

	quiet := &recordingTelemetry{}
	chaos6561Investigate(t, 3, 1, 1000, quiet)
	if len(quiet.cohortNarrowingDisclosures) != 0 {
		t.Fatalf("unnarrowed answer emitted disclosure events %+v", quiet.cohortNarrowingDisclosures)
	}
}

// TestCHAOS6561UnreconciledChainIsNotDisclosedWithInventedNumbers: when the
// plan's recorded steps do not account for the whole cut (here: the caller's
// own member limit cut 11 to 8 and no plan step recorded it), no sentence with
// counts is written -- the decision is reported as chain_unreconciled instead.
func TestCHAOS6561UnreconciledChainIsNotDisclosedWithInventedNumbers(t *testing.T) {
	t.Parallel()
	cohort := budgetStageCohort(8)
	cohort.Complete, cohort.Truncated = false, true
	cardinality, resolved := ComputeMembershipCardinality(cohort, 11, nil)
	if !resolved || !cardinality.Narrowed() {
		t.Fatalf("fixture defect: cardinality %+v", cardinality)
	}
	result := InvestigationResult{Limitations: []string{"A model caveat."}}
	event, decided := applyCohortNarrowingDisclosure(&result, QuestionFamilyUnclassified, cardinality, nil)
	if !decided || event.Outcome != CohortNarrowingChainUnreconciled || event.Declared != 11 || event.Served != 8 || event.Steps != 0 {
		t.Fatalf("decision = %+v (decided=%v), want chain_unreconciled 11/8", event, decided)
	}
	if len(result.Limitations) != 1 || result.Limitations[0] != "A model caveat." {
		t.Fatalf("limitations = %q, want the model caveat alone", result.Limitations)
	}
}

// TestCHAOS6561DisclosureIsReplacedNotAccumulatedAcrossPasses: a second
// finalize of the same document states the chain once, for the member set it
// now carries.
func TestCHAOS6561DisclosureIsReplacedNotAccumulatedAcrossPasses(t *testing.T) {
	t.Parallel()
	narrowing := []contractsv1.ContextFabricPlanNarrowing{
		{Stage: contractsv1.ContextFabricPlanNarrowingCardinality, Basis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical, Before: 20, After: 10},
	}
	ten := budgetStageCohort(10)
	ten.Complete, ten.Truncated = false, true
	first, _ := ComputeMembershipCardinality(ten, 11, narrowing)
	result := InvestigationResult{Limitations: []string{}}
	applyCohortNarrowingDisclosure(&result, QuestionFamilyUnclassified, first, narrowing)

	narrowing = append(narrowing, contractsv1.ContextFabricPlanNarrowing{
		Stage: contractsv1.ContextFabricPlanNarrowingAssembledResult, Basis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
		Before: 10, After: 5, Overrun: contractsv1.ContextFabricBudgetOverrunItems,
	})
	five := budgetStageCohort(5)
	five.Complete, five.Truncated = false, true
	second, _ := ComputeMembershipCardinality(five, 11, narrowing)
	applyCohortNarrowingDisclosure(&result, QuestionFamilyUnclassified, second, narrowing)

	if len(result.Limitations) != 1 || !contractsv1.IsContextFabricCohortNarrowingLimitation(result.Limitations[0]) {
		t.Fatalf("limitations after two passes = %q, want exactly one cohort-narrowing sentence", result.Limitations)
	}
	if want := "This answer lists 5 of the 11 "; result.Limitations[0][:len(want)] != want {
		t.Fatalf("surviving sentence %q does not describe the second pass's 5 of 11", result.Limitations[0])
	}
}
