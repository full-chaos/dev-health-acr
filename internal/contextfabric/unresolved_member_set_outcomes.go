package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// What the served document says about a COMPUTED requirement whose server step
// had no member set to run over.
//
// WHY THIS EXISTS BESIDE THE DERIVATION'S OWN GUARD, which already refuses a
// computed step whose frame can never produce a member set. The derivation
// answers a question about the FRAME: is this expression a cohort variant, does
// it declare a member kind, is there a discovery arm for that kind. All three
// are decidable before retrieval runs, and they are decided once, in
// `CohortMemberKindFor`.
//
// There is a fourth condition and NOBODY can decide it at derivation time: a
// perfectly servable kind whose search returns no members. `DiscoveredCohort`
// returns a nil cohort when it retains none, so the frame was right, the arm
// exists, the search ran, and there is still nothing to rank or to count. A
// requirement seeded `satisfied` on the strength of naming a step then stands
// uncorrected on a document whose step never ran.
//
// So the correction is made HERE, on the served document, by the layer that
// holds the fact -- and it is made for EVERY computed step that runs over the
// resolved member set, selected from the declaration table rather than by
// naming the two that exist today. A third such step added later is covered by
// construction instead of being missed silently, which is the failure mode this
// whole file is a response to.

// memberSetResolved reports whether a served document carries a member set for
// a computed step to run over.
//
// ONE test, in one place, for a condition two siblings ask about. It is a nil
// check and it is written as a function anyway, because the alternative is the
// same nil check spelled at each site and a later refinement -- an empty
// non-nil cohort, say -- landing at one of them.
//
// A NIL COHORT IS NOT A ZERO-MEMBER COHORT, and the distinction is the whole
// point. `DiscoveredCohort` returns nil when it retains no members, so nil is
// "no member set was resolved". A non-nil cohort carrying zero members is a
// resolved population that happens to be empty; a count over it is genuinely
// `satisfied` at 0/0 and this file must not touch it.
func memberSetResolved(cohort *Cohort) bool {
	return cohort != nil
}

// stepRunsOverResolvedMemberSet reports whether the step that serves this
// obligation needs a resolved member set to execute.
//
// DERIVED FROM THE DECLARATION TABLE, never from a list of obligation names.
// Two computed steps exist today and both declare it; a hand-written list
// would be correct today and wrong the first time a third is added, which is
// exactly how the defect above reached fifteen kinds. Selecting by the
// declaration means a new step is covered by the act of declaring itself.
//
// It reuses `stepNeedsAResolvedMemberSet`, the SAME predicate the derivation's
// own guard reads, so the pre-retrieval refusal and this post-retrieval
// correction cannot come to disagree about which steps are in scope.
func stepRunsOverResolvedMemberSet(obligation AnswerObligation) bool {
	step, named := StepForComputedObligation(obligation)
	if !named {
		return false
	}
	inputs, declared := InputsForComputedStep(step)
	return stepNeedsAResolvedMemberSet(inputs, declared)
}

// hasAssembledOutcome reports whether an assembled-result row has already been
// appended for this requirement and obligation.
//
// finalizeResult runs more than once on some paths -- the stage-3 retry
// re-finalizes a fresh result, and the outcome layer's own candidate reduction
// re-finalizes one that already carries rows -- so an unguarded append would
// publish two answers for one requirement and leave a reader with two accounts
// of one cell.
//
// It is also what keeps the two siblings from talking over each other. The
// count step runs first and can produce a RICHER row than this file's refusal
// (a satisfied census, or a narrowing with real numbers); this guard is why
// the general sweep that follows it leaves that row alone rather than
// appending a second, poorer one beside it.
func hasAssembledOutcome(rows []RequirementOutcomeRow, requirement, obligation string) bool {
	for _, row := range rows {
		if row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult &&
			row.Requirement == requirement &&
			row.Obligation == obligation {
			return true
		}
	}
	return false
}

// unresolvedMemberSetOutcomeRow states a computed requirement whose server step
// had no member set to run over.
//
// It carries no numbers because nothing was computed -- not a zero-member
// population, which is a different answer and gets its own served row from the
// normal path. The two are told apart by the OUTCOME token, which is the whole
// reason that vocabulary is closed.
//
// The cause is taken from the derivation's own mapping for this concept rather
// than picked by hand here: `computed_population_absent` is already the reason
// a computed obligation with no population carries, and routing through the
// same table means one record of that fact, not two.
//
// IMPACT IS `dimension`, AND THE REASON IS THE SAME FOR BOTH SIBLINGS. The
// reader asked how many, or in what order, and gets no answer to that question
// at all. It is not `scope` -- no fewer things are shown, because the things
// were never enumerated -- and it is not `depth`, which is less detail about
// things the reader did receive. A ranking that could not run and a count that
// could not run lose the same thing: the dimension itself.
func unresolvedMemberSetOutcomeRow(requirement, obligation string) RequirementOutcomeRow {
	return RequirementOutcomeRow{
		Stage:       contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement: requirement,
		Obligation:  obligation,
		Outcome:     contractsv1.ContextFabricRequirementUnavailable,
		Impact:      contractsv1.ContextFabricAnswerImpactDimension,
		// The derivation's own reason for a computed obligation with no
		// population, mapped through the shipped table.
		CauseCoverage: unavailableRequirementCause(RequirementReasonComputedPopulationAbsent),
		// Observed: assembly looked for a member set and there was none.
		// Nothing here defaulted.
		CauseObserved: true,
	}
}

// appendUnresolvedMemberSetOutcomes states every computed requirement whose
// server step runs over the resolved member set, on a document that carries no
// member set.
//
// ORDER MATTERS AT THE CALL SITE and it is stated here so a later edit cannot
// reorder it by accident: this runs AFTER the count sibling. That sibling can
// produce a satisfied or narrowed row with real numbers, which is a better
// account than a refusal, and `hasAssembledOutcome` is what lets it win.
// Running this first would append the refusal and the sibling would then find
// its row already present and say nothing.
//
// It reads the PLANNING rows for its requirement identities rather than
// minting them, for the reason every stage in this layer does: the identity
// belongs to the derivation, and a second authority for which requirement a
// row is about is how the two begin to disagree.
//
// A planning row that is not `satisfied` is LEFT ALONE. The derivation already
// refused that cell and said why -- typically with this same cause, from its
// own pre-retrieval guard -- and re-stating it at the assembled stage would
// publish the same refusal twice under two stage labels, which reads as two
// findings about one cell.
func appendUnresolvedMemberSetOutcomes(rows []RequirementOutcomeRow, cohort *Cohort) []RequirementOutcomeRow {
	if memberSetResolved(cohort) {
		return rows
	}
	var added []RequirementOutcomeRow
	for _, row := range rows {
		if row.Stage != contractsv1.ContextFabricOutcomeStagePlanning || row.Requirement == "" {
			continue
		}
		// Only a cell the derivation reported as served can be wrong in the
		// direction this function corrects.
		if row.Outcome != contractsv1.ContextFabricRequirementSatisfied {
			continue
		}
		if !stepRunsOverResolvedMemberSet(AnswerObligation(row.Obligation)) {
			continue
		}
		if hasAssembledOutcome(rows, row.Requirement, row.Obligation) {
			continue
		}
		// Guards against two planning rows sharing one identity within a
		// single pass, which the loop above cannot see through `rows`
		// because nothing is appended to it until the loop ends.
		if hasAssembledOutcome(added, row.Requirement, row.Obligation) {
			continue
		}
		added = append(added, unresolvedMemberSetOutcomeRow(row.Requirement, row.Obligation))
	}
	return appendOutcomeRows(rows, added...)
}
