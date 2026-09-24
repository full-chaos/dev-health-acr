package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-6561: project the plan's cohort narrowing into the served answer.
//
// The plan records every narrowing step it took (AnswerPlan.Narrowing), and
// the membership_cardinality step knows how many members retrieval found and
// how many the answer carries. Before this file, nothing joined the two for a
// READER: the served answer said `served 5 / declared 11` on its requirement
// rows with a coverage cause that read as "the evidence for the other six was
// not found", while the six had in fact been cut by two recorded budget steps.
//
// This file derives ONE member chain from those two authorities and states it
// twice, in the two places a reader already looks, without changing any
// number the answer serves:
//
//   - as a deterministic limitation sentence
//     (contractsv1.ContextFabricCohortNarrowingLimitation), and
//   - as the per-step Refinements of the distributive member read rows
//     (read_population.go), with the recorded basis and overrun as the row's
//     cause, observed.
//
// NO BUDGET OR PLANNING CHANGE. Everything here reads what the plan already
// did; it decides nothing about how many members are served.

// cohortMemberNarrowingStep is one step of the MEMBER chain: how many members
// the list held either side of one recorded plan narrowing step.
type cohortMemberNarrowingStep struct {
	Stage   contractsv1.ContextFabricPlanNarrowingStage
	Basis   contractsv1.ContextFabricNarrowingBasis
	Overrun contractsv1.ContextFabricBudgetOverrun
	Before  int
	After   int
	// LimitBefore and LimitAfter are the member LIMIT the stage-1 clamp
	// moved -- its own recorded pair, which counts ceilings, not members.
	// Zero on every other stage.
	LimitBefore int
	LimitAfter  int
}

// cohortMemberNarrowingChain walks the plan's recorded MEMBER steps from the
// number of members retrieval found (declared) down to the number the answer
// carries (served).
//
// Each step can only lower the running count to its own After -- for the
// stage-1 clamp that After is a limit, so the members it kept are the smaller
// of the limit and the members that reached it. A step that removed nothing
// from the running count is not a step of this chain.
//
// The second return is false unless the chain RECONCILES: it must end exactly
// at served. A chain that does not (for example, a caller's own member limit
// cut the list and no plan step recorded it) is not stated with numbers the
// plan did not record -- the caller logs the gap instead.
func cohortMemberNarrowingChain(declared, served int, narrowing []contractsv1.ContextFabricPlanNarrowing) ([]cohortMemberNarrowingStep, bool) {
	if served < 0 || declared <= served {
		return nil, false
	}
	current := declared
	var chain []cohortMemberNarrowingStep
	for _, step := range narrowing {
		if step.Groups {
			// Group steps narrow the GROUP axis and count groups.
			continue
		}
		after := step.After
		if after > current {
			after = current
		}
		if after >= current {
			continue
		}
		member := cohortMemberNarrowingStep{
			Stage:   step.Stage,
			Basis:   step.Basis,
			Overrun: step.Overrun,
			Before:  current,
			After:   after,
		}
		if step.Stage == contractsv1.ContextFabricPlanNarrowingCardinality {
			member.LimitBefore = step.Before
			member.LimitAfter = step.After
		}
		chain = append(chain, member)
		current = after
	}
	if len(chain) == 0 || current != served {
		return nil, false
	}
	return chain, true
}

// cohortNarrowingRefinements maps the member chain onto the requirement
// refinement vocabulary.
//
// The stage-1 clamp runs while the plan is being applied, before any read, so
// it is a `planning` refinement; stages 2 and 3 both act on the assembled
// input and result, so they are `assembled_result` refinements. Both are
// members of the outcome-stage vocabulary the enclosing row already speaks.
func cohortNarrowingRefinements(chain []cohortMemberNarrowingStep) []contractsv1.ContextFabricRequirementRefinement {
	refinements := make([]contractsv1.ContextFabricRequirementRefinement, 0, len(chain))
	for _, step := range chain {
		stage := contractsv1.ContextFabricOutcomeStageAssembledResult
		if step.Stage == contractsv1.ContextFabricPlanNarrowingCardinality {
			stage = contractsv1.ContextFabricOutcomeStagePlanning
		}
		overrun := step.Overrun
		if overrun == contractsv1.ContextFabricBudgetFits {
			overrun = ""
		}
		refinements = append(refinements, contractsv1.ContextFabricRequirementRefinement{
			Stage:   stage,
			Basis:   step.Basis,
			Overrun: overrun,
			Before:  step.Before,
			After:   step.After,
		})
	}
	return refinements
}

// cohortNarrowingDisclosureSteps maps the member chain onto the contract's
// disclosure steps.
func cohortNarrowingDisclosureSteps(chain []cohortMemberNarrowingStep) []contractsv1.ContextFabricCohortNarrowingDisclosureStep {
	steps := make([]contractsv1.ContextFabricCohortNarrowingDisclosureStep, 0, len(chain))
	for _, step := range chain {
		steps = append(steps, contractsv1.ContextFabricCohortNarrowingDisclosureStep{
			Stage:       step.Stage,
			Overrun:     step.Overrun,
			Before:      step.Before,
			After:       step.After,
			LimitBefore: step.LimitBefore,
			LimitAfter:  step.LimitAfter,
		})
	}
	return steps
}

// CohortNarrowingDisclosureOutcome is the closed vocabulary for what the
// disclosure step decided on a narrowed cohort.
type CohortNarrowingDisclosureOutcome string

const (
	// CohortNarrowingDisclosed: the chain reconciled and the sentence is on
	// the served answer.
	CohortNarrowingDisclosed CohortNarrowingDisclosureOutcome = "disclosed"
	// CohortNarrowingChainUnreconciled: the answer carries fewer members than
	// were found, and the plan's recorded steps do not account for the whole
	// cut, so no sentence with counts was written.
	CohortNarrowingChainUnreconciled CohortNarrowingDisclosureOutcome = "chain_unreconciled"
	// CohortNarrowingWorkItemCensus: the work-item census path, whose stage-1
	// record mixes a limit pair and a census pair, is out of this step's
	// scope and is left to its own disclosure.
	CohortNarrowingWorkItemCensus CohortNarrowingDisclosureOutcome = "work_item_census"
)

// CohortNarrowingDisclosureEvent is the once-per-served-answer record of the
// disclosure decision. Emitted only when the answer carries fewer members
// than were found, so an unnarrowed answer logs nothing.
type CohortNarrowingDisclosureEvent struct {
	Family   QuestionFamily
	Kind     SubjectKind
	Outcome  CohortNarrowingDisclosureOutcome
	Declared int
	Served   int
	// Steps is how many recorded plan steps the chain used; zero when it did
	// not reconcile.
	Steps int
}

// applyCohortNarrowingDisclosure states the cohort narrowing on the answer's
// limitations, idempotently.
//
// Idempotent because finalizeResult runs once per pass and a later pass can
// re-finalize the same document: any earlier cohort-narrowing sentence is
// removed first, so the served answer carries exactly the one that describes
// its own member set.
//
// Returns the decision and whether there was one to make (false when the
// cohort was not narrowed at all -- the no-disclosure case adds nothing).
func applyCohortNarrowingDisclosure(result *InvestigationResult, family QuestionFamily, cardinality MembershipCardinality, narrowing []contractsv1.ContextFabricPlanNarrowing) (CohortNarrowingDisclosureEvent, bool) {
	if result == nil {
		return CohortNarrowingDisclosureEvent{}, false
	}
	// Every write to the limitations goes through the bounded appender (the
	// package's closure test holds that). An earlier pass's sentence is
	// removed from a LOCAL copy, which reaches the result only through it.
	stripped := withoutCohortNarrowingDisclosure(result.Limitations)
	if len(stripped) != len(result.Limitations) {
		limitations, displaced := appendBoundedLimitations(stripped, nil)
		result.Limitations = limitations
		result.LimitationsDisplaced += displaced
	}
	if !cardinality.Resolved || !cardinality.Narrowed() {
		return CohortNarrowingDisclosureEvent{}, false
	}
	event := CohortNarrowingDisclosureEvent{
		Family:   family,
		Kind:     cardinality.Kind,
		Declared: cardinality.Declared,
		Served:   cardinality.Served,
	}
	if cardinality.Kind == SubjectWorkItem {
		event.Outcome = CohortNarrowingWorkItemCensus
		return event, true
	}
	chain, reconciled := cohortMemberNarrowingChain(cardinality.Declared, cardinality.Served, narrowing)
	if !reconciled {
		event.Outcome = CohortNarrowingChainUnreconciled
		return event, true
	}
	sentence, composed := contractsv1.ContextFabricCohortNarrowingLimitation(
		contractsv1.ContextFabricSubjectKind(cardinality.Kind), cardinality.Declared, cardinality.Served,
		cohortNarrowingDisclosureSteps(chain))
	if !composed {
		event.Outcome = CohortNarrowingChainUnreconciled
		return event, true
	}
	limitations, displaced := appendBoundedLimitations(result.Limitations, []string{sentence})
	result.Limitations = limitations
	result.LimitationsDisplaced += displaced
	event.Outcome = CohortNarrowingDisclosed
	event.Steps = len(chain)
	return event, true
}

// withoutCohortNarrowingDisclosure drops any cohort-narrowing sentence an
// earlier pass wrote. It returns the input unchanged (same backing array)
// when there is none, and never returns nil for a non-nil input.
func withoutCohortNarrowingDisclosure(limitations []string) []string {
	found := false
	for _, limitation := range limitations {
		if contractsv1.IsContextFabricCohortNarrowingLimitation(limitation) {
			found = true
			break
		}
	}
	if !found {
		return limitations
	}
	kept := make([]string, 0, len(limitations))
	for _, limitation := range limitations {
		if contractsv1.IsContextFabricCohortNarrowingLimitation(limitation) {
			continue
		}
		kept = append(kept, limitation)
	}
	return kept
}
