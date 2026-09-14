package contextfabric

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// appendRankingRequirementEvaluations joins the published requirement to the
// server's actual member rankings. A step declaration or a completed function
// call is not proof that any member had enough evidence for a score.
//
// The member outcomes are RankCohort's evidence qualification, not a second
// scoring formula. The result retains those outcomes and missing signals so
// the reason for this decision is visible without access to the fact store.
// Like the read evaluator, this appends once per published identity; retries
// over a fresh result evaluate again, while repeated finalization carries rows.
func appendRankingRequirementEvaluations(
	rows []RequirementOutcomeRow,
	published []contractsv1.ContextFabricPlanRequirement,
	cohort *Cohort,
	cardinality MembershipCardinality,
) []RequirementOutcomeRow {
	for _, requirement := range published {
		if requirement.Obligation != string(ObligationRanking) || requirement.Kind != string(ObligationKindComputed) || !requirement.Served() {
			continue
		}
		if hasAssembledOutcome(rows, requirement.Requirement, requirement.Obligation) {
			continue
		}
		row := rankingRequirementOutcome(requirement, cohort, cardinality)
		rows = appendOutcomeRows(rows, row)
	}
	return rows
}

func rankingRequirementOutcome(requirement contractsv1.ContextFabricPlanRequirement, cohort *Cohort, cardinality MembershipCardinality) RequirementOutcomeRow {
	if cohort == nil || cohort.Kind != requirement.Subject || len(cohort.Members) == 0 {
		return unresolvedMemberSetOutcomeRow(requirement.Requirement, requirement.Obligation)
	}
	row := RequirementOutcomeRow{
		Stage:       contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement: requirement.Requirement, Obligation: requirement.Obligation,
		Outcome: contractsv1.ContextFabricRequirementSatisfied,
		Impact:  contractsv1.ContextFabricAnswerImpactNone,
	}
	ranked, qualified := 0, 0
	for _, member := range cohort.Members {
		if member.Subject.Kind != requirement.Subject || !member.RankingComputed || member.Score == nil {
			continue
		}
		switch member.Outcome {
		case CohortOutcomeQualified:
			ranked++
			qualified++
		case CohortOutcomeProvisional:
			ranked++
		}
	}
	if qualified != len(cohort.Members) {
		row.Outcome = contractsv1.ContextFabricRequirementNarrowed
		row.Impact = contractsv1.ContextFabricAnswerImpactDepth
		if ranked == 0 {
			row.Outcome = contractsv1.ContextFabricRequirementUnavailable
			row.Impact = contractsv1.ContextFabricAnswerImpactDimension
		}
		// This fallback does not claim the provider reported a reason. The
		// observed qualification and missing signals are on each member;
		// the existing cause vocabulary has no ranking-specific token.
		row.CauseCoverage = contractsv1.ContextFabricCoverageDetailFactProviderReported
		return row
	}
	if !cohort.Complete || cohort.Truncated || cardinality.Narrowed() {
		row.Outcome = contractsv1.ContextFabricRequirementNarrowed
		row.Impact = contractsv1.ContextFabricAnswerImpactScope
		row.CauseObserved = true
		row.CauseCoverage = contractsv1.ContextFabricCoverageDetailPopulationTruncated
		if cardinality.Narrowed() && cardinality.Basis != "" {
			row.CauseCoverage = ""
			row.CauseNarrowing = cardinality.Basis
			row.CauseOverrun = cardinality.Overrun
		}
	}
	return row
}
