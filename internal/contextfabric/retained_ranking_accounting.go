package contextfabric

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// AccountForRetainedRanking appends missing assembled accounting on a served
// copy. The saved plan and RankCohort qualifications are its only authority:
// it never ranks, reads facts, changes an existing row or rewrites storage.
// Incomplete legacy qualification is left unaccounted, not interpreted as a
// newly executed ranking with no evidence. The returned events describe both
// that limit and the rows carried or added.
func AccountForRetainedRanking(result InvestigationResult) (InvestigationResult, []RetainedRankingAccountingEvent) {
	if result.AnswerPlan == nil {
		return result, nil
	}
	var events []RetainedRankingAccountingEvent
	for _, requirement := range result.AnswerPlan.Requirements {
		if requirement.Obligation != string(ObligationRanking) || requirement.Kind != string(ObligationKindComputed) || !requirement.Served() {
			continue
		}
		event := retainedRankingQualification(requirement, result.Cohort)
		for _, row := range result.Completeness.Outcomes {
			if row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult && row.Requirement == requirement.Requirement && row.Obligation == requirement.Obligation {
				event.ExistingRow = true
				event.AssembledOutcome = row.Outcome
				break
			}
		}
		if !event.ExistingRow && event.QualificationRecorded {
			scope := rankingScopeQualification{}
			// Stage-1 ceilings and group counts are not member-population evidence.
			// Compare a recorded member cut with the retained set, but do not recover
			// an original population number from flags or absent graph context.
			if step, ok := firstMemberNarrowing(result.AnswerPlan.Narrowing); ok && step.Before > len(result.Cohort.Members) {
				scope = rankingScopeQualification{narrowed: true, basis: step.Basis, overrun: step.Overrun}
			}
			row := rankingRequirementOutcomeForScope(requirement, result.Cohort, scope)
			result.Completeness.Outcomes = appendOutcomeRows(result.Completeness.Outcomes, row)
			event.RowAdded = true
			event.AssembledOutcome = row.Outcome
		}
		events = append(events, event)
	}
	for i := range events {
		events[i].Index = i + 1
		events[i].Total = len(events)
	}
	return result, events
}

func retainedRankingQualification(requirement contractsv1.ContextFabricPlanRequirement, cohort *Cohort) RetainedRankingAccountingEvent {
	event := RetainedRankingAccountingEvent{Requirement: requirement.Requirement, SubjectKind: requirement.Subject}
	if cohort == nil {
		return event
	}
	event.RetainedMembers = len(cohort.Members)
	for _, member := range cohort.Members {
		if member.Subject.Kind != requirement.Subject || !member.RankingComputed {
			continue
		}
		switch member.Outcome {
		case CohortOutcomeQualified:
			if member.Score != nil {
				event.Qualified++
			}
		case CohortOutcomeProvisional:
			if member.Score != nil {
				event.Provisional++
			}
		case CohortOutcomeInsufficientEvidence:
			if member.Score == nil {
				event.InsufficientEvidence++
			}
		case CohortOutcomeNotApplicable:
			if member.Score == nil {
				event.NotApplicable++
			}
		}
	}
	event.UnrecordedMembers = event.RetainedMembers - event.Qualified - event.Provisional - event.InsufficientEvidence - event.NotApplicable
	event.QualificationRecorded = cohort.Kind == requirement.Subject && event.RetainedMembers > 0 && event.UnrecordedMembers == 0
	return event
}
