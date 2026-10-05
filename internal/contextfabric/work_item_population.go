package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// withWorkItemPopulation puts the measured population on a work-item cohort
// and, when the list is shorter than it, says how much shorter. The cohort is
// copied: the stored result is never edited in place. Nothing is set when the
// census holds no population, so an unmeasured read does not claim one.
func withWorkItemPopulation(result InvestigationResult, census *WorkItemTupleCensus) InvestigationResult {
	if census == nil || result.Cohort == nil || result.Cohort.Kind != SubjectWorkItem || census.State == WorkItemMembershipCensusUnmeasured {
		return result
	}
	listed := len(result.Cohort.Members)
	if census.Value < listed || census.Value == 0 {
		return result
	}
	lowerBound := census.State == WorkItemMembershipCensusFloor || census.incomplete
	cohort := *result.Cohort
	cohort.Population = census.Value
	cohort.PopulationLowerBound = lowerBound
	result.Cohort = &cohort
	if sentence, ok := contractsv1.ContextFabricWorkItemListedLimitation(listed, census.Value, lowerBound); ok {
		composed, displaced := appendBoundedLimitations(result.Limitations, []string{sentence})
		result.Limitations = composed
		result.LimitationsDisplaced += displaced
	}
	return result
}
