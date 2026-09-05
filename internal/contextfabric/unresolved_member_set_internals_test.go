package contextfabric

// THE SWEEP'S OWN INTERNALS, tested directly.
//
// A SEPARATE FILE from the harm tests beside it, deliberately: everything here
// names an identifier that does not exist on the parent commit, so this file
// cannot compile there. The harm tests DO compile at the parent and fail at
// runtime, which is what makes their red a statement about behaviour rather
// than about a missing identifier -- and keeping the two apart is what lets
// the red-at-parent proof copy that file VERBATIM instead of hand-trimming a
// variant of it that nobody ships.

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestTheSweepPopulationComesFromTheDeclarationTable pins HOW the sweep
// chooses what to correct.
//
// The population is every computed obligation whose server step declares that
// it RUNS OVER the resolved member set. A hand-written list of obligation
// names would be correct today and wrong the first time a third step is added
// -- which is exactly the shape of the defect this whole change removes.
//
// Quantified over the shipped step vocabulary, so a new step is covered by the
// act of declaring itself, with a negative control on the read side.
func TestTheSweepPopulationComesFromTheDeclarationTable(t *testing.T) {
	t.Parallel()
	// Keyed on the OBLIGATION, because that is what a requirement row carries
	// and therefore what the sweep reads. The step-side coverage check below
	// is what stops that keying from silently missing a declared step.
	declaring := 0
	reachableSteps := make(map[ComputedObligationStep]bool, ComputedObligationStepCount)
	for _, obligation := range AnswerObligationVocabulary() {
		kind, known := KindOfObligation(obligation)
		if !known || kind != ObligationKindComputed {
			continue
		}
		step, named := StepForComputedObligation(obligation)
		if !named {
			t.Errorf("computed obligation %q names no server step, so a requirement row carrying it routes nowhere", obligation)
			continue
		}
		reachableSteps[step] = true
		inputs, declared := InputsForComputedStep(step)
		if !declared {
			t.Errorf("step %q has no declared inputs, so the sweep cannot tell whether it runs over the member set", step)
			continue
		}
		want := inputs.RunsOverResolvedMemberSet
		if got := stepRunsOverResolvedMemberSet(obligation); got != want {
			t.Errorf("obligation %q (step %q): stepRunsOverResolvedMemberSet = %v, the declaration says RunsOverResolvedMemberSet = %v", obligation, step, got, want)
		}
		if want {
			declaring++
		}
	}
	if declaring == 0 {
		t.Fatal("no shipped step declares that it runs over the resolved member set, so the sweep's population is empty and every assertion above is vacuous")
	}
	// COVERAGE IN THE OTHER DIRECTION: every declared step must be reachable
	// from some obligation. A step no obligation names could declare that it
	// runs over the member set and never be corrected, and the loop above
	// could not see it.
	for _, step := range ComputedObligationStepVocabulary() {
		if !reachableSteps[step] {
			t.Errorf("step %q is declared but no obligation names it, so no requirement row can ever route the sweep to it", step)
		}
	}

	// NEGATIVE CONTROL. A READ obligation names no computed step at all, so
	// it must never enter the sweep. Without this the function could return
	// true for everything and every assertion above would still pass.
	for _, obligation := range AnswerObligationVocabulary() {
		kind, known := KindOfObligation(obligation)
		if !known || kind == ObligationKindComputed {
			continue
		}
		if stepRunsOverResolvedMemberSet(obligation) {
			t.Errorf("read obligation %q entered the sweep's population; only a computed obligation names a server step", obligation)
		}
	}
}

// TestMemberSetResolvedTellsAnAbsentSetFromAnEmptyOne pins the distinction the
// whole correction rests on.
//
// A nil cohort is "no member set was resolved". A non-nil cohort carrying zero
// members is a resolved population that happens to be empty, and a count over
// it is genuinely satisfied at 0/0. Collapsing the two would either invent a
// refusal for a real empty answer or hide a real absence.
func TestMemberSetResolvedTellsAnAbsentSetFromAnEmptyOne(t *testing.T) {
	t.Parallel()
	if memberSetResolved(nil) {
		t.Error("memberSetResolved(nil) is true; a nil cohort is the shape a discovery that retained nothing returns")
	}
	empty := &Cohort{Kind: SubjectTeam, Members: []CohortMember{}, Complete: true}
	if !memberSetResolved(empty) {
		t.Error("memberSetResolved reported false for a resolved cohort carrying zero members; an empty population is an answer, not an absence")
	}
	if !memberSetResolved(countingCohort(SubjectTeam, 2)) {
		t.Error("memberSetResolved reported false for a cohort carrying members")
	}
	// The sweep must follow that distinction, not just the predicate.
	rows := []RequirementOutcomeRow{{
		Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
		Requirement: "ranking/member/team",
		Obligation:  string(ObligationRanking),
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
	}}
	if got := appendUnresolvedMemberSetOutcomes(rows, empty); len(got) != len(rows) {
		t.Fatalf("the sweep appended %d row(s) over a resolved but empty cohort; it must fire on an absent member set only", len(got)-len(rows))
	}
	if got := appendUnresolvedMemberSetOutcomes(rows, nil); len(got) != len(rows)+1 {
		t.Fatalf("the sweep appended %d row(s) over an ABSENT member set, want 1 -- the control above would otherwise pass because the sweep never fires at all", len(got)-len(rows))
	}
}
