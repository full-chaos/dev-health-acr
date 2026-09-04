package contextfabric

import (
	"context"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Outcome-driven assembly: the served answer says, per requirement, what
// became of it.
//
// WHAT THIS REPLACES. Stage 3 measures the assembled result and, when it
// does not fit, narrows the COHORT and re-synthesizes once. That is its only
// lever. A single-subject question has no cohort, so the stage reaches its
// terminal with the entire repertoire empty -- no content reduction is ever
// attempted -- and refuses, while the unresolved subject candidates, which
// ARE charged against the item budget, sit untouched. Decision D5's own
// header already recorded that candidates are the term re-synthesis cannot
// reach; this file is that paragraph turned into behaviour.
//
// WHAT IT IS NOT. Assembly here still MEASURES and then reduces. Bounding
// assembly BY CONSTRUCTION -- planning against declared caps so the
// unfittable shape is never created -- is a separate, larger change and is
// not delivered here. Saying so plainly matters: a post-hoc cap on
// candidates that called itself a plan would be that change renamed rather
// than made.

type (
	// RequirementOutcome is what happened to one requirement.
	RequirementOutcome = contractsv1.ContextFabricPlanRequirementOutcome
	// AnswerImpactKind is what the reader loses where it did not happen.
	AnswerImpactKind = contractsv1.ContextFabricAnswerImpactKind
	// RequirementOutcomeRow is one row of the outcome set.
	RequirementOutcomeRow = contractsv1.ContextFabricPlanRequirementOutcomeRow
	// AnswerCompletenessState is what the whole set adds up to.
	AnswerCompletenessState = contractsv1.ContextFabricAnswerCompletenessState
)

// requirementIdentity is a requirement row's stable name.
//
// It is the COORDINATE and nothing else -- obligation, role, subject kind --
// because that is what the derivation itself is keyed on. Minting a separate
// id here would create a second authority for which requirement a row is
// about, and the two would drift the first time the derivation changed.
func requirementIdentity(requirement DerivedRequirement) string {
	return string(requirement.Obligation) + "/" + string(requirement.Role) + "/" + string(requirement.Subject)
}

// seedRequirementOutcomes turns this turn's derived requirement rows into
// the initial outcome set.
//
// A row the registry can serve starts SATISFIED. Assembly appends a
// narrowing later if it reduces the thing that row covers; nothing here
// pre-judges an outcome a stage has not reached yet.
//
// A row no producer can serve is UNAVAILABLE with impact `dimension`, and
// its cause is the derivation's OWN closed reason token carried through
// rather than re-classified: the derivation already decided why the cell is
// empty, and deciding it a second time here is how two authorities for one
// fact begin.
//
// Returns nil when there is no frame or no deriver. That is an honest
// absence and it is visible downstream: an empty set derives the
// `not_derived` completeness state, which says the outcomes were never
// derived rather than claiming everything was fine.
func seedRequirementOutcomes(frame *QuestionFrame, deriver RequirementDeriver) []RequirementOutcomeRow {
	if frame == nil || deriver == nil {
		return nil
	}
	requirements := deriver.DeriveRequirements(*frame)
	if len(requirements) == 0 {
		return nil
	}
	rows := make([]RequirementOutcomeRow, 0, len(requirements))
	for _, requirement := range requirements {
		row := RequirementOutcomeRow{
			// The seed rows belong to the stage that PLANNED them, so a
			// reader can see which rows the plan carried and which a later
			// stage appended.
			Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
			Requirement: requirementIdentity(requirement),
			Obligation:  string(requirement.Obligation),
			Outcome:     contractsv1.ContextFabricRequirementSatisfied,
			Impact:      contractsv1.ContextFabricAnswerImpactNone,
		}
		if !requirement.Served() {
			row.Outcome = contractsv1.ContextFabricRequirementUnavailable
			row.Impact = contractsv1.ContextFabricAnswerImpactDimension
			row.CauseCoverage = unavailableRequirementCause(requirement.Unavailable)
			// Observed: the derivation reported this reason for this cell,
			// it was not defaulted by anything here.
			row.CauseObserved = true
		}
		rows = append(rows, row)
	}
	return rows
}

// unavailableRequirementCause maps the derivation's own unavailable reason
// onto the shipped coverage-detail vocabulary.
//
// The mapping is EXPLICIT rather than a pass-through because the two
// vocabularies are owned by different layers and a silent cast would couple
// them: a new reason token would then reach the wire as a coverage code that
// vocabulary never declared. Anything this table does not name yields the
// empty code, and the row still carries its outcome, impact and numbers --
// an unnamed cause is a gap in this table, not a licence to invent one.
func unavailableRequirementCause(reason RequirementUnavailableReason) contractsv1.ContextFabricCoverageDetailCode {
	switch reason {
	case RequirementReasonNoDeclaringProducer:
		return contractsv1.ContextFabricCoverageDetailFactUnconfigured
	case RequirementReasonSubjectKindUnsupported:
		return contractsv1.ContextFabricCoverageDetailFactPruned
	case RequirementReasonTableShapeUndeclared:
		return contractsv1.ContextFabricCoverageDetailFactUnconfigured
	case RequirementReasonComputedPopulationAbsent:
		return contractsv1.ContextFabricCoverageDetailFactPruned
	default:
		return ""
	}
}

// candidateNarrowing is what narrowCandidatesToBudget did, or did not do.
type candidateNarrowing struct {
	Served   int
	Declared int
	Narrowed bool
}

// narrowCandidatesToBudget reduces the ONE collection assembly may reduce
// without orphaning prose, and returns what it did.
//
// WHY THE CANDIDATES AND NOTHING ELSE. Drivers, findings and claimed facts
// are cited by the composed judgment; dropping one leaves prose describing
// content that is no longer present, which is the defect this seam is
// forbidden to introduce. Resolution candidates are alternatives the
// resolver did NOT commit to -- nothing in the answer cites them -- so
// removing them changes how many options the caller is shown and nothing
// else. That is a real loss, which is why it is disclosed as one.
//
// THE ITEMS AXIS ONLY. The reduction is computed, not searched: the charged
// terms other than candidates are fixed by the time assembly runs, so the
// number of candidates the ceiling admits is arithmetic. There is no
// equivalent arithmetic for the byte axis -- candidate sizes vary -- and the
// honest alternatives are a guess dressed as arithmetic or an iterated
// shrink loop. Both are refused here: a byte overrun keeps the existing
// planned refusal, and the residual is stated rather than hidden.
func narrowCandidatesToBudget(result InvestigationResult, budget ResponseBudget, measurement ResponseMeasurement, overrun contractsv1.ContextFabricBudgetOverrun) (InvestigationResult, candidateNarrowing) {
	declared := len(result.SubjectResolution.Candidates)
	if overrun != contractsv1.ContextFabricBudgetOverrunItems || budget.MaxItems <= 0 || declared == 0 {
		return result, candidateNarrowing{Served: declared, Declared: declared}
	}
	fixed := measurement.Items.Budgeted() - declared
	allowance := budget.MaxItems - fixed
	if allowance < 0 {
		allowance = 0
	}
	if allowance >= declared {
		// The candidates are not what pushed this over. Reducing them
		// would drop content without fixing anything, and would publish a
		// narrowing that did not help as though it had.
		return result, candidateNarrowing{Served: declared, Declared: declared}
	}
	// The LEADING allowance, in the order the resolver returned them --
	// which is its own ranking. No re-ordering: sorting the survivors would
	// change the served document beyond narrowing it, and would let the
	// answer present a different ranking than the resolver computed.
	kept := make([]SubjectCandidate, allowance)
	copy(kept, result.SubjectResolution.Candidates[:allowance])
	resolution := result.SubjectResolution
	resolution.Candidates = kept
	result.SubjectResolution = resolution
	return result, candidateNarrowing{Served: allowance, Declared: declared, Narrowed: true}
}

// candidateNarrowingOutcomeRow names what narrowCandidatesToBudget did.
//
// The cause is the DECLARED CEILING, from the shipped overrun vocabulary --
// not a selection order, because no selection ran: the list was truncated at
// its own declared order. Naming a selection basis here would state that an
// order chose the survivors when a ceiling did.
//
// It carries the requirement it reduced when this turn has requirement rows
// to attribute it to, and carries none when it does not. The second case is
// not a silent gap: the completeness state derived from the set says
// `not_derived` for exactly the turns where attribution was impossible.
func candidateNarrowingOutcomeRow(narrowing candidateNarrowing, overrun contractsv1.ContextFabricBudgetOverrun, requirement string, obligation string) RequirementOutcomeRow {
	return RequirementOutcomeRow{
		Stage:       contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement: requirement,
		Obligation:  obligation,
		Outcome:     contractsv1.ContextFabricRequirementNarrowed,
		// Scope: the caller is shown fewer subjects than the resolver
		// found. Not depth -- the subjects that remain carry exactly the
		// evidence they carried before.
		Impact:       contractsv1.ContextFabricAnswerImpactScope,
		CauseOverrun: overrun,
		// Observed: this stage measured the overrun that forced the cut.
		// Nothing here defaulted.
		CauseObserved: true,
		Served:        narrowing.Served,
		Declared:      narrowing.Declared,
	}
}

// subjectScopeRequirement finds the requirement row a candidate-list
// reduction is attributable to: the one whose obligation is about the
// SUBJECT SET the resolver was choosing from.
//
// Returns empty strings when the outcome set carries no such row. An
// unattributed narrowing is reported as one rather than attached to the
// nearest plausible requirement -- a wrong attribution is worse than an
// absent one, because a reader acts on it.
func subjectScopeRequirement(rows []RequirementOutcomeRow) (string, string) {
	for _, row := range rows {
		if row.Obligation == string(ObligationState) {
			return row.Requirement, row.Obligation
		}
	}
	return "", ""
}

// appendOutcomeRows is the APPEND half of the invariant, in one place.
//
// It appends and never rewrites: no caller may edit a row another stage
// wrote, and routing every append through here is what makes that checkable
// by reading one function rather than every call site.
func appendOutcomeRows(existing []RequirementOutcomeRow, added ...RequirementOutcomeRow) []RequirementOutcomeRow {
	if len(added) == 0 {
		return existing
	}
	out := make([]RequirementOutcomeRow, 0, len(existing)+len(added))
	out = append(out, existing...)
	out = append(out, added...)
	return out
}

// narrowInsteadOfRefusing is the decision this seam adds to stage 3.
//
// It runs at the two points that used to refuse unconditionally: when the
// cohort lever declined, and when the one bounded retry still did not fit.
// At both it asks the question the stage never asked -- is there anything
// charged against this budget that can be reduced without orphaning prose --
// and serves a narrowed, DISCLOSED answer when there is.
//
// It returns false when there is not, and the caller's planned refusal
// stands. That refusal is a real terminal case, not an aspiration: an answer
// whose remaining content is all cited by its own judgment cannot be made
// smaller here without describing content that is no longer present.
//
// The order matters and is not incidental. The document is narrowed, the
// outcome row is APPENDED, and only then is the result re-finalized so
// completeness is derived from the extended set. Deriving before appending
// would be the measure-then-shrink defect this layer exists to remove,
// reproduced inside the fix for it.
func (e *Engine) narrowInsteadOfRefusing(
	ctx context.Context,
	principal storage.Principal,
	plan *AnswerPlan,
	frame *QuestionFrame,
	result InvestigationResult,
	budget ResponseBudget,
	measurement ResponseMeasurement,
	overrun contractsv1.ContextFabricBudgetOverrun,
	grouped bool,
	basis contractsv1.ContextFabricNarrowingBasis,
	before, after int,
	declined RetryDeclinedReason,
	retryAttempted bool,
) (InvestigationResult, bool) {
	narrowedResult, narrowing := narrowCandidatesToBudget(result, budget, measurement, overrun)
	if !narrowing.Narrowed {
		return InvestigationResult{}, false
	}
	requirement, obligation := subjectScopeRequirement(narrowedResult.Completeness.Outcomes)
	row := candidateNarrowingOutcomeRow(narrowing, overrun, requirement, obligation)
	narrowedResult.Completeness.Outcomes = appendOutcomeRows(narrowedResult.Completeness.Outcomes, row)
	narrowedResult = e.finalizeResult(narrowedResult, *plan, frame)

	// Measure what will actually be served. If the reduction did not
	// deliver a fitting document the refusal stands -- serving an answer
	// that still exceeds the ceiling, having announced that it was
	// narrowed to fit, would be worse than refusing.
	servedMeasurement, err := contractsv1.MeasureContextFabricResponse(narrowedResult)
	if err != nil || servedMeasurement.Overrun(budget) != contractsv1.ContextFabricBudgetFits {
		return InvestigationResult{}, false
	}

	e.recordPlanNarrowingStep(plan, PlanNarrowing{
		Stage:   contractsv1.ContextFabricPlanNarrowingAssembledResult,
		Basis:   planStageBasis(contractsv1.ContextFabricPlanNarrowingAssembledResult, grouped, basis),
		Before:  before,
		After:   after,
		Groups:  false,
		Overrun: overrun,
	})
	event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult, before, after, grouped, false, overrun, basis)
	event.MeasuredItems = servedMeasurement.Items.Budgeted()
	event.MeasuredBytes = servedMeasurement.Bytes
	event.PredictedItems = PredictedItemsForPlan(*plan, after)
	event.RetryAttempted = retryAttempted
	event.RetryFit = true
	event.DeadlineReserved = e.synthesisDeadlineReserve > 0
	event.RetryDeclined = declined
	// The decision itself, as its own dimension: this run reached a point
	// that used to refuse and served instead. Without it the refusal rate
	// falls and an operator cannot tell whether questions stopped
	// overrunning or started being narrowed.
	event.OutcomeNarrowedInsteadOfRefused = true
	event.OutcomeItemsServed = narrowing.Served
	event.OutcomeItemsDeclared = narrowing.Declared
	event.OutcomeCompletenessState = narrowedResult.Completeness.State
	e.recordPlanNarrowing(ctx, principal, event)
	return narrowedResult, true
}
