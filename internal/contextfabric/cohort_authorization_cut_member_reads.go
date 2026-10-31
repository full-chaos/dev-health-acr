package contextfabric

import (
	"sort"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// restateCohortAuthorizationCutMemberReads states the authorization cut on
// the SERVED COPY of an answer whose member read rows predate it
// (CHAOS-12153), the member-read twin of restateCohortAuthorizationCutCount.
//
// A result stored before the member read rows read the cut carries the denial
// detail beside `satisfied` assembled-result member read rows. Reuse never
// re-evaluates a row that exists, and a by-id read evaluates nothing, so both
// served those rows and the completeness state `complete`. This APPENDS, for
// each such row, the narrowed row the evaluator now writes for a fresh answer:
// Declared raised by the denied count, the cut as its cause and as its one
// refinement. The stored row is never rewritten (the outcome layer's APPEND
// invariant), and storage is not touched.
//
// The denied count is cohortAuthorizationDenied over the served coverage, the
// function the count restatement reads, so the two restated rows of one answer
// state the same cut.
//
// It restates only the LAST assembled row of a requirement the published plan
// names as a member read of the cohort's kind, and only when that row is
// `satisfied` with Served equal to Declared. A population-counted satisfied
// row has equal counts, so its numbers are members and the raised Declared is
// a member count; a satisfied row with unequal counts is counted in fact KINDS
// (the kind arm), and raising its Declared by members would mix units. It leaves alone: no cohort, a work-item cohort (its census
// carries its own gap), no measured denial, no plan, and a last row that is not
// satisfied (it already keeps the state from `complete`). The last rule is also
// why a second pass appends nothing.
//
// It is called beside restateCohortAuthorizationCutCount in
// ApplyServedStatusFloors, the one late writer every serving surface runs
// (fresh, reused and by id). Unlike the count, no reuse-path line reads the
// member rows before that writer, so it needs no earlier call. On a fresh
// answer the rows already name the cut, so it is a no-op there.
func restateCohortAuthorizationCutMemberReads(result InvestigationResult) (InvestigationResult, bool) {
	if result.Cohort == nil || result.Cohort.Kind == SubjectWorkItem || result.AnswerPlan == nil {
		return result, false
	}
	denied := cohortAuthorizationDenied(result.Coverage)
	if denied == 0 {
		return result, false
	}
	memberReads := map[string]bool{}
	for _, requirement := range result.AnswerPlan.Requirements {
		if requirement.Kind == string(ObligationKindRead) && requirement.Role == string(SubjectRoleMember) && requirement.Subject == result.Cohort.Kind {
			memberReads[requirement.Requirement] = true
		}
	}
	last := map[string]int{}
	for index, row := range result.Completeness.Outcomes {
		if row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult && memberReads[row.Requirement] {
			last[row.Requirement] = index
		}
	}
	requirements := make([]string, 0, len(last))
	for requirement := range last {
		requirements = append(requirements, requirement)
	}
	sort.Strings(requirements)
	var restated []RequirementOutcomeRow
	for _, requirement := range requirements {
		stated := result.Completeness.Outcomes[last[requirement]]
		if stated.Outcome != contractsv1.ContextFabricRequirementSatisfied || stated.Served != stated.Declared {
			continue
		}
		row := stated
		row.Outcome = contractsv1.ContextFabricRequirementNarrowed
		row.Impact = contractsv1.ContextFabricAnswerImpactScope
		row.Declared = stated.Declared + denied
		row.CauseCoverage = contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization
		// Observed: the graph read reported the denial with its count, and
		// the cut is the whole shortfall (the stored row served every member
		// it declared).
		row.CauseObserved = true
		row.CauseNarrowing = ""
		row.CauseOverrun = ""
		row.Refinements = []contractsv1.ContextFabricRequirementRefinement{{
			Stage:    contractsv1.ContextFabricOutcomeStageAssembledResult,
			Coverage: contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization,
			Before:   row.Declared,
			After:    stated.Declared,
		}}
		restated = append(restated, row)
	}
	if len(restated) == 0 {
		return result, false
	}
	return appendServedRestatement(result, restated...), true
}

// appendServedRestatement appends restated rows to a served copy within the
// contract's outcome cap (codex #1807 r1). A stored answer can already hold
// ContextFabricPlanRequirementOutcomeMaxCount rows; appending past it made the
// served document invalid. At the cap the lowest-ranked row gives way first and
// the eviction is disclosed in the limitations, the rule every lever row
// follows (appendLeverOutcomeRow, D6080), so the answer is served valid and
// still states the cut.
func appendServedRestatement(result InvestigationResult, rows ...RequirementOutcomeRow) InvestigationResult {
	outcomes := result.Completeness.Outcomes
	evicted := 0
	for _, row := range rows {
		var gave int
		outcomes, gave = appendLeverOutcomeRow(outcomes, row)
		evicted += gave
	}
	result.Completeness.Outcomes = outcomes
	var displaced int
	result.Limitations, displaced = withOutcomeEvictionLimitation(result.Limitations, evicted)
	result.LimitationsDisplaced += displaced
	return result
}
