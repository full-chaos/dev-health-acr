package contextfabric

import (
	"fmt"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// This file closes codex round 3's second High finding.
//
// THE DEFECT. applyGroupingRefusalDisclosure composed its sentence with a
// local Sprintf, so no consumer could recognise it as service-authored.
// appendBoundedLimitations, at the contract cap, displaces the last
// MODEL-authored caveat and never a service disclosure -- and it asks the
// contract which is which. An unregistered disclosure is a model caveat to
// that rule. So on a full limitation list the commit-affirmation composer,
// which runs LATER on the same result, displaced the grouping-refusal
// sentence to make room for its own retraction disclosure, and the served
// flat answer said nothing whatsoever about having been answered on a
// different axis than the reader asked for.
//
// That is not a cosmetic loss. It is the exact outcome chris's ruling forbids
// -- "a reader who asked for a per-team breakdown and silently received a
// flat list has been told something false by omission" -- reached through the
// one path the existing tests did not drive, because every one of them
// composed the disclosure onto a SHORT list where nothing displaces anything.
//
// The second half of the finding is the accounting: the displacement count
// was discarded into `_`, so a model caveat this investigation produced could
// vanish from the stored answer with LimitationsDisplaced still reading zero.

// fullModelLimitations (chaos4098_synthesis_status_test.go) is exactly the
// contract cap's worth of DISTINCT model caveats -- the state in which every
// append displaces something, and the state no earlier test for this
// disclosure exercised. Distinct matters: appendBoundedLimitations dedups
// first, so identical strings would collapse and leave room, and a test on
// such a list would prove nothing about the cap.

// groupingRefusalOutcome is the round-3 finding's own input: a repository
// planned kind against a team fact source.
func groupingRefusalOutcome() CohortGroupingOutcome {
	facts := []CanonicalFact{teamScopedFact("project_a", "team_security", "Security")}
	_, _, outcome := BuildCohortGroups(
		AnswerPlan{GroupKind: SubjectRepository}, planFixtureCohort("project_a"), facts)
	return outcome
}

// TestTheRefusalDisclosureSurvivesTheCommitAffirmationComposer is the
// round-3 finding reproduced end to end across the two composers in the
// order assembly runs them.
//
// RED at the fix parent (3e7bbeed): the served answer carries the retraction
// disclosure and NO grouping-refusal limitation at all.
func TestTheRefusalDisclosureSurvivesTheCommitAffirmationComposer(t *testing.T) {
	t.Parallel()
	outcome := groupingRefusalOutcome()
	if outcome.Refusal != CohortGroupingRefusalGroupKindSourceMismatch {
		t.Fatalf("fixture drift: outcome.Refusal = %q, want a group-kind source mismatch", outcome.Refusal)
	}

	result := affirmationResult()
	result.Limitations = fullModelLimitations()

	// Assembly order: the grouping disclosure is composed first, then the
	// commit-affirmation gate runs on the same result.
	applyGroupingRefusalDisclosure(&result, outcome)
	applyCommitAffirmation(&result, affirmationInputs{})

	// The gate must actually have fired, or the test proves nothing.
	if !hasLimitation(result.Limitations, commitRetractionLimitation) {
		t.Fatal("fixture drift: the commit-affirmation gate did not retract, so nothing competed for the last slot")
	}

	// Composed HERE from the literal template rather than from the
	// contract's composer, on purpose: this file must COMPILE at the fix
	// parent, and a red-first proof that fails only as a build error proves
	// nothing. It also makes the assertion about the WIRE TEXT a reader
	// sees, independent of which function produced it.
	want := expectedGroupingRefusalSentence(outcome)
	if !hasLimitation(result.Limitations, want) {
		t.Fatalf("the served answer carries NO grouping-refusal limitation: a later composer displaced it, so a grouped question was answered ungrouped in silence.\nwant present: %q", want)
	}
	if len(result.Limitations) > contractsv1.ContextFabricLimitationsMaxCount {
		t.Fatalf("limitations = %d, over the contract cap of %d", len(result.Limitations), contractsv1.ContextFabricLimitationsMaxCount)
	}
}

// TestTheRefusalDisclosureAccountsTheCaveatItDisplaces is the finding's
// second half. A displaced model caveat is gone from the stored answer and
// cannot be recovered downstream -- a displaced list and a list that had room
// are the same length and end the same way -- so LimitationsDisplaced is the
// only record that it existed.
//
// RED at the fix parent: displaced = 0 while a caveat was in fact dropped.
func TestTheRefusalDisclosureAccountsTheCaveatItDisplaces(t *testing.T) {
	t.Parallel()
	result := affirmationResult()
	result.Limitations = fullModelLimitations()
	before := len(result.Limitations)

	applyGroupingRefusalDisclosure(&result, groupingRefusalOutcome())

	if len(result.Limitations) != before {
		t.Fatalf("limitations = %d, want the cap %d held", len(result.Limitations), before)
	}
	if result.LimitationsDisplaced != 1 {
		t.Fatalf("LimitationsDisplaced = %d, want 1: a model caveat was dropped to fit the disclosure and nothing recorded it", result.LimitationsDisplaced)
	}
}

// TestTheRefusalDisclosureCountsNothingWhenItHasRoom is the positive control
// for the test above: incrementing LimitationsDisplaced unconditionally would
// satisfy it and lie on every ordinary answer.
func TestTheRefusalDisclosureCountsNothingWhenItHasRoom(t *testing.T) {
	t.Parallel()
	result := affirmationResult()
	result.Limitations = []string{"One model caveat."}

	applyGroupingRefusalDisclosure(&result, groupingRefusalOutcome())

	if len(result.Limitations) != 2 {
		t.Fatalf("limitations = %v, want the caveat kept and the disclosure appended", result.Limitations)
	}
	if result.LimitationsDisplaced != 0 {
		t.Fatalf("LimitationsDisplaced = %d, want 0: there was room", result.LimitationsDisplaced)
	}
}

// expectedGroupingRefusalSentence is the disclosure's wire text, written out
// in full here so this file names no symbol the fix introduced.
func expectedGroupingRefusalSentence(outcome CohortGroupingOutcome) string {
	return fmt.Sprintf(
		"This question asked for a breakdown by %s, but the available facts group by %s, so the answer is presented ungrouped.",
		outcome.PlannedKind, outcome.SourceKind)
}
