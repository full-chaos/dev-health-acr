package contextfabric

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// withCohortPopulation puts the population retrieval reached on a discovered
// cohort that carries fewer members than it. The cohort is copied, never
// edited in place. Nothing is set when the population is unmeasured, does not
// exceed the listed members, or is already stated.
func withCohortPopulation(cohort *Cohort, population int, lowerBound bool) *Cohort {
	if cohort == nil || cohort.Kind == SubjectWorkItem || cohort.Population > 0 || population <= len(cohort.Members) {
		return cohort
	}
	copied := *cohort
	copied.Population, copied.PopulationLowerBound = population, lowerBound
	return &copied
}

// cohortListedLimitation states "N of M" for a cohort cut below its population,
// naming the cap that cut it and no left-out member.
func cohortListedLimitation(cohort *Cohort, narrowing []contractsv1.ContextFabricPlanNarrowing) (string, bool) {
	if cohort == nil || cohort.Kind == SubjectWorkItem || cohort.Population <= len(cohort.Members) {
		return "", false
	}
	cut := contractsv1.ContextFabricWorkItemListCutRequest
	if _, clamped := renderClampNarrowing(narrowing); clamped {
		cut = contractsv1.ContextFabricWorkItemListCutItems
	}
	return contractsv1.ContextFabricWorkItemListedLimitation(len(cohort.Members), cohort.Population, cohort.PopulationLowerBound, cut, "")
}
