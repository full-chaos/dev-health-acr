package contextfabric

import (
	"regexp"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The closed vocabularies of the read requirement population, observation
// cover, membership cardinality, count population scope, budget assertion and
// item accounting lines, exported so the event specification reads them from
// the producer's own constants.

func alternation(members []string) string {
	quoted := make([]string, 0, len(members))
	for _, member := range members {
		quoted = append(quoted, regexp.QuoteMeta(member))
	}
	return strings.Join(quoted, "|")
}

// AnswerObligationLogVocabulary is the closed set of answer obligations a requirement line names.
func AnswerObligationLogVocabulary() []string {
	all := AnswerObligationVocabulary()
	return planLineStrings(all[:]...)
}

// SubjectRoleLogVocabulary is the closed set of subject roles a requirement line names.
func SubjectRoleLogVocabulary() []string {
	all := SubjectRoleVocabulary()
	return planLineStrings(all[:]...)
}

// RequirementCoordinatePattern is the shape of a requirement identity: the obligation, the role and the subject kind (empty for an organization scope), each from its closed vocabulary, joined by slashes.
func RequirementCoordinatePattern() string {
	return "^(" + alternation(AnswerObligationLogVocabulary()) + ")/(" + alternation(SubjectRoleLogVocabulary()) + ")/(" + alternation(SubjectKindLogVocabulary()) + ")$"
}

// RequirementCoordinateExample is one requirement identity the pattern accepts.
func RequirementCoordinateExample() string {
	return AnswerObligationLogVocabulary()[0] + "/" + SubjectRoleLogVocabulary()[0] + "/" + SubjectKindLogVocabulary()[1]
}

// DistributiveCompletionScopeVocabulary is the closed set of completion scopes a read requirement population line names: every scope but the single subject.
func DistributiveCompletionScopeVocabulary() []string {
	var out []string
	for _, scope := range CompletionScopeVocabulary() {
		if scope != CompletionScopeSingleSubject {
			out = append(out, string(scope))
		}
	}
	return out
}

// PlanRequirementOutcomeLogVocabulary is the closed set of requirement outcomes.
func PlanRequirementOutcomeLogVocabulary() []string {
	all := contractsv1.ContextFabricPlanRequirementOutcomeVocabulary()
	return planLineStrings(all[:]...)
}

// AnswerImpactKindLogVocabulary is the closed set of answer impacts; the empty impact is a row nothing reduced.
func AnswerImpactKindLogVocabulary() []string {
	all := contractsv1.ContextFabricAnswerImpactKindVocabulary()
	return append([]string{""}, planLineStrings(all[:]...)...)
}

// CoverageDetailCodeLogVocabulary is the closed set of coverage causes; the empty cause is a row with none.
func CoverageDetailCodeLogVocabulary() []string {
	all := contractsv1.ContextFabricCoverageDetailCodeVocabulary()
	return append([]string{""}, planLineStrings(all[:]...)...)
}

// CountUnitsLogVocabulary is the closed set of quantities a row's two counts carry.
func CountUnitsLogVocabulary() []string {
	return planLineStrings(countUnitsPopulation, countUnitsKind)
}

// PopulationCensusLogVocabulary is the closed set of population censuses; the empty census is a row no population owner covers.
func PopulationCensusLogVocabulary() []string {
	all := populationCensusVocabulary()
	return append([]string{""}, planLineStrings(all[:]...)...)
}

// BudgetOverrunOrNoneLogVocabulary is the closed set of budget overruns, the empty overrun of a row no budget cut, and the class a value outside the set is logged as.
func BudgetOverrunOrNoneLogVocabulary() []string {
	return append([]string{""}, BudgetOverrunLogVocabulary()...)
}

// RowWithheldReasonLogVocabulary is the closed set of reasons an observation cover row was withheld.
func RowWithheldReasonLogVocabulary() []string {
	return planLineStrings(RowWithheldReasonVocabulary()...)
}

// SubjectExpressionKindLogVocabulary is the closed set of subject expression kinds; the empty kind is a frame that records no requested population.
func SubjectExpressionKindLogVocabulary() []string {
	all := SubjectExpressionKindVocabulary()
	return append([]string{""}, planLineStrings(all[:]...)...)
}

// CountPopulationAssembledOutcomeLogVocabulary is the closed set of outcomes a count population scope line names, and none for a document with no assembled row.
func CountPopulationAssembledOutcomeLogVocabulary() []string {
	return append([]string{"none"}, PlanRequirementOutcomeLogVocabulary()...)
}

// BudgetAssertStageLogVocabulary is the closed set of budget assertion stages.
func BudgetAssertStageLogVocabulary() []string {
	all := BudgetAssertStageVocabulary()
	return planLineStrings(all[:]...)
}

// CapacityVerdictLogVocabulary is the closed set of capacity verdicts, and the class a value outside it is logged as.
func CapacityVerdictLogVocabulary() []string {
	all := contractsv1.ContextFabricCapacityVerdictVocabulary()
	return append(planLineStrings(all[:]...), logUnclassified)
}

// AllocationDisagreementLogVocabulary is the closed set of allocation disagreements; the empty one is grants that agree, and the class is a value outside the set.
func AllocationDisagreementLogVocabulary() []string {
	all := AllocationDisagreementVocabulary()
	return append(planLineStrings(all[:]...), logUnclassified)
}

// ItemAccountingStageLogVocabulary is the closed set of stages an item accounting disagreement is raised under: the stages an assembled attempt is measured under, and the budget assertion stages.
func ItemAccountingStageLogVocabulary() []string {
	return append([]string{
		"budget_trim", "assembled_result", "re_synthesized_result", "facts_only_result", "path_drop", "candidate_narrowing",
		"claim_depth_narrowing", "walk_list_fit", "fact_row_truncation",
	}, BudgetAssertStageLogVocabulary()...)
}

// LedgerDisagreementPattern is the shape of the collection or bucket an item ledger names: empty for a reconciled ledger, a collection or bucket token, a token with a duplicate ordinal, or a prefixed token for an invalid debit.
func LedgerDisagreementPattern() string {
	return `^(|[a-z_]+|[a-z_]+#[0-9]+|(collection|bucket):[a-z_]*)$`
}

// RequirementOutcomeTransitionCauseLogVocabulary is the closed set of a wire cause on the transition line, or none.
func RequirementOutcomeTransitionCauseLogVocabulary(key string) []string {
	switch key {
	case "cause_coverage":
		return append([]string{transitionLineNone}, CoverageDetailCodeLogVocabulary()[1:]...)
	case "cause_overrun":
		return append([]string{transitionLineNone}, BudgetOverrunLogVocabulary()[:3]...)
	case "cause_narrowing":
		return append([]string{transitionLineNone}, NarrowingBasisLogVocabulary()[1:]...)
	}
	return nil
}
