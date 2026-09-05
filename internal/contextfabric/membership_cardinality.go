package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The `membership_cardinality` server step, executed.
//
// WHAT THIS CLOSES. §13.2.3 names a server step for every computed
// obligation, and `count`'s step was named and run by nothing. The number
// reached a reader only through the model narrating over whatever facts the
// plan happened to read -- which is why `status_shadow.go` recorded `count`
// as unobservable, and why the six-authority parity proof had to treat five
// cells as blocking: a step nobody executes says nothing about what the
// answer depends on, so "this step consumes no fact" could not license
// retiring the thing that caused the facts to be read.
//
// WHAT IT DOES NOT CLOSE, stated here rather than left to be discovered.
// The step's declared input is the RESOLVED MEMBER SET, and that is exactly
// what it counts. Whether the resolved set is the whole population is a
// COVERAGE question, and the answer already carries it: `Cohort.Complete`
// and `Cohort.Truncated`. A count served over a cohort the graph read
// stopped short of is a true count OF THE RESOLVED SET and a lower bound on
// the population, and this file does not pretend otherwise. Making the
// population itself countable needs a census the pre-read clamp currently
// makes unobservable, which is its own change.
//
// AND THE ANSWER PROSE IS STILL THE MODEL'S. This step makes the count a
// SERVER RESULT with a requirement identity; it does not stop synthesis from
// also stating a number in words. Feeding this value into the narration so
// there is one counter was considered and is NOT done here, for two measured
// reasons rather than for scope: the cardinality is computed in
// finalizeResult, AFTER synthesis, because it must describe the member set
// the served document actually carries -- stage 3 can narrow the cohort and
// re-synthesize -- so a pre-synthesis value would be a SECOND number that
// disagrees with the served one exactly when a narrowing happened, which is
// the case that matters. And reaching the model at all means a synthesis
// prompt change, which is a behaviour change: a fresh prompt version (the
// reuse-key trap) plus a before/after tally on the rig with replicates.
// Both are real work with their own evidence, and neither is this slice's.
// Stated here rather than left for a reader to discover, because a residual
// nobody wrote down reads as a residual nobody has.

// MembershipCardinality is what the step computed: how many members the
// answer carries, how many were there to carry, and -- when those differ --
// which recorded mechanism cut them.
//
// Served and Declared are two numbers rather than one because a cardinality
// that is approximately right is wrong (`quantifierForComputed` makes
// `count`'s quantifier EXACT), so a count served over a reduced set must be
// distinguishable from one served over everything found. One number cannot
// carry that distinction, and a single number plus a boolean would leave the
// reader unable to say how much was lost.
type MembershipCardinality struct {
	// Kind is the subject kind that was counted.
	Kind SubjectKind
	// Served is the cardinality of the member set the answer carries.
	Served int
	// Declared is the largest member count this turn actually OBSERVED.
	// Equal to Served when nothing narrowed.
	Declared int
	// Basis and Overrun name the recorded mechanism that cut the set,
	// carried from the plan's own narrowing step rather than re-derived.
	// Both empty when nothing narrowed.
	Basis   contractsv1.ContextFabricNarrowingBasis
	Overrun contractsv1.ContextFabricBudgetOverrun
}

// Narrowed reports whether the answer carries fewer members than were found.
func (m MembershipCardinality) Narrowed() bool {
	return m.Served < m.Declared
}

// ComputeMembershipCardinality runs the step over the resolved member set.
//
// PURE: reads its arguments and mutates nothing, like every other composer
// on the assembly path.
//
// The second return is false when there is NO resolved member set. That is
// an absence, and it is reported as one rather than as a count of zero: a
// question whose population could not be resolved and a question whose
// population is genuinely empty are different answers, and one number
// standing for both is the shape "missing is not healthy" forbids.
func ComputeMembershipCardinality(cohort *Cohort, narrowing []contractsv1.ContextFabricPlanNarrowing) (MembershipCardinality, bool) {
	if cohort == nil {
		return MembershipCardinality{}, false
	}
	cardinality := MembershipCardinality{
		Kind:     cohort.Kind,
		Served:   len(cohort.Members),
		Declared: len(cohort.Members),
	}
	if step, found := firstMemberNarrowing(narrowing); found && step.Before > cardinality.Served {
		cardinality.Declared = step.Before
		cardinality.Basis = step.Basis
		cardinality.Overrun = step.Overrun
	}
	return cardinality, true
}

// firstMemberNarrowing finds the earliest recorded step that narrowed
// MEMBERS, whose `Before` is therefore the largest member count this turn
// observed.
//
// IT EXCLUDES STAGE 1, AND THAT IS THE WHOLE REASON THIS IS A FUNCTION
// RATHER THAN A LOOP AT THE CALL SITE. The `cardinality` stage records a
// CEILING pair -- `Before` is the requested MaxCohortMembers and `After` is
// the clamp the plan imposed (see the engine's pre-read clamp) -- so its
// numbers are limits, not members. Reading it as a member count would
// publish "declared 50, served 3" for a three-member organization, which is
// a confident wrong answer about the size of the thing being counted.
//
// It also excludes group steps: those narrow the GROUP axis, and their
// Before/After count groups.
func firstMemberNarrowing(narrowing []contractsv1.ContextFabricPlanNarrowing) (contractsv1.ContextFabricPlanNarrowing, bool) {
	for _, step := range narrowing {
		if step.Groups {
			continue
		}
		if step.Stage == contractsv1.ContextFabricPlanNarrowingCardinality {
			continue
		}
		return step, true
	}
	return contractsv1.ContextFabricPlanNarrowing{}, false
}

// countRequirement finds the identity of the requirement the cardinality
// answers: the `count` row this turn's derivation seeded.
//
// The identity is CARRIED from the derivation that owns it, never minted
// here -- the same rule `subjectScopeRequirement` follows, and for the same
// reason: a second authority for which requirement a row is about is how the
// two begin to disagree.
//
// Returning empty strings is the gate that keeps this step from running on a
// question that never asked for a count: no seeded `count` row means the
// frame derived no such obligation, and nothing is appended.
func countRequirement(rows []RequirementOutcomeRow) (string, string) {
	for _, row := range rows {
		if row.Stage != contractsv1.ContextFabricOutcomeStagePlanning {
			continue
		}
		if row.Obligation == string(ObligationCount) && row.Requirement != "" {
			return row.Requirement, row.Obligation
		}
	}
	return "", ""
}

// hasCountOutcome reports whether an assembled-result `count` row has
// already been appended for this requirement.
//
// finalizeResult runs more than once on some paths -- the stage-3 retry
// re-finalizes a fresh result, and the outcome layer's own candidate
// reduction re-finalizes one that already carries rows -- so an unguarded
// append would publish two cardinalities for one requirement and leave a
// reader with two answers to one question.
func hasCountOutcome(rows []RequirementOutcomeRow, requirement string) bool {
	for _, row := range rows {
		if row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult &&
			row.Requirement == requirement &&
			row.Obligation == string(ObligationCount) {
			return true
		}
	}
	return false
}

// membershipCardinalityOutcomeRow states the computed cardinality as an
// outcome row.
//
// WHY THE COUNT RIDES THIS ROW RATHER THAN A FIELD OF ITS OWN. The row
// already carries the two numbers, the requirement identity that says WHAT
// was counted, and a closed outcome vocabulary whose pairing rule is
// validated: `satisfied` may name no cause and must lose nothing, and
// `narrowed` must name a cause and must be a real reduction. Those are
// exactly the two states a cardinality has, already enforced. Minting a
// third token for "exact vs lower bound" would put a second authority beside
// `Cohort.Complete`/`Truncated` and beside this outcome, which is the
// two-readers-of-one-registry defect this package's review history is about.
func membershipCardinalityOutcomeRow(cardinality MembershipCardinality, requirement, obligation string) RequirementOutcomeRow {
	row := RequirementOutcomeRow{
		Stage:       contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement: requirement,
		Obligation:  obligation,
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
		Impact:      contractsv1.ContextFabricAnswerImpactNone,
		Served:      cardinality.Served,
		Declared:    cardinality.Declared,
	}
	if !cardinality.Narrowed() {
		return row
	}
	row.Outcome = contractsv1.ContextFabricRequirementNarrowed
	// Scope, not depth: the reader is shown fewer of the counted things,
	// and the ones that remain are unchanged.
	row.Impact = contractsv1.ContextFabricAnswerImpactScope
	// The cause is CARRIED from the narrowing step the plan recorded, so it
	// names the mechanism that actually ran. `CauseObserved` says so.
	row.CauseNarrowing = cardinality.Basis
	row.CauseOverrun = cardinality.Overrun
	row.CauseObserved = true
	// The reduction step, derived from the row's own counts and the causes
	// just carried onto it, so the step and the row cannot state different
	// things about one narrowing.
	return contractsv1.ContextFabricWithReductionRefinement(row)
}

// appendMembershipCardinality is the assembly-side wiring: run the step over
// the served member set and state the result on the served document.
//
// Returns the rows unchanged when the frame asked for no count, when there
// is no resolved member set, or when this requirement already has its row.
func appendMembershipCardinality(rows []RequirementOutcomeRow, cohort *Cohort, narrowing []contractsv1.ContextFabricPlanNarrowing) ([]RequirementOutcomeRow, MembershipCardinality, bool) {
	requirement, obligation := countRequirement(rows)
	if requirement == "" {
		return rows, MembershipCardinality{}, false
	}
	if hasCountOutcome(rows, requirement) {
		return rows, MembershipCardinality{}, false
	}
	cardinality, counted := ComputeMembershipCardinality(cohort, narrowing)
	if !counted {
		// STATE the absence rather than saying nothing.
		//
		// This branch was silent, and silence was the bug (codex round 2).
		// `organization_scope` is not a cohort variant, so no member set is
		// ever discovered for it, while a count coordinate at the member
		// role is perfectly legal there. The frame derived a `count`, the
		// planning seed marked it `satisfied` because the registry CAN
		// serve it, and nothing afterwards said otherwise -- so a complete
		// answer carried a count requirement, no countable result, and a
		// declaration claiming the server computes one.
		//
		// "Absent, never zero" was right about not inventing a number and
		// wrong about staying quiet. A requirement the answer could not
		// meet is stated as unmet; the completeness state derived from the
		// set then reads degraded, which is what it is.
		return appendOutcomeRows(rows, unservedCardinalityOutcomeRow(requirement, obligation)), MembershipCardinality{}, false
	}
	return appendOutcomeRows(rows, membershipCardinalityOutcomeRow(cardinality, requirement, obligation)), cardinality, true
}

// unservedCardinalityOutcomeRow states a count requirement that had no member
// set to count.
//
// It carries 0/0 because nothing was counted -- not a zero-member population,
// which is a different answer and gets a `satisfied` 0/0 row from the normal
// path. The two are told apart by the OUTCOME token, which is the whole
// reason that vocabulary is closed.
//
// The cause is taken from the derivation's own mapping for this concept
// rather than picked by hand here: `computed_population_absent` is already
// the reason a computed obligation with no population carries, and routing
// through the same table means one record of that fact, not two.
func unservedCardinalityOutcomeRow(requirement, obligation string) RequirementOutcomeRow {
	return RequirementOutcomeRow{
		Stage:       contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement: requirement,
		Obligation:  obligation,
		Outcome:     contractsv1.ContextFabricRequirementUnavailable,
		// Dimension: the reader asked how many and gets no answer at all --
		// not fewer things (scope) and not less detail about them (depth).
		Impact:        contractsv1.ContextFabricAnswerImpactDimension,
		CauseCoverage: unavailableRequirementCause(RequirementReasonComputedPopulationAbsent),
		// Observed: assembly looked for a member set and there was none.
		// Nothing here defaulted.
		CauseObserved: true,
	}
}

// membershipCardinalityEventFrom builds the telemetry event by READING the
// served document's own count row.
//
// It does not recompute the cardinality. The row is what the reader
// receives, so the log line has to describe that row or it is describing a
// different answer -- the "telemetry describes a different artifact than the
// one served" class the deferred emitters already fix one level up.
//
// The second return is false when the answer states no count, which is the
// same absence the row itself reports: nothing to say, said by saying
// nothing rather than by logging a zero.
func membershipCardinalityEventFrom(result InvestigationResult, family QuestionFamily) (MembershipCardinalityEvent, bool) {
	for _, row := range result.Completeness.Outcomes {
		if row.Stage != contractsv1.ContextFabricOutcomeStageAssembledResult {
			continue
		}
		if row.Obligation != string(ObligationCount) {
			continue
		}
		event := MembershipCardinalityEvent{
			Family:      family,
			Requirement: row.Requirement,
			Outcome:     row.Outcome,
			Served:      row.Served,
			Declared:    row.Declared,
			Basis:       row.CauseNarrowing,
			Overrun:     row.CauseOverrun,
		}
		if result.Cohort != nil {
			event.CohortComplete = result.Cohort.Complete
			event.CohortTruncated = result.Cohort.Truncated
		}
		return event, true
	}
	return MembershipCardinalityEvent{}, false
}

// reusedPlanNarrowing returns the narrowing steps the STORED document
// recorded, so a backfilled cardinality describes that document's own
// history rather than this turn's.
//
// Nil-safe on the plan: a row persisted before the answer plan existed
// carries none, and a cardinality with no narrowing history is exact over
// the member set the document carries -- which is the honest reading, since
// nothing this turn narrowed it.
func reusedPlanNarrowing(reused InvestigationResult) []contractsv1.ContextFabricPlanNarrowing {
	if reused.AnswerPlan == nil {
		return nil
	}
	return reused.AnswerPlan.Narrowing
}

// reusedPlanFamily names the family a REUSED answer was planned under.
//
// Read off the stored plan rather than this turn's, for the same reason the
// narrowing history is: the event describes the document being served, and
// that document was planned under its own family. A stored row with no plan
// carries no family, and the event says so by carrying none rather than by
// borrowing one that would be a guess.
func reusedPlanFamily(reused InvestigationResult) QuestionFamily {
	if reused.AnswerPlan == nil {
		return ""
	}
	return reused.AnswerPlan.Family
}
