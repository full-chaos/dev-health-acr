package contextfabric

import (
	"context"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// withFullOutcomeRows makes every synthesis carry the v1 maximum of outcome
// rows, so a lever has no room for its disclosure row without evicting one.
func withFullOutcomeRows(engine *Engine) *Engine {
	base := engine.synthesizer
	engine.synthesizer = synthesizerFunc(func(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
		result, err := base.Synthesize(ctx, principal, input)
		if err != nil {
			return result, err
		}
		rows := make([]RequirementOutcomeRow, contractsv1.ContextFabricPlanRequirementOutcomeMaxCount)
		for i := range rows {
			rows[i] = RequirementOutcomeRow{
				Stage:   contractsv1.ContextFabricOutcomeStagePlanning,
				Outcome: contractsv1.ContextFabricRequirementSatisfied,
				Impact:  contractsv1.ContextFabricAnswerImpactNone,
			}
		}
		result.Completeness.Outcomes = rows
		return result, nil
	})
	return engine
}
