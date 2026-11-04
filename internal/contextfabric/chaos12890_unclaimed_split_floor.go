package contextfabric

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// withUnclaimedRepositorySplitFloor discloses, and floors a complete answer on the scope-anchor
// allocation route (CHAOS-12606) to partial when no claim names the team's
// repository_theme_breakdown table (CHAOS-12890). The route's one requirement is
// satisfied by reading the team's fact, so nothing else asks whether the split
// the question named was claimed. A table the size cut withheld keeps its claim
// (rows nil), so it counts as claimed here and keeps its own sentence.
func withUnclaimedRepositorySplitFloor(result InvestigationResult, frame *QuestionFrame) InvestigationResult {
	if frame == nil || frame.requirementRoute != RequirementRouteScopeAllocationTeam ||
		result.RefusalBasis != "" ||
		(result.Status != InvestigationComplete && result.Status != InvestigationPartial && result.Status != InvestigationDegraded) {
		return result
	}
	for _, claim := range result.ClaimedFacts {
		if claim.Field == contractsv1.ContextFabricRenderRepositoryFlowTableField {
			return result
		}
	}
	for _, limitation := range result.Limitations {
		if limitation == contractsv1.ContextFabricRepositorySplitUnclaimedLimitation {
			return result
		}
	}
	if result.Status == InvestigationComplete {
		floorKeepingNarrative(&result, InvestigationPartial, contractsv1.ContextFabricRepositorySplitUnclaimedLimitation)
		return result
	}
	composed, displaced := appendBoundedLimitations(result.Limitations, []string{contractsv1.ContextFabricRepositorySplitUnclaimedLimitation})
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	return result
}
