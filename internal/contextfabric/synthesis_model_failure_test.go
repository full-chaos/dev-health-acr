package contextfabric

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// cancellingFailureSynthesizer ends the caller's context while it returns a
// failure the engine would serve, and records whether the engine composed.
type cancellingFailureSynthesizer struct {
	cancel   context.CancelFunc
	composed *bool
}

func (s cancellingFailureSynthesizer) Synthesize(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
	s.cancel()
	return InvestigationResult{}, &SynthesisFailure{Class: SynthesisFailureModelOutputInvalid, cause: ErrModelOutput}
}

func (s cancellingFailureSynthesizer) ComposeDegraded(context.Context, storage.Principal, SynthesisInput, *SynthesisFailure) (InvestigationResult, error) {
	*s.composed = true
	return InvestigationResult{}, nil
}

// A caller that is gone between the failed call and the degraded composition
// is not served an answer.
func TestACancelledCallerIsNotServedADegradedAnswer(t *testing.T) {
	t.Parallel()
	calls := 0
	composed := false
	engine := budgetStageEngine(t, budgetStageCohort(2), 1, budgetStageOptions(1000, time.Second), &calls)
	ctx, cancel := context.WithCancel(context.Background())
	engine.synthesizer = cancellingFailureSynthesizer{cancel: cancel, composed: &composed}

	_, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())

	if err == nil {
		t.Fatal("Investigate() served an answer to a cancelled caller")
	}
	if composed {
		t.Fatal("ComposeDegraded ran after the caller's context ended")
	}
}
