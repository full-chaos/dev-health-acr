package contextfabric

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// FinalizeStateRankingForTest exposes the real finalization seam to external
// tests that construct production providers (which import this package).
func FinalizeStateRankingForTest(frame QuestionFrame, registry *FactCapabilityRegistry, cohort *Cohort, facts CanonicalFactBundle) InvestigationResult {
	requirements := registry.DeriveRequirements(frame)
	plan := AnswerPlan{Requirements: PlanRequirementsFromDerived(requirements)}
	engine := &Engine{requirements: registry, observationKeys: registry}
	cardinality, _ := ComputeMembershipCardinality(cohort, 0, nil)
	result := InvestigationResult{Cohort: cohort, Coverage: facts.Coverage}
	return engine.finalizeResult(context.Background(), storage.Principal{}, result, plan, &frame, facts, nil, 1, cardinality)
}
