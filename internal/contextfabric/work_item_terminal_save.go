package contextfabric

// An accepted question frame is prospective before a project is committed.
// Preserve that reading for the existing subject/window clarification paths;
// it does not assert that membership was measured. Census-bearing and decisive
// results still pass the strict retained-member payload validator at save.
func workItemTuplePreMembershipTerminal(site BudgetAssertStage, result InvestigationResult, state *PersistedSemanticState) bool {
	if state == nil || state.WorkItemCensus != nil || (site != BudgetAssertSubjectlessTerminal && site != BudgetAssertWindowConfirmationRequired) {
		return false
	}
	if result.Status != InvestigationNoMatch && result.Status != InvestigationClarificationRequired {
		return false
	}
	if len(result.SubjectResolution.Committed) != 0 || result.Cohort != nil || len(result.ClaimedFacts) != 0 || len(result.Paths) != 0 || len(result.Drivers) != 0 || len(result.RemainingWork) != 0 || len(result.ReadinessGaps) != 0 || len(result.Conflicts) != 0 || len(result.EvidenceRefIDs) != 0 || len(result.EvidenceRefLabels) != 0 || result.DirectJudgment != "" || result.CurrentState != "" || len(result.StrongestPressures) != 0 {
		return false
	}
	for _, candidate := range result.SubjectResolution.Candidates {
		if candidate.State == ResolutionCommitted {
			return false
		}
	}
	return true
}
