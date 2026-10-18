package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The closed vocabularies of the plan narrowing, synthesis retry selection,
// cohort narrowing disclosure and grouped cohort completeness lines, exported
// so the event specification reads them from the producer's own constants.

// logUnclassified is the value a fail-closed helper writes for a value outside its closed set.
const logUnclassified = "unclassified"

func planLineStrings[T ~string](members ...T) []string {
	out := make([]string, 0, len(members))
	for _, member := range members {
		out = append(out, string(member))
	}
	return out
}

// QuestionFamilyLogVocabulary is the closed set of question families a plan line names.
func QuestionFamilyLogVocabulary() []string {
	all := QuestionFamilyVocabulary()
	return planLineStrings(all[:]...)
}

// QuestionFamilyTableVersionVocabulary is the one question family table version a plan line names.
func QuestionFamilyTableVersionVocabulary() []string { return []string{QuestionFamilyTableVersion} }

// PlanNarrowingStageVocabulary is the closed set of narrowing stages.
func PlanNarrowingStageVocabulary() []string {
	all := contractsv1.ContextFabricPlanNarrowingStageVocabulary()
	return planLineStrings(all[:]...)
}

// NarrowingBasisLogVocabulary is the closed set of narrowing bases; the empty basis is a pass that narrowed nothing.
func NarrowingBasisLogVocabulary() []string {
	all := contractsv1.ContextFabricNarrowingBasisVocabulary()
	return append([]string{""}, planLineStrings(all[:]...)...)
}

// BudgetOverrunLogVocabulary is the closed set of budget overruns, and the class a value outside it is logged as.
func BudgetOverrunLogVocabulary() []string {
	all := contractsv1.ContextFabricBudgetOverrunVocabulary()
	return append(planLineStrings(all[:]...), logUnclassified)
}

// FactsOnlyDeclinedLogVocabulary is the closed set of reasons a facts only answer was declined; the empty reason is an answer served or not tried.
func FactsOnlyDeclinedLogVocabulary() []string {
	return planLineStrings(FactsOnlyServedOrUntried, FactsOnlyComposeFailed, FactsOnlyMeasureFailed, FactsOnlyOverBudget)
}

// RetryDeclinedReasonVocabulary is the closed set of reasons a retry was declined; the empty reason is a pass the retry did not apply to.
func RetryDeclinedReasonVocabulary() []string {
	return planLineStrings(RetryDeclinedNotApplicable, RetryDeclinedNoReserve, RetryDeclinedInsufficientDeadline, RetryDeclinedNothingToNarrow,
		RetryDeclinedCannotReduceAxis, RetryDeclinedSuppliedSynthesis)
}

// NarrowingContinuationAxisLogVocabulary is the closed set of continuation axes; the empty axis is a pass that planned no refusal.
func NarrowingContinuationAxisLogVocabulary() []string {
	all := NarrowingContinuationAxisVocabulary()
	return append([]string{""}, planLineStrings(all[:]...)...)
}

// AnswerCompletenessStateLogVocabulary is the closed set of answer completeness states; the empty state is a pass that reduced no outcome.
func AnswerCompletenessStateLogVocabulary() []string {
	all := contractsv1.ContextFabricAnswerCompletenessStateVocabulary()
	return append([]string{""}, planLineStrings(all[:]...)...)
}

// OutcomeReductionDeclinedLogVocabulary is the closed set of reasons an outcome reduction was declined; the empty reason is a reduction that applied or did not apply.
func OutcomeReductionDeclinedLogVocabulary() []string {
	return planLineStrings(OutcomeReductionNotApplicable, OutcomeReductionNotItemsAxis, OutcomeReductionNoItemBudget, OutcomeReductionNothingReducible,
		OutcomeReductionWouldNotReduce, OutcomeReductionInsufficient, OutcomeReductionUnmeasurable, OutcomeReductionInvalidResult)
}

// LedgerStatusLogVocabulary is the closed set of item ledger statuses, and the class a value outside it is logged as.
func LedgerStatusLogVocabulary() []string {
	all := contractsv1.ContextFabricLedgerStatusVocabulary()
	return append(planLineStrings(all[:]...), logUnclassified)
}

// ItemQuotaAvailabilityLogVocabulary is the closed set of quota availabilities, and the class an unmeasured or unknown value is logged as.
func ItemQuotaAvailabilityLogVocabulary() []string {
	all := ItemQuotaAvailabilityVocabulary()
	return append(planLineStrings(all[:]...), logUnclassified)
}

// CeilingFitStepVocabulary is the closed set of ceiling fit steps; the empty step is a pass no fit step ran on.
func CeilingFitStepVocabulary() []string {
	return planLineStrings(CeilingFitNone, CeilingFitMeasured, CeilingFitRowTruncation, CeilingFitPathDrop, CeilingFitWalkListCut, CeilingFitDecisive)
}

// CohortNarrowingDisclosureOutcomeVocabulary is the closed set of cohort narrowing disclosure outcomes.
func CohortNarrowingDisclosureOutcomeVocabulary() []string {
	return planLineStrings(CohortNarrowingDisclosed, CohortNarrowingChainUnreconciled, CohortNarrowingWorkItemCensus)
}

// CohortGroupingRefusalLogVocabulary is the closed set of grouping refusals a line names, and the class a value outside it is logged as.
func CohortGroupingRefusalLogVocabulary() []string {
	return append(planLineStrings(CohortGroupingRefusalGroupKindSourceMismatch, CohortGroupingRefusalNoMemberPlaced), logUnclassified)
}

// SubjectKindLogVocabulary is the closed set of subject kinds a cohort line names; the empty kind is a line whose cohort names none.
func SubjectKindLogVocabulary() []string {
	all := contractsv1.ContextFabricSubjectKindVocabulary()
	return append([]string{""}, planLineStrings(all[:]...)...)
}
