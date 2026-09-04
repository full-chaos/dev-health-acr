package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The reuse degrade is a narrowing stage between planning and the served
// document, so the APPEND invariant applies to it.
//
// Found by adversarial review of this change, and it is the seam's own
// invariant failing on the one surface the seam did not cover:
//
//	APPEND      -- every narrowing stage between planning and the served
//	               document appends outcome rows.
//	DERIVE LAST -- completeness is a pure function of the whole set,
//	               computed at the surface that serves the answer.
//
// `stripUnverifiedEvidenceRefs` removes evidence references and, when
// stripping empties an object the contract requires to carry evidence,
// DROPS whole candidates, cohort members, drivers, findings and paths. The
// serve path then calls ComputeAnswerCompleteness, which CARRIES the stored
// rows and derives the state from them. A stored answer whose rows all read
// `satisfied` therefore serves a genuinely smaller document while still
// claiming `complete` -- measuring completeness and then shrinking the
// document somewhere the measurement cannot see, which is the exact failure
// this layer exists to forbid.
//
// It is worse than the assembly case this change started from. Assembly at
// least refused; the reuse path serves, and it serves an answer that says
// nothing was lost.
func TestAReusedAnswerThatWasStrippedCannotStillClaimCompleteness(t *testing.T) {
	t.Parallel()
	stored := storedResultWithEveryRefSite(t)
	// The stored row is a fully-derived, nothing-lost answer: every
	// requirement satisfied, state `complete`. That is the strongest
	// starting claim, and therefore the one a later reduction must be able
	// to walk back.
	stored.Completeness.Outcomes = []RequirementOutcomeRow{{
		Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
		Requirement: "state/subject/team",
		Obligation:  string(ObligationState),
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
		Impact:      contractsv1.ContextFabricAnswerImpactNone,
	}}
	stored.Completeness = ComputeAnswerCompleteness(stored)
	if stored.Completeness.State != contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("the fixture starts at state %q, want complete -- this test has nothing to walk back otherwise", stored.Completeness.State)
	}

	auxiliary := auxiliaryRefsOf(stored)
	if len(auxiliary) == 0 {
		t.Fatal("fixture carries no auxiliary refs, so nothing can be stripped and this test would pass vacuously")
	}
	missing := map[string]struct{}{}
	for _, ref := range auxiliary {
		missing[ref] = struct{}{}
	}

	before := len(collectEvidenceRefs(resultEvidenceSurface(stored)))
	degraded, counts, _, ok := degradeReusedResult(stored, missing)
	if !ok {
		t.Fatal("degradeReusedResult() refused; this fixture is meant to degrade")
	}
	after := len(collectEvidenceRefs(resultEvidenceSurface(degraded)))
	if after >= before {
		t.Fatalf("the degrade removed nothing (%d -> %d refs); the premise of this test is that content was lost", before, after)
	}
	t.Logf("reuse degrade removed %d of %d evidence refs and dropped %d whole objects", before-after, before, counts.objectDrops())

	// This is what the serve path does, verbatim (engine.go's reuse arm).
	served := degraded
	served.Completeness = ComputeAnswerCompleteness(served)

	if served.Completeness.State == contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("a reused answer that lost %d evidence references and %d whole objects still serves state=complete; a narrowing the caller is not told about is exactly the silent truncation this layer removes",
			before-after, counts.objectDrops())
	}

	var reuseRow *contractsv1.ContextFabricPlanRequirementOutcomeRow
	for index := range served.Completeness.Outcomes {
		if served.Completeness.Outcomes[index].Stage == contractsv1.ContextFabricOutcomeStageReuse {
			reuseRow = &served.Completeness.Outcomes[index]
		}
	}
	if reuseRow == nil {
		t.Fatal("no outcome row names the reuse reduction; the APPEND invariant says every narrowing stage between planning and the served document appends one")
	}
	if reuseRow.Outcome != contractsv1.ContextFabricRequirementNarrowed {
		t.Fatalf("the reuse row reports outcome %q, want narrowed", reuseRow.Outcome)
	}
	if reuseRow.CauseCoverage != contractsv1.ContextFabricCoverageDetailReuseAuxiliaryRefsStripped {
		t.Fatalf("the reuse row names cause %q, want the shipped reuse_auxiliary_refs_stripped code the disclosure already carries", reuseRow.CauseCoverage)
	}
	if !reuseRow.CauseObserved {
		t.Fatal("CauseObserved is false on a cause the recheck itself observed")
	}
	if reuseRow.Served >= reuseRow.Declared {
		t.Fatalf("served/declared = %d/%d is not a reduction", reuseRow.Served, reuseRow.Declared)
	}

	// APPEND, not rewrite: the planning row the stored answer carried must
	// still be there, untouched. A stage that replaced it would be deleting
	// another stage's disclosure.
	planning := 0
	for _, row := range served.Completeness.Outcomes {
		if row.Stage == contractsv1.ContextFabricOutcomeStagePlanning {
			planning++
			if row.Outcome != contractsv1.ContextFabricRequirementSatisfied {
				t.Fatalf("the planning row now reads %q; the reuse stage rewrote another stage's row instead of appending its own", row.Outcome)
			}
		}
	}
	if planning != 1 {
		t.Fatalf("%d planning rows survive, want the 1 the stored answer carried", planning)
	}

	// And the whole document still has to satisfy the contract it is served
	// under -- an appended row that made the payload invalid would turn a
	// disclosure into a 500.
	if err := ValidateStoredResult(served); err != nil {
		t.Fatalf("the served, degraded, disclosed payload does not validate: %v", err)
	}
}

// A reuse hit that removes NOTHING must append nothing.
//
// The mirror of the test above, and the one that stops the fix from being
// "always append a narrowed row": a row claiming a reduction that did not
// happen is the same defect as a reduction with no row, pointing the other
// way.
func TestAReusedAnswerThatLostNothingAppendsNoOutcomeRow(t *testing.T) {
	t.Parallel()
	stored := storedResultWithEveryRefSite(t)
	stored.Completeness = ComputeAnswerCompleteness(stored)
	beforeRows := len(stored.Completeness.Outcomes)

	degraded, counts, _, ok := degradeReusedResult(stored, map[string]struct{}{})
	if !ok {
		t.Fatal("degradeReusedResult() refused on an empty missing set")
	}
	if counts.Refs() != 0 || counts.objectDrops() != 0 {
		t.Fatalf("an empty missing set removed %d refs and %d objects", counts.Refs(), counts.objectDrops())
	}
	if got := len(degraded.Completeness.Outcomes); got != beforeRows {
		t.Fatalf("a degrade that removed nothing appended %d rows; a narrowing that did not happen must not be published as one", got-beforeRows)
	}
}
