package contextfabric

// `Declared` becomes a number the answer did not already carry.
//
// WHAT THESE PIN. MembershipCardinality's two numbers exist so that "counted
// 14" and "counted 14 of 36" are distinguishable, and until retrieval learned
// to count past the render clamp they could not be: every input to `Declared`
// was derived from `cohort.Members`, so it could only ever equal `Served`. The
// pair carried no information and a count over a clamped cohort read as a
// census.
//
// The tests below are the arithmetic of that fix, at the unit that owns it.
// The stage-1 cases in membership_cardinality_test.go stay exactly as they
// were and are the other half of the claim: the clamp's own Before/After are
// CEILINGS, and this change still refuses to publish them as members.

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestCardinalityDeclaresTheRetrievalPopulationWhenItExceedsTheServedMembers(t *testing.T) {
	t.Parallel()
	// 14 carried, 36 seen: the measured shape of an organization whose
	// project count exceeded its render allowance.
	cardinality, counted := ComputeMembershipCardinality(countingCohort(SubjectProject, 14), 36, nil)

	if !counted {
		t.Fatal("counted = false, want true -- the fixture resolved no member set, so it proves nothing")
	}
	if cardinality.Served != 14 {
		t.Errorf("served = %d, want 14 -- served is what the answer carries and this change must not move it", cardinality.Served)
	}
	if cardinality.Declared != 36 {
		t.Errorf("declared = %d, want 36 -- the population retrieval observed, not the members the budget allowed", cardinality.Declared)
	}
	if !cardinality.Narrowed() {
		t.Error("Narrowed() = false, want true -- 14 of 36 is a cut, and a reader that cannot see it reads the count as a census")
	}
}

func TestCardinalityTakesTheRenderClampBasisWhenThePopulationIsWhatExceededTheAnswer(t *testing.T) {
	t.Parallel()
	// The mechanism that cut 36 to 14 is the pre-read clamp, recorded as the
	// stage-1 `cardinality` step. Its BASIS is the disclosure a reader needs;
	// its numbers are ceilings and must stay unpublished.
	cardinality, counted := ComputeMembershipCardinality(
		countingCohort(SubjectProject, 14), 36,
		[]contractsv1.ContextFabricPlanNarrowing{{
			Stage:  contractsv1.ContextFabricPlanNarrowingCardinality,
			Basis:  contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
			Before: 50, After: 14,
		}})

	if !counted {
		t.Fatal("counted = false, want true")
	}
	if cardinality.Declared != 36 {
		t.Errorf("declared = %d, want 36 -- the clamp's own Before (50) is a ceiling and must never become a member count", cardinality.Declared)
	}
	if cardinality.Basis != contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical {
		t.Errorf("basis = %q, want the clamp's basis -- a cut with no named mechanism is a number the reader cannot act on", cardinality.Basis)
	}
}

func TestCardinalityIgnoresAPopulationThatDoesNotExceedTheServedMembers(t *testing.T) {
	t.Parallel()
	// The reuse path threads 0 because no retrieval ran, and a caller
	// threading a stale value could pass anything below the member count.
	// Either way `Declared` must not fall below `Served`: a count declaring
	// fewer than it served would read through Narrowed() as "nothing was cut".
	for _, population := range []int{0, 3, 11} {
		cardinality, counted := ComputeMembershipCardinality(countingCohort(SubjectTeam, 11), population, nil)
		if !counted {
			t.Fatalf("population %d: counted = false, want true", population)
		}
		if cardinality.Served != 11 || cardinality.Declared != 11 {
			t.Errorf("population %d: served/declared = %d/%d, want 11/11", population, cardinality.Served, cardinality.Declared)
		}
		if cardinality.Narrowed() {
			t.Errorf("population %d: Narrowed() = true, want false -- nothing was cut", population)
		}
	}
}

func TestCardinalityLeavesTheBasisEmptyWhenThePopulationCutNothing(t *testing.T) {
	t.Parallel()
	// population EQUAL to served, with a clamp step present. The clamp ran
	// (it always records a step) but it cut nothing, so there is no loss to
	// attribute and the basis must stay empty.
	//
	// THIS IS THE CELL THAT SEPARATES `>` FROM `>=`. Every other fixture
	// here passes a population either above or below the served count, and
	// both operators agree on those. Weakened to `>=`, this cell publishes a
	// cut mechanism for a cohort that lost nothing -- a disclosure a reader
	// would act on and that never happened.
	cardinality, counted := ComputeMembershipCardinality(
		countingCohort(SubjectProject, 14), 14,
		[]contractsv1.ContextFabricPlanNarrowing{{
			Stage:  contractsv1.ContextFabricPlanNarrowingCardinality,
			Basis:  contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
			Before: 50, After: 14,
		}})

	if !counted {
		t.Fatal("counted = false, want true")
	}
	if cardinality.Declared != 14 || cardinality.Served != 14 {
		t.Errorf("served/declared = %d/%d, want 14/14 -- nothing was cut", cardinality.Served, cardinality.Declared)
	}
	if cardinality.Basis != "" {
		t.Errorf("basis = %q, want empty -- naming a mechanism for a cut that did not happen is a false disclosure", cardinality.Basis)
	}
	if cardinality.Narrowed() {
		t.Error("Narrowed() = true, want false")
	}
}

func TestCardinalityLetsALaterMemberNarrowingOutrankThePopulation(t *testing.T) {
	t.Parallel()
	// Two losses on one turn: the clamp cut the population to what could be
	// rendered, and a stage-3 candidate narrowing then cut further. The
	// LARGEST count observed wins `Declared`, and the basis a reader is shown
	// is the one belonging to that count.
	cardinality, counted := ComputeMembershipCardinality(
		countingCohort(SubjectProject, 4), 9,
		[]contractsv1.ContextFabricPlanNarrowing{{
			Stage:  contractsv1.ContextFabricPlanNarrowingSynthesisInput,
			Basis:  contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
			Before: 20, After: 4,
		}})

	if !counted {
		t.Fatal("counted = false, want true")
	}
	if cardinality.Declared != 20 {
		t.Errorf("declared = %d, want 20 -- a member narrowing observed more than the population arm did, and the largest observed count is the claim", cardinality.Declared)
	}
}
